// vura is the CLI. Milestone 1 ships only `status`, which shows what the
// daemon has collected so the model can be checked against memory before
// any hours are computed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/store"
)

var version = "dev"

func usage() {
	fmt.Fprintf(os.Stderr, `vura %s

usage:
  vura status [--days N]     observed hours per bucket per working day (default 7)
  vura day [DATE]            one day as sessions; DATE = YYYY-MM-DD | today | yesterday | -N
  vura raw [--days N]        collector counts per day, for checking the daemon
  vura reconcile [DATE]      walk pending days oldest first (or one DATE) and push to Tempo
        --amend              redo a pushed day: delete its Tempo worklogs first
        --dry-run            show what would be pushed, write nothing
        --no-push            store drafts locally, push later
  vura push [DATE]           push draft/failed worklogs
  vura note [--day D] BUCKET DURATION [text]   record work nothing observed
  vura nag                   desktop notification if days are pending (run by the timer)
  vura open                  open the dashboard as an app window (Chrome app mode; also a desktop launcher)
  vura backup [--keep N]     copy the database to <data>/backups/ (nightly timer at 03:30)
  vura demo [--dir D]        build a self-contained demo (config + one synthetic day) to try the tool
  vura check                 verify Jira/Tempo tokens and resolve every bucket issue (read-only)
  vura domains [--days N]    busiest browser domains and which bucket claims them
  vura report [--month YYYY-MM] [--html FILE] [--all]   pushed worklogs per bucket
  vura version

tokens: VURA_TEMPO_TOKEN / VURA_JIRA_TOKEN, or the GNOME keyring:
  secret-tool store --label='vura tempo' service vura key tempo
  secret-tool store --label='vura jira'  service vura key jira

env:
  VURA_CONFIG   config path (default %s)
  VURA_DATA     data dir    (default ~/.local/share/vura)
`, version, config.Path())
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	if os.Args[1] == "demo" {
		if err := demoCmd(os.Args[2:]); err != nil {
			fail(err)
		}
		return
	}
	cfg, err := config.Load(config.Path())
	if err != nil {
		fail(err)
	}
	switch os.Args[1] {
	case "status":
		fs := flag.NewFlagSet("status", flag.ExitOnError)
		days := fs.Int("days", 7, "how many working days back")
		_ = fs.Parse(os.Args[2:])
		if err := statusCmd(cfg, *days); err != nil {
			fail(err)
		}
	case "day":
		arg := ""
		if len(os.Args) > 2 {
			arg = os.Args[2]
		}
		if err := dayCmd(cfg, arg, true); err != nil {
			fail(err)
		}
	case "raw":
		fs := flag.NewFlagSet("raw", flag.ExitOnError)
		days := fs.Int("days", 7, "how many working days back")
		_ = fs.Parse(os.Args[2:])
		if err := status(cfg, *days); err != nil {
			fail(err)
		}
	case "reconcile":
		if err := reconcileCmd(cfg, os.Args[2:]); err != nil {
			fail(err)
		}
	case "push":
		if err := pushCmd(cfg, os.Args[2:]); err != nil {
			fail(err)
		}
	case "note":
		if err := noteCmd(cfg, os.Args[2:]); err != nil {
			fail(err)
		}
	case "domains":
		if err := domainsCmd(cfg, os.Args[2:]); err != nil {
			fail(err)
		}
	case "report":
		if err := reportCmd(cfg, os.Args[2:]); err != nil {
			fail(err)
		}
	case "check":
		if err := checkCmd(cfg); err != nil {
			fail(err)
		}
	case "open":
		if err := openCmd(cfg); err != nil {
			fail(err)
		}
	case "backup":
		if err := backupCmd(cfg, os.Args[2:]); err != nil {
			fail(err)
		}
	case "nag":
		if err := nagCmd(cfg); err != nil {
			fail(err)
		}
	case "version":
		fmt.Println("vura", version)
	default:
		usage()
		os.Exit(2)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "vura:", err)
	os.Exit(1)
}

type dayRow struct {
	day          string
	samples      int    // presence rows
	activeSec    int    // seconds with input in the last 5 min
	inhibitedSec int    // seconds an idle inhibitor was held
	hbMinutes    int    // distinct minutes with a heartbeat
	projects     string // top projects by heartbeat count
	callMinutes  int
	shellMinutes int // distinct minutes with a command running or started
	commits      int
	first, last  string
}

