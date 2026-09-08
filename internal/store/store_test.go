package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHeartbeatDedup(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	h := Heartbeat{TS: 1788817598.79, Entity: "/a.go", Plugin: "goland-wakatime/1"}
	for i := 0; i < 3; i++ {
		if err := s.InsertHeartbeat(ctx, h); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM heartbeats`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("want 1 row after replay, got %d", n)
	}
}

func TestAudioLifecycle(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	t0 := time.Unix(1_788_000_000, 0)
	id, err := s.StartAudio(ctx, t0, AudioStream{Stream: "57", App: "Chrome"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.TouchAudio(ctx, id, t0.Add(90*time.Second)); err != nil {
		t.Fatal(err)
	}
	open, err := s.OpenAudioStreams(ctx)
	if err != nil || open["57"] != id {
		t.Fatalf("open = %v, %v", open, err)
	}
	if err := s.EndAudio(ctx, id); err != nil {
		t.Fatal(err)
	}
	var end int64
	if err := s.DB().QueryRow(`SELECT end FROM audio WHERE id=?`, id).Scan(&end); err != nil {
		t.Fatal(err)
	}
	if end != t0.Add(90*time.Second).Unix() {
		t.Errorf("end should be last_seen, got %d", end)
	}
	if open, _ := s.OpenAudioStreams(ctx); len(open) != 0 {
		t.Errorf("still open: %v", open)
	}
}

func TestPresenceAndPrune(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	old := Presence{TS: time.Now().Add(-100 * 24 * time.Hour), IdleMS: 5}
	fresh := Presence{TS: time.Now(), IdleMS: 5, InhibitFlags: 8, Inhibitors: "caffeine-gnome-extension"}
	for _, p := range []Presence{old, fresh} {
		if err := s.InsertPresence(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PrunePresence(ctx, 90*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func TestFileModes(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.InsertPresence(context.Background(), Presence{TS: time.Now()}); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		fi, err := os.Stat(filepath.Join(dir, "v.db"+suffix))
		if err != nil {
			t.Fatalf("%s: %v", suffix, err)
		}
		if m := fi.Mode().Perm(); m != 0o600 {
			t.Errorf("v.db%s mode = %o, want 600", suffix, m)
		}
	}
}

func TestShellUpsertAndState(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	c := ShellCmd{ID: "a1", TS: time.Now(), CWD: "/h/x", Binary: "drush", Sub: "cr", Duration: -1, Exit: -1}
	if err := s.UpsertShell(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.Duration = 40 * time.Second
	c.Exit = 0
	if err := s.UpsertShell(ctx, c); err != nil {
		t.Fatal(err)
	}
	var n, dur, exit int
	if err := s.DB().QueryRow(`SELECT COUNT(*), MAX(duration), MAX(exit) FROM shell`).Scan(&n, &dur, &exit); err != nil {
		t.Fatal(err)
	}
	if n != 1 || dur != 40000 || exit != 0 {
		t.Errorf("n=%d dur=%d exit=%d", n, dur, exit)
	}
	if v, _ := s.GetState(ctx, "x"); v != "" {
		t.Error("missing state should be empty")
	}
	_ = s.SetState(ctx, "x", "1")
	_ = s.SetState(ctx, "x", "2")
	if v, _ := s.GetState(ctx, "x"); v != "2" {
		t.Errorf("state = %q", v)
	}
}

func TestCommitPrune(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now()
	for _, sha := range []string{"old", "keep", "amended"} {
		ts := now
		if sha == "old" {
			ts = now.Add(-48 * time.Hour)
		}
		if err := s.UpsertCommit(ctx, Commit{SHA: sha, TS: ts, Repo: "/r", Subject: sha}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PruneCommits(ctx, "/r", now.Add(-time.Hour), []string{"keep"})
	if err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	var left int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM commits`).Scan(&left)
	if left != 2 {
		t.Errorf("left %d, want old+keep", left)
	}
}

