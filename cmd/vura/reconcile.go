package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/describe"
	"github.com/valicm/vura/internal/push"
	"github.com/valicm/vura/internal/reconcile"
	"github.com/valicm/vura/internal/session"
	"github.com/valicm/vura/internal/store"
)

type reconcileOpts struct {
	amend, dryRun, noPush, yes bool
}

// pendingDays lists undecided working days from the configured first day (or
// the daemon's first run) up to yesterday, oldest first. Days with nothing
// observed, or under min_day with no commits, are closed as skipped here so
// a holiday never becomes a queue.
func pendingDays(ctx context.Context, st *store.Store, cfg *config.Config, res *bucket.Resolver) ([]string, error) {
	first := cfg.Day.First
	if first == "" {
		fr, err := st.FirstRun(ctx)
		if err != nil {
			return nil, err
		}
		if fr.IsZero() {
			return nil, nil
		}
		first = cfg.Boundary.Day(fr.In(cfg.Location))
	}
	decided, err := st.DecidedDays(ctx)
	if err != nil {
		return nil, err
	}
	today := cfg.Boundary.Day(time.Now().In(cfg.Location))
	var out []string
	d, err := time.ParseInLocation("2006-01-02", first, cfg.Location)
	if err != nil {
		return nil, fmt.Errorf("day.first: %w", err)
	}
	for day := first; day < today; day = d.Format("2006-01-02") {
		d = d.AddDate(0, 0, 1)
		if decided[day] != "" {
			continue
		}
		ss, _, err := session.Day(ctx, st, cfg, res, day)
		if err != nil {
			return nil, err
		}
		var observed time.Duration
		commits := 0
		for _, s := range ss {
			if s.Bucket != session.BucketUnattributed && s.Bucket != session.BucketCall {
				observed += s.Duration()
				commits += s.Kinds[session.KindCommit]
			}
		}
		if len(ss) == 0 || (observed < cfg.Day.MinDay.Duration && commits == 0) {
			if err := st.SetDayState(ctx, day, store.DaySkipped); err != nil {
				return nil, err
			}
			_ = st.Audit(ctx, "day.autoskip", day)
			continue
		}
		out = append(out, day)
	}
	sort.Strings(out)
	return out, nil
}

func reconcileCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("reconcile", flag.ExitOnError)
	var o reconcileOpts
	fs.BoolVar(&o.amend, "amend", false, "re-do a day already pushed: delete its Tempo worklogs first")
	fs.BoolVar(&o.dryRun, "dry-run", false, "show what accept would push; write nothing")
	fs.BoolVar(&o.noPush, "no-push", false, "accept stores drafts locally; push later with `vura push`")
	fs.BoolVar(&o.yes, "yes", false, "do not ask before deleting remote worklogs on --amend")
	_ = fs.Parse(flagsFirst(fs, args))

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	res := bucket.New(cfg)

	var days []string
	if fs.NArg() > 0 {
		day, err := resolveDay(cfg, fs.Arg(0))
		if err != nil {
			return err
		}
		days = []string{day}
	} else {
		if days, err = pendingDays(ctx, st, cfg, res); err != nil {
			return err
		}
		if len(days) == 0 {
			fmt.Println("nothing pending")
			return nil
		}
	}

	in := bufio.NewScanner(os.Stdin)
	for i, day := range days {
		state, _ := st.DayState(ctx, day)
		if state == store.DayDone && !o.amend {
			fmt.Printf("%s is done; use --amend to redo it\n", day)
			continue
		}
		if o.amend {
			if err := amendDay(ctx, st, cfg, day, o, in); err != nil {
				return err
			}
		}
		again, err := reconcileDay(ctx, st, cfg, res, day, fmt.Sprintf("%d/%d", i+1, len(days)), o, in)
		if err != nil {
			return err
		}
		if !again {
			return nil // quit
		}
	}
	return nil
}

