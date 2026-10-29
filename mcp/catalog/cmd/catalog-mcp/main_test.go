package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcptest"
)

// TestOverStreamableHTTP runs the catalog server against the repository's catalog/ and calls its
// tools through the SDK client over real HTTP.
func TestOverStreamableHTTP(t *testing.T) {
	s := mcptest.NewServer()
	if err := register(s, config{catalogDir: "../../../../catalog"}); err != nil {
		t.Fatal(err)
	}
	cs, base := mcptest.Connect(t, s)
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
		t.Fatalf("lookup_service error: %s", mcptest.Text(res))
	}
	var got map[string]any
	raw, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(raw, &got)
	want := map[string]any{
		"service": "payments", "system": "commerce", "team": "payments", "team_display_name": "Payments Team",
		"slack_channel": "#payments-ops", "oncall_primary": "Dana", "oncall_secondary": "Lee",
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
	if !res.IsError || !strings.Contains(mcptest.Text(res), "known services: checkout, payments") {
		t.Errorf("unknown service: IsError=%v text=%q", res.IsError, mcptest.Text(res))
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_services", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("list_services: %v %s", err, mcptest.Text(res))
	}
	if !strings.Contains(mcptest.Text(res), `"name":"checkout"`) {
		t.Errorf("list_services = %s", mcptest.Text(res))
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

func TestRejectsMissingCatalog(t *testing.T) {
	if err := register(mcptest.NewServer(), config{catalogDir: t.TempDir() + "/none"}); err == nil {
		t.Fatal("started without a catalog")
	}
}
