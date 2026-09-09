// Package web is the local dashboard: a few JSON endpoints over the same data
// the CLI shows, and one embedded page that renders them. Read-only; the
// reconcile actions stay in the CLI. Served only on the loopback listener.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/session"
	"github.com/valicm/vura/internal/store"
)

//go:embed index.html
var indexHTML []byte

//go:embed favicon.png
var faviconPNG []byte

type Handler struct {
	cfg     *config.Config
	st      *store.Store
	version string
	started time.Time
}

func New(cfg *config.Config, st *store.Store, version string) http.Handler {
	h := &Handler{cfg: cfg, st: st, version: version, started: time.Now()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", h.index)
	mux.HandleFunc("GET /favicon.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "max-age=86400")
		_, _ = w.Write(faviconPNG)
	})
	mux.HandleFunc("GET /api/vura/overview", h.overview)
	mux.HandleFunc("GET /api/vura/day/{date}", h.day)
	mux.HandleFunc("GET /api/vura/cues", h.cues)
	mux.HandleFunc("GET /api/vura/health", h.health)
	mux.HandleFunc("POST /api/vura/day/{date}/op", h.op)
	mux.HandleFunc("POST /api/vura/day/{date}/reset", h.reset)
	mux.HandleFunc("POST /api/vura/day/{date}/decide", h.decide)
	mux.HandleFunc("POST /api/vura/day/{date}/worklog", h.worklog)
	return mux
}

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(indexHTML)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// --- overview: the week --------------------------------------------------------

type bucketTotal struct {
	Bucket   string `json:"bucket"`
	Label    string `json:"label"`
	Issue    string `json:"issue"`
	Observed int    `json:"observed"` // seconds
	Logged   int    `json:"logged"`
	Remote   bool   `json:"remote"`
}

type dayTotal struct {
	Day          string        `json:"day"`
	Weekday      string        `json:"weekday"`
	State        string        `json:"state"` // pending | done | skipped | today | ""
	WallStart    string        `json:"wallStart,omitempty"`
	WallEnd      string        `json:"wallEnd,omitempty"`
	Observed     int           `json:"observed"`
	Logged       int           `json:"logged"`
	Unattributed int           `json:"unattributed"`
	Calls        int           `json:"calls"`
	Pushed       bool          `json:"pushed"` // buckets and logged come from Tempo worklogs
	Buckets      []bucketTotal `json:"buckets"`
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	days := 7
	if v, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && v > 0 && v <= 62 {
		days = v
	}
	ctx := r.Context()
	res := bucket.New(h.cfg)
	decided, err := h.st.DecidedDays(ctx)
	if err != nil {
		writeErr(w, err, 500)
		return
	}
	now := time.Now().In(h.cfg.Location)
	today := h.cfg.Boundary.Day(now)
	out := make([]dayTotal, 0, days)
	for i := 0; i < days; i++ {
		day := h.cfg.Boundary.Day(now.AddDate(0, 0, -i))
		ss, _, err := session.Day(ctx, h.st, h.cfg, res, day)
		if err != nil {
			writeErr(w, err, 500)
			return
		}
		d, _ := time.ParseInLocation("2006-01-02", day, h.cfg.Location)
		dt := dayTotal{Day: day, Weekday: d.Format("Mon"), State: decided[day], Buckets: []bucketTotal{}}
		if day == today {
			dt.State = "today"
		}
		per := map[string]*bucketTotal{}
		round := h.cfg.Session.RoundMin.Duration
		var ws, we time.Time
		for _, s := range ss {
			if ws.IsZero() || s.Start.Before(ws) {
				ws = s.Start
			}
			if s.End.After(we) {
				we = s.End
			}
			switch s.Bucket {
			case session.BucketUnattributed:
				dt.Unattributed += int(s.Duration().Seconds())
				continue
			case session.BucketCall:
				dt.Calls += int(s.Duration().Seconds())
				continue
			}
			bt := per[s.Bucket]
			if bt == nil {
				b := h.cfg.Buckets[s.Bucket]
				bt = &bucketTotal{Bucket: s.Bucket, Label: b.Label, Issue: b.Issue}
				if s.Bucket == session.BucketUnmapped {
					bt.Label, bt.Issue = "unmapped", "?"
				}
				per[s.Bucket] = bt
			}
			bt.Observed += int(s.Duration().Seconds())
			if s.Duration() > 0 {
				bt.Logged += int(session.RoundUp(s.Duration(), round).Seconds())
			}
			bt.Remote = bt.Remote || s.Remote
		}
		for _, bt := range per {
			if bt.Observed == 0 {
				continue // moments only: identity without hours
			}
			dt.Buckets = append(dt.Buckets, *bt)
			dt.Observed += bt.Observed
			dt.Logged += bt.Logged
		}
		// A decided day is what reached Tempo, edits and all, not a fresh
		// rebuild of the evidence. Keep observed from the evidence; take
		// logged and the per-bucket split from the pushed worklogs.
		if wls, err := h.st.WorklogsForDay(ctx, day); err == nil && len(wls) > 0 {
			pushed := map[string]int{}
			total := 0
			for _, wl := range wls {
				if wl.State == store.WorklogPushed {
					pushed[wl.Bucket] += wl.Seconds
					total += wl.Seconds
				}
			}
			if total > 0 {
				dt.Logged = total
				dt.Buckets = dt.Buckets[:0]
				for b, secs := range pushed {
					bb := h.cfg.Buckets[b]
					dt.Buckets = append(dt.Buckets, bucketTotal{Bucket: b, Label: bb.Label, Issue: bb.Issue, Observed: secs, Logged: secs, Remote: false})
				}
				dt.Pushed = true
			}
		}
		sort.Slice(dt.Buckets, func(a, b int) bool { return dt.Buckets[a].Observed > dt.Buckets[b].Observed })
		if !ws.IsZero() {
			dt.WallStart, dt.WallEnd = ws.Format("15:04"), we.Format("15:04")
		}
		out = append(out, dt)
	}
	writeJSON(w, map[string]any{"days": out, "today": today, "roundMin": int(h.cfg.Session.RoundMin.Seconds())})
}

