// Command rollout-mcp serves the incident demo's rollout tools over MCP (Streamable HTTP):
// rollout_history, and rollback_deployment for the deployments it is allowed to roll back.
package main

import (
	"context"
	"flag"
	"log/slog"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/kubeclient"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcpserve"
	"github.com/agentkourier/kourier-incident-demo/mcp/rollout"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type config struct {
	namespace   string
	deployments string
}

func register(s *mcp.Server, c config, kube func() (kubernetes.Interface, error)) error {
	cs, err := kube()
	if err != nil {
		return err
	}
	return rollout.Register(s, rollout.New(cs, c.namespace, strings.Split(c.deployments, ",")))
}

// newFlags returns the command's flag set and the config it parses into.
func newFlags() (*flag.FlagSet, *config) {
	c := &config{}
	fs := flag.NewFlagSet("rollout-mcp", flag.ContinueOnError)
	fs.StringVar(&c.namespace, "allowed-namespace", "commerce", "the only namespace the tools read")
	fs.StringVar(&c.deployments, "allowed-deployments", "payments", "comma-separated deployments that may be rolled back")
	return fs, c
}

func main() {
	fs, c := newFlags()
	mcpserve.Main(fs, version, func(_ context.Context, s *mcp.Server, _ *slog.Logger) error {
		return register(s, *c, kubeclient.InCluster)
	})
}
