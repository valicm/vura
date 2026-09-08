package session

import (
	"testing"
	"time"

	"github.com/valicm/vura/internal/clock"
)

var loc, _ = time.LoadLocation("Europe/Zagreb")

func at(h, m int) time.Time { return time.Date(2026, 9, 4, h, m, 0, 0, loc) }

func params() Params {
	from, _ := clock.ParseBoundary("08:00")
	to, _ := clock.ParseBoundary("01:00")
	return Params{
		DayStart: at(3, 0), DayEnd: at(3, 0).AddDate(0, 0, 1),
		IdleGap: 30 * time.Minute, AwayAfter: 10 * time.Minute, DetectMin: 5 * time.Minute,
		PresentIdle: 5 * time.Minute, PresenceSpan: 5 * time.Minute,
		ActiveStart: from, ActiveEnd: to,
	}
}

// heartbeats every `every` from a to b (inclusive of a, exclusive of b).
func beats(kind Kind, bucket, label string, a, b time.Time, every time.Duration) []Evidence {
	var out []Evidence
	for t := a; t.Before(b); t = t.Add(every) {
		out = append(out, Evidence{Start: t, End: t, Bucket: bucket, Label: label, Kind: kind})
	}
	return out
}

// present emits a sample every minute from a to b with idle = 0, or idle
// growing from `since` if since is set.
func present(a, b time.Time) []Presence {
	var out []Presence
	for t := a; t.Before(b); t = t.Add(time.Minute) {
		out = append(out, Presence{TS: t, IdleMS: 0})
	}
	return out
}

func idle(a, b time.Time, since time.Time) []Presence {
	var out []Presence
	for t := a; t.Before(b); t = t.Add(time.Minute) {
		out = append(out, Presence{TS: t, IdleMS: t.Sub(since).Milliseconds()})
	}
	return out
}

func find(ss []Session, bucket string) []Session {
	var out []Session
	for _, s := range ss {
		if s.Bucket == bucket {
			out = append(out, s)
		}
	}
	return out
}

func TestSteadyEditing(t *testing.T) {
	ev := beats(KindEditor, "ACME", "Acme", at(9, 34), at(12, 52), 2*time.Minute)
	pres := present(at(9, 30), at(13, 0))
	ss := Build(ev, pres, params())
	auc := find(ss, "ACME")
	if len(auc) != 1 {
		t.Fatalf("want 1 AUC session, got %+v", ss)
	}
	s := auc[0]
	if !s.Start.Equal(at(9, 34)) || !s.End.Equal(at(12, 50)) || s.Remote || s.Label != "Acme" {
		t.Errorf("session = %+v", s)
	}
	// 09:30-09:34 is under DetectMin; 12:50-13:00 (last sample 12:59 + one
	// interval) is 10m of presence with no evidence.
	if u := find(ss, BucketUnattributed); len(u) != 1 || u[0].Duration() != 10*time.Minute {
		t.Errorf("unattributed = %+v", u)
	}
}

func TestLunchSplitsByIdle(t *testing.T) {
	// Typing until 12:00, away until 12:26 (gap 26m < IdleGap 30m), typing again.
	ev := append(beats(KindEditor, "ACME", "a", at(11, 0), at(12, 1), 2*time.Minute),
		beats(KindEditor, "ACME", "a", at(12, 26), at(13, 1), 2*time.Minute)...)
	pres := append(present(at(11, 0), at(12, 1)), idle(at(12, 1), at(12, 26), at(12, 0))...)
	pres = append(pres, present(at(12, 26), at(13, 1))...)
	ss := find(Build(ev, pres, params()), "ACME")
	if len(ss) != 2 {
		t.Fatalf("want split into 2, got %+v", ss)
	}
	if !ss[0].End.Equal(at(12, 0)) {
		t.Errorf("first session should end at last input 12:00, got %v", ss[0].End)
	}
	if !ss[1].Start.Equal(at(12, 26)) {
		t.Errorf("second start %v", ss[1].Start)
	}
}

func TestReadingInsideGapIsAbsorbed(t *testing.T) {
	// Typing until 10:00, present but no evidence until 10:20, typing again.
	ev := append(beats(KindEditor, "ACME", "a", at(9, 0), at(10, 1), 2*time.Minute),
		beats(KindEditor, "ACME", "a", at(10, 20), at(11, 1), 2*time.Minute)...)
	pres := present(at(9, 0), at(11, 1))
	ss := find(Build(ev, pres, params()), "ACME")
	if len(ss) != 1 || ss[0].Duration() != 2*time.Hour {
		t.Errorf("want one 2h session, got %+v", ss)
	}
}

