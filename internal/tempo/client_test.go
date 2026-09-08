package tempo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCreateAndDelete(t *testing.T) {
	var created map[string]any
	issueCalls := 0
	jira := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != "me@x" || p != "jtok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/rest/api/3/myself":
			io.WriteString(w, `{"accountId":"acc-1"}`)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/EXT-88"):
			issueCalls++
			io.WriteString(w, `{"id":"10234","key":"EXT-88"}`)
		default:
			w.WriteHeader(404)
			io.WriteString(w, `{"errorMessages":["Issue does not exist"]}`)
		}
	}))
	defer jira.Close()
	tempo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ttok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.Method == "POST" && r.URL.Path == "/worklogs":
			json.NewDecoder(r.Body).Decode(&created)
			io.WriteString(w, `{"tempoWorklogId":555,"timeSpentSeconds":900}`)
		case r.Method == "DELETE" && r.URL.Path == "/worklogs/555":
			w.WriteHeader(204)
		case r.Method == "DELETE" && r.URL.Path == "/worklogs/gone":
			w.WriteHeader(404)
		default:
			w.WriteHeader(400)
		}
	}))
	defer tempo.Close()

	cache := map[string]string{}
	c := New("ttok", jira.URL, "me@x", "jtok")
	c.TempoBase = tempo.URL
	c.GetCached = func(k string) (string, bool) { v, ok := cache[k]; return v, ok }
	c.SetCached = func(k, v string) { cache[k] = v }

	loc, _ := time.LoadLocation("Europe/Zagreb")
	ctx := context.Background()
	id, err := c.Create(ctx, Worklog{IssueKey: "EXT-88", Start: time.Date(2026, 9, 4, 9, 34, 0, 0, loc), Seconds: 900, Description: "AUC-8600 x"})
	if err != nil || id != "555" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	if created["issueId"].(float64) != 10234 || created["startDate"] != "2026-09-04" || created["startTime"] != "09:34:00" ||
		created["timeSpentSeconds"].(float64) != 900 || created["authorAccountId"] != "acc-1" {
		t.Errorf("body: %v", created)
	}
	if _, err := c.Create(ctx, Worklog{IssueKey: "EXT-88", Start: time.Now(), Seconds: 60}); err != nil {
		t.Fatal(err)
	}
	if issueCalls != 1 {
		t.Errorf("issue lookup should be cached, got %d calls", issueCalls)
	}
	if _, err := c.Create(ctx, Worklog{IssueKey: "NOPE-1", Start: time.Now(), Seconds: 60}); err == nil {
		t.Error("unknown issue must fail")
	} else if ae, ok := err.(*APIError); !ok || ae.Status != 404 {
		t.Errorf("want APIError 404, got %v", err)
	}
	if err := c.Delete(ctx, "555"); err != nil {
		t.Error(err)
	}
	if err := c.Delete(ctx, "gone"); err != nil {
		t.Error("404 on delete should be success:", err)
	}
}
