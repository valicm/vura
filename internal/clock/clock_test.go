package clock

import (
	"testing"
	"time"
)

func TestDay(t *testing.T) {
	b, err := ParseBoundary("03:00")
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Zagreb")
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 9, 4, 10, 0, 0, 0, loc), "2026-09-04"},
		{time.Date(2026, 9, 5, 0, 45, 0, 0, loc), "2026-09-04"}, // past midnight, same day
		{time.Date(2026, 9, 5, 2, 59, 0, 0, loc), "2026-09-04"},
		{time.Date(2026, 9, 5, 3, 0, 0, 0, loc), "2026-09-05"},
	}
	for _, c := range cases {
		if got := b.Day(c.at); got != c.want {
			t.Errorf("Day(%v) = %s, want %s", c.at, got, c.want)
		}
	}
	s, e, err := b.Range("2026-09-04", loc)
	if err != nil {
		t.Fatal(err)
	}
	if s.Hour() != 3 || e.Sub(s) != 24*time.Hour {
		t.Errorf("Range = %v..%v", s, e)
	}
}

func TestParseBoundaryRejects(t *testing.T) {
	for _, s := range []string{"3", "25:00", "03:60", "x:y"} {
		if _, err := ParseBoundary(s); err == nil {
			t.Errorf("ParseBoundary(%q) accepted", s)
		}
	}
}

func TestInWindow(t *testing.T) {
	from, _ := ParseBoundary("08:00")
	to, _ := ParseBoundary("01:00")
	at := func(h, m int) time.Time { return time.Date(2026, 9, 4, h, m, 0, 0, time.UTC) }
	if !InWindow(at(9, 0), from, to) || !InWindow(at(23, 59), from, to) || !InWindow(at(0, 30), from, to) {
		t.Error("should be inside")
	}
	if InWindow(at(1, 0), from, to) || InWindow(at(7, 59), from, to) || InWindow(at(3, 0), from, to) {
		t.Error("should be outside")
	}
	day, _ := ParseBoundary("17:00")
	if !InWindow(at(12, 0), from, day) || InWindow(at(18, 0), from, day) {
		t.Error("non-wrapping window")
	}
}