func TestClaudeWhileIdleIsRemote(t *testing.T) {
	// Keyboard untouched since 14:00; Claude heartbeats 14:05-15:05.
	ev := beats(KindClaude, "BETA", "beta-shop", at(14, 5), at(15, 6), 2*time.Minute)
	pres := idle(at(14, 0), at(15, 10), at(14, 0))
	ss := find(Build(ev, pres, params()), "BETA")
	if len(ss) != 1 {
		t.Fatalf("got %+v", ss)
	}
	if !ss[0].Remote || ss[0].Duration() != time.Hour {
		t.Errorf("want remote 1h, got remote=%v dur=%v", ss[0].Remote, ss[0].Duration())
	}
	// Same heartbeats with the keyboard active: not remote.
	ss = find(Build(ev, present(at(14, 0), at(15, 10)), params()), "BETA")
	if len(ss) != 1 || ss[0].Remote {
		t.Errorf("want non-remote, got %+v", ss)
	}
	// Short idle (under IdleGap) is not remote either.
	pres = append(present(at(14, 0), at(14, 40)), idle(at(14, 40), at(15, 10), at(14, 40))...)
	ss = find(Build(ev, pres, params()), "BETA")
	if len(ss) != 1 || ss[0].Remote {
		t.Errorf("idle under IdleGap should not flag remote: %+v", ss)
	}
}

func TestOutsideWindowIdleDrops(t *testing.T) {
	// 02:00-02:40 Claude heartbeats while idle since 01:00: not work.
	ev := beats(KindClaude, "ACME", "a", at(2, 0).AddDate(0, 0, 1), at(2, 41).AddDate(0, 0, 1), 2*time.Minute)
	pres := idle(at(1, 0).AddDate(0, 0, 1), at(2, 50).AddDate(0, 0, 1), at(1, 0).AddDate(0, 0, 1))
	if ss := find(Build(ev, pres, params()), "ACME"); len(ss) != 0 {
		t.Errorf("should drop, got %+v", ss)
	}
	// Same time but the keyboard is active: counts.
	pres = present(at(1, 0).AddDate(0, 0, 1), at(2, 50).AddDate(0, 0, 1))
	if ss := find(Build(ev, pres, params()), "ACME"); len(ss) != 1 {
		t.Errorf("should keep, got %+v", ss)
	}
}

func TestLongCommandAndShortBurst(t *testing.T) {
	ev := []Evidence{
		{Start: at(15, 0), End: at(15, 40), Bucket: "BETA", Label: "beta-shop", Kind: KindShell},
		{Start: at(17, 0), End: at(17, 0), Bucket: "ZED", Label: "zed", Kind: KindShell},
		{Start: at(17, 3), End: at(17, 3), Bucket: "ZED", Label: "zed", Kind: KindShell},
		{Start: at(18, 0), End: at(18, 0), Bucket: "ZED", Label: "zed", Kind: KindCommit, Ticket: "ZED-1"},
		{Start: at(18, 6), End: at(18, 6), Bucket: "ZED", Label: "zed", Kind: KindCommit, Ticket: "ZED-2"},
		{Start: at(18, 6), End: at(18, 6), Bucket: "ZED", Label: "zed", Kind: KindCommit, Ticket: "ZED-1"},
	}
	ss := Build(ev, nil, params())
	if f := find(ss, "BETA"); len(f) != 1 || f[0].Duration() != 40*time.Minute {
		t.Errorf("long command: %+v", f)
	}
	z := find(ss, "ZED")
	if len(z) != 1 || !z[0].Start.Equal(at(18, 0)) || len(z[0].Tickets) != 2 || z[0].Kinds[KindCommit] != 3 {
		t.Errorf("3-minute burst must drop, commit trio must stay as identity: %+v", z)
	}
	if !z[0].PointsOnly || z[0].Duration() != 0 || z[0].Span() != 6*time.Minute {
		t.Errorf("commits alone carry no duration: %+v", z[0])
	}
	// Commits inside real activity are not points-only.
	mixed := append(beats(KindEditor, "ZED", "zed", at(18, 0), at(18, 11), 2*time.Minute),
		Evidence{Start: at(18, 5), End: at(18, 5), Bucket: "ZED", Label: "zed", Kind: KindCommit, Ticket: "ZED-1"})
	if m := find(Build(mixed, nil, params()), "ZED"); len(m) != 1 || m[0].PointsOnly || m[0].Duration() != 10*time.Minute {
		t.Errorf("mixed: %+v", m)
	}
	// A single anchor is dropped; two anchors survive as identity.
	one := []Evidence{{Start: at(9, 0), End: at(9, 0), Bucket: "ACME", Kind: KindAnchor, Ref: "review x#1"}}
	if len(find(Build(one, nil, params()), "ACME")) != 0 {
		t.Error("lone anchor must drop")
	}
}

