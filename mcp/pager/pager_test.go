package pager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentkourier/kourier-incident-demo/mcp/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
)

const testToken = "xoxb-fake-test-token"

// fakeSlack records chat.postMessage calls.
type fakeSlack struct {
	mu    sync.Mutex
	posts []url.Values
	fail  string // when set, respond ok=false with this error
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/chat.postMessage" || r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if f.fail != "" {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": f.fail})
		return
	}
	f.posts = append(f.posts, r.PostForm)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": r.PostForm.Get("channel"), "ts": "1730000000.000100"})
}

func (f *fakeSlack) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.posts)
}

var testIDs = &slackids.Config{Teams: map[string]slackids.Team{
	"payments": {ChannelID: "C0PAY", OncallGroupID: "S0PAY", PrimaryUserID: "U0DANA", SecondaryUserID: "U0LEE"},
	"checkout": {ChannelID: "C0CHK", OncallGroupID: "S0CHK", PrimaryUserID: "U0PRIYA"},
}}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newTestPager(t *testing.T) (*Pager, *fakeSlack, *clock) {
	t.Helper()
	cat, err := catalog.Load("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeSlack{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	clk := &clock{t: time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)}
	return New(cat, testIDs, slackapi.New(srv.URL, testToken, srv.Client()), 0, clk.now), fake, clk
}

func TestPageMessageFormat(t *testing.T) {
	p, fake, _ := newTestPager(t)
	out, err := p.Page(context.Background(), Input{
		Team: "Payments", Severity: 2,
		Summary:      "checkout 502s since payments v2.14.0",
		IncidentLink: "https://kourier.slack.com/archives/C0INC/p1730000000000100",
	})
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	want := Output{Posted: true, Channel: "#payments-ops", TS: "1730000000.000100"}
	if out != want {
		t.Errorf("out = %+v, want %+v", out, want)
	}
	if fake.count() != 1 {
		t.Fatalf("posts = %d, want 1", fake.count())
	}
	got := fake.posts[0]
	wantText := "SEV2 for <!subteam^S0PAY> → paging <@U0DANA> (primary on-call)\n" +
		"Escalates to <@U0LEE> (secondary) if not acknowledged in 5 min.\n" +
		"*Summary:* checkout 502s since payments v2.14.0\n" +
		"<https://kourier.slack.com/archives/C0INC/p1730000000000100|Alert thread>"
	if got.Get("text") != wantText {
		t.Errorf("text =\n%s\nwant\n%s", got.Get("text"), wantText)
	}
	for k, v := range map[string]string{"channel": "C0PAY", "parse": "none", "link_names": "false", "unfurl_links": "false"} {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
	if got.Has("thread_ts") {
		t.Error("page was posted in a thread")
	}
}

func TestPageWithoutSecondaryOrLink(t *testing.T) {
	p, fake, _ := newTestPager(t)
	if _, err := p.Page(context.Background(), Input{Team: "checkout", Severity: 3, Summary: "elevated latency"}); err != nil {
		t.Fatal(err)
	}
	want := "SEV3 for <!subteam^S0CHK> → paging <@U0PRIYA> (primary on-call)\n*Summary:* elevated latency"
	if got := fake.posts[0].Get("text"); got != want {
		t.Errorf("text =\n%s\nwant\n%s", got, want)
	}
}

func TestPageTeamWithoutGroup(t *testing.T) {
	ids := slackids.Team{ChannelID: "C0CHK", PrimaryUserID: "U0PRIYA"}
	if got, want := Message(2, ids, "x", ""), "SEV2 → paging <@U0PRIYA> (primary on-call)\n*Summary:* x"; got != want {
		t.Errorf("Message = %q, want %q", got, want)
	}
}

func TestPageNeutralisesModelMarkup(t *testing.T) {
	p, fake, _ := newTestPager(t)
	out, err := p.Page(context.Background(), Input{
		Team: "payments", Severity: 1,
		Summary:      "<!channel> <@U0EVIL> <https://evil.example|click> & ‮spoof\x00\nnext line",
		IncidentLink: "https://evil.example/https://x.slack.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.LinkDropped {
		t.Error("LinkDropped = false, want true")
	}
	text := fake.posts[0].Get("text")
	lines := strings.Split(text, "\n")
	if len(lines) != 3 {
		t.Fatalf("text has %d lines, want 3:\n%s", len(lines), text)
	}
	wantLast := "*Summary:* &lt;!channel&gt; &lt;@U0EVIL&gt; &lt;https://evil.example|click&gt; &amp; spoof next line"
	if lines[2] != wantLast {
		t.Errorf("summary line = %q, want %q", lines[2], wantLast)
	}
	// The only markup in the message is the tool's own.
	if n := strings.Count(text, "<"); n != 3 {
		t.Errorf("message has %d '<', want 3 (group, primary, secondary):\n%s", n, text)
	}
}

func TestCleanSummary(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  plain   text  ", "plain text"},
		{"a\r\nb\tc", "a b c"},
		{"<@U1> & <!here>", "&lt;@U1&gt; &amp; &lt;!here&gt;"},
		{"zero​width⁦iso", "zerowidthiso"},
		{"bell\x07", "bell"},
		{strings.Repeat("é", 310), strings.Repeat("é", 300)},
		{strings.Repeat("<", 301), strings.Repeat("&lt;", 300)},
		{"\x00\n", ""},
	}
	for _, tt := range tests {
		if got := CleanSummary(tt.in); got != tt.want {
			t.Errorf("CleanSummary(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCleanLink(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"https://kourier.slack.com/archives/C1/p123", "https://kourier.slack.com/archives/C1/p123", true},
		{"https://slack.com/x?a=1&b=2", "https://slack.com/x?a=1&amp;b=2", true},
		{"https://app.slack.com/client/T1/C1", "https://app.slack.com/client/T1/C1", true},
		{"http://kourier.slack.com/archives/C1", "", false},
		{"https://slack.com.evil.example/x", "", false},
		{"https://evilslack.com/x", "", false},
		{"https://user@kourier.slack.com/x", "", false},
		{"https://kourier.slack.com:8443/x", "", false},
		{"https://kourier.slack.com/a>b|<!channel>", "https://kourier.slack.com/a%3Eb%7C%3C%21channel%3E", true},
		{"https://kourier.slack.com/x?a=b|c", "https://kourier.slack.com/x?a=b%7Cc", true},
		{"javascript:alert(1)", "", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := CleanLink(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("CleanLink(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
		if strings.ContainsAny(got, "<>|") {
			t.Errorf("CleanLink(%q) = %q keeps markup", tt.in, got)
		}
	}
}

func TestPageRejectsWithoutPosting(t *testing.T) {
	tests := []struct {
		name string
		in   Input
		want string
	}{
		{"unknown team", Input{Team: "inventory", Severity: 2, Summary: "x"}, "unknown team"},
		{"severity low", Input{Team: "payments", Severity: 0, Summary: "x"}, "severity"},
		{"severity high", Input{Team: "payments", Severity: 5, Summary: "x"}, "severity"},
		{"empty summary", Input{Team: "payments", Severity: 2, Summary: " \x00 "}, "summary"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, fake, _ := newTestPager(t)
			_, err := p.Page(context.Background(), tt.in)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
			if fake.count() != 0 {
				t.Errorf("posted %d messages, want 0", fake.count())
			}
		})
	}
}

func TestPageTeamWithoutIDs(t *testing.T) {
	p, fake, _ := newTestPager(t)
	p.ids = &slackids.Config{Teams: map[string]slackids.Team{}}
	if _, err := p.Page(context.Background(), Input{Team: "payments", Severity: 2, Summary: "x"}); err == nil {
		t.Fatal("Page succeeded for a team without Slack IDs")
	}
	if fake.count() != 0 {
		t.Error("posted without IDs")
	}
}

func TestPageRateLimit(t *testing.T) {
	p, fake, clk := newTestPager(t)
	ctx := context.Background()
	in := Input{Team: "payments", Severity: 2, Summary: "x"}
	if _, err := p.Page(ctx, in); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(9 * time.Minute)
	if _, err := p.Page(ctx, in); err == nil || !strings.Contains(err.Error(), "already paged") {
		t.Fatalf("second page within 10 min: err = %v", err)
	}
	// Another team is not limited by the first.
	if _, err := p.Page(ctx, Input{Team: "checkout", Severity: 2, Summary: "x"}); err != nil {
		t.Fatalf("other team: %v", err)
	}
	clk.t = clk.t.Add(time.Minute)
	if _, err := p.Page(ctx, in); err != nil {
		t.Fatalf("page after 10 min: %v", err)
	}
	if fake.count() != 3 {
		t.Errorf("posts = %d, want 3", fake.count())
	}
}

func TestFailedPostDoesNotConsumeSlot(t *testing.T) {
	p, fake, _ := newTestPager(t)
	ctx := context.Background()
	in := Input{Team: "payments", Severity: 2, Summary: "x"}
	fake.fail = "channel_not_found"
	if _, err := p.Page(ctx, in); err == nil || !strings.Contains(err.Error(), "channel_not_found") {
		t.Fatalf("err = %v, want channel_not_found", err)
	}
	fake.fail = ""
	if _, err := p.Page(ctx, in); err != nil {
		t.Fatalf("retry after failed post: %v", err)
	}
}