// amendDay deletes the day's pushed worklogs from Tempo and locally.
func amendDay(ctx context.Context, st *store.Store, cfg *config.Config, day string, o reconcileOpts, in *bufio.Scanner) error {
	pushed, err := push.Pushed(ctx, st, day)
	if err != nil {
		return err
	}
	if len(pushed) == 0 {
		return st.SetDayState(ctx, day, store.DayPending)
	}
	fmt.Printf("%s has %d worklogs in Tempo:\n", day, len(pushed))
	for _, w := range pushed {
		fmt.Printf("  %s %-7s %6s  %s\n", w.Start.In(cfg.Location).Format("15:04"), w.Issue, hmD(time.Duration(w.Seconds)*time.Second), w.Desc)
	}
	if o.dryRun {
		fmt.Println("dry-run: would delete these and redo the day")
		return nil
	}
	if !o.yes {
		fmt.Print("delete them from Tempo and redo the day? [y/N] ")
		if !in.Scan() || strings.ToLower(strings.TrimSpace(in.Text())) != "y" {
			return errors.New("amend cancelled")
		}
	}
	n, err := push.Amend(ctx, st, cfg, day)
	if err != nil {
		return err
	}
	fmt.Printf("  deleted %d\n", n)
	return nil
}

// reconcileDay runs the screen for one day. Returns false if the user quit.
func reconcileDay(ctx context.Context, st *store.Store, cfg *config.Config, res *bucket.Resolver, day, pos string, o reconcileOpts, in *bufio.Scanner) (bool, error) {
	ss, _, err := session.Day(ctx, st, cfg, res, day)
	if err != nil {
		return false, err
	}
	from, to, _ := cfg.Boundary.Range(day, cfg.Location)
	commits, err := st.CommitsRange(ctx, from, to)
	if err != nil {
		return false, err
	}
	notes, err := st.NotesForDay(ctx, day)
	if err != nil {
		return false, err
	}
	d := reconcile.New(cfg, day, ss, commits, notes)
	_ = st.ReplaceSessions(ctx, day, session.ToStore(day, cfg.Identity.Device, ss))

	for {
		printScreen(cfg, d, pos)
		fmt.Print("> ")
		if !in.Scan() {
			fmt.Println()
			return false, nil
		}
		line := strings.TrimSpace(in.Text())
		f := strings.Fields(line)
		cmd := ""
		if len(f) > 0 {
			cmd = strings.ToLower(f[0])
		}
		var err error
		switch cmd {
		case "", "ok", "accept":
			t := d.Totals()
			if t.Blocking > 0 {
				err = fmt.Errorf("%d entries with evidence are unassigned: `a N BUCKET` or `x N` first", t.Blocking)
				break
			}
			if len(d.Worklogs()) == 0 {
				err = errors.New("nothing to log; use `s` to skip the day")
				break
			}
			if err = accept(ctx, st, cfg, d, o); err != nil {
				break
			}
			return true, nil
		case "s", "skip":
			if o.dryRun {
				fmt.Println("dry-run: would mark the day skipped")
				return true, nil
			}
			if err = push.Skip(ctx, st, day); err == nil {
				return true, nil
			}
		case "q", "quit":
			return false, nil
		case "e", "edit":
			var n int
			var dur time.Duration
			if n, err = argN(f, 1); err == nil {
				if dur, err = parseDur(argRest(f, 2)); err == nil {
					err = d.SetLogged(n, dur)
				}
			}
		case "m", "merge":
			var ns []int
			for _, a := range f[1:] {
				n, e := strconv.Atoi(a)
				if e != nil {
					err = fmt.Errorf("merge: %q is not a row number", a)
					break
				}
				ns = append(ns, n)
			}
			if err == nil {
				err = d.Merge(ns...)
			}
		case "a", "assign":
			var n int
			if n, err = argN(f, 1); err == nil {
				if len(f) < 3 {
					err = errors.New("assign: a N BUCKET")
				} else {
					err = d.Assign(n, strings.ToUpper(f[2]))
				}
			}
		case "t", "time", "slot":
			var n int
			if n, err = argN(f, 1); err == nil {
				var a, b time.Time
				if a, b, err = parseSlot(cfg, day, argRest(f, 2)); err == nil {
					err = d.SetSlot(n, a, b)
				}
			}
		case "d", "desc", "describe":
			var n int
			if n, err = argN(f, 1); err == nil {
				err = d.SetDesc(n, argRest(f, 2))
			}
		case "x", "drop":
			var n int
			if n, err = argN(f, 1); err == nil {
				err = d.Drop(n)
			}
		case "n", "note":
			// n BUCKET 1h30m text...
			if len(f) < 3 {
				err = errors.New("note: n BUCKET DURATION [text]")
				break
			}
			var dur time.Duration
			if dur, err = parseDur(f[2]); err == nil {
				text := argRest(f, 3)
				bk := strings.ToUpper(f[1])
				at := from.Add(6 * time.Hour)
				if err = d.AddNote(bk, dur, text, at); err == nil && !o.dryRun {
					err = st.AddNote(ctx, store.Note{TS: at, Day: day, Bucket: bk, Hours: dur.Hours(), Text: text})
				}
			}
		case "c", "claude":
			if !describe.Available() {
				err = errors.New("claude CLI not found on PATH")
				break
			}
			var rows []int
			if len(f) > 1 && f[1] == "all" {
				rows = d.Live()
			} else {
				var n int
				if n, err = argN(f, 1); err == nil {
					rows = []int{n}
				}
			}
			for _, n := range rows {
				e, gerr := d.Entry(n)
				if gerr != nil || e.Bucket == "" {
					continue
				}
				if len(rows) > 1 && !e.AutoDesc {
					continue // do not overwrite what the user typed
				}
				fmt.Printf("  … %d", n)
				text, cerr := describe.Worklog(ctx, describe.Input{
					Client: cfg.Buckets[e.Bucket].Label, Tickets: e.Tickets, Commits: e.Commits,
					Evidence: e.Detail, Current: e.Desc,
				})
				if cerr != nil {
					fmt.Printf(" ✗ %v\n", cerr)
					continue
				}
				_ = d.SetDesc(n, text)
				fmt.Printf(" ✓ %s\n", text)
			}
		case "?", "h", "help":
			printHelp()
			continue
		default:
			err = fmt.Errorf("unknown command %q (? for help)", cmd)
		}
		if err != nil {
			fmt.Printf("  ! %v\n", err)
		}
	}
}

