// Package pager implements page_team: it posts one page for a team to the team's Slack channel.
//
// The tool builds the whole message. Mentions come only from the slack-ids file; the summary and
// incident link come from a model, so they are treated as untrusted text: control and format
// characters are stripped, Slack's three markup characters are escaped, and only https links on
// slack.com are kept.
package pager

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
)

// MaxSummaryRunes caps the summary's length.
const MaxSummaryRunes = 300

// DefaultWindow is how long a team cannot be paged again after a page.
const DefaultWindow = 10 * time.Minute

// Teams finds a team in the catalog.
type Teams interface {
	Team(name string) (catalog.Team, error)
}

// Poster posts a top-level Slack message and returns its ts.
type Poster interface {
	PostMessage(ctx context.Context, channel, text string) (string, error)
}

// Input is page_team's input.
type Input struct {
	Team         string `json:"team" jsonschema:"the team to page, a catalog team name such as payments"`
	Severity     int    `json:"severity" jsonschema:"incident severity, 1 (highest) to 4"`
	Summary      string `json:"summary" jsonschema:"one-line summary of the incident, at most 300 characters"`
	IncidentLink string `json:"incident_link,omitempty" jsonschema:"optional https link to the incident's Slack thread"`
}

// Output is page_team's result.
type Output struct {
	Posted  bool   `json:"posted" jsonschema:"whether the page was posted"`
	Channel string `json:"channel" jsonschema:"the channel the page went to"`
	TS      string `json:"ts" jsonschema:"the Slack ts of the page message"`
	// LinkDropped is set when an incident_link was given but was not an https slack.com URL.
	LinkDropped bool `json:"link_dropped,omitempty" jsonschema:"true when incident_link was rejected and left out"`
}

// Pager pages teams. It is safe for concurrent use.
type Pager struct {
	teams  Teams
	ids    *slackids.Config
	slack  Poster
	window time.Duration
	now    func() time.Time

	mu       sync.Mutex
	lastPage map[string]time.Time
}

// New returns a Pager. window <= 0 means DefaultWindow; now nil means time.Now.
func New(teams Teams, ids *slackids.Config, slack Poster, window time.Duration, now func() time.Time) *Pager {
	if window <= 0 {
		window = DefaultWindow
	}
	if now == nil {
		now = time.Now
	}
	return &Pager{teams: teams, ids: ids, slack: slack, window: window, now: now, lastPage: map[string]time.Time{}}
}

// Page validates in, builds the message and posts it. Nothing is posted on any error.
func (p *Pager) Page(ctx context.Context, in Input) (Output, error) {
	if in.Severity < 1 || in.Severity > 4 {
		return Output{}, fmt.Errorf("severity must be 1 to 4, got %d", in.Severity)
	}
	summary := CleanSummary(in.Summary)
	if summary == "" {
		return Output{}, errors.New("summary is empty")
	}
	team, err := p.teams.Team(in.Team)
	if err != nil {
		return Output{}, err
	}
	key := strings.ToLower(team.Name)
	ids, ok := p.ids.Teams[key]
	if !ok {
		return Output{}, fmt.Errorf("team %q has no Slack IDs configured; nothing was posted", team.Name)
	}
	link, linkOK := CleanLink(in.IncidentLink)
	text := Message(in.Severity, ids, summary, link)

	if err := p.reserve(key); err != nil {
		return Output{}, err
	}
	ts, err := p.slack.PostMessage(ctx, ids.ChannelID, text)
	if err != nil {
		p.release(key)
		return Output{}, fmt.Errorf("post page for %s: %w", team.Name, err)
	}
	return Output{Posted: true, Channel: team.SlackChannel, TS: ts, LinkDropped: in.IncidentLink != "" && !linkOK}, nil
}

// reserve claims the team's page slot, or fails if it was paged within the window.
func (p *Pager) reserve(team string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if last, ok := p.lastPage[team]; ok && now.Sub(last) < p.window {
		wait := p.window - now.Sub(last)
		return fmt.Errorf("team %s was already paged %s ago; at most one page per %s (try again in %s)",
			team, now.Sub(last).Round(time.Second), p.window, wait.Round(time.Second))
	}
	p.lastPage[team] = now
	return nil
}

// release frees the slot reserve claimed, after a failed post.
func (p *Pager) release(team string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.lastPage, team)
}

// Message builds the page text. summary and link must already be cleaned.
func Message(severity int, ids slackids.Team, summary, link string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "SEV%d for <!subteam^%s> → paging <@%s> (primary on-call)\n", severity, ids.OncallGroupID, ids.PrimaryUserID)
	if ids.SecondaryUserID != "" {
		fmt.Fprintf(&b, "Escalates to <@%s> (secondary) if not acknowledged in 5 min.\n", ids.SecondaryUserID)
	}
	b.WriteString(summary)
	if link != "" {
		b.WriteString(" · ")
		b.WriteString(link)
	}
	return b.String()
}

// CleanSummary makes model-written text safe to put in a Slack message: whitespace runs (newlines
// included) become one space, control and invisible format characters are dropped, the result is
// cut to MaxSummaryRunes and then &, < and > are escaped, so no mention, broadcast or link markup
// can survive.
func CleanSummary(s string) string {
	var kept strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			kept.WriteByte(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == unicode.ReplacementChar:
			// dropped
		default:
			kept.WriteRune(r)
		}
	}
	out := strings.Join(strings.Fields(kept.String()), " ")
	if runes := []rune(out); len(runes) > MaxSummaryRunes {
		out = strings.TrimSpace(string(runes[:MaxSummaryRunes]))
	}
	return Escape(out)
}

// Escape escapes Slack's three markup characters.
func Escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// CleanLink returns link, escaped, if it is an https URL on slack.com or a subdomain of it, and
// reports whether it was kept. Anything else is dropped.
func CleanLink(link string) (string, bool) {
	link = strings.TrimSpace(link)
	if link == "" {
		return "", false
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if u.Port() != "" || (host != "slack.com" && !strings.HasSuffix(host, ".slack.com")) {
		return "", false
	}
	// Re-encode so spaces, quotes and other odd characters cannot end the link early.
	out := u.String()
	for _, r := range out {
		if unicode.IsSpace(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return "", false
		}
	}
	return Escape(out), true
}
