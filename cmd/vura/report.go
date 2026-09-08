package main

import (
	"context"
	"flag"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/store"
)

// domainsCmd lists the busiest browser domains no bucket claims.
func domainsCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("domains", flag.ExitOnError)
	days := fs.Int("days", 7, "look back this many days")
	_ = fs.Parse(flagsFirst(fs, args))
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	since := time.Now().AddDate(0, 0, -*days).Unix()
	rows, err := st.DB().QueryContext(context.Background(), `
		SELECT lower(entity), COUNT(DISTINCT CAST(ts/60 AS INTEGER)) m FROM heartbeats
		WHERE type IN ('domain','url') AND ts >= ? GROUP BY 1 ORDER BY m DESC LIMIT 200`, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	res := bucketResolver(cfg)
	fmt.Printf("%-6s %-8s %s\n", "MIN", "BUCKET", "DOMAIN")
	for rows.Next() {
		var ent string
		var m int
		if err := rows.Scan(&ent, &m); err != nil {
			return err
		}
		b := res.ByDomain(ent)
		if b == "" {
			b = "-"
		}
		if i := strings.Index(ent, "://"); i >= 0 {
			ent = ent[i+3:]
		}
		fmt.Printf("%-6d %-8s %s\n", m, b, ent)
	}
	return rows.Err()
}

// reportCmd summarises pushed worklogs for a month.
func reportCmd(cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	month := fs.String("month", time.Now().In(cfg.Location).Format("2006-01"), "YYYY-MM")
	htmlOut := fs.String("html", "", "write an HTML report to this file")
	all := fs.Bool("all", false, "include draft and failed worklogs, not just pushed")
	_ = fs.Parse(flagsFirst(fs, args))
	first, err := time.ParseInLocation("2006-01", *month, cfg.Location)
	if err != nil {
		return fmt.Errorf("--month: want YYYY-MM")
	}
	last := first.AddDate(0, 1, 0)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	q := `SELECT day, bucket, issue, start, seconds, observed, COALESCE(desc,''), state FROM worklogs WHERE day >= ? AND day < ?`
	if !*all {
		q += ` AND state = 'pushed'`
	}
	rows, err := st.DB().QueryContext(context.Background(), q+` ORDER BY day, start`, first.Format("2006-01-02"), last.Format("2006-01-02"))
	if err != nil {
		return err
	}
	defer rows.Close()
	type wl struct {
		day, bucket, issue, desc, state string
		start                           time.Time
		seconds, observed               int
	}
	var wls []wl
	perBucket := map[string]int{}
	perDay := map[string]int{}
	total, observed := 0, 0
	for rows.Next() {
		var w wl
		var start int64
		if err := rows.Scan(&w.day, &w.bucket, &w.issue, &start, &w.seconds, &w.observed, &w.desc, &w.state); err != nil {
			return err
		}
		w.start = time.Unix(start, 0).In(cfg.Location)
		wls = append(wls, w)
		perBucket[w.bucket] += w.seconds
		perDay[w.day] += w.seconds
		total += w.seconds
		observed += w.observed
	}
	buckets := make([]string, 0, len(perBucket))
	for b := range perBucket {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool { return perBucket[buckets[i]] > perBucket[buckets[j]] })

	fmt.Printf("%s · %d worklogs · %d days · logged %s · observed %s\n\n", first.Format("January 2006"), len(wls), len(perDay),
		hm(total), hm(observed))
	for _, b := range buckets {
		label := cfg.Buckets[b].Label
		if label == "" {
			label = b
		}
		fmt.Printf("  %-8s %-14s %8s\n", cfg.Buckets[b].Issue, label, hm(perBucket[b]))
	}
	if *htmlOut == "" {
		return nil
	}
	var sb strings.Builder
	sb.WriteString("<!doctype html><meta charset=utf-8><title>vura " + html.EscapeString(*month) + "</title>")
	sb.WriteString("<style>body{font:14px system-ui;margin:2rem;max-width:60rem}table{border-collapse:collapse;width:100%}td,th{padding:4px 8px;border-bottom:1px solid #ddd;text-align:left;vertical-align:top}th{font-size:12px;text-transform:uppercase;color:#666}.r{text-align:right}h2{margin-top:2rem}</style>")
	fmt.Fprintf(&sb, "<h1>%s</h1><p>%d worklogs over %d days · logged <b>%s</b> · observed %s</p>", first.Format("January 2006"), len(wls), len(perDay), hm(total), hm(observed))
	sb.WriteString("<table><tr><th>Issue</th><th>Client</th><th class=r>Hours</th></tr>")
	for _, b := range buckets {
		label := cfg.Buckets[b].Label
		if label == "" {
			label = b
		}
		fmt.Fprintf(&sb, "<tr><td>%s</td><td>%s</td><td class=r>%s</td></tr>", html.EscapeString(cfg.Buckets[b].Issue), html.EscapeString(label), hm(perBucket[b]))
	}
	sb.WriteString("</table>")
	for _, b := range buckets {
		label := cfg.Buckets[b].Label
		if label == "" {
			label = b
		}
		fmt.Fprintf(&sb, "<h2>%s · %s</h2><table><tr><th>Day</th><th>Start</th><th class=r>Hours</th><th>Description</th></tr>", html.EscapeString(label), hm(perBucket[b]))
		for _, w := range wls {
			if w.bucket != b {
				continue
			}
			fmt.Fprintf(&sb, "<tr><td>%s</td><td>%s</td><td class=r>%s</td><td>%s</td></tr>", w.day, w.start.Format("15:04"), hm(w.seconds), html.EscapeString(w.desc))
		}
		sb.WriteString("</table>")
	}
	if err := os.WriteFile(*htmlOut, []byte(sb.String()), 0o600); err != nil {
		return err
	}
	fmt.Printf("\nwrote %s\n", *htmlOut)
	return nil
}
