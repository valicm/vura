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

// Slack finds messages the token owner sent, via search.messages. Needs a
// user token (xoxp-…) with the search:read scope. Only the channel name and
// timestamp are kept; message text never reaches the store.
type Slack struct {
	WS    string // workspace label used in refs: "work#client-dev"
	Token string
	HTTP  *http.Client
	Base  string // default https://slack.com/api

	userID string
	names  map[string]string // user id -> handle, for DMs; needs users:read, else ids
	Cache  *store.Store
}

func (s *Slack) base() string {
	if s.Base != "" {
		return s.Base
	}
	return "https://slack.com/api"
}

func (s *Slack) auth(r *http.Request) { r.Header.Set("Authorization", "Bearer "+s.Token) }

// Name implements Source.
func (s *Slack) Name() string { return "slack:" + s.WS }

func (s *Slack) me(ctx context.Context) (string, error) {
	if s.userID != "" {
		return s.userID, nil
	}
	var resp struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		UserID string `json:"user_id"`
	}
	if _, err := getJSON(ctx, s.HTTP, s.base()+"/auth.test", s.auth, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("slack auth.test: %s", resp.Error)
	}
	s.userID = resp.UserID
	return resp.UserID, nil
}

type slackMatch struct {
	TS      string `json:"ts"`
	Channel struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		IsPrivate bool   `json:"is_private"`
		IsIM      bool   `json:"is_im"`
		IsMPIM    bool   `json:"is_mpim"`
	} `json:"channel"`
	Permalink string `json:"permalink"`
}

func (s *Slack) Fetch(ctx context.Context, since time.Time) ([]store.Anchor, error) {
	me, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	query := fmt.Sprintf("from:<@%s> after:%s", me, since.AddDate(0, 0, -1).Format("2006-01-02"))
	var out []store.Anchor
	for page := 1; page <= 10; page++ {
		q := url.Values{"query": {query}, "sort": {"timestamp"}, "sort_dir": {"desc"}, "count": {"100"}, "page": {strconv.Itoa(page)}}
		var resp struct {
			OK       bool   `json:"ok"`
			Error    string `json:"error"`
			Messages struct {
				Matches []slackMatch `json:"matches"`
				Paging  struct {
					Pages int `json:"pages"`
				} `json:"paging"`
			} `json:"messages"`
		}
		if _, err := getJSON(ctx, s.HTTP, s.base()+"/search.messages?"+q.Encode(), s.auth, &resp); err != nil {
			return out, err
		}
		if !resp.OK {
			return out, fmt.Errorf("slack search.messages: %s", resp.Error)
		}
		older := false
		for _, m := range resp.Messages.Matches {
			ts := slackTS(m.TS)
			if ts.Before(since) {
				older = true
				continue
			}
			out = append(out, s.slackAnchor(ctx, m, ts))
		}
		if older || page >= resp.Messages.Paging.Pages {
			break
		}
	}
	return out, nil
}

func slackTS(s string) time.Time {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(int64(f), int64((f-float64(int64(f)))*1e9))
}

// userName resolves a Slack user id to its handle when users:read is
// granted, else returns the id. Cached in memory and in state.
func (s *Slack) userName(ctx context.Context, id string) string {
	if s.names == nil {
		s.names = map[string]string{}
	}
	if n, ok := s.names[id]; ok {
		return n
	}
	key := "slack.user." + s.WS + "." + id
	if s.Cache != nil {
		if v, err := s.Cache.GetState(ctx, key); err == nil && v != "" {
			s.names[id] = v
			return v
		}
	}
	name := id
	var resp struct {
		OK   bool `json:"ok"`
		User struct {
			Name    string `json:"name"`
			Profile struct {
				DisplayName string `json:"display_name"`
			} `json:"profile"`
		} `json:"user"`
	}
	if _, err := getJSON(ctx, s.HTTP, s.base()+"/users.info?user="+url.QueryEscape(id), s.auth, &resp); err == nil && resp.OK {
		if resp.User.Name != "" {
			name = resp.User.Name
		}
		if s.Cache != nil {
			_ = s.Cache.SetState(ctx, key, name)
		}
	}
	s.names[id] = name
	return name
}

func (s *Slack) slackAnchor(ctx context.Context, m slackMatch, ts time.Time) store.Anchor {
	return slackAnchor(s.WS, m, ts, func(id string) string { return s.userName(ctx, id) })
}

