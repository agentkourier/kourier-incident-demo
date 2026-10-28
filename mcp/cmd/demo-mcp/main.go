// Command demo-mcp runs one of the Agent Kourier incident demo's MCP tool servers over Streamable
// HTTP: catalog, pager, rollout or slackread, chosen with --server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the image is distroless static: no zoneinfo on disk

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/pager"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/rollout"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackread"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

// tokenEnv names the environment variable holding the Slack bot token.
const tokenEnv = "SLACK_BOT_TOKEN"

type config struct {
	server             string
	addr               string
	catalogDir         string
	slackIDs           string
	slackAPI           string
	allowedNamespace   string
	allowedDeployments string
	channels           string
	timezone           string
	slackToken         string
}

func parseFlags(args []string, stderr io.Writer) (config, error) {
	var c config
	fs := flag.NewFlagSet("demo-mcp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.server, "server", "", "which server to run: catalog | pager | rollout | slackread")
	fs.StringVar(&c.addr, "addr", ":8084", "listen address")
	fs.StringVar(&c.catalogDir, "catalog-dir", "/catalog", "directory of Backstage-style catalog YAML (catalog, pager)")
	fs.StringVar(&c.slackIDs, "slack-ids", "/etc/demo-slack-ids/slack-ids.yaml", "workspace Slack IDs file (pager, slackread)")
	fs.StringVar(&c.slackAPI, "slack-api-url", slackapi.DefaultBaseURL, "Slack Web API base URL (pager, slackread)")
	fs.StringVar(&c.allowedNamespace, "allowed-namespace", "commerce", "the only namespace the rollout tools touch (rollout)")
	fs.StringVar(&c.allowedDeployments, "allowed-deployments", "payments", "comma-separated deployments that may be rolled back (rollout)")
	fs.StringVar(&c.channels, "channels", "checkout-alerts,payments-ops,incidents", "comma-separated channel names that may be read (slackread)")
	fs.StringVar(&c.timezone, "tz", "UTC", "IANA time zone for message times (slackread)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stderr, version)
		return c, flag.ErrHelp
	}
	c.slackToken = os.Getenv(tokenEnv)
	return c, nil
}

// newServer builds the MCP server the config selects.
func newServer(c config) (*mcp.Server, error) {
	s := mcp.NewServer(&mcp.Implementation{Name: "demo-mcp-" + c.server, Version: version}, nil)
	switch c.server {
	case "catalog":
		cat, err := catalog.Load(c.catalogDir)
		if err != nil {
			return nil, err
		}
		catalog.Register(s, cat)
	case "pager":
		cat, err := catalog.Load(c.catalogDir)
		if err != nil {
			return nil, err
		}
		ids, err := slackids.Load(c.slackIDs)
		if err != nil {
			return nil, err
		}
		if c.slackToken == "" {
			return nil, fmt.Errorf("%s is not set", tokenEnv)
		}
		p := pager.New(cat, ids, slackapi.New(c.slackAPI, c.slackToken, nil), 0, nil)
		if err := pager.Register(s, p); err != nil {
			return nil, err
		}
	case "rollout":
		rc, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf("in-cluster config: %w", err)
		}
		cs, err := kubernetes.NewForConfig(rc)
		if err != nil {
			return nil, fmt.Errorf("kubernetes client: %w", err)
		}
		if err := rollout.Register(s, rollout.New(cs, c.allowedNamespace, strings.Split(c.allowedDeployments, ","))); err != nil {
			return nil, err
		}
	case "slackread":
		ids, err := slackids.Load(c.slackIDs)
		if err != nil {
			return nil, err
		}
		if c.slackToken == "" {
			return nil, fmt.Errorf("%s is not set", tokenEnv)
		}
		loc, err := time.LoadLocation(c.timezone)
		if err != nil {
			return nil, fmt.Errorf("--tz: %w", err)
		}
		r, err := slackread.New(slackapi.New(c.slackAPI, c.slackToken, nil), strings.Split(c.channels, ","), ids.Channels, loc, nil)
		if err != nil {
			return nil, err
		}
		if err := slackread.Register(s, r); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("--server must be catalog, pager, rollout or slackread, got %q", c.server)
	}
	return s, nil
}

func run(ctx context.Context, args []string, stderr io.Writer) error {
	c, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(stderr, nil)).With("server", c.server)
	s, err := newServer(c)
	if err != nil {
		return err
	}
	return mcpserve.Serve(ctx, c.addr, mcpserve.Handler(s, logger), logger)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "demo-mcp:", err)
		os.Exit(1)
	}
}
