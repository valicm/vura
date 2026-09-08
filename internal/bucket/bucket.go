// Package bucket maps evidence (a path, a ticket key) to a bucket name.
// Deterministic and config-driven; this is the only place attribution
// rules live.
package bucket

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/valicm/vura/internal/config"
)

type repo struct {
	path, bucket string
}

type Resolver struct {
	repos    []repo            // longest path first
	tickets  map[string]string // prefix -> bucket
	projects map[string]string // lower(basename(repo)) -> bucket
	domains  []pattern         // longest first
	calendar []pattern
	slack    []pattern
}

type pattern struct{ needle, bucket string }

// ticketRe matches Jira-style keys: AUC-8600, DP-5338, EXT-88.
var ticketRe = regexp.MustCompile(`\b([A-Z][A-Z0-9]{1,9})-(\d{1,6})\b`)

func New(cfg *config.Config) *Resolver {
	r := &Resolver{tickets: map[string]string{}, projects: map[string]string{}}
	for name, b := range cfg.Buckets {
		for _, p := range b.Repos {
			p = filepath.Clean(p)
			r.repos = append(r.repos, repo{path: p, bucket: name})
			r.projects[strings.ToLower(filepath.Base(p))] = name
		}
		for _, t := range b.Tickets {
			r.tickets[strings.ToUpper(t)] = name
		}
		for _, d := range b.Domains {
			r.domains = append(r.domains, pattern{strings.ToLower(d), name})
		}
		for _, c := range b.Calendar {
			r.calendar = append(r.calendar, pattern{strings.ToLower(c), name})
		}
		for _, c := range b.Slack {
			r.slack = append(r.slack, pattern{strings.ToLower(c), name})
		}
	}
	longestFirst := func(ps []pattern) {
		sort.Slice(ps, func(i, j int) bool {
			if len(ps[i].needle) != len(ps[j].needle) {
				return len(ps[i].needle) > len(ps[j].needle)
			}
			return ps[i].needle < ps[j].needle
		})
	}
	longestFirst(r.domains)
	longestFirst(r.calendar)
	longestFirst(r.slack)
	sort.Slice(r.repos, func(i, j int) bool {
		if len(r.repos[i].path) != len(r.repos[j].path) {
			return len(r.repos[i].path) > len(r.repos[j].path)
		}
		return r.repos[i].path < r.repos[j].path
	})
	return r
}

// ByPath returns the bucket and configured repo root that contain p,
// or "", "" when p is outside every configured repo.
func (r *Resolver) ByPath(p string) (bucket, repoRoot string) {
	p = filepath.Clean(p)
	for _, rp := range r.repos {
		if p == rp.path || strings.HasPrefix(p, rp.path+string(filepath.Separator)) {
			return rp.bucket, rp.path
		}
	}
	return "", ""
}

// ByProject maps a WakaTime project name (the repo directory's basename) to
// a bucket. Used for heartbeats whose entity is not a path.
func (r *Resolver) ByProject(name string) string {
	return r.projects[strings.ToLower(name)]
}

// ByDomain maps a browser entity (domain or URL) to a bucket by substring.
func (r *Resolver) ByDomain(entity string) string {
	e := strings.ToLower(entity)
	for _, p := range r.domains {
		if strings.Contains(e, p.needle) {
			return p.bucket
		}
	}
	return ""
}

// ByEvent maps a calendar event to a bucket by title or attendee substring.
func (r *Resolver) ByEvent(title string, attendees []string) string {
	hay := strings.ToLower(title + " " + strings.Join(attendees, " "))
	for _, p := range r.calendar {
		if strings.Contains(hay, p.needle) {
			return p.bucket
		}
	}
	return ""
}

// BySlack maps "workspace#channel" to a bucket by substring.
func (r *Resolver) BySlack(ref string) string {
	e := strings.ToLower(ref)
	for _, p := range r.slack {
		if strings.Contains(e, p.needle) {
			return p.bucket
		}
	}
	return ""
}

// Repos returns every configured repo root with its bucket.
func (r *Resolver) Repos() map[string]string {
	out := make(map[string]string, len(r.repos))
	for _, rp := range r.repos {
		out[rp.path] = rp.bucket
	}
	return out
}

// ByTicket maps a key like "AUC-8600" to its bucket via the project prefix.
func (r *Resolver) ByTicket(key string) string {
	i := strings.IndexByte(key, '-')
	if i < 0 {
		return ""
	}
	return r.tickets[strings.ToUpper(key[:i])]
}

// Tickets extracts ticket keys from free text (a branch name, a subject) in
// order of appearance, de-duplicated.
func Tickets(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range ticketRe.FindAllString(s, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}
