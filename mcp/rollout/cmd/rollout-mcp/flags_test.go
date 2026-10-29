package main

import (
	"slices"
	"testing"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcptest"
)

// TestFlags parses the Deployment's command line with the real flag set: every arg it passes is a
// flag this command defines, and the defaults and the Deployment's values are the ones expected.
func TestFlags(t *testing.T) {
	command, args := mcptest.Deployment(t, "../../deploy/deployment.yaml")
	if want := []string{"/rollout-mcp"}; !slices.Equal(command, want) {
		t.Errorf("Deployment command = %v, want %v", command, want)
	}
	want := config{namespace: "commerce", deployments: "payments"}
	for name, args := range map[string][]string{"defaults": nil, "deployment": args} {
		fs, c := newFlags()
		mcptest.Parse(t, fs, args)
		if *c != want {
			t.Errorf("%s: config = %+v, want %+v", name, *c, want)
		}
	}
}