func accept(ctx context.Context, st *store.Store, cfg *config.Config, d *reconcile.Day, o reconcileOpts) error {
	if o.dryRun {
		fmt.Println("dry-run: would push")
		for _, w := range d.Worklogs() {
			fmt.Printf("  %s %-7s %6s  %s\n", w.Start.Format("15:04"), w.Issue, hmD(time.Duration(w.Seconds)*time.Second), w.Desc)
		}
		return nil
	}
	results, failed, err := push.Accept(ctx, st, cfg, d, o.noPush)
	printResults(results)
	if err != nil {
		return err
	}
	if o.noPush {
		fmt.Printf("stored %d drafts for %s; `vura push %s` when ready\n", len(d.Worklogs()), d.Day, d.Day)
		return nil
	}
	if failed > 0 {
		fmt.Printf("%d worklogs failed; day stays pending, retry with `vura push %s`\n", failed, d.Day)
	}
	return nil
}

func printResults(results []push.Result) {
	for _, r := range results {
		if r.Err != nil {
			fmt.Printf("  ✗ %s %-7s %6s  %v\n", r.Start.Format("15:04"), r.Issue, hmD(time.Duration(r.Seconds)*time.Second), r.Err)
		} else {
			fmt.Printf("  ✓ %s %-7s %6s  #%s\n", r.Start.Format("15:04"), r.Issue, hmD(time.Duration(r.Seconds)*time.Second), r.TempoID)
		}
	}
}

