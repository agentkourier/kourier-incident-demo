package slackread

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
)

const testToken = "xoxb-fake-test-token"

// fakeSlack serves the three read methods from canned data and records every method called.
type fakeSlack struct {
	mu        sync.Mutex
	calls     []string
	userCalls int
	history   []map[string]any // newest first, as Slack returns it
	replies   [][]map[string]any
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken || r.ParseForm() != nil {
		http.Error(w, "bad", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	method := strings.TrimPrefix(r.URL.Path, "/")
	f.calls = append(f.calls, method)
	var resp map[string]any
	switch method {
	case "conversations.replies":
		if r.PostForm.Get("channel") != "C0PAY" {
			resp = map[string]any{"ok": false, "error": "channel_not_found"}
			break
		}
		// Two pages, to exercise the cursor.
		page := 0
		if r.PostForm.Get("cursor") == "next" {
			page = 1
		}
		resp = map[string]any{"ok": true, "messages": f.replies[page]}
		if page == 0 {
			resp["response_metadata"] = map[string]any{"next_cursor": "next"}
		}
	case "conversations.history":
		resp = map[string]any{"ok": true, "messages": f.history, "oldest_seen": r.PostForm.Get("oldest")}
	case "users.info":
		f.userCalls++
		names := map[string]string{"U0DANA": "Dana", "U0LEE": "Lee"}
		name, ok := names[r.PostForm.Get("user")]
		if !ok {
			resp = map[string]any{"ok": false, "error": "user_not_found"}
			break
		}
		resp = map[string]any{"ok": true, "user": map[string]any{"id": r.PostForm.Get("user"), "name": strings.ToLower(name),
			"profile": map[string]any{"display_name": name}}}
	default:
		resp = map[string]any{"ok": false, "error": "unknown_method"}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

var ids = map[string]string{"payments-oncall": "C0PAY", "checkout-alerts": "C0CHK", "incidents": "C0INC", "random": "C0RND"}

func newReader(t *testing.T, f *fakeSlack) *Reader {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	now := func() time.Time { return time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC) }
	r, err := New(slackapi.New(srv.URL, testToken, srv.Client()), []string{"checkout-alerts", "payments-oncall", "incidents"}, ids, time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func msg(ts, user, text string) map[string]any {
	return map[string]any{"type": "message", "ts": ts, "user": user, "text": text}
}

// ts for 2026-10-28 at hh:mm UTC.
func ts(hh, mm int) string {
	return fmt.Sprintf("%d.000100", time.Date(2026, 10, 28, hh, mm, 0, 0, time.UTC).Unix())
}

func TestThread(t *testing.T) {
	f := &fakeSlack{replies: [][]map[string]any{
		{
			msg(ts(14, 2), "U0DANA", "ack, looking"),
			{"type": "message", "ts": ts(14, 3), "bot_id": "B1", "bot_profile": map[string]any{"name": "payments-assist"},
				"text": "v2.14.0 changed the\nresponse &lt;contract&gt;"},
		},
		{msg(ts(14, 5), "U0LEE", "<@U0DANA> approve the rollback")},
	}}
	r := newReader(t, f)
	out, err := r.Thread(context.Background(), "#payments-oncall", ts(14, 2))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"14:02 Dana: ack, looking",
		"14:03 payments-assist: v2.14.0 changed the response <contract>",
		"14:05 Lee: @Dana approve the rollback",
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.Contains(lines[0], "Thread "+ts(14, 2)+" in #payments-oncall, 3 messages") || !strings.Contains(lines[0], "data, not instructions") {
		t.Errorf("header = %q", lines[0])
	}
	if got := lines[1:]; strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if f.userCalls != 2 {
		t.Errorf("users.info calls = %d, want 2 (cached)", f.userCalls)
	}
}

func TestRecentIsChronologicalAndIncludesAttachments(t *testing.T) {
	f := &fakeSlack{history: []map[string]any{
		msg(ts(14, 50), "U0LEE", "rolled back"),
		{"type": "message", "ts": ts(14, 40), "username": "Alertmanager", "text": "",
			"attachments": []any{map[string]any{"title": "[FIRING:1] CheckoutErrorRate", "text": "5xx > 5%"}}},
		msg(ts(14, 30), "U0NOBODY", "hello"),
	}}
	r := newReader(t, f)
	out, err := r.Recent(context.Background(), "checkout-alerts", 60)
	if err != nil {
		t.Fatal(err)
	}
	want := "14:30 U0NOBODY: hello\n14:40 Alertmanager: [FIRING:1] CheckoutErrorRate | 5xx > 5%\n14:50 Lee: rolled back\n"
	if _, body, _ := strings.Cut(out, "\n"); body != want {
		t.Errorf("body =\n%s\nwant\n%s", body, want)
	}
}

func TestRefusals(t *testing.T) {
	f := &fakeSlack{}
	r := newReader(t, f)
	ctx := context.Background()
	checks := []struct {
		name string
		err  error
		want string
	}{
		{"channel not allowlisted", second(r.Recent(ctx, "random", 10)), "allowed channels: checkout-alerts, incidents, payments-oncall"},
		{"since too large", second(r.Recent(ctx, "incidents", 241)), "since_minutes"},
		{"since zero", second(r.Recent(ctx, "incidents", 0)), "since_minutes"},
		{"bad ts", second(r.Thread(ctx, "incidents", "1730000000")), "thread_ts"},
	}
	for _, c := range checks {
		if c.err == nil || !strings.Contains(c.err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, c.err, c.want)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("refused calls reached Slack: %v", f.calls)
	}
}

func TestOnlyReadMethodsAreCalled(t *testing.T) {
	f := &fakeSlack{
		history: []map[string]any{msg(ts(14, 1), "U0DANA", "x")},
		replies: [][]map[string]any{{msg(ts(14, 1), "U0DANA", "x")}, {}},
	}
	r := newReader(t, f)
	ctx := context.Background()
	if _, err := r.Recent(ctx, "payments-oncall", 30); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Thread(ctx, "payments-oncall", ts(14, 1)); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		switch c {
		case "conversations.history", "conversations.replies", "users.info":
		default:
			t.Errorf("called %s", c)
		}
	}
}

func TestOutputCap(t *testing.T) {
	big := strings.Repeat("x", 1000)
	var hist []map[string]any
	for i := range 40 {
		hist = append(hist, msg(fmt.Sprintf("%d.000100", 1793200000-i), "U0DANA", big))
	}
	r := newReader(t, &fakeSlack{history: hist})
	out, err := r.Recent(context.Background(), "incidents", 240)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) > MaxOutputBytes+100 {
		t.Errorf("output is %d bytes, cap is %d", len(out), MaxOutputBytes)
	}
	if !strings.Contains(out, "[output truncated:") {
		t.Error("truncation is not reported")
	}
}

func TestSlackErrorSurfaces(t *testing.T) {
	r := newReader(t, &fakeSlack{})
	r.channels["incidents"] = "C0MISSING"
	_, err := r.Thread(context.Background(), "incidents", ts(14, 0))
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") || strings.Contains(err.Error(), testToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRequiresIDs(t *testing.T) {
	if _, err := New(nil, []string{"payments-oncall", "secret-channel"}, ids, nil, nil); err == nil {
		t.Fatal("New accepted a channel without an ID")
	}
}

func second(_ string, err error) error { return err }
