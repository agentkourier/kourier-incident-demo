package main

import (
	"testing"

	"github.com/agentkourier/kourier-incident-demo/mcp/internal/mcptest"
)

func TestRejectsBadInputs(t *testing.T) {
	ok := config{
		slackIDs: "../../../../catalog/slack-ids.example.yaml", slackToken: "xoxb-fake-test-token",
		channels: "checkout-alerts,payments-ops,incidents", timezone: "UTC",
	}
	if err := register(mcptest.NewServer(), ok); err != nil {
		t.Fatalf("good config: %v", err)
	}
	noToken := ok
	noToken.slackToken = ""
	badTZ := ok
	badTZ.timezone = "Mars/Olympus"
	for name, c := range map[string]config{"no token": noToken, "bad tz": badTZ} {
		if err := register(mcptest.NewServer(), c); err == nil {
			t.Errorf("%s: started", name)
		}
	}
}
