// Package ics parses iCalendar feeds (RFC 5545) well enough for meeting
// attribution: timed VEVENTs, TZID/UTC/floating times, common RRULEs,
// EXDATE, RECURRENCE-ID overrides and cancellations. All-day events are
// skipped; they are not meetings.
package ics

import (
	"bufio"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Event struct {
	UID       string
	Start     time.Time
	End       time.Time
	Title     string
	Attendees []string // email addresses, lower-case
	Organizer string
}

// ID is stable per occurrence.
func (e Event) ID() string { return e.UID + "@" + strconv.FormatInt(e.Start.Unix(), 10) }

type prop struct {
	name   string
	params map[string]string
	value  string
}

type vevent struct {
	props map[string][]prop
}

func (v vevent) first(name string) (prop, bool) {
	ps := v.props[name]
	if len(ps) == 0 {
		return prop{}, false
	}
	return ps[0], true
}

// Parse expands the feed into concrete occurrences overlapping [from, to).
// loc is used for floating times (no TZID, no Z).
func Parse(data string, from, to time.Time, loc *time.Location) ([]Event, error) {
	events, err := split(data)
	if err != nil {
		return nil, err
	}
	// Index overrides by UID + RECURRENCE-ID.
	type key struct {
		uid string
		at  int64
	}
	overrides := map[key]vevent{}
	var masters []vevent
	for _, ve := range events {
		if rid, ok := ve.first("RECURRENCE-ID"); ok {
			uid, _ := ve.first("UID")
			t, err := parseTime(rid, loc)
			if err != nil {
				continue
			}
			overrides[key{uid.value, t.Unix()}] = ve
			continue
		}
		masters = append(masters, ve)
	}
	var out []Event
	for _, ve := range masters {
		uid, _ := ve.first("UID")
		if st, ok := ve.first("STATUS"); ok && strings.EqualFold(st.value, "CANCELLED") {
			continue
		}
		dtstart, ok := ve.first("DTSTART")
		if !ok || isDate(dtstart) {
			continue
		}
		start, err := parseTime(dtstart, loc)
		if err != nil {
			continue
		}
		var dur time.Duration
		if dtend, ok := ve.first("DTEND"); ok {
			end, err := parseTime(dtend, loc)
			if err != nil {
				continue
			}
			dur = end.Sub(start)
		} else if d, ok := ve.first("DURATION"); ok {
			dur = parseDuration(d.value)
		}
		if dur <= 0 {
			dur = time.Hour
		}
		base := Event{UID: uid.value, Title: unescape(text(ve, "SUMMARY")), Attendees: attendees(ve), Organizer: mailto(text(ve, "ORGANIZER"))}
		exdates := map[int64]bool{}
		for _, p := range ve.props["EXDATE"] {
			for _, v := range strings.Split(p.value, ",") {
				if t, err := parseTime(prop{params: p.params, value: v}, loc); err == nil {
					exdates[t.Unix()] = true
				}
			}
		}
		var starts []time.Time
		if rr, ok := ve.first("RRULE"); ok {
			starts = expand(start, rr.value, from, to.Add(dur), loc)
		} else {
			starts = []time.Time{start}
		}
		for _, s := range starts {
			if exdates[s.Unix()] {
				continue
			}
			ev := base
			ev.Start, ev.End = s, s.Add(dur)
			if ov, ok := overrides[key{uid.value, s.Unix()}]; ok {
				if st, ok := ov.first("STATUS"); ok && strings.EqualFold(st.value, "CANCELLED") {
					continue
				}
				if ds, ok := ov.first("DTSTART"); ok {
					if t, err := parseTime(ds, loc); err == nil {
						ev.Start = t
						ev.End = t.Add(dur)
						if de, ok := ov.first("DTEND"); ok {
							if e, err := parseTime(de, loc); err == nil {
								ev.End = e
							}
						}
					}
				}
				if t := text(ov, "SUMMARY"); t != "" {
					ev.Title = unescape(t)
				}
				if a := attendees(ov); len(a) > 0 {
					ev.Attendees = a
				}
			}
			if ev.End.After(from) && ev.Start.Before(to) {
				out = append(out, ev)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// split unfolds lines and collects VEVENT blocks.
func split(data string) ([]vevent, error) {
	var lines []string
	sc := bufio.NewScanner(strings.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		l := strings.TrimRight(sc.Text(), "\r")
		if (strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t")) && len(lines) > 0 {
			lines[len(lines)-1] += l[1:]
			continue
		}
		lines = append(lines, l)
	}
	var out []vevent
	var cur *vevent
	depth := 0 // nesting inside VEVENT (VALARM etc.) is ignored
	for _, l := range lines {
		switch {
		case l == "BEGIN:VEVENT":
			cur = &vevent{props: map[string][]prop{}}
			depth = 0
		case l == "END:VEVENT":
			if cur != nil {
				out = append(out, *cur)
			}
			cur = nil
		case cur != nil && strings.HasPrefix(l, "BEGIN:"):
			depth++
		case cur != nil && strings.HasPrefix(l, "END:"):
			depth--
		case cur != nil && depth == 0:
			p, ok := parseProp(l)
			if ok {
				cur.props[p.name] = append(cur.props[p.name], p)
			}
		}
	}
	if cur != nil {
		return nil, fmt.Errorf("ics: unterminated VEVENT")
	}
	return out, nil
}

func parseProp(l string) (prop, bool) {
	// NAME;PARAM=VAL;PARAM2="quoted:val":VALUE — the colon that ends the
	// name/params is the first one outside quotes.
	inQ := false
	for i, r := range l {
		switch {
		case r == '"':
			inQ = !inQ
		case r == ':' && !inQ:
			head, val := l[:i], l[i+1:]
			parts := strings.Split(head, ";")
			p := prop{name: strings.ToUpper(parts[0]), params: map[string]string{}, value: val}
			for _, kv := range parts[1:] {
				if k, v, ok := strings.Cut(kv, "="); ok {
					p.params[strings.ToUpper(k)] = strings.Trim(v, `"`)
				}
			}
			return p, true
		}
	}
	return prop{}, false
}

func isDate(p prop) bool { return p.params["VALUE"] == "DATE" || len(p.value) == 8 }

// parseTime handles 20260904T093000Z, 20260904T093000 with TZID, floating.
func parseTime(p prop, loc *time.Location) (time.Time, error) {
	v := strings.TrimSpace(p.value)
	if strings.HasSuffix(v, "Z") {
		return time.ParseInLocation("20060102T150405Z", v, time.UTC)
	}
	l := loc
	if tz := p.params["TZID"]; tz != "" {
		if z, err := time.LoadLocation(strings.TrimPrefix(tz, "/")); err == nil {
			l = z
		}
	}
	if len(v) == 8 {
		return time.ParseInLocation("20060102", v, l)
	}
	return time.ParseInLocation("20060102T150405", v, l)
}

// parseDuration handles PT1H30M, P1D, PT45M.
func parseDuration(s string) time.Duration {
	s = strings.TrimPrefix(strings.ToUpper(s), "P")
	var d time.Duration
	num := ""
	inTime := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			num += string(r)
		case r == 'T':
			inTime = true
		default:
			n, _ := strconv.Atoi(num)
			num = ""
			switch {
			case r == 'W':
				d += time.Duration(n) * 7 * 24 * time.Hour
			case r == 'D':
				d += time.Duration(n) * 24 * time.Hour
			case r == 'H':
				d += time.Duration(n) * time.Hour
			case r == 'M' && inTime:
				d += time.Duration(n) * time.Minute
			case r == 'S':
				d += time.Duration(n) * time.Second
			}
		}
	}
	return d
}

func text(v vevent, name string) string {
	if p, ok := v.first(name); ok {
		return p.value
	}
	return ""
}

func unescape(s string) string {
	r := strings.NewReplacer(`\n`, " ", `\N`, " ", `\,`, ",", `\;`, ";", `\\`, `\`)
	return strings.TrimSpace(r.Replace(s))
}

func mailto(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(strings.ToLower(s), "mailto:"); i >= 0 {
		s = s[i+7:]
	}
	return strings.ToLower(s)
}

func attendees(v vevent) []string {
	var out []string
	for _, p := range v.props["ATTENDEE"] {
		if m := mailto(p.value); m != "" {
			out = append(out, m)
		}
	}
	return out
}

// --- recurrence -------------------------------------------------------------------

var weekdays = map[string]time.Weekday{"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday, "TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday}

// expand returns occurrence starts within [from, to] for the RRULE. Supports
// FREQ=DAILY|WEEKLY|MONTHLY|YEARLY, INTERVAL, COUNT, UNTIL, BYDAY (weekly),
// BYMONTHDAY (monthly). Anything fancier falls back to the plain frequency.
func expand(start time.Time, rule string, from, to time.Time, loc *time.Location) []time.Time {
	parts := map[string]string{}
	for _, kv := range strings.Split(rule, ";") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			parts[strings.ToUpper(k)] = strings.ToUpper(v)
		}
	}
	freq := parts["FREQ"]
	interval := 1
	if v, err := strconv.Atoi(parts["INTERVAL"]); err == nil && v > 0 {
		interval = v
	}
	count := -1
	if v, err := strconv.Atoi(parts["COUNT"]); err == nil {
		count = v
	}
	var until time.Time
	if u := parts["UNTIL"]; u != "" {
		if t, err := parseTime(prop{value: u, params: map[string]string{}}, loc); err == nil {
			until = t
		}
	}
	var byday []time.Weekday
	if bd := parts["BYDAY"]; bd != "" && freq == "WEEKLY" {
		for _, d := range strings.Split(bd, ",") {
			if wd, ok := weekdays[strings.TrimLeft(d, "-+0123456789")]; ok {
				byday = append(byday, wd)
			}
		}
	}
	var out []time.Time
	emitted := 0
	emit := func(t time.Time) bool {
		if !until.IsZero() && t.After(until) {
			return false
		}
		if count >= 0 && emitted >= count {
			return false
		}
		emitted++
		if !t.Before(from) && !t.After(to) {
			out = append(out, t)
		}
		return true
	}
	limit := 5000 // safety
	switch freq {
	case "DAILY":
		for t, i := start, 0; i < limit && !t.After(to); t, i = t.AddDate(0, 0, interval), i+1 {
			if !emit(t) {
				break
			}
		}
	case "WEEKLY":
		if len(byday) == 0 {
			byday = []time.Weekday{start.Weekday()}
		}
		// Walk week by week from the week containing start; within a week,
		// days in BYDAY order by calendar (Sunday-first week per RFC default).
		weekStart := start.AddDate(0, 0, -int(start.Weekday()))
		for w, i := weekStart, 0; i < limit && !w.After(to); w, i = w.AddDate(0, 0, 7*interval), i+1 {
			for d := 0; d < 7; d++ {
				day := w.AddDate(0, 0, d)
				if !hasDay(byday, day.Weekday()) {
					continue
				}
				t := time.Date(day.Year(), day.Month(), day.Day(), start.Hour(), start.Minute(), start.Second(), 0, start.Location())
				if t.Before(start) {
					continue
				}
				if !emit(t) {
					return out
				}
			}
		}
	case "MONTHLY":
		mday := start.Day()
		if v, err := strconv.Atoi(strings.Split(parts["BYMONTHDAY"], ",")[0]); err == nil && v > 0 {
			mday = v
		}
		for m, i := 0, 0; i < limit; m, i = m+interval, i+1 {
			first := time.Date(start.Year(), start.Month()+time.Month(m), 1, start.Hour(), start.Minute(), start.Second(), 0, start.Location())
			if first.AddDate(0, 1, 0).Before(from) && i > 0 && count < 0 {
				continue
			}
			t := time.Date(first.Year(), first.Month(), mday, start.Hour(), start.Minute(), start.Second(), 0, start.Location())
			if t.Month() != first.Month() {
				continue // e.g. the 31st in a short month
			}
			if t.After(to) {
				break
			}
			if t.Before(start) {
				continue
			}
			if !emit(t) {
				break
			}
		}
	case "YEARLY":
		for t, i := start, 0; i < limit && !t.After(to); t, i = t.AddDate(interval, 0, 0), i+1 {
			if !emit(t) {
				break
			}
		}
	default:
		if !start.Before(from) && !start.After(to) {
			out = append(out, start)
		}
	}
	return out
}

func hasDay(days []time.Weekday, d time.Weekday) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}
