// Command escalation-mcp serves the incident demo's escalation tools over MCP (Streamable HTTP):
// escalate_incident, which posts to #incidents only after its own Prometheus and Kubernetes
// evidence check passes, and ask_oncall.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes"

	"github.com/agentkourier/kourier-incident-demo/mcp/catalog"
	"github.com/agentkourier/kourier-incident-demo/mcp/escalation"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/kubeclient"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/promapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackids"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	catalogDir       string
	slackIDs         string
	slackAPI         string
	slackToken       string
	namespace        string
	prometheusURL    string
	incidentsChannel string
	askChannels      string
}

// register adds the escalation tools to s. kube is called only once the other inputs check out.
// ctx bounds the recovery watchers.
func register(ctx context.Context, s *mcp.Server, logger *slog.Logger, c config, kube func() (kubernetes.Interface, error)) error {
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
	cs, err := kube()
	if err != nil {
		return err
	}
	e, err := escalation.New(ctx, escalation.Config{
		Namespace:        c.namespace,
		IncidentsChannel: c.incidentsChannel,
		AskChannels:      strings.Split(c.askChannels, ","),
		Logger:           logger,
	}, cat, ids, slackapi.New(c.slackAPI, c.slackToken, nil), promapi.New(c.prometheusURL, nil), cs)
	if err != nil {
		return err
	}
	return escalation.Register(s, e)
}

func main() {
	var c config
	fs := flag.NewFlagSet("escalation-mcp", flag.ContinueOnError)
	fs.StringVar(&c.catalogDir, "catalog-dir", "/catalog", "directory of Backstage-style catalog YAML")
	fs.StringVar(&c.slackIDs, "slack-ids", "/etc/demo-slack-ids/slack-ids.yaml", "workspace Slack IDs file")
	fs.StringVar(&c.slackAPI, "slack-api-url", slackapi.DefaultBaseURL, "Slack Web API base URL")
	fs.StringVar(&c.namespace, "allowed-namespace", "commerce", "the only namespace the evidence check reads")
	fs.StringVar(&c.prometheusURL, "prometheus-url", "http://prometheus-server.observability.svc", "Prometheus base URL")
	fs.StringVar(&c.incidentsChannel, "incidents-channel", "incidents", "channel name escalations and resolutions are posted to")
	fs.StringVar(&c.askChannels, "ask-channels", "checkout-alerts", "comma-separated channel names ask_oncall may reply in")
	mcpserve.Main(fs, version, func(ctx context.Context, s *mcp.Server, logger *slog.Logger) error {
		c.slackToken = os.Getenv(slackapi.TokenEnv)
		return register(ctx, s, logger, c, kubeclient.InCluster)
	})
}
