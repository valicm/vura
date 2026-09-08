// Package push is the write path to Tempo, shared by the CLI and the
// dashboard: build a client from the keyring, push a day's drafts, amend a
// pushed day. Every remote change leaves an audit row.
package push

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/reconcile"
	"github.com/valicm/vura/internal/secrets"
	"github.com/valicm/vura/internal/store"
	"github.com/valicm/vura/internal/tempo"
)

// Client builds a Tempo/Jira client with issue ids cached in the store.
func Client(ctx context.Context, st *store.Store, cfg *config.Config) (*tempo.Client, error) {
	if cfg.Identity.JiraSite == "" || cfg.Identity.JiraEmail == "" {
		return nil, errors.New("identity.jira_site and identity.jira_email (or emails) must be set")
	}
	tt, err := secrets.Get("tempo")
	if err != nil {
		return nil, err
	}
	jt, err := secrets.Get("jira")
	if err != nil {
		return nil, err
	}
	cl := tempo.New(tt, cfg.Identity.JiraSite, cfg.Identity.JiraEmail, jt)
	cl.GetCached = func(key string) (string, bool) {
		v, err := st.GetState(ctx, "jira.issue."+key)
		return v, err == nil && v != ""
	}
	cl.SetCached = func(key, id string) { _ = st.SetState(ctx, "jira.issue."+key, id) }
	return cl, nil
}

// Result is one worklog's outcome.
type Result struct {
	Start   time.Time
	Issue   string
	Seconds int
	Desc    string
	TempoID string
	Err     error
}

// Day pushes every draft or failed worklog of the day. Returns per-worklog
// results; Failed counts the ones that did not land.
func Day(ctx context.Context, st *store.Store, cfg *config.Config, day string) (results []Result, failed int, err error) {
	wls, err := st.WorklogsForDay(ctx, day)
	if err != nil {
		return nil, 0, err
	}
	var todo []store.Worklog
	for _, w := range wls {
		if w.State != store.WorklogPushed {
			todo = append(todo, w)
		}
	}
	if len(todo) == 0 {
		return nil, 0, nil
	}
	cl, err := Client(ctx, st, cfg)
	if err != nil {
		return nil, len(todo), err
	}
	for _, w := range todo {
		r := Result{Start: w.Start.In(cfg.Location), Issue: w.Issue, Seconds: w.Seconds, Desc: w.Desc}
		id, err := cl.Create(ctx, tempo.Worklog{IssueKey: w.Issue, Start: w.Start.In(cfg.Location), Seconds: w.Seconds, Description: w.Desc})
		if err != nil {
			r.Err = err
			failed++
			_ = st.MarkFailed(ctx, w.ID, err.Error())
			_ = st.Audit(ctx, "tempo.failed", fmt.Sprintf("%s %s %ds: %v", day, w.Issue, w.Seconds, err))
		} else {
			r.TempoID = id
			_ = st.MarkPushed(ctx, w.ID, id)
			_ = st.Audit(ctx, "tempo.create", fmt.Sprintf("%s %s %s %ds %q", day, id, w.Issue, w.Seconds, w.Desc))
		}
		results = append(results, r)
	}
	return results, failed, nil
}

// Accept stores the day's entries as drafts and pushes them. With noPush it
// stores drafts and marks the day done for a later `vura push`.
func Accept(ctx context.Context, st *store.Store, cfg *config.Config, d *reconcile.Day, noPush bool) (results []Result, failed int, err error) {
	t := d.Totals()
	if t.Blocking > 0 {
		return nil, 0, fmt.Errorf("%d entries with evidence are unassigned; assign or drop them first", t.Blocking)
	}
	wls := d.Worklogs()
	if len(wls) == 0 {
		return nil, 0, errors.New("nothing to log; skip the day instead")
	}
	if err := st.ReplaceDrafts(ctx, d.Day, wls); err != nil {
		return nil, 0, err
	}
	if noPush {
		return nil, 0, st.SetDayState(ctx, d.Day, store.DayDone)
	}
	results, failed, err = Day(ctx, st, cfg, d.Day)
	if err != nil {
		return results, failed, err
	}
	if failed > 0 {
		return results, failed, nil
	}
	_ = st.Audit(ctx, "day.done", d.Day)
	return results, 0, st.SetDayState(ctx, d.Day, store.DayDone)
}

// Settle marks a day done if every worklog is pushed; returns whether it did.
func Settle(ctx context.Context, st *store.Store, day string) (bool, error) {
	wls, err := st.WorklogsForDay(ctx, day)
	if err != nil {
		return false, err
	}
	if len(wls) == 0 {
		return false, nil
	}
	for _, w := range wls {
		if w.State != store.WorklogPushed {
			return false, nil
		}
	}
	_ = st.Audit(ctx, "day.done", day)
	return true, st.SetDayState(ctx, day, store.DayDone)
}

// Pushed lists the day's worklogs that exist in Tempo.
func Pushed(ctx context.Context, st *store.Store, day string) ([]store.Worklog, error) {
	wls, err := st.WorklogsForDay(ctx, day)
	if err != nil {
		return nil, err
	}
	var out []store.Worklog
	for _, w := range wls {
		if w.State == store.WorklogPushed && w.TempoID != "" {
			out = append(out, w)
		}
	}
	return out, nil
}

// Amend deletes the day's pushed worklogs from Tempo and locally and reopens
// the day. The caller is responsible for having asked.
func Amend(ctx context.Context, st *store.Store, cfg *config.Config, day string) (deleted int, err error) {
	pushed, err := Pushed(ctx, st, day)
	if err != nil {
		return 0, err
	}
	if len(pushed) == 0 {
		return 0, st.SetDayState(ctx, day, store.DayPending)
	}
	cl, err := Client(ctx, st, cfg)
	if err != nil {
		return 0, err
	}
	for _, w := range pushed {
		if err := cl.Delete(ctx, w.TempoID); err != nil {
			return deleted, fmt.Errorf("delete tempo worklog %s: %w", w.TempoID, err)
		}
		_ = st.Audit(ctx, "tempo.delete", fmt.Sprintf("%s %s %s %ds %q", day, w.TempoID, w.Issue, w.Seconds, w.Desc))
		if err := st.DeleteWorklog(ctx, w.ID); err != nil {
			return deleted, err
		}
		deleted++
	}
	return deleted, st.SetDayState(ctx, day, store.DayPending)
}

// Skip closes a day with nothing logged.
func Skip(ctx context.Context, st *store.Store, day string) error {
	_ = st.Audit(ctx, "day.skip", day)
	return st.SetDayState(ctx, day, store.DaySkipped)
}
