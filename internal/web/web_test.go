package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/reconcile"
	"github.com/valicm/vura/internal/seed"
	"github.com/valicm/vura/internal/store"
)

func TestEndpoints(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(seed.Config), 0o600)
	t.Setenv("VURA_DATA", dir)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	day := time.Now().In(cfg.Location).AddDate(0, 0, -1)
	if err := seed.Day(context.Background(), st, day, cfg.Location); err != nil {
		t.Fatal(err)
	}
	h := New(cfg, st, "test")
	get := func(path string, out any) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if out != nil && rec.Code == 200 {
			if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
		}
		return rec.Code
	}
	var ov struct {
		Days []struct {
			Day      string                    `json:"day"`
			Observed int                       `json:"observed"`
			Buckets  []struct{ Bucket string } `json:"buckets"`
		} `json:"days"`
	}
	if c := get("/api/vura/overview?days=3", &ov); c != 200 || len(ov.Days) != 3 {
		t.Fatalf("overview: %d %+v", c, ov)
	}
	yd := day.Format("2006-01-02")
	var found bool
	for _, d := range ov.Days {
		if d.Day == yd && d.Observed > 7*3600 && len(d.Buckets) >= 3 {
			found = true
		}
	}
	if !found {
		t.Errorf("seeded day missing from overview: %+v", ov.Days)
	}
	var dd struct {
		Entries  []struct{ Bucket, Kind string } `json:"entries"`
		Meetings []any                           `json:"meetings"`
		Anchors  []any                           `json:"anchors"`
		Observed int                             `json:"observed"`
	}
	if c := get("/api/vura/day/"+yd, &dd); c != 200 || len(dd.Entries) < 5 || len(dd.Meetings) != 2 || len(dd.Anchors) != 6 || dd.Observed == 0 {
		t.Errorf("day: %d entries=%d meetings=%d anchors=%d observed=%d", c, len(dd.Entries), len(dd.Meetings), len(dd.Anchors), dd.Observed)
	}
	if c := get("/api/vura/day/not-a-date", nil); c != 400 {
		t.Errorf("bad date: %d", c)
	}
	var cues struct {
		Cues []struct{ Kind, Name string } `json:"cues"`
	}
	if c := get("/api/vura/cues", &cues); c != 200 {
		t.Errorf("cues: %d", c)
	}
	for _, c := range cues.Cues {
		if c.Kind == "meeting" && c.Name == "Dentist" {
			found = true
		}
	}
	var he struct {
		Evidence []struct{ Name string } `json:"evidence"`
		Pending  int                     `json:"pending"`
	}
	if c := get("/api/vura/health", &he); c != 200 || len(he.Evidence) < 5 {
		t.Errorf("health: %d %+v", c, he)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("index: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	_ = http.StatusOK
}

func TestActions(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte(seed.Config), 0o600)
	t.Setenv("VURA_DATA", dir)
	cfg, _ := config.Load(cfgPath)
	st, _ := store.Open(cfg.DBPath)
	defer st.Close()
	day := time.Now().In(cfg.Location).AddDate(0, 0, -1)
	yd := day.Format("2006-01-02")
	_ = seed.Day(context.Background(), st, day, cfg.Location)
	h := New(cfg, st, "test")
	post := func(path, body string, headers map[string]string) (int, map[string]any) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ok := map[string]string{"X-Vura": "1"}
	// Cross-site and header-less writes are refused.
	if c, _ := post("/api/vura/day/"+yd+"/op", `{"op":"drop","target":"x"}`, nil); c != 403 {
		t.Errorf("no header: %d", c)
	}
	if c, _ := post("/api/vura/day/"+yd+"/op", `{"op":"drop","target":"x"}`, map[string]string{"X-Vura": "1", "Origin": "http://evil.example"}); c != 403 {
		t.Errorf("cross-origin: %d", c)
	}
	// Read the day, drop the first unattributed entry, set a description, merge two.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/vura/day/"+yd, nil))
	var d struct {
		Entries []struct {
			Key, Bucket, Kind string
			N                 int
		} `json:"entries"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	var pres, acme []string
	for _, e := range d.Entries {
		if e.Kind == "presence" {
			pres = append(pres, e.Key)
		}
		if e.Bucket == "ACME" {
			acme = append(acme, e.Key)
		}
	}
	if len(pres) == 0 || len(acme) < 2 {
		t.Fatalf("fixture: presence=%d acme=%d", len(pres), len(acme))
	}
	if c, out := post("/api/vura/day/"+yd+"/op", `{"op":"drop","target":"`+pres[0]+`"}`, ok); c != 200 || out["edits"].(float64) != 1 {
		t.Errorf("drop: %d %v", c, out["error"])
	}
	if c, _ := post("/api/vura/day/"+yd+"/op", `{"op":"desc","target":"`+acme[0]+`","text":"ACME-812 cart tax zones"}`, ok); c != 200 {
		t.Errorf("desc: %d", c)
	}
	body, _ := json.Marshal(map[string]any{"op": "merge", "target": acme[0], "others": []string{acme[1]}})
	c, out := post("/api/vura/day/"+yd+"/op", string(body), ok)
	if c != 200 || out["edits"].(float64) != 3 {
		t.Errorf("merge: %d %v", c, out["error"])
	}
	// Edits persist across a fresh load.
	ops, _ := reconcile.LoadOps(context.Background(), st, yd)
	if len(ops) != 3 {
		t.Errorf("persisted ops: %d", len(ops))
	}
	// Dry run lists what would push, with the merged entry once.
	c, out = post("/api/vura/day/"+yd+"/decide", `{"action":"dryrun"}`, ok)
	if c != 200 || out["wouldPush"] == nil {
		t.Errorf("dryrun: %d %v", c, out)
	}
	// Reset clears.
	if c, out := post("/api/vura/day/"+yd+"/reset", ``, ok); c != 200 || out["edits"].(float64) != 0 {
		t.Errorf("reset: %d", c)
	}
	// Today cannot be accepted.
	td := time.Now().In(cfg.Location).Format("2006-01-02")
	if c, _ := post("/api/vura/day/"+td+"/decide", `{"action":"skip"}`, ok); c != 409 {
		t.Errorf("today skip: %d", c)
	}
	// Skip yesterday: state changes.
	if c, _ := post("/api/vura/day/"+yd+"/decide", `{"action":"skip"}`, ok); c != 200 {
		t.Errorf("skip: %d", c)
	}
	if s, _ := st.DayState(context.Background(), yd); s != store.DaySkipped {
		t.Errorf("state %q", s)
	}
}
