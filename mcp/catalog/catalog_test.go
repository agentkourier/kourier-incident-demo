package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoCatalog is the catalog shipped at the repository root.
const repoCatalog = "../../catalog"

func TestLoadRepoCatalog(t *testing.T) {
	c, err := Load(repoCatalog)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tests := []struct {
		name string
		want Service
	}{
		{"payments", Service{
			Service: "payments", System: "commerce", Team: "payments", TeamDisplayName: "Payments Team",
			SlackChannel: "#payments-ops", OncallPrimary: "Dana", OncallSecondary: "Lee",
			Repo: "agentkourier/demo-payments", Tier: "tier-1",
		}},
		{" Checkout ", Service{
			Service: "checkout", System: "commerce", Team: "checkout", TeamDisplayName: "Checkout Team",
			SlackChannel: "#checkout-alerts", OncallPrimary: "Priya",
			Repo: "agentkourier/demo-checkout", Tier: "tier-1",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Service(tt.name)
			if err != nil {
				t.Fatalf("Service: %v", err)
			}
			if got != tt.want {
				t.Errorf("Service(%q)\n got %+v\nwant %+v", tt.name, got, tt.want)
			}
		})
	}
}

func TestUnknownServiceListsKnown(t *testing.T) {
	c, err := Load(repoCatalog)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Service("inventory")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "known services: checkout, payments") {
		t.Errorf("error %q does not list the known services", err)
	}
}

func TestLoadRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	doc := "apiVersion: backstage.io/v1alpha1\nkind: Component\nmetadata: {name: a}\nspec: {owner: group:x}\n"
	if err := os.WriteFile(filepath.Join(dir, "c.yaml"), []byte(doc+"---\n"+doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "defined twice") {
		t.Fatalf("Load = %v, want duplicate error", err)
	}
}

func TestGroupName(t *testing.T) {
	for in, want := range map[string]string{
		"group:payments":         "payments",
		"group:default/payments": "payments",
		"Payments":               "payments",
	} {
		if got := groupName(in); got != want {
			t.Errorf("groupName(%q) = %q, want %q", in, got, want)
		}
	}
}
