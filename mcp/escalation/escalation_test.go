package escalation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/agentkourier/kourier-incident-demo/mcp/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/promapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
)

const testToken = "xoxb-fake-test-token"

var testIDs = &slackids.Config{
	Teams: map[string]slackids.Team{
		"payments": {ChannelID: "C0PAY", OncallGroupID: "S0PAY", PrimaryUserID: "U0DANA", SecondaryUserID: "U0LEE"},
		"checkout": {ChannelID: "C0CHK", PrimaryUserID: "U0PRIYA"},
	},
	Channels: map[string]string{"checkout-alerts": "C0CHK", "payments-ops": "C0PAY", "incidents": "C0INC"},
}

// fakeSlack records chat.postMessage calls and answers chat.getPermalink.
type fakeSlack struct {
	mu    sync.Mutex
	posts []url.Values
	fail  string
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer "+testToken {
		http.Error(w, "bad token", http.StatusBadRequest)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/chat.postMessage":
		if f.fail != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": f.fail})
			return
		}
		f.posts = append(f.posts, r.PostForm)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "ts": fmt.Sprintf("1793200000.%06d", len(f.posts))})
	case "/chat.getPermalink":
		ts := strings.ReplaceAll(r.PostForm.Get("message_ts"), ".", "")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "permalink": "https://kourier.slack.com/archives/" + r.PostForm.Get("channel") + "/p" + ts})
	default:
		http.Error(w, "unexpected method "+r.URL.Path, http.StatusBadRequest)
	}
}

func (f *fakeSlack) all() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.posts...)
}

// fakeProm answers instant and range queries by which service the query selects.
type fakeProm struct {
	mu       sync.Mutex
	impacted float64 // checkout 5xx ratio
	dep      float64 // checkout -> payments failing calls
	suspect  float64 // payments 5xx ratio
	onset    time.Time
	queries  []string
}

func (p *fakeProm) set(f func(*fakeProm)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	f(p)
}

func (p *fakeProm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	q := r.URL.Query().Get("query")
	p.queries = append(p.queries, q)
	val := func(q string) float64 {
		switch {
		case strings.Contains(q, "payments_client_requests_total"):
			return p.dep
		case strings.Contains(q, `app="payments"`):
			return p.suspect
		default:
			return p.impacted
		}
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v1/query":
		ts, _ := strconv.ParseFloat(r.URL.Query().Get("time"), 64)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"resultType": "vector",
			"result":     []any{map[string]any{"metric": map[string]string{}, "value": []any{ts, strconv.FormatFloat(val(q), 'f', -1, 64)}}},
		}})
	case "/api/v1/query_range":
		start, _ := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
		end, _ := strconv.ParseFloat(r.URL.Query().Get("end"), 64)
		var values [][]any
		for t := start; t <= end; t += 15 {
			v := 0.0
			if !p.onset.IsZero() && t >= float64(p.onset.Unix()) {
				v = p.impacted
			}
			values = append(values, []any{t, strconv.FormatFloat(v, 'f', -1, 64)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{
			"resultType": "matrix", "result": []any{map[string]any{"metric": map[string]string{}, "values": values}},
		}})
	default:
		http.Error(w, "unexpected path", http.StatusNotFound)
	}
}

// fakeClock advances by step on every read, so the watcher sees time pass between polls.
type fakeClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.t
	c.t = c.t.Add(c.step)
	return t
}

var t0 = time.Date(2026, 10, 28, 19, 20, 0, 0, time.UTC)

// deployment returns payments with two revisions; the current one was created at rolledOut.
func deployment(rolledOut time.Time) *fake.Clientset {
	d := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "payments", Namespace: "commerce", UID: "d-1",
		Annotations: map[string]string{"deployment.kubernetes.io/revision": "2"},
	}}
	rs := func(name, rev, version string, created time.Time) *appsv1.ReplicaSet {
		return &appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: "commerce", CreationTimestamp: metav1.NewTime(created),
				Annotations:     map[string]string{"deployment.kubernetes.io/revision": rev},
				OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "payments", UID: "d-1"}},
			},
			Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app.kubernetes.io/version": version}},
			}},
		}
	}
	return fake.NewClientset(d, rs("payments-a", "1", "v2.13.0", rolledOut.Add(-24*time.Hour)), rs("payments-b", "2", "v2.14.0", rolledOut))
}

