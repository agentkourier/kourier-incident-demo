// Command slackread-mcp serves the incident demo's Slack reader over MCP (Streamable HTTP):
// read_thread and read_recent, the incident commander's read-only view of the channels it may read.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
	_ "time/tzdata" // the image is distroless static: no zoneinfo on disk

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
	"github.com/agentkourier/kourier-incident-demo/mcp/slackread"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	slackIDs   string
	slackAPI   string
	slackToken string
	channels   string
	timezone   string
}

func register(s *mcp.Server, c config) error {
	ids, err := slackids.Load(c.slackIDs)
	if err != nil {
		return err
	}
	if c.slackToken == "" {
		return fmt.Errorf("%s is not set", slackapi.TokenEnv)
	}
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		return fmt.Errorf("--tz: %w", err)
	}
	r, err := slackread.New(slackapi.New(c.slackAPI, c.slackToken, nil), strings.Split(c.channels, ","), ids.Channels, loc, nil)
	if err != nil {
		return err
	}
	return slackread.Register(s, r)
}

// newFlags returns the command's flag set and the config it parses into.
func newFlags() (*flag.FlagSet, *config) {
	c := &config{}
	fs := flag.NewFlagSet("slackread-mcp", flag.ContinueOnError)
	fs.StringVar(&c.slackIDs, "slack-ids", "/etc/demo-slack-ids/slack-ids.yaml", "workspace Slack IDs file")
	fs.StringVar(&c.slackAPI, "slack-api-url", slackapi.DefaultBaseURL, "Slack Web API base URL")
	fs.StringVar(&c.channels, "channels", "checkout-alerts,payments-ops,incidents", "comma-separated channel names that may be read")
	fs.StringVar(&c.timezone, "tz", "UTC", "IANA time zone for message times")
	return fs, c
}

func main() {
	fs, c := newFlags()
	mcpserve.Main(fs, version, func(_ context.Context, s *mcp.Server, _ *slog.Logger) error {
		c.slackToken = os.Getenv(slackapi.TokenEnv)
		return register(s, *c)
	})
}
