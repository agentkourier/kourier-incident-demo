package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcptest"
)

// TestChecksInputsBeforeKubernetes: a missing input fails startup before the Kubernetes client is
// built, and the Kubernetes error is returned once the inputs check out.
func TestChecksInputsBeforeKubernetes(t *testing.T) {
	errKube := errors.New("no cluster")
	kube := func() (kubernetes.Interface, error) { return nil, errKube }
	ok := config{catalogDir: "../../../../catalog", slackIDs: "../../../../catalog/slack-ids.example.yaml", slackToken: "xoxb-fake-test-token"}
	noToken := ok
	noToken.slackToken = ""
	noIDs := ok
	noIDs.slackIDs = "/nonexistent"
	for name, tc := range map[string]struct {
		c    config
		want string
	}{
		"no token":    {noToken, "SLACK_BOT_TOKEN is not set"},
		"no IDs file": {noIDs, "nonexistent"},
		"no cluster":  {ok, errKube.Error()},
	} {
		err := register(context.Background(), mcptest.NewServer(), slog.New(slog.DiscardHandler), tc.c, kube)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to mention %q", name, err, tc.want)
		}
	}
}
