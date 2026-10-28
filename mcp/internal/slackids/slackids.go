// Package slackids loads the workspace-specific Slack IDs the demo servers need. The file is kept
// out of the public repository and mounted from a Secret; nothing in it is ever taken from a model.
package slackids

import (
	"fmt"
	"os"
	"regexp"
	"sort"

	"sigs.k8s.io/yaml"
)

// Team holds the Slack IDs used to page one team.
type Team struct {
	// ChannelID is the channel pages are posted to.
	ChannelID string `json:"channel_id"`
	// OncallGroupID is the user group mentioned in the page.
	OncallGroupID string `json:"oncall_group_id"`
	// PrimaryUserID is the primary on-call person.
	PrimaryUserID string `json:"primary_user_id"`
	// SecondaryUserID is the secondary on-call person; empty when the team has none.
	SecondaryUserID string `json:"secondary_user_id,omitempty"`
}

// Config is the whole slack-ids file.
type Config struct {
	// Teams maps a team name (a catalog Group) to its IDs.
	Teams map[string]Team `json:"teams"`
	// Channels maps a channel name, without '#', to its ID.
	Channels map[string]string `json:"channels"`
}

// idPattern matches a Slack-issued ID. Checking it keeps a malformed or tampered file from putting
// anything but an ID inside the mention markup the pager builds.
var idPattern = regexp.MustCompile(`^[A-Z0-9]{2,32}$`)

// Load reads and validates a slack-ids file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read slack ids: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates slack-ids YAML.
func Parse(data []byte) (*Config, error) {
	var cfg Config
	if err := yaml.UnmarshalStrict(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse slack ids: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("validate slack ids: %w", err)
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	for _, name := range sortedKeys(c.Teams) {
		t := c.Teams[name]
		required := map[string]string{
			"channel_id":      t.ChannelID,
			"oncall_group_id": t.OncallGroupID,
			"primary_user_id": t.PrimaryUserID,
		}
		for _, field := range sortedKeys(required) {
			if !idPattern.MatchString(required[field]) {
				return fmt.Errorf("team %q: %s %q is not a Slack ID", name, field, required[field])
			}
		}
		if t.SecondaryUserID != "" && !idPattern.MatchString(t.SecondaryUserID) {
			return fmt.Errorf("team %q: secondary_user_id %q is not a Slack ID", name, t.SecondaryUserID)
		}
	}
	for _, name := range sortedKeys(c.Channels) {
		if !idPattern.MatchString(c.Channels[name]) {
			return fmt.Errorf("channel %q: %q is not a Slack ID", name, c.Channels[name])
		}
	}
	return nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
