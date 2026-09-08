package anchors

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGitHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || !strings.HasPrefix(r.URL.Path, "/users/valicm/events") {
			w.WriteHeader(401)
			return
		}
		io.WriteString(w, `[
		 {"id":"1","type":"PullRequestReviewEvent","created_at":"2026-09-04T10:00:00Z","repo":{"name":"acme/webshop"},
		  "payload":{"action":"created","review":{"state":"approved"},"pull_request":{"number":12,"title":"Drupal 11","html_url":"u"}}},
		 {"id":"2","type":"IssueCommentEvent","created_at":"2026-09-04T09:00:00Z","repo":{"name":"acme/webshop"},
		  "payload":{"issue":{"number":7,"title":"Bug"}}},
		 {"id":"3","type":"WatchEvent","created_at":"2026-09-04T08:00:00Z","repo":{"name":"x/y"}},
		 {"id":"4","type":"PushEvent","created_at":"2026-08-01T08:00:00Z","repo":{"name":"x/y"},"payload":{"size":3,"ref":"refs/heads/main"}}
		]`)
	}))
	defer srv.Close()
	g := &GitHub{User: "valicm", Token: "tok", HTTP: srv.Client(), Base: srv.URL}
	as, err := g.Fetch(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 2 || as[0].Kind != "approve" || as[0].Ref != "acme/webshop#12" || as[1].Kind != "comment" || as[1].Ref != "acme/webshop#7" {
		t.Errorf("%+v", as)
	}
}

