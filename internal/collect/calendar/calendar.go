// Package calendar pulls an iCalendar feed (Proton Calendar's share link,
// or any .ics URL) and stores concrete occurrences. Meetings have real start
// and end times, which is what makes a call attributable to a client.
package calendar

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/valicm/vura/internal/ics"
	"github.com/valicm/vura/internal/store"
)

type Collector struct {
	name   string
	url    string
	bucket string // default for events no rule claims
	store  *store.Store
	loc    *time.Location
	log    *slog.Logger
	http   *http.Client
	down   bool
	// Window expanded on every fetch.
	Back, Ahead time.Duration
}

func New(name, url, bucket string, st *store.Store, loc *time.Location, log *slog.Logger) *Collector {
	return &Collector{name: name, url: url, bucket: bucket, store: st, loc: loc, log: log,
		http: &http.Client{Timeout: 60 * time.Second}, Back: 30 * 24 * time.Hour, Ahead: 7 * 24 * time.Hour}
}

func (c *Collector) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	c.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.poll(ctx)
		}
	}
}

func (c *Collector) poll(ctx context.Context) {
	n, err := c.Fetch(ctx)
	if err != nil {
		if !c.down {
			c.log.Warn("calendar: fetch failed, will retry", "feed", c.name, "err", err)
			c.down = true
		}
		return
	}
	if c.down {
		c.log.Info("calendar: feed back", "feed", c.name)
		c.down = false
	}
	_ = c.store.SetState(ctx, "collector.ok.calendar:"+c.name, time.Now().Format(time.RFC3339))
	c.log.Debug("calendar: synced", "feed", c.name, "events", n)
}

// Fetch downloads the feed and replaces stored events inside the window.
func (c *Collector) Fetch(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "text/calendar")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, err
	}
	if !strings.Contains(string(body[:min(len(body), 200)]), "BEGIN:VCALENDAR") {
		return 0, fmt.Errorf("not an iCalendar feed (starts %q)", string(body[:min(len(body), 40)]))
	}
	now := time.Now()
	from, to := now.Add(-c.Back), now.Add(c.Ahead)
	evs, err := ics.Parse(string(body), from, to, c.loc)
	if err != nil {
		return 0, err
	}
	rows := make([]store.Event, 0, len(evs))
	seen := map[string]bool{}
	for _, e := range evs {
		// Google exports a re-created recurring series alongside the old
		// open-ended one; identical title and times are one meeting.
		key := e.Title + "|" + e.Start.UTC().Format(time.RFC3339) + "|" + e.End.UTC().Format(time.RFC3339)
		if seen[key] {
			continue
		}
		seen[key] = true
		rows = append(rows, store.Event{ID: c.name + ":" + e.ID(), Start: e.Start, End: e.End, Title: e.Title,
			Attendees: strings.Join(e.Attendees, ","), Bucket: c.bucket})
	}
	return len(rows), c.store.ReplaceEvents(ctx, "ics:"+c.name, from, to, rows)
}
