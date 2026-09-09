// Package session turns raw evidence into observed sessions per bucket.
// This is the arithmetic that becomes invoices, so it is a pure function
// over loaded inputs, deterministic, and covered by tests. No LLM, no
// guessing: where the data is ambiguous the output says so (bucket "" for
// unattributed presence, "?" for evidence from an unmapped project, "call"
// for a microphone stream nobody has assigned yet).
package session

import (
	"sort"
	"time"

	"github.com/valicm/vura/internal/clock"
)

// Kind is where a piece of evidence came from.
type Kind string

const (
	KindEditor Kind = "editor" // IDE heartbeat
	KindClaude Kind = "claude" // Claude Code heartbeat
	KindShell  Kind = "shell"  // terminal command
	KindCommit Kind = "commit" // git commit (identity, but also a moment of activity)
	KindCall   Kind = "call"   // microphone capture stream
	KindWeb    Kind = "web"    // browser heartbeat on a mapped domain
	KindEvent  Kind = "event"  // calendar meeting, confirmed by presence or a call
	KindAnchor Kind = "anchor" // external activity: PR review, MR approval, ticket comment
)

// Well-known pseudo buckets in the output.
const (
	BucketUnattributed = ""     // presence with no evidence
	BucketUnmapped     = "?"    // evidence from a project no bucket claims
	BucketCall         = "call" // a call not yet assigned
)

// Evidence is one signal, already resolved to a bucket where possible.
type Evidence struct {
	Start, End time.Time // End == Start for point evidence
	Bucket     string    // "" if unresolved
	Label      string    // project / repo / app name, for display and grouping of unresolved
	Kind       Kind
	Ticket     string // commits and Jira anchors
	Ref        string // anchors: "review acme/repo#12: title", for descriptions
}

// Presence is one sampler row.
type Presence struct {
	TS        time.Time
	IdleMS    int64
	Inhibited bool
}

// Params are the tunables, all from config.
type Params struct {
	DayStart, DayEnd       time.Time
	IdleGap                time.Duration // longer gap between evidence closes a session
	AwayAfter              time.Duration // keyboard idle beyond this, with no evidence, closes a session
	DetectMin              time.Duration // shorter sessions are dropped
	PresentIdle            time.Duration // idle below this counts as "present"
	PresenceSpan           time.Duration // max time one presence row covers (sampler gap cap)
	ActiveStart, ActiveEnd clock.Boundary
	// EvidenceTimeout: a moment of evidence keeps its bucket active this long.
	EvidenceTimeout time.Duration
	// ShareAll: every bucket shares each minute (day never exceeds wall clock).
	// Otherwise autonomous evidence counts in full and only attention evidence
	// (editor, browser, quick commands) is shared.
	ShareAll bool
}

// Session is the output unit. Overlap between buckets is allowed.
type Session struct {
	Bucket  string
	Label   string
	Start   time.Time
	End     time.Time
	Remote  bool // evidence continued while local input was idle past IdleGap
	Kinds   map[Kind]int
	Tickets []string
	Refs    []string // anchor descriptions in time order, de-duplicated
	// PointsOnly: every piece of evidence is a moment (commit, anchor) with
	// no activity signal behind it. Identity without duration: shown, never
	// counted until the user assigns time.
	PointsOnly bool
	// Allocated is the session's share of wall-clock time after minutes
	// shared with other buckets are split. Duration() reports it.
	Allocated time.Duration
	// Shared is true when a meaningful part of the span went to other buckets.
	Shared bool

	cover []coverSpan // evidence coverage inside the span
}

// coverSpan is one piece of evidence coverage; auto marks evidence that
// progresses without the user's attention (Claude, a call, a meeting, a
// running command) and so is not shared with other buckets in parallel mode.
type coverSpan struct {
	a, b time.Time
	auto bool
}

