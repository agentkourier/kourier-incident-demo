package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
)

// connect serves c's server over Streamable HTTP on a local port and returns a client session.
func connect(t *testing.T, c config) (*mcp.ClientSession, string) {
	t.Helper()
	s, err := newServer(c)
	if err != nil {
		t.Fatalf("newServer: %v", err)
	}
	srv := httptest.NewServer(mcpserve.Handler(s, slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + mcpserve.Path}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs, srv.URL
}

func text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// TestCatalogOverStreamableHTTP runs the catalog server against the repository's catalog/ and
// calls its tools through the SDK client over real HTTP.
func TestCatalogOverStreamableHTTP(t *testing.T) {
	cs, base := connect(t, config{server: "catalog", catalogDir: "../../../catalog"})
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "list_services,lookup_service" {
		t.Errorf("tools = %v", names)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "lookup_service", Arguments: map[string]any{"name": "payments"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("lookup_service error: %s", text(res))
	}
	var got map[string]any
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &got)
	want := map[string]any{
		"service": "payments", "system": "commerce", "team": "payments", "team_display_name": "Payments Team",
		"slack_channel": "#payments-oncall", "oncall_primary": "Dana", "oncall_secondary": "Lee",
		"repo": "agentkourier/demo-payments", "tier": "tier-1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "lookup_service", Arguments: map[string]any{"name": "inventory"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(text(res), "known services: checkout, payments") {
		t.Errorf("unknown service: IsError=%v text=%q", res.IsError, text(res))
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_services", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_services: %v %s", err, text(res))
	}
	if !strings.Contains(text(res), `"name":"checkout"`) {
		t.Errorf("list_services = %s", text(res))
	}

	hr, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(hr.Body)
	_ = hr.Body.Close()
	if hr.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Errorf("healthz = %d %q", hr.StatusCode, body)
	}
}

// TestPagerOverStreamableHTTP checks page_team's advertised schema and one page end to end
// against a fake Slack.
func TestPagerOverStreamableHTTP(t *testing.T) {
	var posted string
	slack := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		posted = r.PostForm.Get("text")
		_, _ = w.Write([]byte(`{"ok":true,"ts":"1730000000.000100"}`))
	}))
	t.Cleanup(slack.Close)
	idsFile := filepath.Join(t.TempDir(), "slack-ids.yaml")
	ids := "teams:\n  payments: {channel_id: C0PAY, oncall_group_id: S0PAY, primary_user_id: U0DANA, secondary_user_id: U0LEE}\n"
	if err := os.WriteFile(idsFile, []byte(ids), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, _ := connect(t, config{
		server: "pager", catalogDir: "../../../catalog", slackIDs: idsFile,
		slackAPI: slack.URL, slackToken: "xoxb-fake-test-token",
	})
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 1 {
		t.Fatalf("ListTools: %v %v", err, tools)
	}
	schema, _ := json.Marshal(tools.Tools[0].InputSchema)
	for _, want := range []string{`"minimum":1`, `"maximum":4`, `"required":["team","severity","summary"]`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("schema %s lacks %s", schema, want)
		}
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "page_team", Arguments: map[string]any{
		"team": "payments", "severity": 2, "summary": "checkout failing <!here>",
	}})
	if err != nil || res.IsError {
		t.Fatalf("page_team: %v %s", err, text(res))
	}
	if !strings.HasSuffix(posted, "checkout failing &lt;!here&gt;") || !strings.HasPrefix(posted, "SEV2 for <!subteam^S0PAY>") {
		t.Errorf("posted %q", posted)
	}

	// Schema validation rejects an out-of-range severity before the handler runs.
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "page_team", Arguments: map[string]any{
		"team": "checkout", "severity": 9, "summary": "x",
	}})
	if err == nil && !res.IsError {
		t.Error("severity 9 was accepted")
	}
}

func TestNewServerRejectsUnknown(t *testing.T) {
	if _, err := newServer(config{server: "shell"}); err == nil {
		t.Fatal("unknown --server accepted")
	}
	if _, err := newServer(config{server: "pager", catalogDir: "../../../catalog", slackIDs: "/nonexistent"}); err == nil {
		t.Fatal("pager started without a slack-ids file")
	}
}
