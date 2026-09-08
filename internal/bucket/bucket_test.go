package bucket

import (
	"reflect"
	"testing"

	"github.com/valicm/vura/internal/config"
)

func newR() *Resolver {
	return New(&config.Config{Buckets: map[string]config.Bucket{
		"ACME": {Repos: []string{"/h/Development/Acme", "/h/Development/AcmeTrade"}, Tickets: []string{"ACME", "VS"},
			Domains: []string{"acme.atlassian.net"}, Calendar: []string{"roadmap", "@acme.example"}, Slack: []string{"work#acme"}},
		"BETA":  {Repos: []string{"/h/Development/beta-shop"}, Tickets: []string{"BT"}, Slack: []string{"beta#"}},
		"OTHER": {Repos: []string{"/h/Development"}, Domains: []string{"atlassian.net"}, Calendar: []string{"standup"}},
	}})
}

func TestByPath(t *testing.T) {
	r := newR()
	cases := map[string]string{
		"/h/Development/Acme/web/index.php": "ACME",
		"/h/Development/Acme":               "ACME",
		"/h/Development/AcmeX":              "OTHER", // sibling, not a child
		"/h/Development/beta-shop/":         "BETA",
		"/h/Development/misc":               "OTHER",
		"/tmp":                              "",
	}
	for p, want := range cases {
		if got, _ := r.ByPath(p); got != want {
			t.Errorf("ByPath(%q) = %q, want %q", p, got, want)
		}
	}
	if b, root := r.ByPath("/h/Development/AcmeTrade/x"); b != "ACME" || root != "/h/Development/AcmeTrade" {
		t.Errorf("root = %q", root)
	}
}

func TestByProject(t *testing.T) {
	r := newR()
	if r.ByProject("acme") != "ACME" || r.ByProject("beta-shop") != "BETA" || r.ByProject("nope") != "" {
		t.Error("ByProject")
	}
}

func TestByDomainAndEvent(t *testing.T) {
	r := newR()
	if r.ByDomain("acme.atlassian.net") != "ACME" || r.ByDomain("https://work.atlassian.net/browse/X") != "OTHER" ||
		r.ByDomain("youtube.com") != "" {
		t.Error("ByDomain")
	}
	if r.ByEvent("Roadmap sync", nil) != "ACME" || r.ByEvent("Call", []string{"anna@acme.example"}) != "ACME" ||
		r.ByEvent("Daily Standup", nil) != "OTHER" || r.ByEvent("Dentist", nil) != "" {
		t.Error("ByEvent")
	}
}

func TestBySlack(t *testing.T) {
	r := newR()
	if r.BySlack("work#acme-dev") != "ACME" || r.BySlack("beta#random") != "BETA" || r.BySlack("work#general") != "" {
		t.Error("BySlack")
	}
}

func TestTickets(t *testing.T) {
	r := newR()
	got := Tickets("feature/ACME-8600--ACME-8457 fix BT-1 and ACME-8600 again, not lower-1 or X-1234567")
	want := []string{"ACME-8600", "ACME-8457", "BT-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Tickets = %v, want %v", got, want)
	}
	if r.ByTicket("bt-5338") != "BETA" || r.ByTicket("ZZZ-1") != "" || r.ByTicket("nope") != "" {
		t.Error("ByTicket")
	}
	if Tickets("Fix composer lock") != nil {
		t.Error("expected no tickets")
	}
}
