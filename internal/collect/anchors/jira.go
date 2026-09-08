package anchors

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/valicm/vura/internal/store"
)

// Jira pulls recently updated issues in the watched projects and keeps the
// changelog entries and comments authored by the token owner.
type Jira struct {
	Site     string
	Email    string
	Token    string
	Projects []string
	HTTP     *http.Client

	accountID string
}

func (j *Jira) Name() string { return "jira:" + j.Site }

func (j *Jira) base() string {
	if strings.HasPrefix(j.Site, "http") {
		return strings.TrimRight(j.Site, "/")
	}
	return "https://" + j.Site
}

func (j *Jira) auth(r *http.Request) { r.SetBasicAuth(j.Email, j.Token) }

type jiraIssue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
		Comment struct {
			Comments []struct {
				ID     string `json:"id"`
				Author struct {
					AccountID string `json:"accountId"`
				} `json:"author"`
				Created jiraTime `json:"created"`
				Updated jiraTime `json:"updated"`
			} `json:"comments"`
		} `json:"comment"`
	} `json:"fields"`
	Changelog struct {
		Histories []struct {
			ID     string `json:"id"`
			Author struct {
				AccountID string `json:"accountId"`
			} `json:"author"`
			Created jiraTime `json:"created"`
			Items   []struct {
				Field    string `json:"field"`
				ToString string `json:"toString"`
			} `json:"items"`
		} `json:"histories"`
	} `json:"changelog"`
}

// jiraTime parses Jira's "2026-09-04T09:34:12.000+0200".
type jiraTime struct{ time.Time }

func (t *jiraTime) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		return nil
	}
	for _, layout := range []string{"2006-01-02T15:04:05.000-0700", "2006-01-02T15:04:05.000Z07:00", time.RFC3339} {
		if v, err := time.Parse(layout, s); err == nil {
			t.Time = v
			return nil
		}
	}
	return fmt.Errorf("jira time %q", s)
}

func (j *Jira) me(ctx context.Context) (string, error) {
	if j.accountID != "" {
		return j.accountID, nil
	}
	var me struct {
		AccountID string `json:"accountId"`
	}
	if _, err := getJSON(ctx, j.HTTP, j.base()+"/rest/api/3/myself", j.auth, &me); err != nil {
		return "", err
	}
	j.accountID = me.AccountID
	return me.AccountID, nil
}

func (j *Jira) Fetch(ctx context.Context, since time.Time) ([]store.Anchor, error) {
	me, err := j.me(ctx)
	if err != nil {
		return nil, err
	}
	days := int(time.Since(since).Hours()/24) + 1
	jql := fmt.Sprintf(`updated >= "-%dd" ORDER BY updated DESC`, days)
	if len(j.Projects) > 0 {
		jql = fmt.Sprintf(`project in (%s) AND `, strings.Join(j.Projects, ",")) + jql
	}
	var out []store.Anchor
	token := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"jql": {jql}, "fields": {"summary,comment"}, "expand": {"changelog"}, "maxResults": {"50"}}
		if token != "" {
			q.Set("nextPageToken", token)
		}
		var resp struct {
			Issues        []jiraIssue `json:"issues"`
			NextPageToken string      `json:"nextPageToken"`
			IsLast        bool        `json:"isLast"`
		}
		if _, err := getJSON(ctx, j.HTTP, j.base()+"/rest/api/3/search/jql?"+q.Encode(), j.auth, &resp); err != nil {
			return out, err
		}
		for _, is := range resp.Issues {
			out = append(out, jiraAnchors(is, me, since, j.base())...)
		}
		if resp.IsLast || resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	return out, nil
}

func jiraAnchors(is jiraIssue, me string, since time.Time, base string) []store.Anchor {
	var out []store.Anchor
	url := base + "/browse/" + is.Key
	for _, h := range is.Changelog.Histories {
		if h.Author.AccountID != me || h.Created.Before(since) {
			continue
		}
		kind, title := "update", is.Fields.Summary
		for _, it := range h.Items {
			if it.Field == "status" {
				kind, title = "transition", is.Fields.Summary+" → "+it.ToString
				break
			}
		}
		out = append(out, store.Anchor{ID: "jira:" + is.Key + ":h" + h.ID, TS: h.Created.Time, Source: "jira", Kind: kind, Ref: is.Key, Title: title, URL: url})
	}
	for _, c := range is.Fields.Comment.Comments {
		if c.Author.AccountID != me {
			continue
		}
		ts := c.Created.Time
		if c.Updated.After(ts) {
			ts = c.Updated.Time
		}
		if ts.Before(since) {
			continue
		}
		out = append(out, store.Anchor{ID: "jira:" + is.Key + ":c" + c.ID, TS: ts, Source: "jira", Kind: "comment", Ref: is.Key, Title: is.Fields.Summary, URL: url})
	}
	return out
}