func TestMigrateFromV2(t *testing.T) {
	// Simulate a v2 database: full current schema minus shell.sub, stamped 2.
	dir := t.TempDir()
	path := filepath.Join(dir, "v.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	old := strings.Replace(schema, "    sub      TEXT,                           -- subcommand for known wrappers (git commit, ddev drush)\n", "", 1)
	if old == schema {
		t.Fatal("test setup: sub column line not found")
	}
	if _, err := db.Exec(old); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	_ = s.DB().QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != schemaVersion {
		t.Errorf("version %d", v)
	}
	if err := s.UpsertShell(context.Background(), ShellCmd{ID: "x", TS: time.Now(), CWD: "/", Binary: "ls", Sub: "y"}); err != nil {
		t.Errorf("sub column missing after migration: %v", err)
	}
}

func TestTableSQL(t *testing.T) {
	sql := tableSQL("sessions")
	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS sessions") || !strings.Contains(sql, "sessions_day") {
		t.Errorf("tableSQL(sessions) = %q", sql)
	}
	if strings.Contains(sql, "worklogs") {
		t.Error("picked up another table")
	}
}

func TestReplaceSessions(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	in := []Session{{Day: "2026-09-04", Bucket: "AUC", Start: now, End: now.Add(time.Hour), Label: "a", Remote: true}}
	if err := s.ReplaceSessions(ctx, "2026-09-04", in); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSessions(ctx, "2026-09-04", in); err != nil {
		t.Fatal(err)
	}
	var n, secs, remote int
	_ = s.DB().QueryRow(`SELECT COUNT(*), MAX(seconds), MAX(remote) FROM sessions`).Scan(&n, &secs, &remote)
	if n != 1 || secs != 3600 || remote != 1 {
		t.Errorf("n=%d secs=%d remote=%d", n, secs, remote)
	}
}

func TestWorklogsAndDays(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	wl := []Worklog{{Day: "2026-09-04", Bucket: "AUC", Issue: "EXT-88", Start: now, Seconds: 900, Observed: 480, Desc: "x"}}
	if err := s.ReplaceDrafts(ctx, "2026-09-04", wl); err != nil {
		t.Fatal(err)
	}
	got, _ := s.WorklogsForDay(ctx, "2026-09-04")
	if len(got) != 1 || got[0].State != WorklogDraft {
		t.Fatalf("%+v", got)
	}
	if err := s.MarkFailed(ctx, got[0].ID, "limit"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateWorklog(ctx, got[0].ID, 600, "shorter"); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.WorklogsForDay(ctx, "2026-09-04"); g[0].Seconds != 600 || g[0].State != WorklogDraft || g[0].Error != "" {
		t.Errorf("after update: %+v", g[0])
	}
	if err := s.MarkPushed(ctx, got[0].ID, "t-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateWorklog(ctx, got[0].ID, 1, "x"); err == nil {
		t.Error("pushed worklog must not be editable")
	}
	if err := s.ReplaceDrafts(ctx, "2026-09-04", wl); err == nil {
		t.Error("must refuse to replace pushed worklogs")
	}
	if err := s.DeleteWorklog(ctx, got[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceDrafts(ctx, "2026-09-04", wl); err != nil {
		t.Error("after delete, replace should work:", err)
	}
	if st, _ := s.DayState(ctx, "2026-09-04"); st != "" {
		t.Error("undecided day should be empty")
	}
	_ = s.SetDayState(ctx, "2026-09-04", DayDone)
	_ = s.SetDayState(ctx, "2026-09-05", DaySkipped)
	dd, _ := s.DecidedDays(ctx)
	if dd["2026-09-04"] != DayDone || dd["2026-09-05"] != DaySkipped {
		t.Errorf("%v", dd)
	}
	_ = s.AddNote(ctx, Note{TS: now, Day: "2026-09-04", Bucket: "AH", Hours: 1.5, Text: "call"})
	ns, _ := s.NotesForDay(ctx, "2026-09-04")
	if len(ns) != 1 || ns[0].Hours != 1.5 {
		t.Errorf("%+v", ns)
	}
}

func TestBackup(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	_ = s.InsertPresence(ctx, Presence{TS: time.Now(), IdleMS: 1})
	dst := filepath.Join(t.TempDir(), "b", "copy.db")
	if err := s.Backup(ctx, dst); err != nil {
		t.Fatal(err)
	}
	c, err := Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	_ = c.DB().QueryRow(`SELECT COUNT(*) FROM presence`).Scan(&n)
	if n != 1 {
		t.Errorf("backup rows = %d", n)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %o", fi.Mode().Perm())
	}
}
