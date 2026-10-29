// Command catalog-mcp serves the incident demo's software catalog over MCP (Streamable HTTP):
// lookup_service and list_services, read from Backstage-style catalog YAML.
package main

import (
	"context"
	"flag"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentkourier/kourier-incident-demo/mcp/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	catalogDir string
}

func register(s *mcp.Server, c config) error {
	cat, err := catalog.Load(c.catalogDir)
	if err != nil {
		return err
	}
	catalog.Register(s, cat)
	return nil
}

func main() {
	var c config
	fs := flag.NewFlagSet("catalog-mcp", flag.ContinueOnError)
	fs.StringVar(&c.catalogDir, "catalog-dir", "/catalog", "directory of Backstage-style catalog YAML")
	mcpserve.Main(fs, version, func(_ context.Context, s *mcp.Server, _ *slog.Logger) error { return register(s, c) })
}