// --- one day -----------------------------------------------------------------------

type entryJSON struct {
	N        int      `json:"n"`
	Key      string   `json:"key"`
	Bucket   string   `json:"bucket"`
	Label    string   `json:"label"`
	Issue    string   `json:"issue"`
	Kind     string   `json:"kind"`
	Start    string   `json:"start"` // RFC3339
	End      string   `json:"end"`
	Observed int      `json:"observed"`
	Logged   int      `json:"logged"`
	Desc     string   `json:"desc"`
	Auto     bool     `json:"auto"`
	Detail   string   `json:"detail"`
	Remote   bool     `json:"remote"`
	Shared   bool     `json:"shared"`
	Span     int      `json:"span"` // seconds, end-start
	Identity bool     `json:"identity"`
	Tickets  []string `json:"tickets"`
}

type meetingJSON struct {
	Start  string `json:"start"`
	End    string `json:"end"`
	Title  string `json:"title"`
	Source string `json:"source"`
	Bucket string `json:"bucket"`
}

type anchorJSON struct {
	TS     string `json:"ts"`
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Ref    string `json:"ref"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

type worklogJSON struct {
	ID      int64  `json:"id"`
	Start   string `json:"start"`
	Issue   string `json:"issue"`
	Seconds int    `json:"seconds"`
	Desc    string `json:"desc"`
	State   string `json:"state"`
	TempoID string `json:"tempoId"`
	Error   string `json:"error,omitempty"`
}

func (h *Handler) day(w http.ResponseWriter, r *http.Request) {
	day := r.PathValue("date")
	if _, err := time.ParseInLocation("2006-01-02", day, h.cfg.Location); err != nil {
		writeErr(w, err, 400)
		return
	}
	ctx := r.Context()
	res := bucket.New(h.cfg)
	d, skipped, err := h.build(ctx, day)
	if err != nil {
		writeErr(w, err, 500)
		return
	}
	from, to, _ := h.cfg.Boundary.Range(day, h.cfg.Location)
	t := d.Totals()

	entries := make([]entryJSON, 0, len(d.Entries))
	meetings, anchors, worklogs := []meetingJSON{}, []anchorJSON{}, []worklogJSON{}
	for _, e := range d.Entries {
		if e.Dropped {
			continue
		}
		label := h.cfg.Buckets[e.Bucket].Label
		if e.Bucket == "" {
			label = map[string]string{"presence": "unattributed", "call": "call", "unmapped": "unmapped"}[e.Kind]
		}
		entries = append(entries, entryJSON{N: e.N, Key: e.Key(), Bucket: e.Bucket, Label: label, Issue: e.Issue, Kind: e.Kind,
			Start: e.Start.Format(time.RFC3339), End: e.End.Format(time.RFC3339),
			Observed: int(e.Observed.Seconds()), Logged: int(e.Logged.Seconds()), Desc: e.Desc, Auto: e.AutoDesc,
			Detail: e.Detail, Remote: e.Remote, Shared: e.Shared, Span: int(e.End.Sub(e.Start).Seconds()), Identity: e.Identity, Tickets: e.Tickets})
	}
	if evs, err := h.st.EventsRange(ctx, from, to); err == nil {
		for _, e := range evs {
			var att []string
			if e.Attendees != "" {
				att = strings.Split(e.Attendees, ",")
			}
			bk := res.ByEvent(e.Title, att)
			if strings.HasPrefix(e.Source, "slack:") {
				bk = res.BySlack(strings.TrimPrefix(e.Title, "Huddle "))
			}
			if bk == "" {
				bk = e.Bucket
			}
			meetings = append(meetings, meetingJSON{Start: e.Start.In(h.cfg.Location).Format(time.RFC3339), End: e.End.In(h.cfg.Location).Format(time.RFC3339),
				Title: e.Title, Source: e.Source, Bucket: bk})
		}
	}
	if as, err := h.st.AnchorsRange(ctx, from, to); err == nil {
		for _, a := range as {
			anchors = append(anchors, anchorJSON{TS: a.TS.In(h.cfg.Location).Format(time.RFC3339), Source: a.Source, Kind: a.Kind, Ref: a.Ref, Title: a.Title, URL: a.URL})
		}
	}
	if wls, err := h.st.WorklogsForDay(ctx, day); err == nil {
		for _, wl := range wls {
			worklogs = append(worklogs, worklogJSON{ID: wl.ID, Start: wl.Start.In(h.cfg.Location).Format(time.RFC3339), Issue: wl.Issue, Seconds: wl.Seconds, Desc: wl.Desc, State: wl.State, TempoID: wl.TempoID, Error: wl.Error})
		}
	}
	state, _ := h.st.DayState(ctx, day)
	partial := false
	pushedTotal := 0
	for _, wl := range worklogs {
		if wl.State == store.WorklogPushed {
			pushedTotal += wl.Seconds
			if state != store.DayDone {
				partial = true
			}
		}
	}
	loggedSec := int(t.Logged.Seconds())
	if pushedTotal > 0 {
		loggedSec = pushedTotal
	}
	dd, _ := time.ParseInLocation("2006-01-02", day, h.cfg.Location)
	writeJSON(w, map[string]any{
		"day": day, "title": dd.Format("Monday 2 January"), "state": state,
		"dayStart": from.Format(time.RFC3339), "dayEnd": to.Format(time.RFC3339),
		"wallStart": fmtT(t.WallStart), "wallEnd": fmtT(t.WallEnd),
		"observed": int(t.Observed.Seconds()), "logged": loggedSec, "inTempo": pushedTotal,
		"unassigned": t.Unassigned, "blocking": t.Blocking,
		"edits": len(d.Ops), "editsSkipped": skipped, "today": day == h.cfg.Boundary.Day(time.Now().In(h.cfg.Location)),
		"partial": partial,
		"entries": entries, "meetings": meetings, "anchors": anchors, "worklogs": worklogs,
		"buckets": h.bucketMeta(),
	})
}

func fmtT(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func (h *Handler) bucketMeta() map[string]map[string]string {
	out := map[string]map[string]string{}
	for k, b := range h.cfg.Buckets {
		out[k] = map[string]string{"label": b.Label, "issue": b.Issue}
	}
	return out
}

// --- cues: what a rule would claim ------------------------------------------------

func (h *Handler) cues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res := bucket.New(h.cfg)
	since := time.Now().AddDate(0, 0, -7).Unix()
	type cue struct {
		Kind    string `json:"kind"` // domain | project | slack | meeting
		Name    string `json:"name"`
		Minutes int    `json:"minutes"`
	}
	var out []cue
	rows, err := h.st.DB().QueryContext(ctx, `SELECT lower(entity), COUNT(DISTINCT CAST(ts/60 AS INTEGER)) m FROM heartbeats
		WHERE type IN ('domain','url') AND ts >= ? GROUP BY 1 ORDER BY m DESC LIMIT 100`, since)
	if err == nil {
		for rows.Next() {
			var ent string
			var m int
			_ = rows.Scan(&ent, &m)
			if res.ByDomain(ent) == "" && m >= 3 {
				out = append(out, cue{"domain", bareDomain(ent), m})
			}
		}
		rows.Close()
	}
	rows, err = h.st.DB().QueryContext(ctx, `SELECT COALESCE(project,''), entity, COUNT(DISTINCT CAST(ts/60 AS INTEGER)) m FROM heartbeats
		WHERE type NOT IN ('domain','url') AND ts >= ? GROUP BY 1 ORDER BY m DESC LIMIT 100`, since)
	if err == nil {
		for rows.Next() {
			var project, entity string
			var m int
			_ = rows.Scan(&project, &entity, &m)
			bk := ""
			if strings.HasPrefix(entity, "/") {
				bk, _ = res.ByPath(entity)
			}
			if bk == "" && project != "" {
				bk = res.ByProject(project)
			}
			if bk == "" && project != "" && m >= 3 {
				out = append(out, cue{"project", project, m})
			}
		}
		rows.Close()
	}
	rows, err = h.st.DB().QueryContext(ctx, `SELECT ref, COUNT(*) FROM anchors WHERE source='slack' AND ts >= ? GROUP BY 1`, since)
	if err == nil {
		for rows.Next() {
			var ref string
			var n int
			_ = rows.Scan(&ref, &n)
			if res.BySlack(ref) == "" {
				out = append(out, cue{"slack", ref, n})
			}
		}
		rows.Close()
	}
	if evs, err := h.st.EventsRange(ctx, time.Unix(since, 0), time.Now().AddDate(0, 0, 7)); err == nil {
		seen := map[string]bool{}
		for _, e := range evs {
			if e.Bucket != "" || strings.HasPrefix(e.Source, "slack:") || seen[e.Title] {
				continue
			}
			var att []string
			if e.Attendees != "" {
				att = strings.Split(e.Attendees, ",")
			}
			if res.ByEvent(e.Title, att) == "" {
				seen[e.Title] = true
				out = append(out, cue{"meeting", e.Title, int(e.End.Sub(e.Start).Minutes())})
			}
		}
	}
	writeJSON(w, map[string]any{"cues": out})
}

// --- health ---------------------------------------------------------------------------

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	type src struct {
		Name string `json:"name"`
		Last string `json:"last"` // RFC3339 or ""
		Age  int    `json:"age"`  // seconds, -1 unknown
	}
	var out []src
	add := func(name string, t time.Time) {
		s := src{Name: name, Age: -1}
		if !t.IsZero() {
			s.Last = t.In(h.cfg.Location).Format(time.RFC3339)
			s.Age = int(time.Since(t).Seconds())
		}
		out = append(out, s)
	}
	last := func(q string) time.Time {
		var v *float64
		if err := h.st.DB().QueryRowContext(ctx, q).Scan(&v); err != nil || v == nil {
			return time.Time{}
		}
		return time.Unix(int64(*v), 0)
	}
	add("presence", last(`SELECT MAX(ts) FROM presence`))
	add("editor heartbeats", last(`SELECT MAX(ts) FROM heartbeats WHERE type='file' AND editor NOT LIKE 'claude-code%'`))
	add("claude code", last(`SELECT MAX(ts) FROM heartbeats WHERE editor LIKE 'claude-code%'`))
	add("browser", last(`SELECT MAX(ts) FROM heartbeats WHERE type IN ('domain','url')`))
	add("shell", last(`SELECT MAX(ts) FROM shell`))
	add("commits", last(`SELECT MAX(ts) FROM commits`))
	add("calls", last(`SELECT MAX(last_seen) FROM audio`))
	states, _ := h.st.StatesLike(ctx, "collector.ok.")
	keys := make([]string, 0, len(states))
	for k := range states {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var collectors []src
	for _, k := range keys {
		t, _ := time.Parse(time.RFC3339, states[k])
		s := src{Name: strings.TrimPrefix(k, "collector.ok."), Age: -1}
		if !t.IsZero() {
			s.Last, s.Age = t.In(h.cfg.Location).Format(time.RFC3339), int(time.Since(t).Seconds())
		}
		collectors = append(collectors, s)
	}
	pending := 0
	if days, err := PendingCount(ctx, h.st, h.cfg); err == nil {
		pending = days
	}
	writeJSON(w, map[string]any{
		"version": h.version, "uptime": int(time.Since(h.started).Seconds()),
		"evidence": out, "collectors": collectors, "pending": pending,
	})
}

// bareDomain strips a scheme and path: "https://a.b/c" -> "a.b".
func bareDomain(e string) string {
	if i := strings.Index(e, "://"); i >= 0 {
		e = e[i+3:]
	}
	if i := strings.IndexByte(e, '/'); i >= 0 {
		e = e[:i]
	}
	return e
}

// PendingCount mirrors the CLI's queue without auto-skipping anything.
func PendingCount(ctx context.Context, st *store.Store, cfg *config.Config) (int, error) {
	first := cfg.Day.First
	if first == "" {
		fr, err := st.FirstRun(ctx)
		if err != nil || fr.IsZero() {
			return 0, err
		}
		first = cfg.Boundary.Day(fr.In(cfg.Location))
	}
	decided, err := st.DecidedDays(ctx)
	if err != nil {
		return 0, err
	}
	today := cfg.Boundary.Day(time.Now().In(cfg.Location))
	d, err := time.ParseInLocation("2006-01-02", first, cfg.Location)
	if err != nil {
		return 0, err
	}
	n := 0
	for day := first; day < today; day = d.Format("2006-01-02") {
		d = d.AddDate(0, 0, 1)
		if decided[day] == "" {
			n++
		}
	}
	return n, nil
}
