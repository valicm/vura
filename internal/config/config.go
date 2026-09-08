// Package config loads ~/.config/vura/config.toml.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/valicm/vura/internal/clock"
)

// Config is the whole file. Durations are TOML strings like "20m".
type Config struct {
	Day      Day               `toml:"day"`
	Session  Session           `toml:"session"`
	Sources  Sources           `toml:"sources"`
	Calendar Calendar          `toml:"calendar"`
	Anchors  Anchors           `toml:"anchors"`
	Identity Identity          `toml:"identity"`
	Buckets  map[string]Bucket `toml:"buckets"`

	// Derived, not in the file.
	Boundary    clock.Boundary `toml:"-"`
	ActiveStart clock.Boundary `toml:"-"`
	ActiveEnd   clock.Boundary `toml:"-"`
	Location    *time.Location `toml:"-"`
	DataDir     string         `toml:"-"`
	DBPath      string         `toml:"-"`
}

type Day struct {
	First        string `toml:"first"` // YYYY-MM-DD; reconcile queue starts here (default: day vurad first ran)
	Boundary     string `toml:"boundary"`
	ActiveWindow string `toml:"active_window"`
	ReconcileAt  string `toml:"reconcile_at"`
	NagDays      int    `toml:"nag_days"`
	MinDay       Dur    `toml:"min_day"`
	MaxWall      int    `toml:"max_wall"`
}

type Session struct {
	IdleGap      Dur  `toml:"idle_gap"`
	DetectMin    Dur  `toml:"detect_min"`
	RoundMin     Dur  `toml:"round_min"`
	MaxEntry     Dur  `toml:"max_entry"`
	AllowOverlap bool `toml:"allow_overlap"`
	// AwayAfter: keyboard idle beyond this, between two pieces of evidence,
	// closes the session at the moment input stopped. Evidence during the idle
	// stretch (Claude heartbeats, a running command) overrides it.
	AwayAfter Dur `toml:"away_after"`
}

type Sources struct {
	Atuin       string `toml:"atuin"`
	Listen      string `toml:"listen"`
	WakatimeKey string `toml:"wakatime_key"` // if set, heartbeats must carry it
	Gnome       bool   `toml:"gnome"`
	Audio       bool   `toml:"audio"`
	Tray        bool   `toml:"tray"` // top-bar indicator (GNOME needs the AppIndicator extension)
	// Sampling intervals; sane defaults if omitted.
	PresenceEvery Dur `toml:"presence_every"`
	AudioEvery    Dur `toml:"audio_every"`
	ShellEvery    Dur `toml:"shell_every"`
	GitEvery      Dur `toml:"git_every"`
	GitSince      Dur `toml:"git_since"` // how far back each scan looks; also the amend-prune window
	// Binaries whose name alone is sensitive; stored as "[redacted]".
	ShellDenylist []string `toml:"shell_denylist"`
	// Browser domains that mean "in a call"; label calls and never count as browsing.
	MeetingDomains []string `toml:"meeting_domains"`
}

type Calendar struct {
	ICS   string `toml:"ics"`   // single feed shorthand; same as one feeds entry named "ics"
	Every Dur    `toml:"every"` // fetch interval
	Feeds []Feed `toml:"feeds"`
}

// Feed is one iCalendar URL: Proton's share link, Google Calendar's "secret
// address in iCal format", any .ics. A feed may carry a default bucket for
// events no title/attendee rule claims (a client workspace's calendar).
type Feed struct {
	Name   string `toml:"name"`
	URL    string `toml:"url"`
	Bucket string `toml:"bucket"`
}

// Anchors are external pulls: work that leaves no local trace (reviews,
// approvals, ticket comments). Anchors are identity and moments, not duration.
type Anchors struct {
	Every  Dur        `toml:"every"`
	Since  Dur        `toml:"since"`  // look-back per fetch
	GitHub string     `toml:"github"` // username; token key "github"
	GitLab string     `toml:"gitlab"` // base URL; token key "gitlab" (scope read_api)
	Jira   []JiraSite `toml:"jira"`
	Slack  []SlackWS  `toml:"slack"`
}

type SlackWS struct {
	Name string `toml:"name"` // label used in refs ("work#channel"); token key "slack:<name>" (user token, search:read)
}

type JiraSite struct {
	Site     string   `toml:"site"`     // e.g. client.atlassian.net; token key "jira:<site>", else the "jira" key
	Email    string   `toml:"email"`    // login for that site's token; defaults to identity.jira_email
	Projects []string `toml:"projects"` // project keys to watch
}

type Identity struct {
	Emails    []string `toml:"emails"`
	JiraSite  string   `toml:"jira_site"`
	JiraEmail string   `toml:"jira_email"` // login for the Jira API token; defaults to emails[0]
	Timezone  string   `toml:"timezone"`
	Device    string   `toml:"device"` // defaults to hostname
}

