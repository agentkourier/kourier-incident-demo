// Package catalog reads a Backstage-style software catalog (Component and Group entities) from a
// directory of YAML files and answers service and team lookups from it.
package catalog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Annotation keys the demo catalog uses.
const (
	AnnotationProjectSlug     = "github.com/project-slug"
	AnnotationTier            = "demo.agentkourier.dev/tier"
	AnnotationSlackChannel    = "demo.agentkourier.dev/slack-channel"
	AnnotationOncallPrimary   = "demo.agentkourier.dev/oncall-primary"
	AnnotationOncallSecondary = "demo.agentkourier.dev/oncall-secondary"
)

// Entity is the subset of a Backstage entity the demo reads.
type Entity struct {
	APIVersion string   `json:"apiVersion"`
	Kind       string   `json:"kind"`
	Metadata   Metadata `json:"metadata"`
	Spec       Spec     `json:"spec"`
}

// Metadata is a Backstage entity's metadata.
type Metadata struct {
	Name        string            `json:"name"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// Spec holds the Component and Group spec fields the demo reads.
type Spec struct {
	Type    string  `json:"type,omitempty"`
	Owner   string  `json:"owner,omitempty"`
	System  string  `json:"system,omitempty"`
	Profile Profile `json:"profile,omitempty"`
}

// Profile is a Group's profile.
type Profile struct {
	DisplayName string `json:"displayName,omitempty"`
}

// Service is what lookup_service returns: names only, never workspace IDs.
type Service struct {
	Service         string `json:"service" jsonschema:"the service (catalog Component) name"`
	System          string `json:"system" jsonschema:"the system the service belongs to"`
	Team            string `json:"team" jsonschema:"the owning team (catalog Group) name"`
	TeamDisplayName string `json:"team_display_name" jsonschema:"the owning team's display name"`
	SlackChannel    string `json:"slack_channel" jsonschema:"the owning team's Slack channel name"`
	OncallPrimary   string `json:"oncall_primary" jsonschema:"the owning team's primary on-call"`
	OncallSecondary string `json:"oncall_secondary,omitempty" jsonschema:"the owning team's secondary on-call, if any"`
	Repo            string `json:"repo" jsonschema:"the GitHub repository, owner/name"`
	Tier            string `json:"tier" jsonschema:"the service tier"`
}

// Team is a catalog Group as the demo uses it.
type Team struct {
	Name            string
	DisplayName     string
	SlackChannel    string
	OncallPrimary   string
	OncallSecondary string
}

// Summary is one entry of list_services.
type Summary struct {
	Name        string `json:"name" jsonschema:"the service name"`
	Description string `json:"description,omitempty" jsonschema:"what the service does"`
	Team        string `json:"team" jsonschema:"the owning team name"`
}

// Catalog is a loaded catalog. It is read-only after Load and safe for concurrent use.
type Catalog struct {
	components map[string]Entity
	groups     map[string]Entity
}

// ErrNotFound is wrapped by lookups of an unknown service or team.
var ErrNotFound = errors.New("not found")

// Load reads every *.yaml and *.yml file in dir. Files may hold several YAML documents; entities
// other than Component and Group are ignored. A name defined twice is an error.
func Load(dir string) (*Catalog, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read catalog dir: %w", err)
	}
	c := &Catalog{components: map[string]Entity{}, groups: map[string]Entity{}}
	for _, e := range entries {
		name := e.Name()
		ext := filepath.Ext(name)
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") || strings.HasSuffix(name, ".example.yaml") ||
			strings.HasPrefix(name, "slack-ids") || strings.HasPrefix(name, "kustomization") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		if err := c.add(name, data); err != nil {
			return nil, err
		}
	}
	if len(c.components) == 0 {
		return nil, fmt.Errorf("catalog dir %s has no Component entities", dir)
	}
	return c, nil
}

func (c *Catalog) add(file string, data []byte) error {
	dec := k8syaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	for {
		var ent Entity
		err := dec.Decode(&ent)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("parse %s: %w", file, err)
		}
		var into map[string]Entity
		switch ent.Kind {
		case "Component":
			into = c.components
		case "Group":
			into = c.groups
		default:
			continue
		}
		key := normalize(ent.Metadata.Name)
		if key == "" {
			return fmt.Errorf("%s: %s without metadata.name", file, ent.Kind)
		}
		if _, dup := into[key]; dup {
			return fmt.Errorf("%s: %s %q defined twice", file, ent.Kind, key)
		}
		into[key] = ent
	}
}

// Service looks a service up by name and joins in its owning team.
func (c *Catalog) Service(name string) (Service, error) {
	comp, ok := c.components[normalize(name)]
	if !ok {
		return Service{}, fmt.Errorf("unknown service %q (known services: %s): %w",
			name, strings.Join(c.ServiceNames(), ", "), ErrNotFound)
	}
	out := Service{
		Service: comp.Metadata.Name,
		System:  comp.Spec.System,
		Repo:    comp.Metadata.Annotations[AnnotationProjectSlug],
		Tier:    comp.Metadata.Annotations[AnnotationTier],
	}
	teamName := groupName(comp.Spec.Owner)
	out.Team = teamName
	if team, err := c.Team(teamName); err == nil {
		out.TeamDisplayName = team.DisplayName
		out.SlackChannel = team.SlackChannel
		out.OncallPrimary = team.OncallPrimary
		out.OncallSecondary = team.OncallSecondary
	}
	return out, nil
}

// Team looks a team (catalog Group) up by name.
func (c *Catalog) Team(name string) (Team, error) {
	g, ok := c.groups[normalize(name)]
	if !ok {
		return Team{}, fmt.Errorf("unknown team %q (known teams: %s): %w",
			name, strings.Join(sortedKeys(c.groups), ", "), ErrNotFound)
	}
	display := g.Spec.Profile.DisplayName
	if display == "" {
		display = g.Metadata.Name
	}
	return Team{
		Name:            g.Metadata.Name,
		DisplayName:     display,
		SlackChannel:    g.Metadata.Annotations[AnnotationSlackChannel],
		OncallPrimary:   g.Metadata.Annotations[AnnotationOncallPrimary],
		OncallSecondary: g.Metadata.Annotations[AnnotationOncallSecondary],
	}, nil
}

// ServiceNames returns the known service names, sorted.
func (c *Catalog) ServiceNames() []string { return sortedKeys(c.components) }

// Services returns a summary of every service, sorted by name.
func (c *Catalog) Services() []Summary {
	out := make([]Summary, 0, len(c.components))
	for _, name := range c.ServiceNames() {
		comp := c.components[name]
		out = append(out, Summary{
			Name:        comp.Metadata.Name,
			Description: comp.Metadata.Description,
			Team:        groupName(comp.Spec.Owner),
		})
	}
	return out
}

// groupName turns an owner entity reference ("group:payments", "group:default/payments" or
// "payments") into the group's name.
func groupName(ref string) string {
	ref = strings.TrimPrefix(normalize(ref), "group:")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}
	return ref
}

func normalize(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func sortedKeys(m map[string]Entity) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
