// Package wakatime implements the small slice of the WakaTime API that
// wakatime-cli talks to, so every editor plugin (JetBrains, Claude Code, ...)
// pointed at api_url = http://127.0.0.1:4242/api lands here.
//
// Routes (all under the /api prefix wakatime-cli appends to api_url):
//
//	POST /api/users/current/heartbeats.bulk   -- the one that matters
//	POST /api/users/current/heartbeats
//	GET  /api/users/current/statusbar/today   -- IDE status bar; we answer politely
//	GET  /api/v1/... same, for plugins that hardcode /v1
package wakatime

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/valicm/vura/internal/store"
	"github.com/valicm/vura/internal/useragent"
)

type Server struct {
	store  *store.Store
	key    string // if non-empty, required
	device string
	log    *slog.Logger
	// Today's totals for the status bar; a closure so the package does not
	// know about day boundaries.
	Today func(ctx context.Context) (time.Duration, error)
	// Web, if set, serves everything that is not a WakaTime route: the dashboard.
	Web http.Handler
}

func New(st *store.Store, key, device string, log *slog.Logger) *Server {
	return &Server{store: st, key: key, device: device, log: log}
}

// ListenAndServe binds addr (must be loopback) and serves until ctx is done.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("wakatime listen %q: refusing non-loopback address", addr)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/users/current/heartbeats.bulk", s.bulk)
	mux.HandleFunc("POST /api/v1/users/current/heartbeats.bulk", s.bulk)
	mux.HandleFunc("POST /api/users/current/heartbeats", s.bulk)
	mux.HandleFunc("POST /api/v1/users/current/heartbeats", s.bulk)
	mux.HandleFunc("GET /api/users/current/statusbar/today", s.today)
	mux.HandleFunc("GET /api/v1/users/current/statusbar/today", s.today)
	mux.HandleFunc("GET /api/users/current", s.user)
	mux.HandleFunc("GET /api/v1/users/current", s.user)
	mux.HandleFunc("GET /api/users/current/summaries", s.summaries)
	mux.HandleFunc("GET /api/v1/users/current/summaries", s.summaries)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	if s.Web != nil {
		mux.Handle("/", s.Web)
	}
	srv := &http.Server{
		Addr: addr, Handler: cors(mux),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	s.log.Info("wakatime: listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// cors lets browser extensions (chrome-wakatime) post from their own origin.
// Loopback only, so a permissive policy costs nothing.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/users/") && !strings.HasPrefix(r.URL.Path, "/api/v1/users/") {
			next.ServeHTTP(w, r) // the dashboard is same-origin only
			return
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Machine-Name")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// heartbeat mirrors the fields wakatime-cli sends; unknown ones survive in raw.
type heartbeat struct {
	Entity    string `json:"entity"`
	Type      string `json:"type"`
	Category  string `json:"category"`
	Time      hbTime `json:"time"`
	Project   string `json:"project"`
	Branch    string `json:"branch"`
	Language  string `json:"language"`
	IsWrite   bool   `json:"is_write"`
	UserAgent string `json:"user_agent"`
	Plugin    string `json:"plugin"` // browser extensions: "firefox-wakatime/4.1.0"
}

// hbTime accepts 1788858000.123 or "1788858000.123": wakatime-cli sends a
// number, the browser extensions a string.
type hbTime float64

func (t *hbTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*t = hbTime(v)
	return nil
}

func (s *Server) authorized(r *http.Request) bool {
	if s.key == "" {
		return true
	}
	// The WakaTime browser extensions send ?api_key=; wakatime-cli sends Basic auth.
	if q := r.URL.Query().Get("api_key"); q != "" {
		return subtle.ConstantTimeCompare([]byte(q), []byte(s.key)) == 1
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Basic ") {
		return false
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
	if err != nil {
		return false
	}
	// wakatime-cli sends base64(key); curl -u and some clients send base64("key:").
	raw = bytes.TrimSuffix(raw, []byte(":"))
	return subtle.ConstantTimeCompare(raw, []byte(s.key)) == 1
}

func (s *Server) bulk(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		s.log.Warn("wakatime: rejected heartbeat, bad or missing key", "ua", r.Header.Get("User-Agent"), "origin", r.Header.Get("Origin"))
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, `{"error":"read"}`, http.StatusBadRequest)
		return
	}
	// Accept either a bare object or an array of them.
	var raws []json.RawMessage
	if len(body) > 0 && body[0] == '{' {
		raws = []json.RawMessage{body}
	} else if err := json.Unmarshal(body, &raws); err != nil {
		http.Error(w, `{"error":"bad json"}`, http.StatusBadRequest)
		return
	}
	ua := r.Header.Get("User-Agent")
	responses := make([][2]any, 0, len(raws))
	stored := 0
	for _, raw := range raws {
		var hb heartbeat
		if err := json.Unmarshal(raw, &hb); err != nil || hb.Entity == "" || hb.Time == 0 {
			responses = append(responses, [2]any{map[string]any{"error": "invalid heartbeat"}, 400})
			continue
		}
		agent := hb.UserAgent
		if agent == "" {
			agent = ua
		}
		editor, plugin := useragent.Parse(agent)
		if hb.Plugin != "" {
			plugin = hb.Plugin
		}
		err := s.store.InsertHeartbeat(r.Context(), store.Heartbeat{
			TS: float64(hb.Time), Entity: hb.Entity, Type: hb.Type, Category: hb.Category, Project: hb.Project,
			Branch: hb.Branch, Language: hb.Language, IsWrite: hb.IsWrite, Plugin: plugin, Editor: editor,
			Device: s.device, Raw: string(raw),
		})
		if err != nil {
			s.log.Error("wakatime: insert", "err", err)
			responses = append(responses, [2]any{map[string]any{"error": "store"}, 500})
			continue
		}
		stored++
		responses = append(responses, [2]any{map[string]any{"data": map[string]any{
			"entity": hb.Entity, "time": float64(hb.Time), "project": hb.Project,
		}}, 201})
	}
	s.log.Debug("wakatime: heartbeats", "received", len(raws), "stored", stored, "ua", ua)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"responses": responses})
}

// user answers the browser extensions' "am I signed in" probe.
func (s *Server) user(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"id": "vura", "username": "vura", "display_name": "vura", "full_name": "vura",
		"email": "", "is_email_public": false, "timezone": time.Local.String(), "has_premium_features": false,
	}})
}

// summaries feeds the extension popup: one entry for today.
func (s *Server) summaries(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var d time.Duration
	if s.Today != nil {
		if v, err := s.Today(r.Context()); err == nil {
			d = v
		}
	}
	day := time.Now().Format("2006-01-02")
	text := fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data": []map[string]any{{
			"grand_total": map[string]any{"text": text, "total_seconds": d.Seconds(), "hours": int(d.Hours()), "minutes": int(d.Minutes()) % 60},
			"range":       map[string]any{"date": day, "text": "Today"},
			"projects":    []any{}, "categories": []any{}, "editors": []any{}, "languages": []any{},
		}},
		"start": day, "end": day, "cumulative_total": map[string]any{"text": text, "seconds": d.Seconds()},
	})
}

func (s *Server) today(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	var d time.Duration
	if s.Today != nil {
		if v, err := s.Today(r.Context()); err == nil {
			d = v
		}
	}
	text := fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{
		"grand_total": map[string]any{"text": text, "total_seconds": d.Seconds()},
	}})
}
