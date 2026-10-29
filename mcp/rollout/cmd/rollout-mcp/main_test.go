package main

import (
	"context"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcptest"
)

func TestRegistersBothTools(t *testing.T) {
	s := mcptest.NewServer()
	kube := func() (kubernetes.Interface, error) { return fake.NewClientset(), nil }
	if err := register(s, config{namespace: "commerce", deployments: "payments"}, kube); err != nil {
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
	if strings.Join(names, ",") != "rollback_deployment,rollout_history" {
		t.Errorf("tools = %v", names)
	}
}
