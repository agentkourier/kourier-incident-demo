// Package slackapi is a minimal Slack Web API client: the four methods the demo servers call,
// over plain net/http. The token is never logged or put in an error.
package slackapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultBaseURL is the Slack Web API base URL.
const DefaultBaseURL = "https://slack.com/api"

// maxResponseBytes caps how much of a Slack response is read.
const maxResponseBytes = 4 << 20

// Client calls the Slack Web API with one bot token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client for baseURL (DefaultBaseURL when empty). httpClient may be nil.
func New(baseURL, token string, httpClient *http.Client) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: httpClient}
}

// APIError is a response with ok=false.
type APIError struct {
	Method string
	Code   string
}

func (e *APIError) Error() string { return fmt.Sprintf("slack %s: %s", e.Method, e.Code) }

// Message is a Slack message as conversations.history and conversations.replies return it.
type Message struct {
	Type        string       `json:"type"`
	Subtype     string       `json:"subtype,omitempty"`
	User        string       `json:"user,omitempty"`
	BotID       string       `json:"bot_id,omitempty"`
	Username    string       `json:"username,omitempty"`
	BotProfile  *BotProfile  `json:"bot_profile,omitempty"`
	Text        string       `json:"text"`
	TS          string       `json:"ts"`
	ThreadTS    string       `json:"thread_ts,omitempty"`
	ReplyCount  int          `json:"reply_count,omitempty"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// BotProfile names the bot that posted a message.
type BotProfile struct {
	Name string `json:"name"`
}

// Attachment is a legacy message attachment (Alertmanager's Slack receiver uses them).
type Attachment struct {
	Title    string `json:"title,omitempty"`
	Text     string `json:"text,omitempty"`
	Fallback string `json:"fallback,omitempty"`
}

// User is the part of users.info the demo reads.
type User struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RealName string `json:"real_name"`
	Profile  struct {
		DisplayName string `json:"display_name"`
		RealName    string `json:"real_name"`
	} `json:"profile"`
}

// DisplayName is the name Slack shows for the user.
func (u User) DisplayName() string {
	for _, n := range []string{u.Profile.DisplayName, u.Profile.RealName, u.RealName, u.Name} {
		if n != "" {
			return n
		}
	}
	return u.ID
}

// PostMessage posts text as a new top-level message in channel and returns its ts. Slack parses
// only explicit markup (parse=none, link_names=false), and links are not unfurled.
func (c *Client) PostMessage(ctx context.Context, channel, text string) (string, error) {
	form := url.Values{
		"channel":      {channel},
		"text":         {text},
		"parse":        {"none"},
		"link_names":   {"false"},
		"unfurl_links": {"false"},
		"unfurl_media": {"false"},
	}
	var resp struct {
		TS string `json:"ts"`
	}
	if err := c.call(ctx, "chat.postMessage", form, &resp); err != nil {
		return "", err
	}
	return resp.TS, nil
}

// Page is one page of messages and the cursor for the next (empty when there is none).
type Page struct {
	Messages   []Message
	NextCursor string
}

// Replies returns one page of a thread, oldest first.
func (c *Client) Replies(ctx context.Context, channel, threadTS, cursor string, limit int) (Page, error) {
	form := url.Values{"channel": {channel}, "ts": {threadTS}, "limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		form.Set("cursor", cursor)
	}
	return c.page(ctx, "conversations.replies", form)
}

// History returns one page of a channel's messages posted after oldest, newest first.
func (c *Client) History(ctx context.Context, channel, oldest, cursor string, limit int) (Page, error) {
	form := url.Values{"channel": {channel}, "oldest": {oldest}, "limit": {fmt.Sprint(limit)}}
	if cursor != "" {
		form.Set("cursor", cursor)
	}
	return c.page(ctx, "conversations.history", form)
}

// UserInfo returns a user's profile.
func (c *Client) UserInfo(ctx context.Context, userID string) (User, error) {
	var resp struct {
		User User `json:"user"`
	}
	if err := c.call(ctx, "users.info", url.Values{"user": {userID}}, &resp); err != nil {
		return User{}, err
	}
	return resp.User, nil
}

func (c *Client) page(ctx context.Context, method string, form url.Values) (Page, error) {
	var resp struct {
		Messages []Message `json:"messages"`
		Metadata struct {
			NextCursor string `json:"next_cursor"`
		} `json:"response_metadata"`
	}
	if err := c.call(ctx, method, form, &resp); err != nil {
		return Page{}, err
	}
	return Page{Messages: resp.Messages, NextCursor: resp.Metadata.NextCursor}, nil
}

// call POSTs a form-encoded request to method and decodes the response into out after checking ok.
func (c *Client) call(ctx context.Context, method string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/"+method, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("slack %s: build request: %w", method, err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+c.token)
	res, err := c.http.Do(req)
	if err != nil {
		// The error names the URL, never the token (it is a header).
		return fmt.Errorf("slack %s: %w", method, err)
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("slack %s: rate limited (retry after %ss)", method, res.Header.Get("Retry-After"))
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("slack %s: HTTP %d", method, res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("slack %s: read response: %w", method, err)
	}
	var status struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("slack %s: decode response: %w", method, err)
	}
	if !status.OK {
		if status.Error == "" {
			status.Error = "unknown_error"
		}
		return &APIError{Method: method, Code: status.Error}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("slack %s: decode response: %w", method, err)
	}
	return nil
}
