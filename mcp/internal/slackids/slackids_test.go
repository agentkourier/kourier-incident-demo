package slackids

import (
	"os"
	"testing"
)

func TestExampleFileParses(t *testing.T) {
	data, err := os.ReadFile("../../../catalog/slack-ids.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Teams["payments"].SecondaryUserID == "" || cfg.Teams["checkout"].SecondaryUserID != "" {
		t.Errorf("teams = %+v", cfg.Teams)
	}
	if len(cfg.Channels) != 3 {
		t.Errorf("channels = %v", cfg.Channels)
	}
}

func TestParseRejects(t *testing.T) {
	tests := map[string]string{
		"markup in an ID":   "teams: {p: {channel_id: C1, oncall_group_id: S1, primary_user_id: 'U1> <!channel'}}",
		"missing group":     "teams: {p: {channel_id: C1, primary_user_id: U1}}",
		"lower-case ID":     "teams: {p: {channel_id: c1, oncall_group_id: S1, primary_user_id: U1}}",
		"bad secondary":     "teams: {p: {channel_id: C1, oncall_group_id: S1, primary_user_id: U1, secondary_user_id: '<@U2>'}}",
		"bad channel":       "channels: {incidents: '#incidents'}",
		"unknown field":     "teams: {p: {channel: C1}}",
		"unknown top level": "team: {}",
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatalf("Parse(%q) succeeded", doc)
			}
		})
	}
}