func TestUnmappedAndCalls(t *testing.T) {
	ev := append(beats(KindEditor, "", "gamma", at(10, 0), at(10, 31), 2*time.Minute),
		beats(KindEditor, "", "Barcode", at(10, 0), at(10, 31), 2*time.Minute)...)
	ev = append(ev, Evidence{Start: at(11, 0), End: at(11, 30), Kind: KindCall, Label: "Google Chrome"})
	ss := Build(ev, present(at(9, 55), at(11, 35)), params())
	if u := find(ss, BucketUnmapped); len(u) != 2 {
		t.Errorf("two unmapped projects must stay separate: %+v", u)
	}
	if c := find(ss, BucketCall); len(c) != 1 || c[0].Duration() != 30*time.Minute || c[0].Label != "Google Chrome" {
		t.Errorf("call: %+v", c)
	}
	// Presence with no keyboard evidence is unattributed, and a call does not
	// explain the keyboard: 09:55-10:00 (5m, exactly DetectMin) and 10:30-11:35.
	u := find(ss, BucketUnattributed)
	if len(u) != 2 || u[0].Duration() != 5*time.Minute || u[1].Duration() != 65*time.Minute {
		t.Errorf("unattributed: %+v", u)
	}
}

func TestOverlapAllowed(t *testing.T) {
	ev := append(beats(KindEditor, "ACME", "a", at(15, 0), at(16, 1), 2*time.Minute),
		Evidence{Start: at(15, 20), End: at(16, 0), Bucket: "BETA", Label: "f", Kind: KindShell})
	ss := Build(ev, nil, params())
	if len(find(ss, "ACME")) != 1 || len(find(ss, "BETA")) != 1 {
		t.Errorf("both buckets must have sessions: %+v", ss)
	}
}

func TestRoundUp(t *testing.T) {
	q := 15 * time.Minute
	for d, want := range map[time.Duration]time.Duration{
		8 * time.Minute: 15 * time.Minute, 15 * time.Minute: 15 * time.Minute,
		16 * time.Minute: 30 * time.Minute, 0: 0,
	} {
		if got := RoundUp(d, q); got != want {
			t.Errorf("RoundUp(%v) = %v, want %v", d, got, want)
		}
	}
}

func TestCalendarOverlay(t *testing.T) {
	p := params()
	// A mapped meeting 15:00-15:45 with the mic open 15:03-15:44: one AUC session labelled by the meeting.
	ev := []Evidence{
		{Start: at(15, 0), End: at(15, 45), Bucket: "ACME", Label: "Roadmap sync", Kind: KindEvent},
		{Start: at(15, 3), End: at(15, 44), Label: "Google Chrome", Kind: KindCall},
	}
	ss := Build(ev, nil, p)
	if a := find(ss, "ACME"); len(a) != 1 || a[0].Label != "Roadmap sync" || a[0].Kinds[KindCall] != 1 || a[0].Kinds[KindEvent] != 1 {
		t.Errorf("mapped meeting + call: %+v", ss)
	}
	if len(find(ss, BucketCall)) != 0 {
		t.Error("call should have been attributed")
	}
	// Unmapped meeting + call: stays a call, but carries the title.
	ev = []Evidence{
		{Start: at(15, 0), End: at(15, 45), Bucket: "", Label: "Dentist", Kind: KindEvent},
		{Start: at(15, 3), End: at(15, 44), Label: "Google Chrome", Kind: KindCall},
	}
	ss = Build(ev, nil, p)
	if c := find(ss, BucketCall); len(c) != 1 || c[0].Label != "Dentist" {
		t.Errorf("unmapped meeting + call: %+v", ss)
	}
	// Mapped meeting, no call, nobody present: not confirmed, nothing logged.
	ev = []Evidence{{Start: at(15, 0), End: at(15, 45), Bucket: "ACME", Label: "Skipped", Kind: KindEvent}}
	if ss := find(Build(ev, idle(at(14, 0), at(16, 0), at(14, 0)), p), "ACME"); len(ss) != 0 {
		t.Errorf("unconfirmed meeting must drop: %+v", ss)
	}
	// Mapped meeting, no call, present throughout: counts as the meeting.
	ss = Build(ev, present(at(14, 55), at(15, 50)), p)
	if a := find(ss, "ACME"); len(a) != 1 || a[0].Duration() != 45*time.Minute {
		t.Errorf("present meeting: %+v", ss)
	}
	// Web evidence on a mapped domain clusters like an editor heartbeat.
	web := beats(KindWeb, "BETA", "gitlab.beta.example", at(10, 0), at(10, 31), 1*time.Minute)
	if f := find(Build(web, nil, p), "BETA"); len(f) != 1 || f[0].Duration() != 30*time.Minute {
		t.Errorf("web: %+v", f)
	}
}
