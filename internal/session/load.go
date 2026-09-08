package session

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/valicm/vura/internal/bucket"
	"github.com/valicm/vura/internal/config"
	"github.com/valicm/vura/internal/store"
)

// Day is Build applied to one working day of stored evidence. Attribution is
// re-derived from the current config every time, so a bucket fix in
// config.toml changes past days on the next run; nothing is baked in.
func Day(ctx context.Context, st *store.Store, cfg *config.Config, res *bucket.Resolver, day string) ([]Session, Params, error) {
	from, to, err := cfg.Boundary.Range(day, cfg.Location)
	if err != nil {
		return nil, Params{}, err
	}
	p := Params{
		DayStart: from, DayEnd: to,
		IdleGap: cfg.Session.IdleGap.Duration, AwayAfter: cfg.Session.AwayAfter.Duration,
		DetectMin:   cfg.Session.DetectMin.Duration,
		PresentIdle: 5 * time.Minute, PresenceSpan: 5 * time.Minute,
		ActiveStart: cfg.ActiveStart, ActiveEnd: cfg.ActiveEnd,
	}
	var ev []Evidence

	hbs, err := st.HeartbeatsRange(ctx, from, to)
	if err != nil {
		return nil, p, fmt.Errorf("heartbeats: %w", err)
	}
	type webBeat struct {
		ts     time.Time
		domain string
	}
	var meetings []webBeat // browser heartbeats on meeting domains, to label calls
	for _, h := range hbs {
		if h.Type == "domain" || h.Type == "url" {
			ent := strings.ToLower(h.Entity)
			if md := meetingDomain(cfg, ent); md != "" {
				meetings = append(meetings, webBeat{h.TS.In(cfg.Location), md})
				continue
			}
			bk := res.ByDomain(ent)
			if bk == "" {
				continue // not client work as far as the config knows
			}
			label := ent
			if i := strings.Index(label, "://"); i >= 0 {
				label = label[i+3:]
			}
			if i := strings.IndexByte(label, '/'); i >= 0 {
				label = label[:i]
			}
			ev = append(ev, Evidence{Start: h.TS.In(cfg.Location), End: h.TS.In(cfg.Location), Bucket: bk, Label: label, Kind: KindWeb})
			continue
		}
		kind := KindEditor
		if strings.HasPrefix(h.Editor, "claude-code/") {
			kind = KindClaude
		}
		bk := ""
		if strings.HasPrefix(h.Entity, "/") {
			bk, _ = res.ByPath(h.Entity)
		}
		if bk == "" && h.Project != "" {
			bk = res.ByProject(h.Project)
		}
		label := h.Project
		if label == "" {
			label = filepath.Base(h.Entity)
		}
		ev = append(ev, Evidence{Start: h.TS.In(cfg.Location), End: h.TS.In(cfg.Location), Bucket: bk, Label: label, Kind: kind})
	}

	cmds, err := st.ShellRange(ctx, from, to)
	if err != nil {
		return nil, p, fmt.Errorf("shell: %w", err)
	}
	for _, c := range cmds {
		bk, root := res.ByPath(c.CWD)
		label := filepath.Base(root)
		if root == "" {
			label = filepath.Base(c.CWD)
		}
		end := c.TS
		if c.Duration > 0 {
			end = c.TS.Add(c.Duration)
		}
		ev = append(ev, Evidence{Start: c.TS.In(cfg.Location), End: end.In(cfg.Location), Bucket: bk, Label: label, Kind: KindShell})
	}

	commits, err := st.CommitsRange(ctx, from, to)
	if err != nil {
		return nil, p, fmt.Errorf("commits: %w", err)
	}
	for _, c := range commits {
		bk, _ := res.ByPath(c.Repo)
		if c.Ticket != "" {
			if b := res.ByTicket(c.Ticket); b != "" {
				bk = b
			}
		}
		ev = append(ev, Evidence{Start: c.TS.In(cfg.Location), End: c.TS.In(cfg.Location), Bucket: bk,
			Label: filepath.Base(c.Repo), Kind: KindCommit, Ticket: c.Ticket})
	}

	calls, err := st.AudioRange(ctx, from, to)
	if err != nil {
		return nil, p, fmt.Errorf("audio: %w", err)
	}
	for _, a := range calls {
		start, end := a.Start.In(cfg.Location), a.End.In(cfg.Location)
		label := a.App
		for _, m := range meetings {
			if !m.ts.Before(start) && !m.ts.After(end) {
				label = m.domain
				break
			}
		}
		ev = append(ev, Evidence{Start: start, End: end, Label: label, Kind: KindCall})
	}

	events, err := st.EventsRange(ctx, from, to)
	if err != nil {
		return nil, p, fmt.Errorf("events: %w", err)
	}
	for _, e := range events {
		var att []string
		if e.Attendees != "" {
			att = strings.Split(e.Attendees, ",")
		}
		bk := res.ByEvent(e.Title, att)
		if strings.HasPrefix(e.Source, "slack:") {
			bk = res.BySlack(strings.TrimPrefix(e.Title, "Huddle "))
		}
		if bk == "" {
			bk = e.Bucket // the feed's default, e.g. a client workspace calendar
		}
		ev = append(ev, Evidence{Start: e.Start.In(cfg.Location), End: e.End.In(cfg.Location),
			Bucket: bk, Label: e.Title, Kind: KindEvent})
	}

	anchors, err := st.AnchorsRange(ctx, from, to)
	if err != nil {
		return nil, p, fmt.Errorf("anchors: %w", err)
	}
	for _, a := range anchors {
		bk, ticket := "", ""
		if a.Source == "slack" {
			bk = res.BySlack(a.Ref)
			ev = append(ev, Evidence{Start: a.TS.In(cfg.Location), End: a.TS.In(cfg.Location), Bucket: bk,
				Label: a.Ref, Kind: KindAnchor, Ref: "message " + a.Ref})
			continue
		}
		if ts := bucket.Tickets(a.Ref + " " + a.Title); len(ts) > 0 {
			ticket = ts[0]
			bk = res.ByTicket(ticket)
		}
		if bk == "" {
			// "acme/webshop#12" -> project "webshop"; else a domains rule like "github.com/acme".
			repo := a.Ref
			if i := strings.IndexAny(repo, "#!"); i >= 0 {
				repo = repo[:i]
			}
			bk = res.ByProject(filepath.Base(repo))
			if bk == "" {
				bk = res.ByDomain(a.Source + ".com/" + repo)
			}
			if bk == "" {
				bk = res.ByDomain(a.URL)
			}
		}
		ref := a.Kind + " " + a.Ref
		if a.Title != "" {
			ref += ": " + a.Title
		}
		ev = append(ev, Evidence{Start: a.TS.In(cfg.Location), End: a.TS.In(cfg.Location), Bucket: bk,
			Label: anchorLabel(a.Ref), Kind: KindAnchor, Ticket: ticket, Ref: ref})
	}

	prows, err := st.PresenceRange(ctx, from, to)
	if err != nil {
		return nil, p, fmt.Errorf("presence: %w", err)
	}
	pres := make([]Presence, 0, len(prows))
	for _, r := range prows {
		pres = append(pres, Presence{TS: r.TS.In(cfg.Location), IdleMS: r.IdleMS, Inhibited: r.InhibitFlags&8 != 0})
	}

	return Build(ev, pres, p), p, nil
}

