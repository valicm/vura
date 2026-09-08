package reconcile

import (
	"strings"
	"testing"
	"time"

	"github.com/valicm/vura/internal/clock"
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/session"
	"github.com/valicm/vura/internal/store"
)

var loc, _ = time.LoadLocation("Europe/Zagreb")

func at(h, m int) time.Time { return time.Date(2026, 9, 4, h, m, 0, 0, loc) }

func cfg() *config.Config {
	b, _ := clock.ParseBoundary("03:00")
	return &config.Config{
		Boundary: b, Location: loc,
		Session: config.Session{RoundMin: config.Dur{Duration: 15 * time.Minute}, MaxEntry: config.Dur{Duration: 4 * time.Hour}},
		Buckets: map[string]config.Bucket{
			"ACME": {Issue: "EXT-88", Label: "Acme"},
			"BETA": {Issue: "EXT-48", Label: "BETA"},
		},
	}
}

func sess(bucket, label string, a, b time.Time, tickets ...string) session.Session {
	return session.Session{Bucket: bucket, Label: label, Start: a, End: b, Kinds: map[session.Kind]int{}, Tickets: tickets}
}

func TestNewRoundsSplitsAndDescribes(t *testing.T) {
	c := cfg()
	ss := []session.Session{
		sess("ACME", "Acme", at(9, 0), at(14, 0), "ACME-8600"), // 5h -> split 4h + 1h
		sess("BETA", "beta-shop", at(15, 0), at(15, 8)),        // 8m -> 15m
		sess(session.BucketUnattributed, "", at(16, 0), at(16, 20)),
		sess(session.BucketCall, "Google Chrome", at(17, 0), at(17, 30)),
	}
	commits := []store.Commit{
		{TS: at(10, 0), Repo: "/h/Acme", Ticket: "ACME-8600", Subject: "ACME-8600 Drupal 11 readiness"},
		{TS: at(11, 0), Repo: "/h/Acme", Ticket: "ACME-8600", Subject: "ACME-8600: Composer bumps"},
		{TS: at(11, 5), Repo: "/h/Acme", Ticket: "ACME-8600", Subject: "Merge branch 'x'"},
		{TS: at(12, 0), Repo: "/h/Acme", Ticket: "ACME-8600", Subject: "composer bumps"}, // dup, case-insensitive
	}
	d := New(c, "2026-09-04", ss, commits, nil)
	if len(d.Entries) != 5 {
		t.Fatalf("want 5 entries (split + 3), got %d: %+v", len(d.Entries), d.Entries)
	}
	if d.Entries[0].Logged != 4*time.Hour || d.Entries[1].Logged != time.Hour || d.Entries[1].Start != at(13, 0) {
		t.Errorf("split: %+v %+v", d.Entries[0], d.Entries[1])
	}
	want := "ACME-8600 Drupal 11 readiness; Composer bumps"
	if d.Entries[0].Desc != want {
		t.Errorf("desc = %q, want %q", d.Entries[0].Desc, want)
	}
	if d.Entries[2].Logged != 15*time.Minute || d.Entries[2].Desc != "beta-shop development" {
		t.Errorf("short: %+v", d.Entries[2])
	}
	tot := d.Totals()
	if tot.Observed != 5*time.Hour+8*time.Minute || tot.Logged != 5*time.Hour+15*time.Minute || tot.Unassigned != 2 || tot.Blocking != 1 {
		t.Errorf("totals: %+v", tot)
	}
	if wl := d.Worklogs(); len(wl) != 3 || wl[0].Issue != "EXT-88" || wl[0].Seconds != 14400 {
		t.Errorf("worklogs: %+v", wl)
	}
}

