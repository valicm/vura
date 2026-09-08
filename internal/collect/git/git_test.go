package git

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/store"
)

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=me@example.com",
		"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=me@example.com",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestCollect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	run(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0o644)
	run(t, repo, "add", ".")
	run(t, repo, "commit", "-q", "-m", "Initial")
	run(t, repo, "checkout", "-q", "-b", "feature/ACME-8600--ACME-8457")
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	run(t, repo, "commit", "-q", "-am", "Fix composer lock")
	// A commit by someone else must be ignored.
	cmd := exec.Command("git", "-C", repo, "commit", "-q", "--allow-empty", "-m", "BT-1 not mine")
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=X", "GIT_AUTHOR_EMAIL=x@example.com",
		"GIT_COMMITTER_NAME=X", "GIT_COMMITTER_EMAIL=x@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := &config.Config{Buckets: map[string]config.Bucket{
		"ACME": {Repos: []string{repo}, Tickets: []string{"ACME"}},
		"BETA": {Tickets: []string{"BT"}},
	}}
	c := New(st, bucket.New(cfg), []string{"me@example.com"}, 24*time.Hour, slog.Default())
	n, err := c.Collect(context.Background(), repo, "ACME")
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	rows, err := st.DB().Query(`SELECT subject, branch, ticket, bucket, files, ins, del FROM commits ORDER BY ts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		subject, branch, ticket, bucket string
		files, ins, del                 int
	}
	var got []row
	for rows.Next() {
		var r row
		var branch, ticket, bkt *string
		if err := rows.Scan(&r.subject, &branch, &ticket, &bkt, &r.files, &r.ins, &r.del); err != nil {
			t.Fatal(err)
		}
		if branch != nil {
			r.branch = *branch
		}
		if ticket != nil {
			r.ticket = *ticket
		}
		if bkt != nil {
			r.bucket = *bkt
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("rows: %+v", got)
	}
	fix := got[1]
	if fix.subject != "Fix composer lock" || fix.branch != "feature/ACME-8600--ACME-8457" || fix.ticket != "ACME-8600" ||
		fix.bucket != "ACME" || fix.files != 1 || fix.ins != 2 || fix.del != 0 {
		t.Errorf("feature commit = %+v", fix)
	}
	if got[0].subject != "Initial" || got[0].ticket != "" {
		t.Errorf("initial = %+v", got[0])
	}

	// Drop the foreign commit, then amend the feature commit: the old sha
	// must be pruned, the new one kept.
	run(t, repo, "reset", "-q", "--hard", "HEAD^")
	run(t, repo, "commit", "-q", "--amend", "-m", "Fix composer lock (amended)")
	if _, err := c.Collect(context.Background(), repo, "ACME"); err != nil {
		t.Fatal(err)
	}
	var cnt int
	var subj string
	_ = st.DB().QueryRow(`SELECT COUNT(*), MAX(subject) FROM commits WHERE branch LIKE 'feature/%'`).Scan(&cnt, &subj)
	if cnt != 1 || subj != "Fix composer lock (amended)" {
		t.Errorf("after amend: %d rows, subject %q", cnt, subj)
	}
}