// Duration is observed time: the session's allocated share of the wall
// clock; zero for a points-only session.
func (s Session) Duration() time.Duration {
	if s.PointsOnly {
		return 0
	}
	return s.Allocated
}

// Span is first to last evidence, regardless of kind.
func (s Session) Span() time.Duration { return s.End.Sub(s.Start) }

// Build clusters evidence into sessions. Inputs need not be sorted.
func Build(ev []Evidence, pres []Presence, p Params) []Session {
	sort.Slice(pres, func(i, j int) bool { return pres[i].TS.Before(pres[j].TS) })
	sort.Slice(ev, func(i, j int) bool { return ev[i].Start.Before(ev[j].Start) })
	tl := timeline{rows: pres, span: p.PresenceSpan}
	ev = overlayEvents(ev, tl, p)

	// Group evidence by bucket key. Unresolved evidence groups by label so
	// two unknown projects do not merge into one session.
	groups := map[string][]Evidence{}
	var order []string
	for _, e := range ev {
		if e.End.Before(e.Start) {
			e.End = e.Start
		}
		// Clamp to the day.
		if e.End.Before(p.DayStart) || !e.Start.Before(p.DayEnd) {
			continue
		}
		if e.Start.Before(p.DayStart) {
			e.Start = p.DayStart
		}
		if e.End.After(p.DayEnd) {
			e.End = p.DayEnd
		}
		// Outside the active window, idle means away: evidence that arrives
		// while input has been idle past IdleGap is not work.
		if !clock.InWindow(e.Start, p.ActiveStart, p.ActiveEnd) && e.Kind != KindCall && e.Kind != KindEvent {
			if idle, ok := tl.idleAt(e.Start); ok && idle > p.IdleGap {
				continue
			}
		}
		key := e.Bucket
		if key == "" {
			if e.Kind == KindCall {
				key = BucketCall
			} else {
				key = BucketUnmapped + ":" + e.Label
			}
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], e)
	}
	sort.Strings(order)

	var out []Session
	for _, key := range order {
		out = append(out, cluster(groups[key], tl, p)...)
	}
	var kept []Session
	for _, s := range out {
		if s.PointsOnly {
			// Two or more moments are worth showing; a lone commit is noise.
			if n := s.Kinds[KindCommit] + s.Kinds[KindAnchor]; n >= 2 {
				kept = append(kept, s)
			}
			continue
		}
		if s.End.Sub(s.Start) >= p.DetectMin {
			kept = append(kept, s)
		}
	}
	allocate(kept, tl, p)
	// Drop sessions whose share fell under the floor.
	n := 0
	for _, s := range kept {
		if s.PointsOnly || s.Duration() >= p.DetectMin {
			kept[n] = s
			n++
		}
	}
	kept = kept[:n]
	// Presence with no surviving session over it is unattributed.
	for _, s := range unattributed(tl, kept, p) {
		s.Allocated = s.End.Sub(s.Start)
		if s.Duration() >= p.DetectMin {
			kept = append(kept, s)
		}
	}
	sort.Slice(kept, func(i, j int) bool {
		if !kept[i].Start.Equal(kept[j].Start) {
			return kept[i].Start.Before(kept[j].Start)
		}
		return kept[i].Bucket < kept[j].Bucket
	})
	return kept
}

