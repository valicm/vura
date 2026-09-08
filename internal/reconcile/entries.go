// Package reconcile turns a day's sessions into worklog entries and applies
// the user's edits. Everything here is arithmetic and string assembly; the
// numbers are traceable to sessions and the user's explicit changes.
package reconcile

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/session"
	"github.com/valicm/vura/internal/store"
)

// Entry is one candidate worklog on the reconcile screen.
type Entry struct {
	N        int    // 1-based row number shown to the user
	Bucket   string // "" = needs assignment (unattributed presence, call, unmapped)
	Issue    string
	Label    string
	Start    time.Time
	End      time.Time
	Observed time.Duration
	Logged   time.Duration // after rounding and edits
	Desc     string
	Detail   string // evidence summary, display only
	AutoDesc bool   // Desc was generated, not written by the user
	Remote   bool
	Kind     string // "session" | "call" | "presence" | "unmapped" | "note"
	Tickets  []string
	Dropped  bool
	Merged   []int    // row numbers merged into this one
	Commits  []string // commit subjects, for Claude descriptions
	Identity bool     // moments only (commits, anchors): zero time until edited

	origBucket string // bucket at build time; part of the entry's stable key
}

// Day is the reconcile state for one working day.
type Day struct {
	Day     string
	Entries []Entry
	Ops     []Op // edits applied, in order
	cfg     *config.Config
}

// New builds entries from sessions, commits (for descriptions) and notes.
func New(cfg *config.Config, day string, ss []session.Session, commits []store.Commit, notes []store.Note) *Day {
	d := &Day{Day: day, cfg: cfg}
	round := cfg.Session.RoundMin.Duration
	maxEntry := cfg.Session.MaxEntry.Duration
	for _, s := range ss {
		e := Entry{Label: s.Label, Start: s.Start, End: s.End, Observed: s.Duration(), Remote: s.Remote, Tickets: s.Tickets, Detail: s.Summary(), Identity: s.PointsOnly}
		switch s.Bucket {
		case session.BucketUnattributed:
			e.Kind = "presence"
		case session.BucketCall:
			e.Kind = "call"
		case session.BucketUnmapped:
			e.Kind = "unmapped"
		default:
			e.Kind = "session"
			e.Bucket = s.Bucket
			e.Issue = cfg.Buckets[s.Bucket].Issue
		}
		e.Logged = session.RoundUp(e.Observed, round)
		cs := commitsIn(commits, s)
		for _, c := range cs {
			e.Commits = append(e.Commits, c.Subject)
		}
		e.Desc, e.AutoDesc = Describe(cfg, s, cs), true
		// Split long sessions so no single worklog exceeds max_entry.
		for maxEntry > 0 && e.Logged > maxEntry {
			part := e
			part.End = part.Start.Add(maxEntry)
			part.Observed, part.Logged = maxEntry, maxEntry
			d.Entries = append(d.Entries, part)
			e.Start = part.End
			e.Observed -= maxEntry
			e.Logged -= maxEntry
		}
		d.Entries = append(d.Entries, e)
	}
	for _, n := range notes {
		start := n.TS
		if cfg.Boundary.Day(n.TS.In(cfg.Location)) != day {
			s, _, _ := cfg.Boundary.Range(day, cfg.Location)
			start = s.Add(6 * time.Hour) // 09:00 for a 03:00 boundary
		}
		dur := time.Duration(n.Hours * float64(time.Hour))
		d.Entries = append(d.Entries, Entry{
			Kind: "note", Bucket: n.Bucket, Issue: cfg.Buckets[n.Bucket].Issue, Label: "note",
			Start: start, End: start.Add(dur), Observed: dur, Logged: session.RoundUp(dur, round),
			Desc: n.Text, AutoDesc: n.Text == "",
		})
	}
	sort.SliceStable(d.Entries, func(i, j int) bool { return d.Entries[i].Start.Before(d.Entries[j].Start) })
	for i := range d.Entries {
		d.Entries[i].origBucket = d.Entries[i].Bucket
	}
	d.renumber()
	return d
}

func (d *Day) renumber() {
	for i := range d.Entries {
		d.Entries[i].N = i + 1
	}
}

func commitsIn(commits []store.Commit, s session.Session) []store.Commit {
	var out []store.Commit
	for _, c := range commits {
		if !c.TS.Before(s.Start) && !c.TS.After(s.End) && sameBucketCommit(c, s) {
			out = append(out, c)
		}
	}
	return out
}

// sameBucketCommit: the session already carries the tickets of its commits,
// so a commit belongs if its ticket is listed or, for ticketless commits, if
// the repo basename matches the session label.
func sameBucketCommit(c store.Commit, s session.Session) bool {
	if c.Ticket != "" {
		for _, t := range s.Tickets {
			if t == c.Ticket {
				return true
			}
		}
		return false
	}
	return strings.HasSuffix(c.Repo, "/"+s.Label)
}