type Bucket struct {
	Issue    string   `toml:"issue"`
	Label    string   `toml:"label"`
	Repos    []string `toml:"repos"`
	Tickets  []string `toml:"tickets"`
	Domains  []string `toml:"domains"`  // browser: substring match on the domain/URL
	Calendar []string `toml:"calendar"` // events: substring match on title or attendee email
	Slack    []string `toml:"slack"`    // messages: substring match on "workspace#channel"
}

// Dur is a time.Duration that unmarshals from a TOML string.
type Dur struct{ time.Duration }

func (d *Dur) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// Path returns the default config path.
func Path() string {
	if p := os.Getenv("VURA_CONFIG"); p != "" {
		return p
	}
	base, err := os.UserConfigDir()
	if err != nil {
		base = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(base, "vura", "config.toml")
}

// Load reads the config file, applying defaults. A missing file is not an
// error: the daemon can collect with defaults alone.
func Load(path string) (*Config, error) {
	c := defaults()
	if b, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return c, c.finish()
}

func defaults() *Config {
	return &Config{
		Day: Day{Boundary: "03:00", ActiveWindow: "08:00-01:00", ReconcileAt: "10:00",
			NagDays: 4, MinDay: Dur{20 * time.Minute}, MaxWall: 16},
		Session: Session{IdleGap: Dur{20 * time.Minute}, DetectMin: Dur{5 * time.Minute},
			RoundMin: Dur{15 * time.Minute}, MaxEntry: Dur{4*time.Hour + 30*time.Minute}, AllowOverlap: true,
			AwayAfter: Dur{10 * time.Minute}},
		Sources: Sources{Atuin: "~/.local/share/atuin/history.db", Listen: "127.0.0.1:4242",
			Gnome: true, Audio: true, Tray: true, PresenceEvery: Dur{60 * time.Second}, AudioEvery: Dur{30 * time.Second},
			ShellEvery: Dur{60 * time.Second}, GitEvery: Dur{time.Hour}, GitSince: Dur{14 * 24 * time.Hour},
			ShellDenylist:  []string{"pass", "gpg", "gpg2", "ssh-add", "op", "bw", "vault", "gopass", "keepassxc-cli"},
			MeetingDomains: []string{"meet.google.com", "teams.microsoft.com", "teams.live.com", "zoom.us", "whereby.com", "meet.jit.si"}},
		Calendar: Calendar{Every: Dur{time.Hour}},
		Anchors:  Anchors{Every: Dur{time.Hour}, Since: Dur{72 * time.Hour}},
		Identity: Identity{Timezone: "Local"},
		Buckets:  map[string]Bucket{},
	}
}

func (c *Config) finish() error {
	b, err := clock.ParseBoundary(c.Day.Boundary)
	if err != nil {
		return err
	}
	c.Boundary = b
	aw := strings.SplitN(c.Day.ActiveWindow, "-", 2)
	if len(aw) != 2 {
		return fmt.Errorf("active_window %q: want HH:MM-HH:MM", c.Day.ActiveWindow)
	}
	if c.ActiveStart, err = clock.ParseBoundary(aw[0]); err != nil {
		return fmt.Errorf("active_window: %w", err)
	}
	if c.ActiveEnd, err = clock.ParseBoundary(aw[1]); err != nil {
		return fmt.Errorf("active_window: %w", err)
	}
	loc, err := time.LoadLocation(c.Identity.Timezone)
	if err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	c.Location = loc
	if c.Identity.Device == "" {
		c.Identity.Device, _ = os.Hostname()
	}
	if c.Identity.JiraEmail == "" && len(c.Identity.Emails) > 0 {
		c.Identity.JiraEmail = c.Identity.Emails[0]
	}
	if c.Calendar.ICS != "" {
		c.Calendar.Feeds = append([]Feed{{Name: "ics", URL: c.Calendar.ICS}}, c.Calendar.Feeds...)
	}
	for i := range c.Calendar.Feeds {
		f := &c.Calendar.Feeds[i]
		if f.Name == "" {
			f.Name = fmt.Sprintf("feed%d", i+1)
		}
		if f.Bucket != "" {
			if _, ok := c.Buckets[f.Bucket]; !ok {
				return fmt.Errorf("calendar feed %q: unknown bucket %q", f.Name, f.Bucket)
			}
		}
	}
	for i := range c.Anchors.Jira {
		if c.Anchors.Jira[i].Email == "" {
			c.Anchors.Jira[i].Email = c.Identity.JiraEmail
		}
	}
	c.Sources.Atuin = expand(c.Sources.Atuin)
	for k, bk := range c.Buckets {
		for i, r := range bk.Repos {
			bk.Repos[i] = expand(r)
		}
		c.Buckets[k] = bk
	}
	c.DataDir = os.Getenv("VURA_DATA")
	if c.DataDir == "" {
		base, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			c.DataDir = filepath.Join(x, "vura")
		} else {
			c.DataDir = filepath.Join(base, ".local", "share", "vura")
		}
	}
	c.DBPath = filepath.Join(c.DataDir, "vura.db")
	return nil
}

func expand(p string) string {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[2:])
		}
	}
	return p
}