// anchorLabel reduces "acme/webshop#12" to "webshop" and keeps ticket keys.
func anchorLabel(ref string) string {
	if i := strings.IndexAny(ref, "#!"); i >= 0 {
		ref = ref[:i]
	}
	return filepath.Base(ref)
}

// meetingDomain returns a display name if the entity is a known meeting site.
func meetingDomain(cfg *config.Config, entity string) string {
	for _, d := range cfg.Sources.MeetingDomains {
		if strings.Contains(entity, strings.ToLower(d)) {
			switch {
			case strings.Contains(d, "google"):
				return "Google Meet"
			case strings.Contains(d, "teams"):
				return "Teams"
			case strings.Contains(d, "zoom"):
				return "Zoom"
			}
			return d
		}
	}
	return ""
}

// ToStore converts for persistence.
func ToStore(day, device string, ss []Session) []store.Session {
	out := make([]store.Session, 0, len(ss))
	for _, s := range ss {
		out = append(out, store.Session{
			Day: day, Bucket: s.Bucket, Label: s.Label, Tickets: strings.Join(s.Tickets, ","),
			Source: s.SourceString(), Start: s.Start, End: s.End, Remote: s.Remote, Device: device,
		})
	}
	return out
}

// SourceString renders evidence counts as "editor:41,commit:3", stable order.
func (s Session) SourceString() string {
	keys := make([]string, 0, len(s.Kinds))
	for k := range s.Kinds {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", k, s.Kinds[Kind(k)]))
	}
	return strings.Join(parts, ",")
}

// Summary renders the evidence behind a session for a human: label, counts
// per kind, tickets.
func (s Session) Summary() string {
	var parts []string
	if s.Label != "" {
		parts = append(parts, s.Label)
	}
	kinds := make([]string, 0, len(s.Kinds))
	for k, n := range s.Kinds {
		switch k {
		case KindEditor:
			kinds = append(kinds, fmt.Sprintf("editor ×%d", n))
		case KindClaude:
			kinds = append(kinds, fmt.Sprintf("claude ×%d", n))
		case KindShell:
			kinds = append(kinds, fmt.Sprintf("%d cmds", n))
		case KindCommit:
			kinds = append(kinds, fmt.Sprintf("%d commits", n))
		case KindCall:
			kinds = append(kinds, "mic active")
		case KindWeb:
			kinds = append(kinds, fmt.Sprintf("browser ×%d", n))
		case KindEvent:
			kinds = append(kinds, "calendar")
		case KindAnchor:
			kinds = append(kinds, fmt.Sprintf("%d external", n))
		}
	}
	sort.Strings(kinds)
	parts = append(parts, kinds...)
	if len(s.Tickets) > 0 {
		parts = append(parts, strings.Join(s.Tickets, ", "))
	}
	return strings.Join(parts, " · ")
}
