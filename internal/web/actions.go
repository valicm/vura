package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/describe"
	"github.com/valicm/vura/internal/push"
	"github.com/valicm/vura/internal/reconcile"
	"github.com/valicm/vura/internal/session"
	"github.com/valicm/vura/internal/store"
)

// sameOrigin refuses writes that did not come from the dashboard page
// itself. Another site open in the same browser could otherwise POST to the
// loopback port. A custom header forces a CORS preflight, which the WakaTime
// CORS policy does not grant to /api/vura/, and the Origin check covers the
// rest.
func sameOrigin(r *http.Request) error {
	if r.Header.Get("X-Vura") != "1" {
		return errors.New("missing X-Vura header")
	}
	if o := r.Header.Get("Origin"); o != "" {
		host := strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://")
		if host != r.Host {
			return fmt.Errorf("cross-origin request from %s", o)
		}
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return fmt.Errorf("sec-fetch-site %s", sfs)
	}
	return nil
}

// build loads a day and replays its stored edits.
func (h *Handler) build(ctx context.Context, day string) (*reconcile.Day, int, error) {
	res := bucket.New(h.cfg)
	ss, _, err := session.Day(ctx, h.st, h.cfg, res, day)
	if err != nil {
		return nil, 0, err
	}
	from, to, _ := h.cfg.Boundary.Range(day, h.cfg.Location)
	commits, _ := h.st.CommitsRange(ctx, from, to)
	notes, _ := h.st.NotesForDay(ctx, day)
	d := reconcile.New(h.cfg, day, ss, commits, notes)
	ops, err := reconcile.LoadOps(ctx, h.st, day)
	if err != nil {
		return nil, 0, err
	}
	skipped := d.Replay(ops)
	return d, skipped, nil
}

type opRequest struct {
	Op     string   `json:"op"`
	Target string   `json:"target"`
	Others []string `json:"others"`
	Dur    string   `json:"dur"`
	Text   string   `json:"text"`
	Bucket string   `json:"bucket"`
	At     string   `json:"at"`
	All    bool     `json:"all"` // claude: every auto-described entry
}

func validDay(day string, h *Handler) bool {
	_, err := time.ParseInLocation("2006-01-02", day, h.cfg.Location)
	return err == nil
}

// op applies one edit and persists the log.
func (h *Handler) op(w http.ResponseWriter, r *http.Request) {
	if err := sameOrigin(r); err != nil {
		writeErr(w, err, http.StatusForbidden)
		return
	}
	day := r.PathValue("date")
	if !validDay(day, h) {
		writeErr(w, errors.New("bad date"), 400)
		return
	}
	var req opRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeErr(w, err, 400)
		return
	}
	ctx := r.Context()
	if st, _ := h.st.DayState(ctx, day); st == store.DayDone {
		writeErr(w, errors.New("day is done; amend it first"), http.StatusConflict)
		return
	}
	if p, _ := push.Pushed(ctx, h.st, day); len(p) > 0 {
		writeErr(w, errors.New("part of this day is already in Tempo; retry or fix the failed worklogs, or amend"), http.StatusConflict)
		return
	}
	d, _, err := h.build(ctx, day)
	if err != nil {
		writeErr(w, err, 500)
		return
	}
	switch req.Op {
	case "claude":
		if !describe.Available() {
			writeErr(w, errors.New("claude CLI not found on the daemon's PATH"), 501)
			return
		}
		var targets []string
		if req.All {
			for _, n := range d.Live() {
				e, _ := d.Entry(n)
				if e != nil && e.Bucket != "" && e.AutoDesc {
					targets = append(targets, e.Key())
				}
			}
		} else {
			targets = []string{req.Target}
		}
		for _, key := range targets {
			var e *reconcile.Entry
			for _, n := range d.Live() {
				if x, _ := d.Entry(n); x != nil && x.Key() == key {
					e = x
				}
			}
			if e == nil || e.Bucket == "" {
				continue
			}
			text, err := describe.Worklog(ctx, describe.Input{Client: h.cfg.Buckets[e.Bucket].Label, Tickets: e.Tickets,
				Commits: e.Commits, Evidence: e.Detail, Current: e.Desc})
			if err != nil {
				writeErr(w, err, 502)
				return
			}
			if err := d.Apply(reconcile.Op{Kind: "desc", Target: key, Text: text}); err != nil {
				writeErr(w, err, 400)
				return
			}
		}
	case "note":
		if req.At == "" {
			from, _, _ := h.cfg.Boundary.Range(day, h.cfg.Location)
			req.At = from.Add(6 * time.Hour).Format(time.RFC3339)
		}
		dur, err := parseDur(req.Dur)
		if err != nil {
			writeErr(w, err, 400)
			return
		}
		op := reconcile.Op{Kind: "note", Bucket: strings.ToUpper(req.Bucket), Dur: dur.String(), Text: req.Text, At: req.At}
		if err := d.Apply(op); err != nil {
			writeErr(w, err, 400)
			return
		}
		at, _ := time.Parse(time.RFC3339, req.At)
		_ = h.st.AddNote(ctx, store.Note{TS: at, Day: day, Bucket: op.Bucket, Hours: dur.Hours(), Text: req.Text})
		// The note is now in the notes table and would be rebuilt on the next
		// load, so do not also keep it as an op.
		d.Ops = d.Ops[:len(d.Ops)-1]
	case "logged":
		dur, err := parseDur(req.Dur)
		if err != nil {
			writeErr(w, err, 400)
			return
		}
		if err := d.Apply(reconcile.Op{Kind: "logged", Target: req.Target, Dur: dur.String()}); err != nil {
			writeErr(w, err, 400)
			return
		}
	case "desc", "assign", "drop", "merge":
		if err := d.Apply(reconcile.Op{Kind: req.Op, Target: req.Target, Others: req.Others, Text: req.Text, Bucket: req.Bucket}); err != nil {
			writeErr(w, err, 400)
			return
		}
	default:
		writeErr(w, fmt.Errorf("unknown op %q", req.Op), 400)
		return
	}
	if err := reconcile.SaveOps(ctx, h.st, day, d.Ops); err != nil {
		writeErr(w, err, 500)
		return
	}
	h.day(w, r)
}

