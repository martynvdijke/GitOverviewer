package authelia

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// parseAccessControl unmarshals the generated snippet into the same struct
// shape used for rendering, proving the YAML is valid and well-formed.
func parseAccessControl(t *testing.T, out string) accessControl {
	t.Helper()
	var ac accessControl
	if err := yaml.Unmarshal([]byte(out), &ac); err != nil {
		t.Fatalf("generated YAML is not parseable: %v\n---\n%s", err, out)
	}
	return ac
}

func TestAccessRuleYAML_ValidAndContainsDomain(t *testing.T) {
	out, err := AccessRuleYAML("app.example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ac := parseAccessControl(t, out)

	if ac.DefaultPolicy != "deny" {
		t.Fatalf("expected default_policy deny, got %q", ac.DefaultPolicy)
	}
	if len(ac.Rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(ac.Rules))
	}

	bypass := ac.Rules[0]
	if bypass.Policy != "bypass" {
		t.Fatalf("expected first rule to be bypass (must precede deny), got %q", bypass.Policy)
	}
	if bypass.Domain != "app.example.com" {
		t.Fatalf("expected domain app.example.com, got %q", bypass.Domain)
	}
	found := false
	for _, r := range bypass.Resources {
		if strings.Contains(r, "one-time-password") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected bypass rule to include one-time-password resource, got %v", bypass.Resources)
	}

	deny := ac.Rules[1]
	if deny.Policy != "deny" {
		t.Fatalf("expected second rule to be deny, got %q", deny.Policy)
	}
	if deny.Domain != "app.example.com" {
		t.Fatalf("expected deny rule domain app.example.com, got %q", deny.Domain)
	}
}

func TestAccessRuleYAML_StripsScheme(t *testing.T) {
	out, err := AccessRuleYAML("https://app.example.com/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ac := parseAccessControl(t, out)
	if ac.Rules[0].Domain != "app.example.com" {
		t.Fatalf("expected stripped domain app.example.com, got %q", ac.Rules[0].Domain)
	}
}

func TestAccessRuleYAML_EmptyDomain(t *testing.T) {
	if _, err := AccessRuleYAML(""); err == nil {
		t.Fatal("expected error for empty domain")
	}
	if _, err := AccessRuleYAML("   "); err == nil {
		t.Fatal("expected error for whitespace-only domain")
	}
}
