package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

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

func TestRegistersBothTools(t *testing.T) {
	s := mcptest.NewServer()
	kube := func() (kubernetes.Interface, error) { return fake.NewClientset(), nil }
	c := config{
		catalogDir: "../../../../catalog", slackIDs: "../../../../catalog/slack-ids.example.yaml", slackToken: "xoxb-fake-test-token",
		namespace: "commerce", incidentsChannel: "incidents", askChannels: "checkout-alerts",
	}
	if err := register(t.Context(), s, slog.New(slog.DiscardHandler), c, kube); err != nil {
		t.Fatal(err)
	}
	cs, _ := mcptest.Connect(t, s)
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	if want := []string{"ask_oncall", "escalate_incident"}; !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}
}