// reset discards the day's edits.
func (h *Handler) reset(w http.ResponseWriter, r *http.Request) {
	if err := sameOrigin(r); err != nil {
		writeErr(w, err, http.StatusForbidden)
		return
	}
	day := r.PathValue("date")
	if !validDay(day, h) {
		writeErr(w, errors.New("bad date"), 400)
		return
	}
	if err := reconcile.SaveOps(r.Context(), h.st, day, nil); err != nil {
		writeErr(w, err, 500)
		return
	}
	h.day(w, r)
}

type decisionRequest struct {
	Action string `json:"action"` // accept | dryrun | skip | amend | nopush
}

// decide accepts, dry-runs, skips or amends a day.
func (h *Handler) decide(w http.ResponseWriter, r *http.Request) {
	if err := sameOrigin(r); err != nil {
		writeErr(w, err, http.StatusForbidden)
		return
	}
	day := r.PathValue("date")
	if !validDay(day, h) {
		writeErr(w, errors.New("bad date"), 400)
		return
	}
	var req decisionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, err, 400)
		return
	}
	ctx := r.Context()
	today := h.cfg.Boundary.Day(time.Now().In(h.cfg.Location))
	if day >= today && req.Action != "dryrun" {
		writeErr(w, errors.New("today is still collecting; decide it tomorrow"), http.StatusConflict)
		return
	}
	type line struct {
		Start   string `json:"start"`
		Issue   string `json:"issue"`
		Seconds int    `json:"seconds"`
		Desc    string `json:"desc"`
		TempoID string `json:"tempoId,omitempty"`
		Error   string `json:"error,omitempty"`
	}
	switch req.Action {
	case "dryrun":
		d, _, err := h.build(ctx, day)
		if err != nil {
			writeErr(w, err, 500)
			return
		}
		var lines []line
		for _, wl := range d.Worklogs() {
			lines = append(lines, line{Start: wl.Start.Format(time.RFC3339), Issue: wl.Issue, Seconds: wl.Seconds, Desc: wl.Desc})
		}
		t := d.Totals()
		writeJSON(w, map[string]any{"wouldPush": lines, "blocking": t.Blocking})
	case "accept", "nopush":
		if st, _ := h.st.DayState(ctx, day); st == store.DayDone {
			writeErr(w, errors.New("day is done; amend it first"), http.StatusConflict)
			return
		}
		d, _, err := h.build(ctx, day)
		if err != nil {
			writeErr(w, err, 500)
			return
		}
		results, failed, err := push.Accept(ctx, h.st, h.cfg, d, req.Action == "nopush")
		var lines []line
		for _, r := range results {
			l := line{Start: r.Start.Format(time.RFC3339), Issue: r.Issue, Seconds: r.Seconds, Desc: r.Desc, TempoID: r.TempoID}
			if r.Err != nil {
				l.Error = r.Err.Error()
			}
			lines = append(lines, l)
		}
		if err != nil {
			writeJSON(w, map[string]any{"pushed": lines, "failed": failed, "error": err.Error()})
			return
		}
		_ = reconcile.SaveOps(ctx, h.st, day, nil)
		writeJSON(w, map[string]any{"pushed": lines, "failed": failed})
	case "skip":
		if err := push.Skip(ctx, h.st, day); err != nil {
			writeErr(w, err, 500)
			return
		}
		_ = reconcile.SaveOps(ctx, h.st, day, nil)
		writeJSON(w, map[string]any{"skipped": true})
	case "amend":
		n, err := push.Amend(ctx, h.st, h.cfg, day)
		if err != nil {
			writeErr(w, err, 502)
			return
		}
		writeJSON(w, map[string]any{"deleted": n})
	default:
		writeErr(w, fmt.Errorf("unknown action %q", req.Action), 400)
	}
}