// pushDay sends every draft/failed worklog for the day. Returns how many failed.
func pushDay(ctx context.Context, st *store.Store, cfg *config.Config, day string) (int, error) {
	results, failed, err := push.Day(ctx, st, cfg, day)
	printResults(results)
	if err != nil {
		return failed, err
	}
	if len(results) == 0 {
		fmt.Printf("%s: nothing to push\n", day)
	}
	return failed, nil
}

func pushCmd(cfg *config.Config, args []string) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	var days []string
	if len(args) > 0 {
		day, err := resolveDay(cfg, args[0])
		if err != nil {
			return err
		}
		days = []string{day}
	} else {
		rows, err := st.DB().QueryContext(ctx, `SELECT DISTINCT day FROM worklogs WHERE state != ? ORDER BY day`, store.WorklogPushed)
		if err != nil {
			return err
		}
		for rows.Next() {
			var d string
			_ = rows.Scan(&d)
			days = append(days, d)
		}
		rows.Close()
		if len(days) == 0 {
			fmt.Println("nothing to push")
			return nil
		}
	}
	for _, day := range days {
		failed, err := pushDay(ctx, st, cfg, day)
		if err != nil {
			return err
		}
		if failed == 0 {
			if state, _ := st.DayState(ctx, day); state != store.DayDone {
				_ = st.SetDayState(ctx, day, store.DayDone)
			}
		}
	}
	return nil
}

func noteCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("note", flag.ExitOnError)
	dayArg := fs.String("day", "today", "working day the note is for")
	_ = fs.Parse(flagsFirst(fs, args))
	if fs.NArg() < 2 {
		return errors.New("usage: vura note [--day D] BUCKET DURATION [text]")
	}
	bk := strings.ToUpper(fs.Arg(0))
	if _, ok := cfg.Buckets[bk]; !ok {
		return fmt.Errorf("unknown bucket %q", bk)
	}
	dur, err := parseDur(fs.Arg(1))
	if err != nil {
		return err
	}
	day, err := resolveDay(cfg, *dayArg)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	text := strings.Join(fs.Args()[2:], " ")
	if err := st.AddNote(context.Background(), store.Note{TS: time.Now(), Day: day, Bucket: bk, Hours: dur.Hours(), Text: text}); err != nil {
		return err
	}
	fmt.Printf("noted %s %s for %s\n", bk, hmD(dur), day)
	return nil
}

// nagCmd is what the timers run: notify if days are pending within nag_days.
func nagCmd(cfg *config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	days, err := pendingDays(ctx, st, cfg, bucket.New(cfg))
	if err != nil {
		return err
	}
	cutoff := cfg.Boundary.Day(time.Now().In(cfg.Location).AddDate(0, 0, -cfg.Day.NagDays))
	recent := 0
	for _, d := range days {
		if d >= cutoff {
			recent++
		}
	}
	if recent == 0 {
		return nil
	}
	msg := fmt.Sprintf("%d day pending", recent)
	if recent > 1 {
		msg = fmt.Sprintf("%d days pending", recent)
	}
	fmt.Println(msg)
	_ = execNotify("vura", msg+" · run `vura reconcile`")
	return nil
}

// --- screen -------------------------------------------------------------------