func slackAnchor(ws string, m slackMatch, ts time.Time, name func(string) string) store.Anchor {
	ch := m.Channel.Name
	switch {
	case m.Channel.IsIM || strings.HasPrefix(m.Channel.ID, "D"):
		// search reports the counterpart's user id as the channel name
		ch = "dm-" + name(m.Channel.Name)
	case m.Channel.IsMPIM:
		// keep "mpdm-anna--ben-1": the participants are what makes it attributable
	case ch == "":
		ch = m.Channel.ID
	}
	return store.Anchor{
		ID: "slack:" + ws + ":" + m.Channel.ID + ":" + m.TS, TS: ts, Source: "slack", Kind: "message",
		Ref: ws + "#" + strings.ToLower(ch), URL: m.Permalink,
	}
}

// --- huddles ----------------------------------------------------------------

// EventSource is implemented by sources that also produce meetings.
type EventSource interface {
	Source
	Events(ctx context.Context, since time.Time) ([]store.Event, error)
}

// Events lists huddles the token owner took part in. Slack posts a message
// with subtype "huddle_thread" in the channel where a huddle happened, whose
// "room" carries real start and end times. Needs the *:read and *:history
// user scopes for the conversation types you want covered.
func (s *Slack) Events(ctx context.Context, since time.Time) ([]store.Event, error) {
	me, err := s.me(ctx)
	if err != nil {
		return nil, err
	}
	convs, err := s.conversations(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.Event
	for i, c := range convs {
		if i > 0 {
			time.Sleep(1200 * time.Millisecond) // conversations.history is Tier 3: ~50/min
		}
		q := url.Values{"channel": {c.id}, "oldest": {strconv.FormatInt(since.Unix(), 10)}, "limit": {"200"}}
		var resp struct {
			OK       bool   `json:"ok"`
			Error    string `json:"error"`
			Messages []struct {
				Subtype string `json:"subtype"`
				TS      string `json:"ts"`
				Room    struct {
					ID                 string   `json:"id"`
					DateStart          int64    `json:"date_start"`
					DateEnd            int64    `json:"date_end"`
					ParticipantHistory []string `json:"participant_history"`
					Participants       []string `json:"participants"`
				} `json:"room"`
			} `json:"messages"`
		}
		if _, err := getJSON(ctx, s.HTTP, s.base()+"/conversations.history?"+q.Encode(), s.auth, &resp); err != nil {
			return out, err
		}
		if !resp.OK {
			if resp.Error == "not_in_channel" || resp.Error == "channel_not_found" {
				continue
			}
			return out, fmt.Errorf("slack conversations.history %s: %s", c.name, resp.Error)
		}
		for _, m := range resp.Messages {
			if m.Subtype != "huddle_thread" || m.Room.DateStart == 0 {
				continue
			}
			if !containsStr(m.Room.ParticipantHistory, me) && !containsStr(m.Room.Participants, me) {
				continue
			}
			start := time.Unix(m.Room.DateStart, 0)
			end := time.Unix(m.Room.DateEnd, 0)
			if m.Room.DateEnd == 0 {
				end = time.Now() // still running
			}
			if end.Sub(start) < time.Minute {
				continue
			}
			out = append(out, store.Event{
				ID: "huddle:" + s.WS + ":" + c.id + ":" + m.TS, Start: start, End: end,
				Title: "Huddle " + s.WS + "#" + c.name, Source: "slack:" + s.WS,
			})
		}
	}
	return out, nil
}

type slackConv struct{ id, name string }

// conversations lists channels, private groups, DMs and group DMs the user is in.
func (s *Slack) conversations(ctx context.Context) ([]slackConv, error) {
	var out []slackConv
	cursor := ""
	for page := 0; page < 20; page++ {
		q := url.Values{"types": {"public_channel,private_channel,im,mpim"}, "exclude_archived": {"true"}, "limit": {"200"}}
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		var resp struct {
			OK       bool   `json:"ok"`
			Error    string `json:"error"`
			Channels []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				User     string `json:"user"` // DM counterpart
				IsIM     bool   `json:"is_im"`
				IsMPIM   bool   `json:"is_mpim"`
				IsMember bool   `json:"is_member"`
			} `json:"channels"`
			ResponseMetadata struct {
				NextCursor string `json:"next_cursor"`
			} `json:"response_metadata"`
		}
		if _, err := getJSON(ctx, s.HTTP, s.base()+"/conversations.list?"+q.Encode(), s.auth, &resp); err != nil {
			return nil, err
		}
		if !resp.OK {
			return nil, fmt.Errorf("slack conversations.list: %s", resp.Error)
		}
		for _, c := range resp.Channels {
			name := c.Name
			switch {
			case c.IsIM:
				name = "dm-" + s.userName(ctx, c.User)
			case c.IsMPIM:
				// keep the mpdm-… name: it carries the participants
			case !c.IsMember:
				continue
			}
			out = append(out, slackConv{c.ID, strings.ToLower(name)})
		}
		cursor = resp.ResponseMetadata.NextCursor
		if cursor == "" {
			break
		}
	}
	return out, nil
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
