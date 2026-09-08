// Package seed writes a synthetic working day into a store: every kind of
// evidence vura collects, shaped like a real freelance day. Used by
// `vura demo` so a newcomer can see the tool work before collecting
// anything, and as a fixture for UI work.
package seed

import (
	"context"
	"fmt"
	"time"

	"github.com/valicm/vura/internal/store"
)

// Config is the demo config.toml; the demo database is built to match it.
const Config = `[identity]
emails   = ["dev@example.com"]
jira_site = "demo.atlassian.net"
timezone = "Europe/Zagreb"
device   = "demo"

[session]
idle_gap   = "30m"
away_after = "10m"

[sources]
listen = ""            # no endpoint in demo mode
gnome  = false
audio  = false
atuin  = ""

[[anchors.slack]]
name = "work"

[buckets.ACME]
issue    = "EXT-1"
label    = "Acme"
repos    = ["/home/demo/Development/acme-shop"]
tickets  = ["ACME"]
domains  = ["acme.atlassian.net", "github.com"]   # browser reports the domain only; "github.com/acme" would match anchors, not tabs
calendar = ["acme", "@acme.example"]
slack    = ["work#acme", "work#dm-anna.smith"]

[buckets.BETA]
issue   = "EXT-2"
label   = "Beta"
repos   = ["/home/demo/Development/beta"]
tickets = ["BT"]
domains = ["gitlab.beta.example"]

[buckets.OTHER]
issue = "INT-1"
label = "Internal"
repos = ["/home/demo/Development/internal"]
`

