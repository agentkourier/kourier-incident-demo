package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcptest"
	"github.com/agentkourier/kourier-incident-demo/mcp/internal/slackapi"
)

// TestFlags parses the Deployment's command line with the real flag set: the command names this
// binary, every arg is a flag this command defines, and the defaults and the Deployment's values are
// the ones expected.
func TestFlags(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Base(wd) // go build names the binary after this directory
	command, args := mcptest.Deployment(t, "../../deploy/deployment.yaml")
	if want := []string{"/" + binary}; !slices.Equal(command, want) {
		t.Errorf("Deployment command = %v, want %v", command, want)
	}
	want := config{
		slackIDs: "/etc/demo-slack-ids/slack-ids.yaml", slackAPI: slackapi.DefaultBaseURL,
		channels: "checkout-alerts,payments-ops,incidents", timezone: "UTC",
	}
	for name, args := range map[string][]string{"defaults": nil, "deployment": args} {
		fs, c := newFlags()
		if fs.Name() != binary {
			t.Errorf("flag set is named %q, want the binary's name %q", fs.Name(), binary)
		}
		mcptest.Parse(t, fs, args)
		if *c != want {
			t.Errorf("%s: config = %+v, want %+v", name, *c, want)
		}
	}
}
