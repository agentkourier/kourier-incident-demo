// Package mcptest helps the servers' command tests: it connects an MCP client over real Streamable
// HTTP, and checks a command's flags against its Deployment manifest.
package mcptest

import (
	"context"
	"errors"
	"flag"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/yaml"

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

// errParsed stops mcpserve.Run once the command line is parsed.
var errParsed = errors.New("parsed")

// Parse runs fs over args through mcpserve.Run, as the command's main does, and stops before the
// server is built. It fails the test when the command line does not parse.
func Parse(t testing.TB, fs *flag.FlagSet, args []string) {
	t.Helper()
	err := mcpserve.Run(context.Background(), fs, args, io.Discard, "test",
		func(context.Context, *mcp.Server, *slog.Logger) error { return errParsed })
	if !errors.Is(err, errParsed) {
		t.Fatalf("parse %v: %v", args, err)
	}
}

// Deployment returns the container command and args of the one-container Deployment manifest at path.
func Deployment(t testing.TB, path string) (command, args []string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d appsv1.Deployment
	if err := yaml.UnmarshalStrict(data, &d); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if n := len(d.Spec.Template.Spec.Containers); n != 1 {
		t.Fatalf("%s: %d containers, want 1", path, n)
	}
	c := d.Spec.Template.Spec.Containers[0]
	return c.Command, c.Args
}