type harness struct {
	e     *Escalator
	slack *fakeSlack
	prom  *fakeProm
	clk   *fakeClock
}

func newHarness(t *testing.T, rolledOut time.Time, onset time.Time) *harness {
	t.Helper()
	cat, err := catalog.Load("../../catalog")
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSlack{}
	ss := httptest.NewServer(fs)
	t.Cleanup(ss.Close)
	fp := &fakeProm{impacted: 0.80, dep: 0.99, suspect: 0, onset: onset}
	ps := httptest.NewServer(fp)
	t.Cleanup(ps.Close)
	clk := &fakeClock{t: t0}
	ctx, cancel := context.WithCancel(context.Background())
	e, err := New(ctx, Config{
		Namespace: "commerce", IncidentsChannel: "incidents", AskChannels: []string{"checkout-alerts"},
		PollEvery: 2 * time.Millisecond, Now: clk.now,
	}, cat, testIDs, slackapi.New(ss.URL, testToken, ss.Client()), promapi.New(ps.URL, ps.Client()), deployment(rolledOut))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); e.Wait() })
	return &harness{e: e, slack: fs, prom: fp, clk: clk}
}

func validInput() EscalateInput {
	return EscalateInput{
		ImpactedService: "checkout", SuspectedService: "payments",
		Summary:         "checkout cannot decode payments v2.14.0 responses",
		AlertThreadLink: "https://kourier.slack.com/archives/C0CHK/p1793215243835489",
	}
}

func TestEscalatePostsWhenEvidencePasses(t *testing.T) {
	rolledOut := t0.Add(-6 * time.Minute)
	h := newHarness(t, rolledOut, rolledOut.Add(90*time.Second))
	h.clk.step = 0
	out, err := h.e.Escalate(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Escalate: %v", err)
	}
	if !out.Passed || !out.Posted || out.Channel != "#incidents" {
		t.Fatalf("out = %+v", out)
	}
	for _, c := range out.Checks {
		if !c.Passed {
			t.Errorf("check %s failed: %s", c.Name, c.Detail)
		}
	}
	posts := h.slack.all()
	if len(posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(posts))
	}
	want := "Escalation · checkout failing (80.0% of requests 5xx)\n" +
		"*Suspected cause:* payments v2.14.0, rolled out 19:14:00 UTC, 1m30s before the errors began (owner: payments)\n" +
		"*Evidence:* checkout → payments calls failing 99.0% · payments 5xx 0.0%\n" +
		"*Agent's finding:* checkout cannot decode payments v2.14.0 responses\n" +
		"<https://kourier.slack.com/archives/C0CHK/p1793215243835489|Alert thread>"
	if got := posts[0].Get("text"); got != want {
		t.Errorf("text =\n%s\nwant\n%s", got, want)
	}
	if posts[0].Get("channel") != "C0INC" || posts[0].Has("thread_ts") || posts[0].Get("parse") != "none" {
		t.Errorf("post form = %v", posts[0])
	}
}