func status(cfg *config.Config, days int) error {
	if _, err := os.Stat(cfg.DBPath); err != nil {
		return fmt.Errorf("no database at %s; is vurad running?", cfg.DBPath)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	db := st.DB()
	now := time.Now().In(cfg.Location)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "DAY\tSPAN\tPRESENT\tINHIBITED\tEDITOR\tSHELL\tCALLS\tCOMMITS\tPROJECTS")
	for i := 0; i < days; i++ {
		day := cfg.Boundary.Day(now.AddDate(0, 0, -i))
		s, e, _ := cfg.Boundary.Range(day, cfg.Location)
		r := dayRow{day: day}

		var first, last *int64
		// Each row covers the time until the next row, capped at 5 minutes so
		// a daemon outage is not counted as presence. Interval changes in the
		// config therefore do not distort the totals.
		err := db.QueryRowContext(ctx, `
			WITH p AS (
			  SELECT ts, idle_ms, inhibit_flags,
			         MIN(COALESCE(LEAD(ts) OVER (ORDER BY ts) - ts, 60), 300) AS span
			  FROM presence WHERE ts >= ? AND ts < ?)
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN idle_ms < 300000 THEN span END),0),
			       COALESCE(SUM(CASE WHEN inhibit_flags & 8 THEN span END),0),
			       MIN(CASE WHEN idle_ms < 300000 THEN ts END),
			       MAX(CASE WHEN idle_ms < 300000 THEN ts END)
			FROM p`, s.Unix(), e.Unix()).
			Scan(&r.samples, &r.activeSec, &r.inhibitedSec, &first, &last)
		if err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(DISTINCT CAST(ts/60 AS INTEGER)) FROM heartbeats WHERE ts >= ? AND ts < ?`,
			s.Unix(), e.Unix()).Scan(&r.hbMinutes); err != nil {
			return err
		}
		rows, err := db.QueryContext(ctx, `
			SELECT COALESCE(project,'?'), COUNT(*) c FROM heartbeats
			WHERE ts >= ? AND ts < ? GROUP BY project ORDER BY c DESC LIMIT 4`, s.Unix(), e.Unix())
		if err != nil {
			return err
		}
		var ps []string
		for rows.Next() {
			var p string
			var c int
			if err := rows.Scan(&p, &c); err != nil {
				rows.Close()
				return err
			}
			ps = append(ps, p)
		}
		rows.Close()
		r.projects = strings.Join(ps, ", ")
		if err := db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(COALESCE(end,last_seen) - start),0)/60 FROM audio
			WHERE start >= ? AND start < ? AND (source IS NULL OR source NOT LIKE '%.monitor')`,
			s.Unix(), e.Unix()).Scan(&r.callMinutes); err != nil {
			return err
		}
		// Shell: minutes covered by commands, counting a long-running command
		// for its whole duration (a 40-minute migration is 40 minutes).
		if err := db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(MAX(1, (MAX(duration,0)/1000 + 59) / 60)),0) FROM shell WHERE ts >= ? AND ts < ?`,
			s.Unix(), e.Unix()).Scan(&r.shellMinutes); err != nil {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM commits WHERE ts >= ? AND ts < ?`,
			s.Unix(), e.Unix()).Scan(&r.commits); err != nil {
			return err
		}
		if r.samples == 0 && r.hbMinutes == 0 && r.callMinutes == 0 && r.shellMinutes == 0 && r.commits == 0 {
			continue
		}
		span := "-"
		if first != nil && last != nil {
			span = time.Unix(*first, 0).In(cfg.Location).Format("15:04") + "–" +
				time.Unix(*last, 0).In(cfg.Location).Format("15:04")
		}
		commits := "-"
		if r.commits > 0 {
			commits = fmt.Sprint(r.commits)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.day, span,
			hm(r.activeSec), hm(r.inhibitedSec),
			hm(r.hbMinutes*60), hm(r.shellMinutes*60), hm(r.callMinutes*60), commits, r.projects)
	}
	tw.Flush()
	fmt.Println()
	fmt.Println("PRESENT   minutes with keyboard/mouse input in the last 5m")
	fmt.Println("INHIBITED minutes an idle inhibitor (Caffeine) was held")
	fmt.Println("EDITOR    distinct minutes with a WakaTime heartbeat")
	fmt.Println("SHELL     minutes covered by terminal commands (long commands count in full)")
	fmt.Println("CALLS     minutes an app held a microphone capture stream")
	fmt.Println("COMMITS   commits by you, any branch, in configured repos")
	return nil
}

func hm(seconds int) string {
	if seconds == 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
