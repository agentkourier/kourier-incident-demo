// Command pager-mcp serves the incident demo's Pager over MCP (Streamable HTTP): page_team, which
// posts a page to the owning team's Slack channel as the Pager app.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentkourier/kourier-incident-demo/mcp/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
	"github.com/agentkourier/kourier-incident-demo/mcp/pager"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	catalogDir string
	slackIDs   string
	slackAPI   string
	slackToken string
}

func register(s *mcp.Server, c config) error {
	cat, err := catalog.Load(c.catalogDir)
	if err != nil {
		return err
	}
	ids, err := slackids.Load(c.slackIDs)
	if err != nil {
		return err
	}
	if c.slackToken == "" {
		return fmt.Errorf("%s is not set", slackapi.TokenEnv)
	}
	return pager.Register(s, pager.New(cat, ids, slackapi.New(c.slackAPI, c.slackToken, nil), 0, nil))
}

// newFlags returns the command's flag set and the config it parses into.
func newFlags() (*flag.FlagSet, *config) {
	c := &config{}
	fs := flag.NewFlagSet("pager-mcp", flag.ContinueOnError)
	fs.StringVar(&c.catalogDir, "catalog-dir", "/catalog", "directory of Backstage-style catalog YAML")
	fs.StringVar(&c.slackIDs, "slack-ids", "/etc/demo-slack-ids/slack-ids.yaml", "workspace Slack IDs file")
	fs.StringVar(&c.slackAPI, "slack-api-url", slackapi.DefaultBaseURL, "Slack Web API base URL")
	return fs, c
}

func main() {
	fs, c := newFlags()
	mcpserve.Main(fs, version, func(_ context.Context, s *mcp.Server, _ *slog.Logger) error {
		c.slackToken = os.Getenv(slackapi.TokenEnv)
		return register(s, *c)
	})
}