func TestEscalateEscapesModelText(t *testing.T) {
	rolledOut := t0.Add(-6 * time.Minute)
	h := newHarness(t, rolledOut, rolledOut.Add(time.Minute))
	h.clk.step = 0
	in := validInput()
	in.Summary = "<!channel> ping <@U0BOSS> & <https://evil.example|click>"
	in.AlertThreadLink = "https://evil.example/archives/C0CHK/p1793215243835489"
	if _, err := h.e.Escalate(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	text := h.slack.all()[0].Get("text")
	for _, bad := range []string{"<!channel>", "<@U0BOSS>", "<https://", "evil.example/archives"} {
		if strings.Contains(text, bad) {
			t.Errorf("text kept %q:\n%s", bad, text)
		}
	}
	if !strings.Contains(text, "&lt;!channel&gt; ping &lt;@U0BOSS&gt; &amp;") {
		t.Errorf("summary not escaped:\n%s", text)
	}
}

func TestEscalateDryRunPostsNothing(t *testing.T) {
	rolledOut := t0.Add(-6 * time.Minute)
	h := newHarness(t, rolledOut, rolledOut.Add(time.Minute))
	in := validInput()
	in.DryRun = true
	out, err := h.e.Escalate(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Passed || out.Posted || !out.DryRun {
		t.Errorf("out = %+v", out)
	}
	if n := len(h.slack.all()); n != 0 {
		t.Errorf("posts = %d, want 0", n)
	}
	// A dry run does not use up the rate limit.
	in.DryRun = false
	if out, err := h.e.Escalate(context.Background(), in); err != nil || !out.Posted {
		t.Errorf("real escalation after a dry run: %+v, %v", out, err)
	}
}

func TestEscalateRefusesBelowTheBar(t *testing.T) {
	tests := map[string]struct {
		rolledOut, onset time.Time
		mutate           func(*fakeProm)
		failing          string
	}{
		"rollout long before the errors": {t0.Add(-3 * time.Hour), t0.Add(-5 * time.Minute), nil, "recent_rollout"},
		"rollout after the errors":       {t0.Add(-1 * time.Minute), t0.Add(-5 * time.Minute), nil, "recent_rollout"},
		"impacted not failing":           {t0.Add(-6 * time.Minute), t0.Add(-5 * time.Minute), func(p *fakeProm) { p.impacted = 0.01 }, "impacted_failing"},
		"calls to suspect succeed":       {t0.Add(-6 * time.Minute), t0.Add(-5 * time.Minute), func(p *fakeProm) { p.dep = 0.02 }, "dependency_calls_failing"},
		"suspect itself failing":         {t0.Add(-6 * time.Minute), t0.Add(-5 * time.Minute), func(p *fakeProm) { p.suspect = 0.4 }, "suspect_own_errors_low"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, tc.rolledOut, tc.onset)
			h.clk.step = 0
			if tc.mutate != nil {
				h.prom.set(tc.mutate)
			}
			out, err := h.e.Escalate(context.Background(), validInput())
			if err != nil {
				t.Fatal(err)
			}
			if out.Passed || out.Posted || out.Note == "" {
				t.Errorf("out = %+v", out)
			}
			for _, c := range out.Checks {
				if c.Name == tc.failing && c.Passed {
					t.Errorf("%s passed: %s", c.Name, c.Detail)
				}
			}
			if n := len(h.slack.all()); n != 0 {
				t.Errorf("posts = %d, want 0", n)
			}
		})
	}
}

func TestOnsetIgnoresShortDips(t *testing.T) {
	h := newHarness(t, t0.Add(-6*time.Minute), t0.Add(-5*time.Minute))
	ps := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// above from -5m, a 30s dip at -2m, above again: the run began at -5m.
		start, _ := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
		end, _ := strconv.ParseFloat(r.URL.Query().Get("end"), 64)
		var values [][]any
		for ts := start; ts <= end; ts += 15 {
			at := time.Unix(int64(ts), 0)
			v := "0"
			if !at.Before(t0.Add(-5*time.Minute)) && (at.Before(t0.Add(-2*time.Minute)) || !at.Before(t0.Add(-90*time.Second))) {
				v = "0.8"
			}
			values = append(values, []any{ts, v})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "success", "data": map[string]any{"resultType": "matrix", "result": []any{map[string]any{"values": values}}}})
	}))
	t.Cleanup(ps.Close)
	h.e.prom = promapi.New(ps.URL, ps.Client())
	got, err := h.e.onset(context.Background(), "checkout", t0)
	if err != nil {
		t.Fatal(err)
	}
	if want := t0.Add(-5 * time.Minute); !got.Equal(want) {
		t.Errorf("onset = %v, want %v", got, want)
	}
}

