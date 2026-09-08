// Package shell imports terminal activity from atuin's history database.
// It reads; it never writes to atuin. Only the binary name (and a
// subcommand for known wrappers) is kept; arguments are discarded before
// they reach vura's store, because shell history is full of credentials.
package shell

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/store"
)

const cursorKey = "atuin.cursor_ns"

// lookback re-reads this much history before the cursor on every poll so
// rows atuin updated after we first saw them (duration, exit) get fixed.
const lookback = 2 * time.Hour

// wrappers are binaries whose second word is a subcommand worth keeping.
var wrappers = map[string]bool{
	"git": true, "ddev": true, "drush": true, "composer": true, "npm": true, "yarn": true, "pnpm": true,
	"make": true, "go": true, "docker": true, "podman": true, "kubectl": true, "systemctl": true,
	"dnf": true, "cargo": true, "terraform": true, "gh": true, "glab": true, "vura": true, "claude": true,
	"lando": true, "vendor/bin/drush": true,
}

// prefixes are skipped to find the real command.
var prefixes = map[string]bool{
	"sudo": true, "time": true, "nice": true, "nohup": true, "env": true, "exec": true, "command": true,
	"builtin": true, "doas": true, "watch": true, "xargs": true,
}

type Importer struct {
	path     string
	store    *store.Store
	res      *bucket.Resolver
	denylist map[string]bool
	device   string
	log      *slog.Logger
	warned   bool
}

func New(atuinDB string, st *store.Store, res *bucket.Resolver, denylist []string, device string, log *slog.Logger) *Importer {
	d := map[string]bool{}
	for _, x := range denylist {
		d[x] = true
	}
	return &Importer{path: atuinDB, store: st, res: res, denylist: d, device: device, log: log}
}

func (im *Importer) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	im.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			im.poll(ctx)
		}
	}
}

func (im *Importer) poll(ctx context.Context) {
	if _, err := os.Stat(im.path); err != nil {
		if !im.warned {
			im.log.Warn("shell: atuin db not found", "path", im.path)
			im.warned = true
		}
		return
	}
	n, err := im.Import(ctx)
	if err != nil {
		im.log.Warn("shell: import", "err", err)
		return
	}
	_ = im.store.SetState(ctx, "collector.ok.shell", time.Now().Format(time.RFC3339))
	if n > 0 {
		im.log.Debug("shell: imported", "rows", n)
	}
}

// Import pulls rows newer than cursor-lookback and returns how many it wrote.
func (im *Importer) Import(ctx context.Context) (int, error) {
	// mode=ro: never create or modify; atuin owns this file.
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=ro&_pragma=busy_timeout(3000)", im.path))
	if err != nil {
		return 0, err
	}
	defer db.Close()

	cur, err := im.store.GetState(ctx, cursorKey)
	if err != nil {
		return 0, err
	}
	var since int64
	if cur != "" {
		since, _ = strconv.ParseInt(cur, 10, 64)
		since -= lookback.Nanoseconds()
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, timestamp, duration, exit, command, cwd, session, hostname
		FROM history WHERE timestamp > ? AND deleted_at IS NULL ORDER BY timestamp`, since)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	n := 0
	maxTS := since
	for rows.Next() {
		var id, command, cwd, session, hostname string
		var ts, dur, exit int64
		if err := rows.Scan(&id, &ts, &dur, &exit, &command, &cwd, &session, &hostname); err != nil {
			return n, err
		}
		if ts > maxTS {
			maxTS = ts
		}
		bin, sub := Parse(command)
		if bin == "" {
			continue
		}
		if im.denylist[bin] {
			bin, sub = "[redacted]", ""
		}
		bk, _ := im.res.ByPath(cwd)
		device := im.device
		if hostname != "" {
			// atuin stores "host:user"; keep the host.
			device = strings.SplitN(hostname, ":", 2)[0]
		}
		d := time.Duration(dur)
		if dur < 0 {
			d = -1 // still running / unknown, preserved as-is
		}
		if err := im.store.UpsertShell(ctx, store.ShellCmd{
			ID: id, TS: time.Unix(0, ts), CWD: cwd, Binary: bin, Sub: sub, Duration: d,
			Exit: int(exit), Session: session, Bucket: bk, Device: device,
		}); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, err
	}
	if maxTS > since {
		if err := im.store.SetState(ctx, cursorKey, strconv.FormatInt(maxTS, 10)); err != nil {
			return n, err
		}
	}
	return n, nil
}

// Parse reduces a command line to (binary, subcommand). Everything else is
// dropped here and never stored. Pipelines and chains yield the first
// command only; that is enough for attribution.
func Parse(cmd string) (bin, sub string) {
	// Cut at the first shell operator so `a | b`, `a && b`, `a; b` give a.
	for _, op := range []string{"|", "&&", ";", "||", ">", "<", "&"} {
		if i := strings.Index(cmd, op); i >= 0 {
			cmd = cmd[:i]
		}
	}
	fields := strings.Fields(cmd)
	i := 0
	for i < len(fields) {
		f := fields[i]
		switch {
		case strings.Contains(f, "=") && !strings.HasPrefix(f, "-") && !strings.ContainsAny(f, "/"):
			i++ // VAR=value prefix
		case prefixes[f]:
			i++
		case strings.HasPrefix(f, "-") && i > 0 && prefixes[fields[i-1]]:
			i++ // flag belonging to a prefix (sudo -E, nice -n)
		default:
			goto found
		}
	}
	return "", ""
found:
	bin = fields[i]
	// vendor/bin/drush stays as-is (it is meaningful); other paths reduce to base.
	if strings.Contains(bin, "/") && !wrappers[bin] {
		bin = filepath.Base(bin)
	}
	if !wrappers[bin] || i+1 >= len(fields) {
		return bin, ""
	}
	next := fields[i+1]
	if isSubcommand(next) {
		sub = next
	}
	return bin, sub
}

// isSubcommand accepts bare words like "commit", "cr", "sql:dump", "run-script";
// rejects flags, paths, numbers and anything that could be data.
func isSubcommand(s string) bool {
	if s == "" || len(s) > 24 || s[0] == '-' || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == ':' || r == '_') {
			return false
		}
	}
	return true
}