// overlayEvents applies the calendar. A meeting counts only if something
// confirms it happened: a call overlapping it, or presence during it. A call
// overlapping a mapped meeting takes the meeting's bucket and title; a call
// overlapping an unmapped meeting takes the title so the user can assign it.
func overlayEvents(ev []Evidence, tl timeline, p Params) []Evidence {
	var events, calls []int
	for i, e := range ev {
		switch e.Kind {
		case KindEvent:
			events = append(events, i)
		case KindCall:
			calls = append(calls, i)
		}
	}
	if len(events) == 0 {
		return ev
	}
	confirmed := map[int]bool{}
	for _, ei := range events {
		e := &ev[ei]
		for _, ci := range calls {
			c := &ev[ci]
			if c.Start.Before(e.End) && e.Start.Before(c.End) {
				confirmed[ei] = true
				if c.Bucket == "" && e.Bucket != "" {
					c.Bucket = e.Bucket
				}
				if e.Label != "" {
					c.Label = e.Label
				}
			}
		}
		if !confirmed[ei] && tl.presentDuring(e.Start, e.End, p.PresentIdle) >= minDuration(e.End.Sub(e.Start)/4, p.DetectMin) {
			confirmed[ei] = true
		}
	}
	out := make([]Evidence, 0, len(ev))
	for i, e := range ev {
		if e.Kind == KindEvent && (!confirmed[i] || e.Bucket == "") {
			continue // unconfirmed, or a meeting no bucket claims (personal)
		}
		out = append(out, e)
	}
	return out
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// allocate shares wall-clock minutes among concurrently active sessions.
// Each minute holds up to two units: one for the keyboard (attention
// evidence: editor, browser, quick commands, shared equally among the
// buckets touched) and one for work that runs on its own (autonomous
// evidence: Claude, calls, meetings, long commands, shared equally among
// those). Three Claude sessions at once share one unit; a Claude run beside
// your own typing counts beside it. If nothing covers a minute, the sessions
// whose span contains it split the keyboard unit, provided the keyboard was
// in use, so reading inside one session counts and an idle span does not.
// Points-only sessions take no share. With ShareAll, everything is
// attention and a day never exceeds the wall clock.
func allocate(ss []Session, tl timeline, p Params) {
	if len(ss) == 0 {
		return
	}
	for i := range ss {
		ss[i].cover = clipCover(ss[i].cover, ss[i].Start, ss[i].End)
	}
	start, end := ss[0].Start, ss[0].End
	for _, s := range ss {
		if s.Start.Before(start) {
			start = s.Start
		}
		if s.End.After(end) {
			end = s.End
		}
	}
	start = start.Truncate(time.Minute)
	shares := make([]float64, len(ss))
	var auto, attention, spanning []int
	for m := start; m.Before(end); m = m.Add(time.Minute) {
		mid := m.Add(30 * time.Second)
		auto, attention, spanning = auto[:0], attention[:0], spanning[:0]
		for i, s := range ss {
			if s.PointsOnly || !inSpan(mid, s.Start, s.End) {
				continue
			}
			spanning = append(spanning, i)
			isAuto, isAtt := false, false
			for _, c := range s.cover {
				if inSpan(mid, c.a, c.b) {
					if c.auto {
						isAuto = true
					} else {
						isAtt = true
					}
				}
			}
			switch {
			case isAuto:
				auto = append(auto, i)
			case isAtt:
				attention = append(attention, i)
			}
		}
		for _, i := range auto {
			shares[i] += 1 / float64(len(auto))
		}
		active := attention
		if len(active) == 0 && len(auto) == 0 {
			if idle, ok := tl.idleAt(mid); ok && idle >= p.PresentIdle {
				continue // nobody at the keyboard and no evidence: not work
			}
			active = spanning
		}
		for _, i := range active {
			shares[i] += 1 / float64(len(active))
		}
	}
	for i := range ss {
		ss[i].Allocated = time.Duration(shares[i] * float64(time.Minute)).Round(time.Second)
		if sp := ss[i].End.Sub(ss[i].Start); sp > 0 && ss[i].Allocated < sp*9/10 {
			ss[i].Shared = true
		}
	}
}

func inSpan(t, a, b time.Time) bool { return !t.Before(a) && t.Before(b) }

func clipCover(spans []coverSpan, a, b time.Time) []coverSpan {
	var out []coverSpan
	for _, s := range spans {
		if s.a.Before(a) {
			s.a = a
		}
		if s.b.After(b) {
			s.b = b
		}
		if s.b.After(s.a) {
			out = append(out, s)
		}
	}
	return out
}

// cluster walks one bucket's evidence in time order.
func cluster(ev []Evidence, tl timeline, p Params) []Session {
	var out []Session
	var cur *Session
	labels := map[string]int{}
	flush := func() {
		if cur == nil {
			return
		}
		cur.Label = top(labels)
		cur.PointsOnly = true
		for k := range cur.Kinds {
			if k != KindCommit && k != KindAnchor {
				cur.PointsOnly = false
			}
		}
		out = append(out, *cur)
		cur = nil
		labels = map[string]int{}
	}
	for _, e := range ev {
		if cur != nil {
			gap := e.Start.Sub(cur.End)
			closeAt, away := tl.awayBetween(cur.End, e.Start, p.AwayAfter)
			switch {
			case gap > p.IdleGap:
				flush()
			case away && e.Kind != KindClaude && e.Kind != KindCall && e.Kind != KindEvent:
				// Nothing happened during the gap and the keyboard went quiet:
				// the session ended when input stopped, not when the next
				// evidence arrived. Claude heartbeats are exempt: that is the
				// phone-driven case and it counts, flagged.
				if closeAt.After(cur.End) {
					cur.End = closeAt
				}
				flush()
			}
		}
		if cur == nil {
			cur = &Session{Bucket: bucketOf(e), Start: e.Start, End: e.End, Kinds: map[Kind]int{}}
		}
		if e.End.After(cur.End) {
			cur.End = e.End
		}
		// A moment keeps the bucket active for EvidenceTimeout; an interval
		// covers itself. Merged later, clipped to the span at allocation.
		ce := e.End
		if t := e.Start.Add(p.EvidenceTimeout); t.After(ce) {
			ce = t
		}
		auto := e.Kind == KindClaude || e.Kind == KindCall || e.Kind == KindEvent ||
			(e.Kind == KindShell && e.End.Sub(e.Start) >= time.Minute)
		cur.cover = append(cur.cover, coverSpan{e.Start, ce, auto && !p.ShareAll})
		cur.Kinds[e.Kind]++
		if e.Label != "" {
			labels[e.Label]++
		}
		if e.Ticket != "" && !contains(cur.Tickets, e.Ticket) {
			cur.Tickets = append(cur.Tickets, e.Ticket)
		}
		if e.Ref != "" && !contains(cur.Refs, e.Ref) {
			cur.Refs = append(cur.Refs, e.Ref)
		}
		if e.Kind == KindClaude {
			if idle, ok := tl.idleAt(e.Start); ok && idle > p.IdleGap {
				cur.Remote = true
			}
		}
	}
	flush()
	return out
}

func bucketOf(e Evidence) string {
	if e.Bucket != "" {
		return e.Bucket
	}
	if e.Kind == KindCall {
		return BucketCall
	}
	return BucketUnmapped
}

// unattributed returns spans where the sampler saw input but no bucket
// session covers the time.
func unattributed(tl timeline, sessions []Session, p Params) []Session {
	present := tl.presentSpans(p.PresentIdle, p.DayStart, p.DayEnd)
	if len(present) == 0 {
		return nil
	}
	var covered []span
	for _, s := range sessions {
		if s.Bucket == BucketCall {
			continue // a call does not explain what the keyboard was doing
		}
		covered = append(covered, span{s.Start, s.End})
	}
	covered = merge(covered)
	var out []Session
	for _, sp := range subtract(present, covered) {
		out = append(out, Session{Bucket: BucketUnattributed, Start: sp.a, End: sp.b, Kinds: map[Kind]int{}})
	}
	return out
}

// --- presence timeline -----------------------------------------------------

type timeline struct {
	rows []Presence // sorted
	span time.Duration
}

// idleAt returns the idle duration reported by the nearest sample at or
// before t (within span), or ok=false if the sampler was not running.
func (tl timeline) idleAt(t time.Time) (time.Duration, bool) {
	i := sort.Search(len(tl.rows), func(i int) bool { return tl.rows[i].TS.After(t) }) - 1
	if i < 0 {
		return 0, false
	}
	r := tl.rows[i]
	if t.Sub(r.TS) > tl.span {
		return 0, false
	}
	// idle as of t = idle at sample + time since sample
	return time.Duration(r.IdleMS)*time.Millisecond + t.Sub(r.TS), true
}

// awayBetween reports whether any sample strictly inside (a, b) shows input
// idle for longer than limit, and when input last happened before that.
func (tl timeline) awayBetween(a, b time.Time, limit time.Duration) (lastInput time.Time, away bool) {
	i := sort.Search(len(tl.rows), func(i int) bool { return tl.rows[i].TS.After(a) })
	for ; i < len(tl.rows) && tl.rows[i].TS.Before(b); i++ {
		r := tl.rows[i]
		idle := time.Duration(r.IdleMS) * time.Millisecond
		if idle > limit {
			return r.TS.Add(-idle), true
		}
	}
	return time.Time{}, false
}

// presentDuring returns how much of [a, b) had recent input.
func (tl timeline) presentDuring(a, b time.Time, presentIdle time.Duration) time.Duration {
	var total time.Duration
	for _, sp := range tl.presentSpans(presentIdle, a, b) {
		total += sp.b.Sub(sp.a)
	}
	return total
}

// presentSpans returns merged intervals during which input was recent. Each
// sample covers until the next one, capped at span.
func (tl timeline) presentSpans(presentIdle time.Duration, from, to time.Time) []span {
	var out []span
	for i, r := range tl.rows {
		if r.TS.Before(from) || !r.TS.Before(to) {
			continue
		}
		if time.Duration(r.IdleMS)*time.Millisecond >= presentIdle {
			continue
		}
		end := r.TS.Add(tl.span)
		switch {
		case i+1 < len(tl.rows):
			if tl.rows[i+1].TS.Before(end) {
				end = tl.rows[i+1].TS
			}
		case i > 0:
			// Last sample of the run: nothing says how long presence lasted,
			// so assume one more sampling interval, not the full cap.
			if e := r.TS.Add(r.TS.Sub(tl.rows[i-1].TS)); e.Before(end) {
				end = e
			}
		default:
			end = r.TS.Add(time.Minute)
		}
		if end.After(to) {
			end = to
		}
		out = append(out, span{r.TS, end})
	}
	return merge(out)
}

// --- interval arithmetic ---------------------------------------------------

type span struct{ a, b time.Time }

func merge(in []span) []span {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool { return in[i].a.Before(in[j].a) })
	out := []span{in[0]}
	for _, s := range in[1:] {
		last := &out[len(out)-1]
		if !s.a.After(last.b) {
			if s.b.After(last.b) {
				last.b = s.b
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// subtract returns the parts of a (merged) not covered by b (merged).
func subtract(a, b []span) []span {
	var out []span
	for _, s := range a {
		cur := s
		for _, c := range b {
			if !c.b.After(cur.a) || !c.a.Before(cur.b) {
				continue
			}
			if c.a.After(cur.a) {
				out = append(out, span{cur.a, c.a})
			}
			if c.b.Before(cur.b) {
				cur.a = c.b
			} else {
				cur.a = cur.b
				break
			}
		}
		if cur.b.After(cur.a) {
			out = append(out, cur)
		}
	}
	return out
}

func top(m map[string]int) string {
	best, n := "", 0
	for k, v := range m {
		if v > n || (v == n && k < best) {
			best, n = k, v
		}
	}
	return best
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// RoundUp rounds d up to the next multiple of unit; a positive d never
// rounds to zero.
func RoundUp(d, unit time.Duration) time.Duration {
	if unit <= 0 || d <= 0 {
		return d
	}
	if r := d % unit; r != 0 {
		return d + unit - r
	}
	return d
}
