package anchors

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/valicm/vura/internal/store"
)

// GitLab reads the token owner's contribution events (needs read_api).
type GitLab struct {
	Base  string
	Token string
	HTTP  *http.Client
	Cache *store.Store // project id -> path, in state
}

func (g *GitLab) Name() string { return "gitlab" }

type glEvent struct {
	ID          int64     `json:"id"`
	ActionName  string    `json:"action_name"`
	TargetType  string    `json:"target_type"`
	TargetTitle string    `json:"target_title"`
	TargetIID   int       `json:"target_iid"`
	ProjectID   int64     `json:"project_id"`
	CreatedAt   time.Time `json:"created_at"`
	PushData    struct {
		Ref         string `json:"ref"`
		CommitCount int    `json:"commit_count"`
		CommitTitle string `json:"commit_title"`
	} `json:"push_data"`
	Note struct {
		NoteableType string `json:"noteable_type"`
		NoteableIID  int    `json:"noteable_iid"`
	} `json:"note"`
}

func (g *GitLab) auth(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", g.Token) }

func (g *GitLab) Fetch(ctx context.Context, since time.Time) ([]store.Anchor, error) {
	base := strings.TrimRight(g.Base, "/")
	var out []store.Anchor
	for page := 1; page <= 5; page++ {
		var evs []glEvent
		u := fmt.Sprintf("%s/api/v4/events?after=%s&per_page=100&page=%d&sort=desc", base,
			since.AddDate(0, 0, -1).Format("2006-01-02"), page)
		if _, err := getJSON(ctx, g.HTTP, u, g.auth, &evs); err != nil {
			return out, err
		}
		for _, e := range evs {
			if e.CreatedAt.Before(since) {
				continue
			}
			path := g.project(ctx, base, e.ProjectID)
			if a, ok := glAnchor(e, path, base); ok {
				out = append(out, a)
			}
		}
		if len(evs) < 100 {
			break
		}
	}
	return out, nil
}

func (g *GitLab) project(ctx context.Context, base string, id int64) string {
	key := "gitlab.project." + strconv.FormatInt(id, 10)
	if g.Cache != nil {
		if v, err := g.Cache.GetState(ctx, key); err == nil && v != "" {
			return v
		}
	}
	var p struct {
		Path string `json:"path_with_namespace"`
	}
	if _, err := getJSON(ctx, g.HTTP, fmt.Sprintf("%s/api/v4/projects/%d", base, id), g.auth, &p); err != nil || p.Path == "" {
		return "project-" + strconv.FormatInt(id, 10)
	}
	if g.Cache != nil {
		_ = g.Cache.SetState(ctx, key, p.Path)
	}
	return p.Path
}

func glAnchor(e glEvent, path, base string) (store.Anchor, bool) {
	a := store.Anchor{ID: "gl:" + strconv.FormatInt(e.ID, 10), TS: e.CreatedAt, Source: "gitlab", Title: e.TargetTitle}
	action := strings.ToLower(e.ActionName)
	switch {
	case strings.HasPrefix(action, "pushed"):
		a.Kind, a.Ref = "push", path
		a.Title = fmt.Sprintf("%d commits to %s", e.PushData.CommitCount, e.PushData.Ref)
		if e.PushData.CommitTitle != "" {
			a.Title += ": " + e.PushData.CommitTitle
		}
	case action == "approved":
		a.Kind, a.Ref = "approve", fmt.Sprintf("%s!%d", path, e.TargetIID)
	case strings.HasPrefix(action, "commented"):
		iid := e.Note.NoteableIID
		if iid == 0 {
			iid = e.TargetIID
		}
		sep := "!"
		if e.Note.NoteableType == "Issue" || e.TargetType == "Issue" {
			sep = "#"
		}
		a.Kind, a.Ref = "comment", fmt.Sprintf("%s%s%d", path, sep, iid)
	case action == "opened" || action == "accepted" || action == "closed" || action == "merged":
		sep := "!"
		if e.TargetType == "Issue" {
			sep = "#"
		}
		kind := "mr"
		if e.TargetType == "Issue" {
			kind = "issue"
		}
		a.Kind, a.Ref = kind+" "+action, fmt.Sprintf("%s%s%d", path, sep, e.TargetIID)
	default:
		return a, false
	}
	a.URL = base + "/" + path
	if u, err := url.Parse(a.URL); err == nil {
		a.URL = u.String()
	}
	return a, true
}