func parseDur(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, errors.New("missing duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	var v float64
	if _, err := fmt.Sscanf(strings.TrimSuffix(s, "h"), "%g", &v); err == nil {
		return time.Duration(v * float64(time.Hour)), nil
	}
	return 0, fmt.Errorf("duration %q: want 1h30m, 45m or 1.5", s)
}

type worklogRequest struct {
	ID   int64  `json:"id"`
	Op   string `json:"op"` // retry | update | delete | retry-all
	Dur  string `json:"dur"`
	Text string `json:"text"`
}

// worklog retries, edits or deletes a draft/failed worklog of a partially
// pushed day, and settles the day when everything is in Tempo.
func (h *Handler) worklog(w http.ResponseWriter, r *http.Request) {
	if err := sameOrigin(r); err != nil {
		writeErr(w, err, http.StatusForbidden)
		return
	}
	day := r.PathValue("date")
	if !validDay(day, h) {
		writeErr(w, errors.New("bad date"), 400)
		return
	}
	var req worklogRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, err, 400)
		return
	}
	ctx := r.Context()
	switch req.Op {
	case "update":
		if req.Dur != "" {
			dur, err := parseDur(req.Dur)
			if err != nil {
				writeErr(w, err, 400)
				return
			}
			wls, _ := h.st.WorklogsForDay(ctx, day)
			desc := req.Text
			for _, x := range wls {
				if x.ID == req.ID && desc == "" {
					desc = x.Desc
				}
			}
			if err := h.st.UpdateWorklog(ctx, req.ID, int(dur.Seconds()), desc); err != nil {
				writeErr(w, err, 400)
				return
			}
		} else {
			wls, _ := h.st.WorklogsForDay(ctx, day)
			for _, x := range wls {
				if x.ID == req.ID {
					if err := h.st.UpdateWorklog(ctx, req.ID, x.Seconds, req.Text); err != nil {
						writeErr(w, err, 400)
						return
					}
				}
			}
		}
	case "delete":
		wls, _ := h.st.WorklogsForDay(ctx, day)
		for _, x := range wls {
			if x.ID == req.ID && x.State == store.WorklogPushed {
				writeErr(w, errors.New("that worklog is in Tempo; amend the day to remove it"), http.StatusConflict)
				return
			}
		}
		if err := h.st.DeleteWorklog(ctx, req.ID); err != nil {
			writeErr(w, err, 500)
			return
		}
		_ = h.st.Audit(ctx, "worklog.discard", fmt.Sprintf("%s #%d", day, req.ID))
	case "retry", "retry-all":
		results, failed, err := push.Day(ctx, h.st, h.cfg, day)
		if err != nil {
			writeErr(w, err, 502)
			return
		}
		_ = results
		_ = failed
	default:
		writeErr(w, fmt.Errorf("unknown op %q", req.Op), 400)
		return
	}
	if done, _ := push.Settle(ctx, h.st, day); done {
		_ = reconcile.SaveOps(ctx, h.st, day, nil)
	}
	h.day(w, r)
}
