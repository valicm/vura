package anchors

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/valicm/vura/internal/store"
)

// GitHub reads the authenticated user's event stream. With a token it
// includes private repos; the API keeps roughly 90 days / 300 events.
type GitHub struct {
	User  string
	Token string
	HTTP  *http.Client
	Base  string // default https://api.github.com
}

func (g *GitHub) Name() string { return "github" }

type ghEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Repo      struct {
		Name string `json:"name"`
	} `json:"repo"`
	Payload struct {
		Action  string `json:"action"`
		Ref     string `json:"ref"`
		Size    int    `json:"size"`
		Commits []struct {
			SHA string `json:"sha"`
		} `json:"commits"`
		PullRequest struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
		} `json:"pull_request"`
		Issue struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
		} `json:"issue"`
		Review struct {
			State string `json:"state"`
		} `json:"review"`
	} `json:"payload"`
}

func (g *GitHub) Fetch(ctx context.Context, since time.Time) ([]store.Anchor, error) {
	base := g.Base
	if base == "" {
		base = "https://api.github.com"
	}
	var out []store.Anchor
	for page := 1; page <= 3; page++ {
		var evs []ghEvent
		url := fmt.Sprintf("%s/users/%s/events?per_page=100&page=%d", base, g.User, page)
		if _, err := getJSON(ctx, g.HTTP, url, func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+g.Token)
			r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		}, &evs); err != nil {
			return out, err
		}
		stop := len(evs) < 100
		for _, e := range evs {
			if e.CreatedAt.Before(since) {
				stop = true
				continue
			}
			if a, ok := ghAnchor(e); ok {
				out = append(out, a)
			}
		}
		if stop {
			break
		}
	}
	return out, nil
}

func ghAnchor(e ghEvent) (store.Anchor, bool) {
	a := store.Anchor{ID: "gh:" + e.ID, TS: e.CreatedAt, Source: "github"}
	p := e.Payload
	switch e.Type {
	case "PullRequestReviewEvent":
		a.Kind, a.Ref, a.Title, a.URL = "review", fmt.Sprintf("%s#%d", e.Repo.Name, p.PullRequest.Number), p.PullRequest.Title, p.PullRequest.HTMLURL
		if p.Review.State == "approved" {
			a.Kind = "approve"
		}
	case "PullRequestReviewCommentEvent":
		a.Kind, a.Ref, a.Title, a.URL = "comment", fmt.Sprintf("%s#%d", e.Repo.Name, p.PullRequest.Number), p.PullRequest.Title, p.PullRequest.HTMLURL
	case "IssueCommentEvent":
		a.Kind, a.Ref, a.Title, a.URL = "comment", fmt.Sprintf("%s#%d", e.Repo.Name, p.Issue.Number), p.Issue.Title, p.Issue.HTMLURL
	case "PullRequestEvent":
		a.Kind, a.Ref, a.Title, a.URL = "pr "+p.Action, fmt.Sprintf("%s#%d", e.Repo.Name, p.PullRequest.Number), p.PullRequest.Title, p.PullRequest.HTMLURL
	case "IssuesEvent":
		a.Kind, a.Ref, a.Title, a.URL = "issue "+p.Action, fmt.Sprintf("%s#%d", e.Repo.Name, p.Issue.Number), p.Issue.Title, p.Issue.HTMLURL
	case "PushEvent":
		n := p.Size
		if n == 0 {
			n = len(p.Commits) // newer payloads omit size
		}
		a.Kind, a.Ref, a.Title = "push", e.Repo.Name, fmt.Sprintf("%d commits to %s", n, trimRef(p.Ref))
	default:
		return a, false
	}
	return a, true
}

func trimRef(r string) string {
	const h = "refs/heads/"
	if len(r) > len(h) && r[:len(h)] == h {
		return r[len(h):]
	}
	return r
}
