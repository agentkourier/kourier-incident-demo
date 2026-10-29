package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcptest"
)

const repoCatalog = "../../../../catalog"

// TestOverStreamableHTTP checks page_team's advertised schema and one page end to end against a
// fake Slack.
func TestOverStreamableHTTP(t *testing.T) {
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
	s := mcptest.NewServer()
	if err := register(s, config{catalogDir: repoCatalog, slackIDs: idsFile, slackAPI: slack.URL, slackToken: "xoxb-fake-test-token"}); err != nil {
		t.Fatal(err)
	}
	cs, _ := mcptest.Connect(t, s)
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
		t.Fatalf("page_team: %v %s", err, mcptest.Text(res))
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

func TestRejectsMissingInputs(t *testing.T) {
	ok := config{catalogDir: repoCatalog, slackIDs: "../../../../catalog/slack-ids.example.yaml", slackToken: "xoxb-fake-test-token"}
	if err := register(mcptest.NewServer(), ok); err != nil {
		t.Fatalf("good config: %v", err)
	}
	noIDs := ok
	noIDs.slackIDs = "/nonexistent"
	noToken := ok
	noToken.slackToken = ""
	for name, tc := range map[string]struct {
		c    config
		want string
	}{
		"no slack-ids file": {noIDs, "nonexistent"},
		"no token":          {noToken, "SLACK_BOT_TOKEN is not set"},
	} {
		if err := register(mcptest.NewServer(), tc.c); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}
