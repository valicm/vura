// Package git collects commits from configured repositories. Commits are
// identity, not duration: they say which ticket and what was done, and
// their author dates anchor a session in time.
package git

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/store"
)

type Collector struct {
	store  *store.Store
	res    *bucket.Resolver
	emails []string
	since  time.Duration
	log    *slog.Logger
	warned map[string]bool
}

func New(st *store.Store, res *bucket.Resolver, emails []string, since time.Duration, log *slog.Logger) *Collector {
	return &Collector{store: st, res: res, emails: emails, since: since, log: log, warned: map[string]bool{}}
}

func (c *Collector) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	c.CollectAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.CollectAll(ctx)
		}
	}
}

// CollectAll scans every configured repo root that is a git work tree.
func (c *Collector) CollectAll(ctx context.Context) {
	total := 0
	for root, bk := range c.res.Repos() {
		if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
			continue // a parent directory like ~/Development, or not cloned here
		}
		n, err := c.Collect(ctx, root, bk)
		if err != nil {
			if !c.warned[root] {
				c.log.Warn("git: collect", "repo", root, "err", err)
				c.warned[root] = true
			}
			continue
		}
		total += n
	}
	_ = c.store.SetState(ctx, "collector.ok.git", time.Now().Format(time.RFC3339))
	c.log.Debug("git: scanned", "commits", total)
}

// Collect imports commits from one repo and prunes ones that disappeared
// (amend, rebase, deleted branch) inside the window.
func (c *Collector) Collect(ctx context.Context, root, repoBucket string) (int, error) {
	since := time.Now().Add(-c.since)
	commits, err := c.Log(ctx, root, since)
	if err != nil {
		return 0, err
	}
	shas := make([]string, 0, len(commits))
	for _, cm := range commits {
		cm.Repo = root
		cm.Bucket = repoBucket
		tickets := bucket.Tickets(cm.Branch)
		if len(tickets) == 0 {
			tickets = bucket.Tickets(cm.Subject)
		}
		if len(tickets) > 0 {
			cm.Ticket = tickets[0]
			if b := c.res.ByTicket(cm.Ticket); b != "" {
				cm.Bucket = b // a ticket prefix is more specific than a repo path
			}
		}
		if err := c.store.UpsertCommit(ctx, cm); err != nil {
			return 0, err
		}
		shas = append(shas, cm.SHA)
	}
	if n, err := c.store.PruneCommits(ctx, root, since, shas); err != nil {
		return len(commits), err
	} else if n > 0 {
		c.log.Info("git: pruned rewritten commits", "repo", root, "rows", n)
	}
	return len(commits), nil
}

// Log runs git log over all refs and returns the author's commits since the
// cutoff. --source labels each commit with the ref it was reached from, which
// for a feature branch ahead of main is the feature branch: the ticket lives
// in that name.
func (c *Collector) Log(ctx context.Context, root string, since time.Time) ([]store.Commit, error) {
	args := []string{"-C", root, "log", "--all", "--source", "--numstat", "--no-color",
		"--since=" + since.UTC().Format(time.RFC3339),
		"--format=%x01%H%x1f%S%x1f%at%x1f%ae%x1f%s"}
	for _, e := range c.emails {
		args = append(args, "--author="+e)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parse(out), nil
}

// parse reads records separated by \x01: a header line of \x1f fields,
// then zero or more numstat lines "ins\tdel\tpath".
func parse(out []byte) []store.Commit {
	var commits []store.Commit
	for _, rec := range bytes.Split(out, []byte{0x01}) {
		rec = bytes.TrimSpace(rec)
		if len(rec) == 0 {
			continue
		}
		lines := strings.Split(string(rec), "\n")
		f := strings.Split(lines[0], "\x1f")
		if len(f) < 5 {
			continue
		}
		ts, _ := strconv.ParseInt(f[2], 10, 64)
		cm := store.Commit{SHA: f[0], Branch: cleanRef(f[1]), TS: time.Unix(ts, 0), Subject: f[4]}
		for _, l := range lines[1:] {
			p := strings.SplitN(l, "\t", 3)
			if len(p) != 3 {
				continue
			}
			cm.Files++
			if v, err := strconv.Atoi(p[0]); err == nil {
				cm.Ins += v
			}
			if v, err := strconv.Atoi(p[1]); err == nil {
				cm.Del += v
			}
		}
		commits = append(commits, cm)
	}
	return commits
}

// cleanRef turns "refs/heads/feature/X" or "refs/remotes/origin/X" into "feature/X" / "X".
func cleanRef(r string) string {
	for _, p := range []string{"refs/heads/", "refs/remotes/origin/", "refs/remotes/", "refs/tags/"} {
		if strings.HasPrefix(r, p) {
			return strings.TrimPrefix(r, p)
		}
	}
	return r
}