// Day writes one day of evidence for the given calendar date in loc.
func Day(ctx context.Context, st *store.Store, date time.Time, loc *time.Location) error {
	at := func(h, m int) time.Time { return time.Date(date.Year(), date.Month(), date.Day(), h, m, 0, 0, loc) }
	dev := "demo"

	// Presence: at the keyboard 09:20–12:40, lunch to 13:25, back until 18:05,
	// then a phone-driven Claude session in the evening with the keyboard idle.
	present := func(a, b time.Time) {
		for t := a; t.Before(b); t = t.Add(time.Minute) {
			_ = st.InsertPresence(ctx, store.Presence{TS: t, IdleMS: 0, InhibitFlags: 8, Inhibitors: "caffeine-gnome-extension", Device: dev})
		}
	}
	idle := func(a, b, since time.Time) {
		for t := a; t.Before(b); t = t.Add(time.Minute) {
			_ = st.InsertPresence(ctx, store.Presence{TS: t, IdleMS: t.Sub(since).Milliseconds(), Device: dev})
		}
	}
	present(at(9, 20), at(12, 40))
	idle(at(12, 40), at(13, 25), at(12, 40))
	present(at(13, 25), at(18, 5))
	idle(at(18, 5), at(23, 0), at(18, 5))

	// Editor heartbeats: PhpStorm on acme-shop 09:34–12:30, on beta 13:30–15:10.
	beats := func(a, b time.Time, every time.Duration, entity, project, branch, editor, plugin, typ, category string) {
		for t := a; t.Before(b); t = t.Add(every) {
			_ = st.InsertHeartbeat(ctx, store.Heartbeat{TS: float64(t.Unix()), Entity: entity, Type: typ, Category: category,
				Project: project, Branch: branch, Editor: editor, Plugin: plugin, Device: dev})
		}
	}
	beats(at(9, 34), at(12, 31), 2*time.Minute, "/home/demo/Development/acme-shop/web/modules/custom/shop/src/Cart.php", "acme-shop", "feature/ACME-812", "phpstorm/2026.2", "phpstorm-wakatime/16.1", "file", "coding")
	beats(at(13, 30), at(15, 11), 2*time.Minute, "/home/demo/Development/beta/src/Api.php", "beta", "feature/BT-77", "phpstorm/2026.2", "phpstorm-wakatime/16.1", "file", "coding")
	// Claude Code from the phone in the evening: keyboard idle, heartbeats continue -> remote.
	beats(at(20, 0), at(21, 16), 2*time.Minute, "Claude demo-session", "acme-shop", "feature/ACME-812", "claude-code/2.1", "claude-code-wakatime/4.1", "app", "ai coding")
	// Browser: Jira and GitHub for Acme during the afternoon review block.
	beats(at(16, 0), at(16, 46), time.Minute, "acme.atlassian.net", "acme.atlassian.net", "", "chrome/152", "vura-browser/1.0", "domain", "browsing")
	beats(at(16, 46), at(17, 31), time.Minute, "github.com", "github.com", "", "chrome/152", "vura-browser/1.0", "domain", "browsing")

	// Shell: a few commands in each repo, one long migration.
	cmds := []struct {
		t      time.Time
		cwd    string
		bin    string
		sub    string
		dur    time.Duration
		bucket string
	}{
		{at(9, 36), "/home/demo/Development/acme-shop", "ddev", "start", 12 * time.Second, "ACME"},
		{at(9, 50), "/home/demo/Development/acme-shop", "ddev", "drush", 3 * time.Second, "ACME"},
		{at(11, 5), "/home/demo/Development/acme-shop", "git", "commit", time.Second, "ACME"},
		{at(13, 32), "/home/demo/Development/beta", "composer", "install", 40 * time.Second, "BETA"},
		{at(13, 40), "/home/demo/Development/beta", "vendor/bin/drush", "migrate:import", 38 * time.Minute, "BETA"},
		{at(17, 40), "/home/demo/Development/internal", "make", "build", 5 * time.Second, "OTHER"},
	}
	for i, c := range cmds {
		_ = st.UpsertShell(ctx, store.ShellCmd{ID: fmt.Sprintf("demo-%d", i), TS: c.t, CWD: c.cwd, Binary: c.bin, Sub: c.sub, Duration: c.dur, Exit: 0, Session: "demo", Bucket: c.bucket, Device: dev})
	}

	// Commits.
	commits := []store.Commit{
		{SHA: "a1", TS: at(11, 5), Repo: "/home/demo/Development/acme-shop", Branch: "feature/ACME-812", Bucket: "ACME", Ticket: "ACME-812", Subject: "ACME-812 Cart totals respect tax zone", Files: 3, Ins: 88, Del: 12},
		{SHA: "a2", TS: at(12, 20), Repo: "/home/demo/Development/acme-shop", Branch: "feature/ACME-812", Bucket: "ACME", Ticket: "ACME-812", Subject: "Composer bumps", Files: 2, Ins: 40, Del: 40},
		{SHA: "b1", TS: at(15, 5), Repo: "/home/demo/Development/beta", Branch: "feature/BT-77", Bucket: "BETA", Ticket: "BT-77", Subject: "BT-77 Migrate legacy orders", Files: 6, Ins: 210, Del: 33},
		{SHA: "c1", TS: at(17, 45), Repo: "/home/demo/Development/internal", Branch: "main", Bucket: "OTHER", Subject: "Bump deploy script", Files: 1, Ins: 4, Del: 2},
		{SHA: "a3", TS: at(21, 10), Repo: "/home/demo/Development/acme-shop", Branch: "feature/ACME-812", Bucket: "ACME", Ticket: "ACME-812", Subject: "ACME-812 Tests for tax zones", Files: 2, Ins: 120, Del: 0},
	}
	for _, c := range commits {
		_ = st.UpsertCommit(ctx, c)
	}

	// A call: mic open 15:15–15:58, inside a mapped meeting.
	id, _ := st.StartAudio(ctx, at(15, 15), store.AudioStream{Stream: "57", App: "Google Chrome", Binary: "chrome", Media: "Meet", Source: "alsa_input.usb-mic", Device: dev})
	_ = st.TouchAudio(ctx, id, at(15, 58))
	_ = st.EndAudio(ctx, id)

	// Calendar: the meeting that explains the call, and a personal one that is ignored.
	_ = st.ReplaceEvents(ctx, "ics:demo", at(0, 0), at(23, 59), []store.Event{
		{ID: "demo:sync", Start: at(15, 15), End: at(16, 0), Title: "Acme weekly sync", Attendees: "anna@acme.example,ben@acme.example"},
		{ID: "demo:dentist", Start: at(8, 0), End: at(8, 45), Title: "Dentist"},
	})

	// Anchors: a review and a ticket transition during the browser block, Slack messages.
	anchors := []store.Anchor{
		{ID: "gh:1", TS: at(16, 50), Source: "github", Kind: "approve", Ref: "acme/acme-shop#41", Title: "Checkout: guest addresses", URL: "https://github.com/acme/acme-shop/pull/41"},
		{ID: "gh:2", TS: at(17, 12), Source: "github", Kind: "comment", Ref: "acme/acme-shop#43", Title: "Search facets", URL: "https://github.com/acme/acme-shop/pull/43"},
		{ID: "jira:ACME-812:h1", TS: at(16, 10), Source: "jira", Kind: "transition", Ref: "ACME-812", Title: "Cart totals respect tax zone → In Review", URL: "https://acme.atlassian.net/browse/ACME-812"},
		{ID: "slack:1", TS: at(10, 12), Source: "slack", Kind: "message", Ref: "work#acme-dev"},
		{ID: "slack:2", TS: at(10, 40), Source: "slack", Kind: "message", Ref: "work#acme-dev"},
		{ID: "slack:3", TS: at(14, 2), Source: "slack", Kind: "message", Ref: "work#dm-anna.smith"},
	}
	for _, a := range anchors {
		_ = st.UpsertAnchor(ctx, a)
	}
	return st.Audit(ctx, "demo.seed", date.Format("2006-01-02"))
}
