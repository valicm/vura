// Package tempo writes worklogs through the Tempo Cloud REST API v4 and
// resolves issue keys and the author's account id through Jira Cloud.
package tempo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	TempoBase  string // https://api.tempo.io/4
	TempoToken string
	JiraBase   string // https://yoursite.atlassian.net
	JiraEmail  string
	JiraToken  string
	HTTP       *http.Client

	// Cache hooks: issue key -> id. Optional.
	GetCached func(key string) (string, bool)
	SetCached func(key, id string)

	accountID string
}

func New(tempoToken, jiraSite, jiraEmail, jiraToken string) *Client {
	base := jiraSite
	if !hasScheme(base) {
		base = "https://" + base
	}
	return &Client{
		TempoBase: "https://api.tempo.io/4", TempoToken: tempoToken,
		JiraBase: base, JiraEmail: jiraEmail, JiraToken: jiraToken,
		HTTP: &http.Client{Timeout: 30 * time.Second},
	}
}

func hasScheme(s string) bool { u, err := url.Parse(s); return err == nil && u.Scheme != "" }

// APIError carries the status and body of a failed call.
type APIError struct {
	Op     string
	Status int
	Body   string
}

func (e *APIError) Error() string {
	b := e.Body
	if len(b) > 300 {
		b = b[:300] + "..."
	}
	return fmt.Sprintf("%s: HTTP %d: %s", e.Op, e.Status, b)
}

func (c *Client) do(ctx context.Context, op, method, u string, auth func(*http.Request), body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	auth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Op: op, Status: resp.StatusCode, Body: string(data)}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("%s: decode: %w", op, err)
		}
	}
	return nil
}

func (c *Client) jiraAuth(r *http.Request)  { r.SetBasicAuth(c.JiraEmail, c.JiraToken) }
func (c *Client) tempoAuth(r *http.Request) { r.Header.Set("Authorization", "Bearer "+c.TempoToken) }

// AccountID returns the Jira account id of the token's owner.
func (c *Client) AccountID(ctx context.Context) (string, error) {
	if c.accountID != "" {
		return c.accountID, nil
	}
	var me struct {
		AccountID string `json:"accountId"`
	}
	if err := c.do(ctx, "jira myself", "GET", c.JiraBase+"/rest/api/3/myself", c.jiraAuth, nil, &me); err != nil {
		return "", err
	}
	if me.AccountID == "" {
		return "", fmt.Errorf("jira myself: empty accountId")
	}
	c.accountID = me.AccountID
	return me.AccountID, nil
}

// IssueID resolves EXT-88 to Jira's numeric id, which Tempo v4 requires.
func (c *Client) IssueID(ctx context.Context, key string) (string, error) {
	if c.GetCached != nil {
		if id, ok := c.GetCached(key); ok {
			return id, nil
		}
	}
	var issue struct {
		ID string `json:"id"`
	}
	u := c.JiraBase + "/rest/api/3/issue/" + url.PathEscape(key) + "?fields=id"
	if err := c.do(ctx, "jira issue "+key, "GET", u, c.jiraAuth, nil, &issue); err != nil {
		return "", err
	}
	if issue.ID == "" {
		return "", fmt.Errorf("jira issue %s: empty id", key)
	}
	if c.SetCached != nil {
		c.SetCached(key, issue.ID)
	}
	return issue.ID, nil
}

// Worklog is what gets pushed.
type Worklog struct {
	IssueKey    string
	Start       time.Time // local time; date and time are sent separately
	Seconds     int
	Description string
}

// Create posts one worklog and returns Tempo's worklog id.
func (c *Client) Create(ctx context.Context, w Worklog) (string, error) {
	acct, err := c.AccountID(ctx)
	if err != nil {
		return "", err
	}
	idStr, err := c.IssueID(ctx, w.IssueKey)
	if err != nil {
		return "", err
	}
	issueID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("issue id %q for %s is not numeric", idStr, w.IssueKey)
	}
	body := map[string]any{
		"authorAccountId":  acct,
		"issueId":          issueID,
		"startDate":        w.Start.Format("2006-01-02"),
		"startTime":        w.Start.Format("15:04:05"),
		"timeSpentSeconds": w.Seconds,
		"description":      w.Description,
	}
	var resp struct {
		TempoWorklogID json.Number `json:"tempoWorklogId"`
	}
	if err := c.do(ctx, "tempo create", "POST", c.TempoBase+"/worklogs", c.tempoAuth, body, &resp); err != nil {
		return "", err
	}
	if resp.TempoWorklogID == "" {
		return "", fmt.Errorf("tempo create: no tempoWorklogId in response")
	}
	return resp.TempoWorklogID.String(), nil
}

// Delete removes a worklog. A 404 counts as success: it is already gone.
func (c *Client) Delete(ctx context.Context, tempoID string) error {
	err := c.do(ctx, "tempo delete "+tempoID, "DELETE", c.TempoBase+"/worklogs/"+url.PathEscape(tempoID), c.tempoAuth, nil, nil)
	if ae, ok := err.(*APIError); ok && ae.Status == http.StatusNotFound {
		return nil
	}
	return err
}

// Ping makes an authenticated read to confirm the Tempo token works.
func (c *Client) Ping(ctx context.Context) error {
	today := time.Now().Format("2006-01-02")
	u := c.TempoBase + "/worklogs?from=" + today + "&to=" + today + "&limit=1"
	return c.do(ctx, "tempo ping", "GET", u, c.tempoAuth, nil, nil)
}