func printScreen(cfg *config.Config, d *reconcile.Day, pos string) {
	t := d.Totals()
	date, _ := time.ParseInLocation("2006-01-02", d.Day, cfg.Location)
	wall := "-"
	if !t.WallStart.IsZero() {
		wall = hmD(t.WallEnd.Sub(t.WallStart)) + " wall"
	}
	fmt.Printf("\n── %s ── %s · %s observed · %s logged", date.Format("Mon 2 Jan"), wall, hmD(t.Observed), hmD(t.Logged))
	if gain := t.Logged - t.Observed; gain > 0 {
		fmt.Printf(" · rounding +%s", hmD(gain))
	}
	fmt.Printf(" ── %s ──\n\n", pos)
	round := cfg.Session.RoundMin.Duration
	for _, e := range d.Entries {
		if e.Dropped {
			continue
		}
		issue, name := e.Issue, ""
		if b, ok := cfg.Buckets[e.Bucket]; ok {
			name = b.Label
		}
		switch {
		case e.Kind == "presence":
			issue, name = "·", "unattributed"
		case e.Kind == "call" && e.Bucket == "":
			issue, name = "☎", "call"
		case e.Kind == "unmapped":
			issue, name = "?", "unmapped"
		}
		flags := ""
		if e.Remote {
			flags += "  ⇡remote"
		}
		if e.Shared {
			flags += fmt.Sprintf("  shared (%s of %s)", hmD(e.Observed), hmD(e.End.Sub(e.Start)))
		}
		if e.Identity {
			flags += "  moments only, 0 logged · e N DUR to log"
		} else if e.Observed < round && e.Logged == round {
			flags += fmt.Sprintf("  %s→%s", hmD(e.Observed), hmD(e.Logged))
		} else if e.Logged != session.RoundUp(e.Observed, round) {
			flags += fmt.Sprintf("  edited (%s observed)", hmD(e.Observed))
		}
		if len(e.Merged) > 0 {
			flags += fmt.Sprintf("  merged %v", e.Merged)
		}
		fmt.Printf(" %2d  %-7s %-13s %6s   %s–%s%s\n", e.N, issue, name, hmD(e.Logged),
			e.Start.Format("15:04"), e.End.Format("15:04"), flags)
		if e.Detail != "" {
			fmt.Printf("     %s\n", e.Detail)
		}
		switch {
		case e.Bucket == "":
			fmt.Printf("     └ needs [a]ssign or [x] drop\n")
		case e.Desc == "":
			fmt.Printf("     └ (needs a description)\n")
		case e.AutoDesc:
			fmt.Printf("     └ \"%s\"\n", e.Desc)
		default:
			fmt.Printf("     └ “%s”\n", e.Desc)
		}
	}
	if t.Unassigned > 0 {
		fmt.Printf("\n %d unassigned", t.Unassigned)
		if t.Blocking > 0 {
			fmt.Printf(" (%d with evidence, must be assigned or dropped)", t.Blocking)
		}
		fmt.Println()
	}
	fmt.Println("\n [enter] accept · e N DUR · t N HH:MM-HH:MM · m N M.. · a N BUCKET · d N text · c N|all · x N · n BUCKET DUR text · s skip · q quit · ? help")
}

func printHelp() {
	fmt.Print(`
  enter / ok       accept: push every assigned entry to Tempo, mark the day done
  e N 1h30m        set logged time of entry N (observed time is kept for the record)
  m N M [K..]      merge entries into N: observed adds up, rounding applies once
  a N BUCKET       assign an unattributed / call / unmapped entry to a bucket
  t N 10:15-11:30  move entry N to another time window (observed and logged follow)
  d N text         set the description of entry N
  c N | c all      ask Claude to word the description (language only; hours never change)
  x N              drop entry N (it will not be logged)
  n BUCKET 1h txt  add a note entry for work nothing observed (phone call, whiteboard)
  s                skip: nothing logged for this day
  q                quit; the day stays pending

`)
}

// --- parsing helpers -------------------------------------------------------------

// flagsFirst lets flags follow positionals (`vura reconcile 2026-09-07 --dry-run`);
// package flag stops at the first non-flag otherwise. A value flag keeps the
// argument that follows it. Everything after "--" is positional.
func flagsFirst(fs *flag.FlagSet, args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || len(a) == 1 {
			pos = append(pos, a)
			continue
		}
		flags = append(flags, a)
		name := strings.TrimLeft(a, "-")
		if strings.Contains(name, "=") {
			continue
		}
		if f := fs.Lookup(name); f != nil && !isBool(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, pos...)
}