// Describe builds a deterministic worklog description. Lead with ticket keys;
// list distinct commit subjects with the key stripped; fall back to the
// label. Kept under 250 characters.
func Describe(cfg *config.Config, s session.Session, commits []store.Commit) string {
	if len(commits) == 0 && len(s.Refs) > 0 {
		return describeRefs(s.Refs)
	}
	if len(commits) == 0 {
		label := s.Label
		if b, ok := cfg.Buckets[s.Bucket]; ok && b.Label != "" && (label == "" || s.Bucket == label) {
			label = b.Label
		}
		if s.Bucket == session.BucketCall {
			return "Call"
		}
		if label == "" {
			return ""
		}
		if len(s.Tickets) > 0 {
			return strings.Join(s.Tickets, ", ") + " " + label
		}
		return label + " development"
	}
	byTicket := map[string][]string{}
	var order []string
	seen := map[string]bool{}
	for _, c := range commits {
		key := c.Ticket
		if _, ok := byTicket[key]; !ok {
			order = append(order, key)
		}
		subj := strings.TrimSpace(c.Subject)
		if key != "" {
			subj = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(subj, key), ":"))
			subj = strings.TrimSpace(strings.TrimPrefix(subj, "-"))
		}
		if subj == "" || seen[strings.ToLower(subj)] || strings.HasPrefix(subj, "Merge ") || strings.HasPrefix(subj, "Revert \"Merge ") {
			continue
		}
		seen[strings.ToLower(subj)] = true
		byTicket[key] = append(byTicket[key], subj)
	}
	var parts []string
	for _, key := range order {
		subs := byTicket[key]
		switch {
		case key != "" && len(subs) > 0:
			parts = append(parts, key+" "+strings.Join(subs, "; "))
		case key != "":
			parts = append(parts, key)
		case len(subs) > 0:
			parts = append(parts, strings.Join(subs, "; "))
		}
	}
	out := strings.Join(parts, " · ")
	if len(out) > 250 {
		out = out[:247] + "..."
	}
	return out
}

// describeRefs words external activity: "Reviewed acme/repo#12 Drupal 11; commented on AUC-8600 Bug".
func describeRefs(refs []string) string {
	verbs := map[string]string{"review": "Reviewed", "approve": "Approved", "comment": "Commented on",
		"transition": "Moved", "update": "Updated", "push": "Pushed to", "message": "Messaged in"}
	var parts []string
	seen := map[string]bool{}
	for _, r := range refs {
		kind, rest, _ := strings.Cut(r, " ")
		verb := verbs[kind]
		if verb == "" {
			verb = strings.ToUpper(kind[:1]) + kind[1:]
			if k2, r2, ok := strings.Cut(rest, " "); ok && (kind == "pr" || kind == "mr" || kind == "issue") {
				verb, rest = strings.ToUpper(k2[:1])+k2[1:]+" "+strings.ToUpper(kind), r2
			}
		}
		key := verb + " " + rest
		if seen[key] {
			continue
		}
		seen[key] = true
		parts = append(parts, key)
	}
	out := strings.Join(parts, "; ")
	if len(out) > 250 {
		out = out[:247] + "..."
	}
	return out
}

// --- edits -------------------------------------------------------------------

func (d *Day) get(n int) (*Entry, error) {
	if n < 1 || n > len(d.Entries) {
		return nil, fmt.Errorf("no entry %d", n)
	}
	e := &d.Entries[n-1]
	if e.Dropped {
		return nil, fmt.Errorf("entry %d was dropped", n)
	}
	return e, nil
}

// SetLogged overrides the logged duration.
func (d *Day) SetLogged(n int, dur time.Duration) error {
	e, err := d.get(n)
	if err != nil {
		return err
	}
	if dur <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	e.Logged = dur
	e.Identity = false
	return nil
}

func (d *Day) SetDesc(n int, text string) error {
	e, err := d.get(n)
	if err != nil {
		return err
	}
	e.Desc, e.AutoDesc = strings.TrimSpace(text), false
	return nil
}

// Assign gives an entry a bucket (and its issue).
func (d *Day) Assign(n int, bucket string) error {
	e, err := d.get(n)
	if err != nil {
		return err
	}
	b, ok := d.cfg.Buckets[bucket]
	if !ok {
		return fmt.Errorf("unknown bucket %q", bucket)
	}
	e.Bucket, e.Issue = bucket, b.Issue
	if e.Kind == "presence" || e.Kind == "unmapped" || e.Kind == "call" {
		e.Kind = "session"
	}
	if e.AutoDesc && (e.Desc == "" || strings.HasSuffix(e.Desc, " development") || e.Desc == "Call") {
		label := b.Label
		if label == "" {
			label = bucket
		}
		if e.Desc == "Call" {
			e.Desc = label + " call"
		} else {
			e.Desc = label + " development"
		}
	}
	return nil
}

