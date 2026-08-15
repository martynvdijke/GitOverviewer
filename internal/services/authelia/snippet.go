package authelia

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// oneTimePasswordResource matches Authelia's one-time-password endpoints
// (generate/verify), which must remain reachable without authentication.
const oneTimePasswordResource = `^/api/one-time-password/(generate|verify)$`

// accessRule is one entry of access_control.rules.
type accessRule struct {
	Domain    string   `yaml:"domain"`
	Resources []string `yaml:"resources,omitempty"`
	Policy    string   `yaml:"policy"`
}

// accessControl mirrors the access_control section of Authelia's config.yml.
type accessControl struct {
	DefaultPolicy string       `yaml:"default_policy"`
	Rules         []accessRule `yaml:"rules"`
}

// AccessRuleYAML renders the access_control.rules snippet for the given
// domain: a bypass rule for Authelia's one-time-password endpoints (which
// must come first — Authelia applies the first matching rule) and an
// explicit deny rule, with default_policy: deny as a safety net. The snippet
// is a starting block: the user pastes it into their Authelia config.yml
// under access_control and reloads Authelia.
func AccessRuleYAML(domain string) (string, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "", fmt.Errorf("authelia: empty domain")
	}
	// Strip any scheme/path the user may have pasted (e.g. https://app.example.com).
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimSuffix(domain, "/")
	if domain == "" {
		return "", fmt.Errorf("authelia: empty domain")
	}

	ac := accessControl{
		DefaultPolicy: "deny",
		Rules: []accessRule{
			{
				Domain:    domain,
				Resources: []string{oneTimePasswordResource},
				Policy:    "bypass",
			},
			{
				Domain: domain,
				Policy: "deny",
			},
		},
	}

	out, err := yaml.Marshal(ac)
	if err != nil {
		return "", fmt.Errorf("authelia: marshal access rules: %w", err)
	}
	return string(out), nil
}
