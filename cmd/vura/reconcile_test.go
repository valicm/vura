package main

import (
	"flag"
	"reflect"
	"testing"
	"time"
)

func TestFlagsFirst(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.Bool("dry-run", false, "")
	fs.Bool("yes", false, "")
	fs.String("month", "", "")
	got := flagsFirst(fs, []string{"2026-09-07", "--dry-run", "--month", "2026-09", "-yes", "--", "-literal"})
	want := []string{"--dry-run", "--month", "2026-09", "-yes", "2026-09-07", "-literal"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
	got = flagsFirst(fs, []string{"--month=2026-08", "x"})
	if !reflect.DeepEqual(got, []string{"--month=2026-08", "x"}) {
		t.Errorf("got %v", got)
	}
}

func TestParseDur(t *testing.T) {
	cases := map[string]time.Duration{"1h30m": 90 * time.Minute, "45m": 45 * time.Minute, "1.5h": 90 * time.Minute,
		"1.5": 90 * time.Minute, "2": 2 * time.Hour, "0.25h": 15 * time.Minute}
	for in, want := range cases {
		if got, err := parseDur(in); err != nil || got != want {
			t.Errorf("parseDur(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "abc", "1x"} {
		if _, err := parseDur(bad); err == nil {
			t.Errorf("parseDur(%q) should fail", bad)
		}
	}
}
