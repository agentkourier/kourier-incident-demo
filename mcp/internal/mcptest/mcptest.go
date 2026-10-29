// Package mcptest connects an MCP client to a server over real Streamable HTTP, for the servers'
// command tests.
package mcptest

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
)

// Connect serves s on a local port with mcpserve's handler and returns a client session and the
// server's base URL. Both are closed when the test ends.
func Connect(t testing.TB, s *mcp.Server) (*mcp.ClientSession, string) {
	t.Helper()
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

// NewServer returns an empty server for a test to register tools on.
func NewServer() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
}

// Text joins the text content of res.
func Text(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