func isBool(f *flag.Flag) bool {
	b, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && b.IsBoolFlag()
}

func argN(f []string, i int) (int, error) {
	if len(f) <= i {
		return 0, errors.New("missing row number")
	}
	n, err := strconv.Atoi(f[i])
	if err != nil {
		return 0, fmt.Errorf("%q is not a row number", f[i])
	}
	return n, nil
}

func argRest(f []string, i int) string {
	if len(f) <= i {
		return ""
	}
	return strings.Join(f[i:], " ")
}

// parseSlot turns "10:15-11:30" into two instants on the working day; an end
// earlier than the start means past midnight.
func parseSlot(cfg *config.Config, day, s string) (time.Time, time.Time, error) {
	a, b, ok := strings.Cut(strings.TrimSpace(s), "-")
	if !ok {
		return time.Time{}, time.Time{}, errors.New("want HH:MM-HH:MM")
	}
	from, _, err := cfg.Boundary.Range(day, cfg.Location)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	at := func(hhmm string) (time.Time, error) {
		t, err := time.ParseInLocation("15:04", strings.TrimSpace(hhmm), cfg.Location)
		if err != nil {
			return time.Time{}, fmt.Errorf("%q: want HH:MM", hhmm)
		}
		d := time.Date(from.Year(), from.Month(), from.Day(), t.Hour(), t.Minute(), 0, 0, cfg.Location)
		if d.Before(from) {
			d = d.AddDate(0, 0, 1) // past midnight, before the 03:00 boundary
		}
		return d, nil
	}
	x, err := at(a)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	y, err := at(b)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if !y.After(x) {
		y = y.AddDate(0, 0, 1)
	}
	return x, y, nil
}

// parseDur accepts 1h30m, 45m, 1.5h, 2h, or a bare decimal meaning hours.
func parseDur(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("missing duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	if strings.HasSuffix(s, "h") {
		s = strings.TrimSuffix(s, "h")
	}
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		return time.Duration(v * float64(time.Hour)), nil
	}
	return 0, fmt.Errorf("duration %q: want 1h30m, 45m or 1.5", s)
}

// execNotify sends a desktop notification; failures are ignored.
func execNotify(summary, body string) error {
	cmd := execCommand("notify-send", "--app-name=vura", "--icon=appointment-soon", summary, body)
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	return cmd.Run()
}

// checkCmd verifies credentials and resolves every bucket issue, read-only.
func checkCmd(cfg *config.Config) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	cl, err := push.Client(ctx, st, cfg)
	if err != nil {
		return err
	}
	acct, err := cl.AccountID(ctx)
	if err != nil {
		return fmt.Errorf("jira login failed: %w", err)
	}
	fmt.Printf("jira   %s as %s (account %s)\n", cfg.Identity.JiraSite, cfg.Identity.JiraEmail, acct)
	names := make([]string, 0, len(cfg.Buckets))
	for n := range cfg.Buckets {
		names = append(names, n)
	}
	sort.Strings(names)
	bad := 0
	for _, n := range names {
		b := cfg.Buckets[n]
		if b.Issue == "" {
			fmt.Printf("  %-7s %-8s no issue configured\n", n, "-")
			bad++
			continue
		}
		id, err := cl.IssueID(ctx, b.Issue)
		if err != nil {
			fmt.Printf("  %-7s %-8s ✗ %v\n", n, b.Issue, err)
			bad++
			continue
		}
		fmt.Printf("  %-7s %-8s ✓ id %s\n", n, b.Issue, id)
	}
	if err := cl.Ping(ctx); err != nil {
		fmt.Printf("tempo  ✗ %v\n", err)
		bad++
	} else {
		fmt.Println("tempo  ✓ token accepted")
	}
	if bad > 0 {
		return fmt.Errorf("%d problems", bad)
	}
	return nil
}
