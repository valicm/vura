package ics

import (
	"testing"
	"time"
)

var loc, _ = time.LoadLocation("Europe/Zagreb")

const feed = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:standup\r\nSUMMARY:Daily standup\\, Acme\r\n" +
	"DTSTART;TZID=Europe/Zagreb:20260901T093000\r\nDTEND;TZID=Europe/Zagreb:20260901T094500\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR\r\nEXDATE;TZID=Europe/Zagreb:20260903T093000\r\n" +
	"ATTENDEE;CN=Anna:mailto:Anna@Acme.example\r\nATTENDEE:mailto:ben@acme.example\r\n" +
	"BEGIN:VALARM\r\nTRIGGER:-PT10M\r\nSUMMARY:ignored alarm\r\nEND:VALARM\r\nEND:VEVENT\r\n" +
	// Friday's standup moved to 10:00
	"BEGIN:VEVENT\r\nUID:standup\r\nRECURRENCE-ID;TZID=Europe/Zagreb:20260904T093000\r\n" +
	"DTSTART;TZID=Europe/Zagreb:20260904T100000\r\nDTEND;TZID=Europe/Zagreb:20260904T101500\r\nSUMMARY:Standup (moved)\r\nEND:VEVENT\r\n" +
	// one-off UTC event with a folded summary
	"BEGIN:VEVENT\r\nUID:roadmap\r\nDTSTART:20260904T131500Z\r\nDTEND:20260904T140000Z\r\n" +
	"SUMMARY:Roadmap sync — \r\n Anna, Ben\r\nEND:VEVENT\r\n" +
	// all-day, must be skipped
	"BEGIN:VEVENT\r\nUID:holiday\r\nDTSTART;VALUE=DATE:20260904\r\nDTEND;VALUE=DATE:20260905\r\nSUMMARY:Off\r\nEND:VEVENT\r\n" +
	// cancelled
	"BEGIN:VEVENT\r\nUID:dead\r\nSTATUS:CANCELLED\r\nDTSTART:20260904T150000Z\r\nDURATION:PT1H\r\nSUMMARY:Cancelled\r\nEND:VEVENT\r\n" +
	// duration form
	"BEGIN:VEVENT\r\nUID:dur\r\nDTSTART;TZID=Europe/Zagreb:20260904T160000\r\nDURATION:PT1H30M\r\nSUMMARY:Long one\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestParseWeek(t *testing.T) {
	from := time.Date(2026, 8, 31, 0, 0, 0, 0, loc)
	to := time.Date(2026, 9, 6, 0, 0, 0, 0, loc)
	evs, err := Parse(feed, from, to, loc)
	if err != nil {
		t.Fatal(err)
	}
	var standups []Event
	byUID := map[string][]Event{}
	for _, e := range evs {
		byUID[e.UID] = append(byUID[e.UID], e)
		if e.UID == "standup" {
			standups = append(standups, e)
		}
	}
	// Mon 31 Aug is before DTSTART; Tue 1, Wed 2, (Thu 3 excluded), Fri 4 moved = 3
	if len(standups) != 3 {
		t.Fatalf("standups: %d %+v", len(standups), standups)
	}
	if !standups[0].Start.Equal(time.Date(2026, 9, 1, 9, 30, 0, 0, loc)) || standups[0].Title != "Daily standup, Acme" {
		t.Errorf("first standup: %+v", standups[0])
	}
	if len(standups[0].Attendees) != 2 || standups[0].Attendees[0] != "anna@acme.example" {
		t.Errorf("attendees: %v", standups[0].Attendees)
	}
	fri := standups[2]
	if !fri.Start.Equal(time.Date(2026, 9, 4, 10, 0, 0, 0, loc)) || fri.Title != "Standup (moved)" {
		t.Errorf("moved standup: %+v", fri)
	}
	w := byUID["roadmap"]
	if len(w) != 1 || !w[0].Start.Equal(time.Date(2026, 9, 4, 15, 15, 0, 0, loc)) || w[0].Title != "Roadmap sync — Anna, Ben" {
		t.Errorf("roadmap: %+v", w)
	}
	if len(byUID["holiday"]) != 0 || len(byUID["dead"]) != 0 {
		t.Error("all-day and cancelled must be skipped")
	}
	if d := byUID["dur"]; len(d) != 1 || d[0].End.Sub(d[0].Start) != 90*time.Minute {
		t.Errorf("duration: %+v", d)
	}
}

func TestExpandRules(t *testing.T) {
	start := time.Date(2026, 1, 15, 10, 0, 0, 0, loc)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, loc)
	to := time.Date(2026, 12, 31, 23, 59, 0, 0, loc)
	if n := len(expand(start, "FREQ=DAILY;COUNT=5", from, to, loc)); n != 5 {
		t.Errorf("daily count: %d", n)
	}
	if n := len(expand(start, "FREQ=DAILY;INTERVAL=2;UNTIL=20260125T100000", from, to, loc)); n != 6 {
		t.Errorf("daily interval/until: %d", n)
	}
	if n := len(expand(start, "FREQ=MONTHLY", from, to, loc)); n != 12 {
		t.Errorf("monthly: %d", n)
	}
	m31 := expand(time.Date(2026, 1, 31, 9, 0, 0, 0, loc), "FREQ=MONTHLY", from, to, loc)
	if len(m31) != 7 { // months with 31 days
		t.Errorf("monthly 31st: %d", len(m31))
	}
	if n := len(expand(start, "FREQ=YEARLY", from, to, loc)); n != 1 {
		t.Errorf("yearly: %d", n)
	}
	wk := expand(start, "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH", from, time.Date(2026, 2, 28, 0, 0, 0, 0, loc), loc)
	if len(wk) != 4 { // Jan 15, 29, Feb 12, 26
		t.Errorf("biweekly: %v", wk)
	}
}
