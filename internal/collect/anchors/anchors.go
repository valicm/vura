// Package anchors pulls activity that happens entirely in a browser and so
// leaves no local trace: PR reviews and comments on GitHub, MR approvals and
// comments on GitLab, ticket transitions and comments on Jira. Each becomes
// a timestamped anchor: identity and a moment, never a duration.
package anchors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/secrets"
	"github.com/valicm/vura/internal/store"
)

// Source fetches anchors since a time.
type Source interface {
	Name() string
	Fetch(ctx context.Context, since time.Time) ([]store.Anchor, error)
}

type Runner struct {
	store   *store.Store
	sources []Source
	since   time.Duration
	log     *slog.Logger
	down    map[string]bool // sources currently failing, to log transitions not repeats
}

// maxCatchup bounds how far back a source looks after a long outage (a VPN
// that was off for a week, a token that expired).
const maxCatchup = 30 * 24 * time.Hour

// New builds sources from config. A source whose token is missing is
// skipped with one warning; the rest still run.
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Runner {
	r := &Runner{store: st, since: cfg.Anchors.Since.Duration, log: log, down: map[string]bool{}}
	if u := cfg.Anchors.GitHub; u != "" {
		if tok, err := secrets.Get("github"); err == nil {
			r.sources = append(r.sources, &GitHub{User: u, Token: tok, HTTP: client()})
		} else {
			log.Warn("anchors: github skipped", "err", err)
		}
	}
	if base := cfg.Anchors.GitLab; base != "" {
		if tok, err := secrets.Get("gitlab"); err == nil {
			r.sources = append(r.sources, &GitLab{Base: base, Token: tok, HTTP: client(), Cache: st})
		} else {
			log.Warn("anchors: gitlab skipped", "err", err)
		}
	}
	for _, w := range cfg.Anchors.Slack {
		tok, err := secrets.Get("slack:" + w.Name)
		if err != nil {
			log.Warn("anchors: slack skipped", "workspace", w.Name, "err", err)
			continue
		}
		r.sources = append(r.sources, &Slack{WS: w.Name, Token: tok, HTTP: client(), Cache: st})
	}
	for _, j := range cfg.Anchors.Jira {
		// An Atlassian API token is per account, not per site: the "jira" key
		// used for Tempo works on every site that account can see. A
		// site-specific key wins when present (a different account).
		tok, err := secrets.Get("jira:" + j.Site)
		if err != nil {
			tok, err = secrets.Get("jira")
		}
		if err != nil {
			log.Warn("anchors: jira skipped", "site", j.Site, "err", err)
			continue
		}
		r.sources = append(r.sources, &Jira{Site: j.Site, Email: j.Email, Token: tok, Projects: j.Projects, HTTP: client()})
	}
	return r
}

func client() *http.Client { return &http.Client{Timeout: 60 * time.Second} }

func (r *Runner) Run(ctx context.Context, every time.Duration) {
	if len(r.sources) == 0 {
		return
	}
	t := time.NewTicker(every)
	defer t.Stop()
	r.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.poll(ctx)
		}
	}
}

func (r *Runner) poll(ctx context.Context) {
	now := time.Now()
	for _, src := range r.sources {
		key := "anchors.ok." + src.Name()
		// Look back the configured window, or to the last successful fetch if
		// that is older: a self-hosted GitLab behind a VPN can be dark for
		// days and must catch up when it returns.
		since := now.Add(-r.since)
		if v, err := r.store.GetState(ctx, key); err == nil && v != "" {
			if last, err := time.Parse(time.RFC3339, v); err == nil && last.Add(-time.Hour).Before(since) {
				since = last.Add(-time.Hour)
			}
		}
		if floor := now.Add(-maxCatchup); since.Before(floor) {
			since = floor
		}
		as, err := src.Fetch(ctx, since)
		if err != nil {
			if !r.down[src.Name()] {
				r.log.Warn("anchors: source unreachable, will retry hourly", "source", src.Name(), "err", err)
				r.down[src.Name()] = true
			}
			continue
		}
		if r.down[src.Name()] {
			r.log.Info("anchors: source back", "source", src.Name(), "since", since.Format(time.RFC3339))
			r.down[src.Name()] = false
		}
		n := 0
		for _, a := range as {
			if a.TS.Before(since) {
				continue
			}
			if err := r.store.UpsertAnchor(ctx, a); err != nil {
				r.log.Error("anchors: store", "err", err)
				continue
			}
			n++
		}
		_ = r.store.SetState(ctx, key, now.Format(time.RFC3339))
		_ = r.store.SetState(ctx, "collector.ok."+src.Name(), now.Format(time.RFC3339))
		r.log.Debug("anchors: synced", "source", src.Name(), "rows", n, "since", since.Format(time.RFC3339))

		if es, ok := src.(EventSource); ok {
			// Meetings are few and their message keeps the meeting's start
			// time, so always scan the whole window, not just since last run.
			evSince := now.Add(-r.since)
			evs, err := es.Events(ctx, evSince)
			ekey := "events:" + src.Name()
			if err != nil {
				if !r.down[ekey] {
					r.log.Warn("anchors: meetings unavailable (missing scopes?)", "source", src.Name(), "err", err)
					r.down[ekey] = true
				}
				continue
			}
			r.down[ekey] = false
			if err := r.store.ReplaceEvents(ctx, "slack:"+strings.TrimPrefix(src.Name(), "slack:"), evSince, now.Add(24*time.Hour), evs); err != nil {
				r.log.Error("anchors: store meetings", "err", err)
				continue
			}
			r.log.Debug("anchors: meetings", "source", src.Name(), "rows", len(evs))
		}
	}
}

// getJSON is the shared HTTP helper.
func getJSON(ctx context.Context, c *http.Client, url string, auth func(*http.Request), out any) (http.Header, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	auth(req)
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b := string(body)
		if len(b) > 200 {
			b = b[:200]
		}
		return resp.Header, fmt.Errorf("GET %s: HTTP %d: %s", url, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return resp.Header, fmt.Errorf("GET %s: decode: %w", url, err)
		}
	}
	return resp.Header, nil
}
