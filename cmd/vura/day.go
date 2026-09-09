package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/session"
	"github.com/valicm/vura/internal/store"
)

func bucketResolver(cfg *config.Config) *bucket.Resolver { return bucket.New(cfg) }

// resolveDay accepts YYYY-MM-DD, "today", "yesterday", or an offset like "-2".
func resolveDay(cfg *config.Config, arg string) (string, error) {
	now := time.Now().In(cfg.Location)
	switch arg {
	case "", "today":
		return cfg.Boundary.Day(now), nil
	case "yesterday":
		return cfg.Boundary.Day(now.AddDate(0, 0, -1)), nil
	}
	if strings.HasPrefix(arg, "-") {
		var n int
		if _, err := fmt.Sscanf(arg, "-%d", &n); err == nil {
			return cfg.Boundary.Day(now.AddDate(0, 0, -n)), nil
		}
	}
	if _, err := time.ParseInLocation("2006-01-02", arg, cfg.Location); err != nil {
		return "", fmt.Errorf("day %q: want YYYY-MM-DD, today, yesterday or -N", arg)
	}
	return arg, nil
}

// dayCmd prints one working day as sessions and persists them.
func dayCmd(cfg *config.Config, arg string, persist bool) error {
	day, err := resolveDay(cfg, arg)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	res := bucket.New(cfg)
	ss, _, err := session.Day(ctx, st, cfg, res, day)
	if err != nil {
		return err
	}
	if persist {
		if err := st.ReplaceSessions(ctx, day, session.ToStore(day, cfg.Identity.Device, ss)); err != nil {
			return err
		}
	}
	printDay(cfg, day, ss)
	return nil
}

func printDay(cfg *config.Config, day string, ss []session.Session) {
	round := cfg.Session.RoundMin.Duration
	var observed, logged, wallStart, wallEnd = time.Duration(0), time.Duration(0), time.Time{}, time.Time{}
	for _, s := range ss {
		if s.Bucket == session.BucketUnattributed || s.Bucket == session.BucketCall {
			continue
		}
		observed += s.Duration()
		logged += session.RoundUp(s.Duration(), round)
	}
	for _, s := range ss {
		if wallStart.IsZero() || s.Start.Before(wallStart) {
			wallStart = s.Start
		}
		if s.End.After(wallEnd) {
			wallEnd = s.End
		}
	}
	d, _ := time.ParseInLocation("2006-01-02", day, cfg.Location)
	wall := "-"
	if !wallStart.IsZero() {
		wall = fmt.Sprintf("%s–%s (%s)", wallStart.Format("15:04"), wallEnd.Format("15:04"), hmD(wallEnd.Sub(wallStart)))
	}
	fmt.Printf("── %s ── wall %s · observed %s · logged %s", d.Format("Mon 2 Jan"), wall, hmD(observed), hmD(logged))
	if gain := logged - observed; gain > 0 {
		fmt.Printf(" · rounding +%s", hmD(gain))
	}
	fmt.Println()
	if len(ss) == 0 {
		fmt.Println("   nothing observed")
		return
	}
	for _, s := range ss {
		name, issue := bucketName(cfg, s.Bucket)
		flags := ""
		if s.Remote {
			flags += " ⇡remote"
		}
		if s.Shared {
			flags += fmt.Sprintf(" shared (%s of %s)", hmD(s.Duration()), hmD(s.Span()))
		}
		if s.PointsOnly {
			flags += " moments only, not counted"
		} else if s.Duration() < round {
			flags += fmt.Sprintf(" %s→%s", hmD(s.Duration()), hmD(round))
		}
		fmt.Printf(" %-7s %-13s %6s   %s–%s%s\n", issue, name, hmD(s.Duration()),
			s.Start.Format("15:04"), s.End.Format("15:04"), flags)
		if detail := s.Summary(); detail != "" {
			fmt.Printf("         %s\n", detail)
		}
	}
}

func bucketName(cfg *config.Config, b string) (name, issue string) {
	switch b {
	case session.BucketUnattributed:
		return "unattributed", "·"
	case session.BucketUnmapped:
		return "unmapped", "?"
	case session.BucketCall:
		return "call", "☎"
	}
	if bk, ok := cfg.Buckets[b]; ok {
		name = bk.Label
		if name == "" {
			name = b
		}
		return name, bk.Issue
	}
	return b, "?"
}

func hmD(d time.Duration) string {
	if d <= 0 {
		return "0m"
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h == 0 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh%02dm", h, m)
}

// statusCmd: one line per bucket per day for the last N days.
func statusCmd(cfg *config.Config, days int) error {
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx := context.Background()
	res := bucket.New(cfg)
	now := time.Now().In(cfg.Location)
	for i := days - 1; i >= 0; i-- {
		day := cfg.Boundary.Day(now.AddDate(0, 0, -i))
		ss, _, err := session.Day(ctx, st, cfg, res, day)
		if err != nil {
			return err
		}
		if len(ss) == 0 {
			continue
		}
		per := map[string]time.Duration{}
		remote := map[string]bool{}
		var order []string
		for _, s := range ss {
			if _, ok := per[s.Bucket]; !ok {
				order = append(order, s.Bucket)
			}
			per[s.Bucket] += s.Duration()
			if s.Remote {
				remote[s.Bucket] = true
			}
		}
		sort.Slice(order, func(a, b int) bool { return per[order[a]] > per[order[b]] })
		d, _ := time.ParseInLocation("2006-01-02", day, cfg.Location)
		var line []string
		var total time.Duration
		for _, b := range order {
			name, _ := bucketName(cfg, b)
			if b != session.BucketUnattributed && b != session.BucketCall {
				total += per[b]
			}
			flag := ""
			if remote[b] {
				flag = "⇡"
			}
			line = append(line, fmt.Sprintf("%s %s%s", name, hmD(per[b]), flag))
		}
		fmt.Printf("%s %s  %6s   %s\n", d.Format("Mon"), day, hmD(total), strings.Join(line, " · "))
	}
	fmt.Fprintln(os.Stderr, "\ntotal = observed bucket time, overlap counted per bucket; excludes unattributed and calls. ⇡ = includes remote (idle keyboard) Claude work.")
	return nil
}