func TestEscalateRejectsBadInput(t *testing.T) {
	h := newHarness(t, t0.Add(-6*time.Minute), t0.Add(-5*time.Minute))
	for name, in := range map[string]EscalateInput{
		"unknown impacted": {ImpactedService: "ledger", SuspectedService: "payments", Summary: "x"},
		"same service":     {ImpactedService: "payments", SuspectedService: "payments", Summary: "x"},
		"empty summary":    {ImpactedService: "checkout", SuspectedService: "payments", Summary: " \n "},
	} {
		if _, err := h.e.Escalate(context.Background(), in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(h.slack.all()); n != 0 {
		t.Errorf("posts = %d, want 0", n)
	}
}

func TestEscalateRateLimit(t *testing.T) {
	rolledOut := t0.Add(-6 * time.Minute)
	h := newHarness(t, rolledOut, rolledOut.Add(time.Minute))
	h.clk.step = 0
	if _, err := h.e.Escalate(context.Background(), validInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.e.Escalate(context.Background(), validInput()); err == nil {
		t.Error("second escalation within 30 minutes was allowed")
	}
}

func TestFailedPostDoesNotConsumeSlot(t *testing.T) {
	rolledOut := t0.Add(-6 * time.Minute)
	h := newHarness(t, rolledOut, rolledOut.Add(time.Minute))
	h.clk.step = 0
	h.slack.fail = "channel_not_found"
	if _, err := h.e.Escalate(context.Background(), validInput()); err == nil {
		t.Fatal("failed post reported success")
	}
	h.slack.fail = ""
	if out, err := h.e.Escalate(context.Background(), validInput()); err != nil || !out.Posted {
		t.Errorf("retry after a failed post: %+v, %v", out, err)
	}
}

func TestWatcherPostsResolved(t *testing.T) {
	rolledOut := t0.Add(-6 * time.Minute)
	h := newHarness(t, rolledOut, rolledOut.Add(time.Minute))
	h.clk.step = 0
	out, err := h.e.Escalate(context.Background(), validInput())
	if err != nil || !out.Posted {
		t.Fatalf("Escalate: %+v, %v", out, err)
	}
	// Recovery: the ratio drops and every poll sees 30s pass.
	h.prom.set(func(p *fakeProm) { p.impacted = 0.002 })
	h.clk.mu.Lock()
	h.clk.step = 30 * time.Second
	h.clk.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.slack.all()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	posts := h.slack.all()
	if len(posts) != 2 {
		t.Fatalf("posts = %d, want 2 (escalation, resolved)", len(posts))
	}
	got := posts[1].Get("text")
	want := "Resolved · checkout error rate under 1.0% since 19:20:00 UTC\n<https://kourier.slack.com/archives/C0INC/p1793200000000001|Escalation thread>"
	if got != want {
		t.Errorf("resolved =\n%s\nwant\n%s", got, want)
	}
	if posts[1].Get("channel") != "C0INC" || posts[1].Has("thread_ts") {
		t.Errorf("resolved form = %v", posts[1])
	}
}

func TestWatcherWaitsForTwoMinutesBelow(t *testing.T) {
	rolledOut := t0.Add(-6 * time.Minute)
	h := newHarness(t, rolledOut, rolledOut.Add(time.Minute))
	h.clk.step = 0
	if _, err := h.e.Escalate(context.Background(), validInput()); err != nil {
		t.Fatal(err)
	}
	// Still failing: each poll sees time pass but the ratio stays high, so nothing is posted.
	h.clk.mu.Lock()
	h.clk.step = 30 * time.Second
	h.clk.mu.Unlock()
	time.Sleep(100 * time.Millisecond)
	if n := len(h.slack.all()); n != 1 {
		t.Errorf("posts = %d while still failing, want 1", n)
	}
}

func TestAskOncall(t *testing.T) {
	h := newHarness(t, t0.Add(-6*time.Minute), t0.Add(-5*time.Minute))
	out, err := h.e.AskOncall(context.Background(), AskInput{
		Team: "checkout", Summary: "errors since 19:16; no dependency rolled out; <!here> ruled out payments",
		AlertThreadLink: "https://kourier.slack.com/archives/C0CHK/p1793215243835489",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Posted || out.Channel != "#checkout-alerts" {
		t.Errorf("out = %+v", out)
	}
	p := h.slack.all()[0]
	want := "<@U0PRIYA> the agent in this thread could not confirm the cause and needs a person: errors since 19:16; no dependency rolled out; &lt;!here&gt; ruled out payments"
	if p.Get("text") != want || p.Get("channel") != "C0CHK" || p.Get("thread_ts") != "1793215243.835489" {
		t.Errorf("post = %v\nwant text %q", p, want)
	}
	if _, err := h.e.AskOncall(context.Background(), AskInput{Team: "checkout", Summary: "again", AlertThreadLink: "https://kourier.slack.com/archives/C0CHK/p1793215243835489"}); err == nil {
		t.Error("second question in the same thread within 10 minutes was allowed")
	}
}

func TestAskOncallMentionsGroupWhenSet(t *testing.T) {
	if got := AskMessage(testIDs.Teams["payments"], "x"); !strings.HasPrefix(got, "<!subteam^S0PAY> <@U0DANA> ") {
		t.Errorf("message = %q", got)
	}
}

func TestAskOncallRefuses(t *testing.T) {
	h := newHarness(t, t0.Add(-6*time.Minute), t0.Add(-5*time.Minute))
	for name, in := range map[string]AskInput{
		"channel not allowed": {Team: "checkout", Summary: "x", AlertThreadLink: "https://kourier.slack.com/archives/C0PAY/p1793215243835489"},
		"unknown team":        {Team: "ledger", Summary: "x", AlertThreadLink: "https://kourier.slack.com/archives/C0CHK/p1793215243835489"},
		"not a slack link":    {Team: "checkout", Summary: "x", AlertThreadLink: "https://example.com/archives/C0CHK/p1793215243835489"},
		"empty summary":       {Team: "checkout", Summary: "", AlertThreadLink: "https://kourier.slack.com/archives/C0CHK/p1793215243835489"},
	} {
		if _, err := h.e.AskOncall(context.Background(), in); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if n := len(h.slack.all()); n != 0 {
		t.Errorf("posts = %d, want 0", n)
	}
}

func TestParseThreadLink(t *testing.T) {
	tests := []struct {
		link, channel, ts string
		ok                bool
	}{
		{"https://kourier-sandbox.slack.com/archives/C0CCTMP4JDB/p1793215243835489", "C0CCTMP4JDB", "1793215243.835489", true},
		{"https://kourier-sandbox.slack.com/archives/C0CCTMP4JDB/p1793215300000100?thread_ts=1793215243.835489&cid=C0CCTMP4JDB", "C0CCTMP4JDB", "1793215243.835489", true},
		{"http://kourier-sandbox.slack.com/archives/C0CCTMP4JDB/p1793215243835489", "", "", false},
		{"https://kourier-sandbox.slack.com:8443/archives/C0CCTMP4JDB/p1793215243835489", "", "", false},
		{"https://slack.com.evil.example/archives/C0CCTMP4JDB/p1793215243835489", "", "", false},
		{"https://kourier-sandbox.slack.com/archives/C0CCTMP4JDB/p17932", "", "", false},
		{"https://kourier-sandbox.slack.com/archives/C0CCTMP4JDB/p1793215243835489?thread_ts=x", "", "", false},
	}
	for _, tc := range tests {
		ch, ts, err := ParseThreadLink(tc.link)
		if (err == nil) != tc.ok || ch != tc.channel || ts != tc.ts {
			t.Errorf("ParseThreadLink(%q) = %q, %q, %v", tc.link, ch, ts, err)
		}
	}
}

func TestNewRejectsUnknownChannels(t *testing.T) {
	if _, err := New(context.Background(), Config{Namespace: "commerce", IncidentsChannel: "war-room"}, nil, testIDs, nil, nil, nil); err == nil {
		t.Error("unknown incidents channel accepted")
	}
	if _, err := New(context.Background(), Config{Namespace: "commerce", IncidentsChannel: "incidents", AskChannels: []string{"random"}}, nil, testIDs, nil, nil, nil); err == nil {
		t.Error("unknown ask channel accepted")
	}
	if _, err := New(context.Background(), Config{Namespace: "Bad NS", IncidentsChannel: "incidents"}, nil, testIDs, nil, nil, nil); err == nil {
		t.Error("bad namespace accepted")
	}
}
