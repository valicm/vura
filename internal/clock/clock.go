// Package clock implements vura's notion of a "day": a working day runs from
// the configured boundary (default 03:00) to the same time the next calendar
// day, so work past midnight files under the day it started.
package clock

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Boundary is a time of day at which one working day ends and the next begins.
type Boundary struct {
	Hour, Minute int
}

// ParseBoundary parses "HH:MM".
func ParseBoundary(s string) (Boundary, error) {
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	if len(parts) != 2 {
		return Boundary{}, fmt.Errorf("boundary %q: want HH:MM", s)
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return Boundary{}, fmt.Errorf("boundary %q: want HH:MM", s)
	}
	return Boundary{Hour: h, Minute: m}, nil
}

// Day returns the working day (as YYYY-MM-DD, in t's location) that t belongs to.
func (b Boundary) Day(t time.Time) string {
	return b.DayStart(t).Format("2006-01-02")
}

// DayStart returns the instant the working day containing t began.
func (b Boundary) DayStart(t time.Time) time.Time {
	start := time.Date(t.Year(), t.Month(), t.Day(), b.Hour, b.Minute, 0, 0, t.Location())
	if t.Before(start) {
		start = start.AddDate(0, 0, -1)
	}
	return start
}

// Range returns [start, end) for the working day labelled YYYY-MM-DD in loc.
func (b Boundary) Range(day string, loc *time.Location) (time.Time, time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := time.Date(d.Year(), d.Month(), d.Day(), b.Hour, b.Minute, 0, 0, loc)
	return start, start.AddDate(0, 0, 1), nil
}

// Minutes returns the boundary as minutes after midnight.
func (b Boundary) Minutes() int { return b.Hour*60 + b.Minute }

// InWindow reports whether t's time of day falls in [from, to). A window
// whose end is earlier than its start wraps past midnight ("08:00-01:00").
func InWindow(t time.Time, from, to Boundary) bool {
	m := t.Hour()*60 + t.Minute()
	f, e := from.Minutes(), to.Minutes()
	if f <= e {
		return m >= f && m < e
	}
	return m >= f || m < e
}