func (d *Day) Drop(n int) error {
	e, err := d.get(n)
	if err != nil {
		return err
	}
	e.Dropped = true
	return nil
}

// Merge folds entries ns[1:] into ns[0]: observed and logged add up, the
// span widens, descriptions join. All must share a bucket (or have none).
func (d *Day) Merge(ns ...int) error {
	if len(ns) < 2 {
		return fmt.Errorf("merge needs at least two entries")
	}
	first, err := d.get(ns[0])
	if err != nil {
		return err
	}
	for _, n := range ns[1:] {
		e, err := d.get(n)
		if err != nil {
			return err
		}
		if e.Bucket != first.Bucket {
			return fmt.Errorf("entry %d is %s, entry %d is %s; assign first", n, orNone(e.Bucket), ns[0], orNone(first.Bucket))
		}
		first.Observed += e.Observed
		first.Logged = session.RoundUp(first.Observed, d.cfg.Session.RoundMin.Duration)
		if e.Start.Before(first.Start) {
			first.Start = e.Start
		}
		if e.End.After(first.End) {
			first.End = e.End
		}
		for _, t := range e.Tickets {
			if !containsStr(first.Tickets, t) {
				first.Tickets = append(first.Tickets, t)
			}
		}
		if e.Desc != "" && e.Desc != first.Desc && !strings.Contains(first.Desc, e.Desc) {
			if first.Desc == "" || first.AutoDesc && strings.HasSuffix(first.Desc, " development") {
				first.Desc = e.Desc
			} else {
				first.Desc += " · " + e.Desc
			}
		}
		first.Remote = first.Remote || e.Remote
		first.Commits = append(first.Commits, e.Commits...)
		first.Merged = append(first.Merged, n)
		e.Dropped = true
	}
	return nil
}

// Entry returns a live entry by row number.
func (d *Day) Entry(n int) (*Entry, error) { return d.get(n) }

// Live returns row numbers of entries that are not dropped.
func (d *Day) Live() []int {
	var out []int
	for _, e := range d.Entries {
		if !e.Dropped {
			out = append(out, e.N)
		}
	}
	return out
}

// AddNote appends a manual entry.
func (d *Day) AddNote(bucket string, dur time.Duration, text string, at time.Time) error {
	b, ok := d.cfg.Buckets[bucket]
	if !ok {
		return fmt.Errorf("unknown bucket %q", bucket)
	}
	d.Entries = append(d.Entries, Entry{
		Kind: "note", Bucket: bucket, Issue: b.Issue, Label: "note", Start: at, End: at.Add(dur),
		Observed: dur, Logged: session.RoundUp(dur, d.cfg.Session.RoundMin.Duration), Desc: text, AutoDesc: text == "",
		origBucket: bucket,
	})
	d.renumber()
	return nil
}

// --- totals and readiness --------------------------------------------------------

type Totals struct {
	WallStart, WallEnd time.Time
	Observed, Logged   time.Duration
	Unassigned         int // live entries with no bucket
	Blocking           int // unassigned entries that carry evidence (unmapped, call)
}

func (d *Day) Totals() Totals {
	var t Totals
	for _, e := range d.Entries {
		if e.Dropped {
			continue
		}
		if t.WallStart.IsZero() || e.Start.Before(t.WallStart) {
			t.WallStart = e.Start
		}
		if e.End.After(t.WallEnd) {
			t.WallEnd = e.End
		}
		if e.Bucket == "" {
			t.Unassigned++
			if (e.Kind == "unmapped" || e.Kind == "call") && !e.Identity {
				t.Blocking++
			}
			continue
		}
		t.Observed += e.Observed
		t.Logged += e.Logged
	}
	return t
}

// Worklogs returns what accept would push: live, assigned entries with time.
func (d *Day) Worklogs() []store.Worklog {
	var out []store.Worklog
	for _, e := range d.Entries {
		if e.Dropped || e.Bucket == "" || e.Logged <= 0 {
			continue
		}
		desc := e.Desc
		if desc == "" {
			desc = d.cfg.Buckets[e.Bucket].Label + " development"
		}
		out = append(out, store.Worklog{
			Day: d.Day, Bucket: e.Bucket, Issue: e.Issue, Start: e.Start,
			Seconds: int(e.Logged.Seconds()), Observed: int(e.Observed.Seconds()), Desc: desc,
		})
	}
	return out
}

func orNone(s string) string {
	if s == "" {
		return "unassigned"
	}
	return s
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