func TestEdits(t *testing.T) {
	c := cfg()
	ss := []session.Session{
		sess("ACME", "zed", at(16, 40), at(16, 48)),
		sess("ACME", "zed", at(17, 22), at(17, 29)),
		sess(session.BucketCall, "Meet", at(15, 5), at(15, 48)),
		sess("BETA", "beta-shop", at(10, 0), at(11, 0)),
	}
	d := New(c, "2026-09-04", ss, nil, nil)
	// Order by start: beta(1) call(2) zed(3) zed(4)
	if err := d.Merge(3, 4); err != nil {
		t.Fatal(err)
	}
	m := d.Entries[2]
	if m.Observed != 15*time.Minute || m.Logged != 15*time.Minute || !d.Entries[3].Dropped || m.End != at(17, 29) {
		t.Errorf("merge: %+v", m)
	}
	if err := d.Merge(2, 3); err == nil {
		t.Error("cross-bucket merge must fail")
	}
	if err := d.Assign(2, "ACME"); err != nil {
		t.Fatal(err)
	}
	if d.Entries[1].Issue != "EXT-88" || d.Entries[1].Desc != "Acme call" || d.Totals().Blocking != 0 {
		t.Errorf("assign: %+v", d.Entries[1])
	}
	if err := d.Assign(1, "NOPE"); err == nil {
		t.Error("unknown bucket must fail")
	}
	if err := d.SetLogged(1, 90*time.Minute); err != nil || d.Entries[0].Logged != 90*time.Minute {
		t.Error("set logged")
	}
	_ = d.SetDesc(1, "  BT-1 things  ")
	if d.Entries[0].Desc != "BT-1 things" || d.Entries[0].AutoDesc {
		t.Error("set desc")
	}
	_ = d.Drop(1)
	if _, err := d.get(1); err == nil {
		t.Error("dropped entry must be unusable")
	}
	if err := d.AddNote("BETA", 30*time.Minute, "standup", at(9, 30)); err != nil {
		t.Fatal(err)
	}
	if len(d.Worklogs()) != 3 { // call(ACME), merged zed(ACME), note(BETA); beta dropped
		t.Errorf("worklogs: %+v", d.Worklogs())
	}
}

func TestDescribeTruncates(t *testing.T) {
	c := cfg()
	s := sess("ACME", "Acme", at(9, 0), at(10, 0), "ACME-1")
	var commits []store.Commit
	for i := 0; i < 30; i++ {
		commits = append(commits, store.Commit{TS: at(9, 1+i), Ticket: "ACME-1", Subject: strings.Repeat("x", 20) + string(rune('a'+i))})
	}
	if d := Describe(c, s, commits); len(d) > 250 || !strings.HasSuffix(d, "...") {
		t.Errorf("len %d: %q", len(d), d)
	}
}

func TestDescribeRefs(t *testing.T) {
	got := describeRefs([]string{
		"approve acme/webshop#12: Drupal 11", "comment acme/webshop#12: Drupal 11", "comment acme/webshop#12: Drupal 11",
		"transition ACME-8600: Report → In Review", "pr opened acme/x#3: Thing",
	})
	want := "Approved acme/webshop#12: Drupal 11; Commented on acme/webshop#12: Drupal 11; Moved ACME-8600: Report → In Review; Opened PR acme/x#3: Thing"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestOpsReplay(t *testing.T) {
	c := cfg()
	ss := []session.Session{
		sess("ACME", "zed", at(16, 40), at(16, 48)),
		sess("ACME", "zed", at(17, 22), at(17, 29)),
		sess(session.BucketCall, "Meet", at(15, 5), at(15, 48)),
	}
	d := New(c, "2026-09-04", ss, nil, nil)
	if err := d.Apply(Op{Kind: "assign", Target: d.Entries[0].Key(), Bucket: "beta"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(Op{Kind: "merge", Target: d.Entries[1].Key(), Others: []string{d.Entries[2].Key()}}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(Op{Kind: "desc", Target: d.Entries[1].Key(), Text: "merged zed"}); err != nil {
		t.Fatal(err)
	}
	if err := d.Apply(Op{Kind: "note", Bucket: "ACME", Dur: "30m", Text: "phone", At: at(9, 0).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if len(d.Ops) != 4 {
		t.Fatalf("ops %d", len(d.Ops))
	}
	// Rebuild from the same sessions and replay: same result.
	d2 := New(c, "2026-09-04", ss, nil, nil)
	if sk := d2.Replay(d.Ops); sk != 0 {
		t.Errorf("skipped %d", sk)
	}
	wl := d2.Worklogs()
	if len(wl) != 3 || wl[0].Bucket != "BETA" || wl[1].Desc != "merged zed" || wl[1].Seconds != 15*60 || wl[2].Desc != "phone" {
		t.Errorf("replayed worklogs: %+v", wl)
	}
	// Rebuild without the call: the assign op has no target and is skipped, the rest apply.
	d3 := New(c, "2026-09-04", ss[:2], nil, nil)
	if sk := d3.Replay(d.Ops); sk != 1 || len(d3.Ops) != 3 {
		t.Errorf("skipped %d, kept %d", sk, len(d3.Ops))
	}
}