func TestGitLab(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "tok" {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/api/v4/events":
			io.WriteString(w, `[
			 {"id":10,"action_name":"approved","target_type":"MergeRequest","target_title":"BT-1 thing","target_iid":34,"project_id":5,"created_at":"2026-09-04T10:00:00.000Z"},
			 {"id":11,"action_name":"commented on","target_type":"DiffNote","target_title":"BT-1 thing","project_id":5,"created_at":"2026-09-04T10:05:00.000Z","note":{"noteable_type":"MergeRequest","noteable_iid":34}},
			 {"id":12,"action_name":"pushed to","project_id":5,"created_at":"2026-09-04T11:00:00.000Z","push_data":{"ref":"feature/BT-1","commit_count":2,"commit_title":"BT-1 fix"}},
			 {"id":13,"action_name":"joined","project_id":5,"created_at":"2026-09-04T11:00:00.000Z"}
			]`)
		case r.URL.Path == "/api/v4/projects/5":
			io.WriteString(w, `{"path_with_namespace":"beta/shop"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	g := &GitLab{Base: srv.URL, Token: "tok", HTTP: srv.Client()}
	as, err := g.Fetch(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 3 || as[0].Kind != "approve" || as[0].Ref != "beta/shop!34" || as[1].Ref != "beta/shop!34" || as[2].Kind != "push" || as[2].Ref != "beta/shop" {
		t.Errorf("%+v", as)
	}
}

func TestJira(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "me@x" || p != "tok" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/rest/api/3/myself":
			io.WriteString(w, `{"accountId":"me"}`)
		case "/rest/api/3/search/jql":
			if !strings.Contains(r.URL.Query().Get("jql"), "project in (ACME,VS)") {
				t.Errorf("jql = %q", r.URL.Query().Get("jql"))
			}
			io.WriteString(w, `{"isLast":true,"issues":[{"id":"1","key":"ACME-8600","fields":{"summary":"Drupal 11",
			 "comment":{"comments":[
			   {"id":"c1","author":{"accountId":"me"},"created":"2026-09-04T09:34:12.000+0200","updated":"2026-09-04T09:34:12.000+0200"},
			   {"id":"c2","author":{"accountId":"other"},"created":"2026-09-04T09:40:00.000+0200","updated":"2026-09-04T09:40:00.000+0200"}]}},
			 "changelog":{"histories":[
			   {"id":"h1","author":{"accountId":"me"},"created":"2026-09-04T10:00:00.000+0200","items":[{"field":"status","toString":"In Review"}]},
			   {"id":"h2","author":{"accountId":"me"},"created":"2026-08-01T10:00:00.000+0200","items":[{"field":"status","toString":"Done"}]}]}}]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	j := &Jira{Site: srv.URL, Email: "me@x", Token: "tok", Projects: []string{"ACME", "VS"}, HTTP: srv.Client()}
	as, err := j.Fetch(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 2 {
		t.Fatalf("%+v", as)
	}
	if as[0].Kind != "transition" || as[0].Ref != "ACME-8600" || !strings.HasSuffix(as[0].Title, "→ In Review") || as[1].Kind != "comment" {
		t.Errorf("%+v", as)
	}
}

func TestSlack(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxp" {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/auth.test":
			io.WriteString(w, `{"ok":true,"user_id":"U1"}`)
		case "/search.messages":
			if q := r.URL.Query().Get("query"); !strings.HasPrefix(q, "from:<@U1> after:") {
				t.Errorf("query = %q", q)
			}
			io.WriteString(w, `{"ok":true,"messages":{"paging":{"pages":1},"matches":[
			 {"ts":"1788858000.000100","channel":{"id":"C1","name":"Acme-Dev"},"permalink":"p1","text":"SECRET"},
			 {"ts":"1788858100.000200","channel":{"id":"D9","name":"U2","is_im":true},"permalink":"p2","text":"SECRET"},
			 {"ts":"1700000000.000000","channel":{"id":"C1","name":"old"},"permalink":"p3"}
			]}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := &Slack{WS: "work", Token: "xoxp", HTTP: srv.Client(), Base: srv.URL}
	as, err := s.Fetch(context.Background(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(as) != 2 || as[0].Ref != "work#acme-dev" || as[0].Kind != "message" || as[1].Ref != "work#dm-u2" || as[0].Title != "" {
		t.Errorf("%+v", as)
	}
	if as[0].TS.Unix() != 1788858000 {
		t.Errorf("ts %v", as[0].TS)
	}
}

func TestSlackHuddles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth.test":
			io.WriteString(w, `{"ok":true,"user_id":"U1"}`)
		case "/conversations.list":
			io.WriteString(w, `{"ok":true,"channels":[
			 {"id":"C1","name":"Team-Acme-Dev","is_member":true},
			 {"id":"C2","name":"not-mine","is_member":false},
			 {"id":"D1","is_im":true,"user":"U2"},
			 {"id":"G1","name":"mpdm-anna--ben-1","is_mpim":true}],"response_metadata":{"next_cursor":""}}`)
		case "/users.info":
			io.WriteString(w, `{"ok":true,"user":{"name":"anna.smith"}}`)
		case "/conversations.history":
			switch r.URL.Query().Get("channel") {
			case "D1":
				io.WriteString(w, `{"ok":true,"messages":[{"subtype":"huddle_thread","ts":"2.1","room":{"date_start":1788870000,"date_end":1788872000,"participant_history":["U1","U2"]}}]}`)
			case "C1":
				io.WriteString(w, `{"ok":true,"messages":[
				 {"subtype":"huddle_thread","ts":"1.1","room":{"id":"R1","date_start":1788858000,"date_end":1788861480,"participant_history":["U1","U2"]}},
				 {"subtype":"huddle_thread","ts":"1.2","room":{"id":"R2","date_start":1788850000,"date_end":1788850020,"participant_history":["U1"]}},
				 {"subtype":"huddle_thread","ts":"1.3","room":{"id":"R3","date_start":1788840000,"date_end":1788843600,"participant_history":["U2"]}},
				 {"ts":"1.4","text":"hello"}]}`)
			default:
				io.WriteString(w, `{"ok":true,"messages":[]}`)
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	s := &Slack{WS: "work", Token: "xoxp", HTTP: srv.Client(), Base: srv.URL}
	evs, err := s.Events(context.Background(), time.Unix(1788800000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Title != "Huddle work#team-acme-dev" || evs[0].End.Sub(evs[0].Start) != 58*time.Minute || evs[0].Source != "slack:work" {
		t.Errorf("%+v", evs)
	}
	if evs[1].Title != "Huddle work#dm-anna.smith" {
		t.Errorf("dm huddle should name the counterpart: %+v", evs[1])
	}
}
