package config

import (
	"fmt"
	"strings"
)

// ClaudeFallbackConfig is opt-in and applies only to native Claude routing.
// It is the legacy spelling of providers.claude.fallback with target codex.
type ClaudeFallbackConfig struct {
	Enabled  bool                    `yaml:"enabled,omitempty" json:"enabled"`
	Mode     string                  `yaml:"mode,omitempty" json:"mode"`
	Mappings []ClaudeFallbackMapping `yaml:"mappings,omitempty" json:"mappings"`
}

type ClaudeFallbackMapping struct {
	From string `yaml:"from" json:"from"`
	To   string `yaml:"to" json:"to"`
}

func (c *ClaudeFallbackConfig) Normalize() error {
	c.Mode = strings.TrimSpace(c.Mode)
	return normalizeFallbackRules(c.Mode, c.Mappings, c.Enabled, "routing.claude-fallback")
}

// IsZero reports whether the legacy block is absent or empty.
func (c ClaudeFallbackConfig) IsZero() bool {
	return !c.Enabled && c.Mode == "" && len(c.Mappings) == 0
}

// normalizeFallbackRules validates the Claude -> Codex fallback mode and trims
// mappings in place. requireMappings is set when the fallback is active.
func normalizeFallbackRules(mode string, mappings []ClaudeFallbackMapping, requireMappings bool, fieldPath string) error {
	switch mode {
	case "", "auto", "always":
	default:
		return fmt.Errorf("%s.mode must be auto or always", fieldPath)
	}
	seen := make(map[string]bool, len(mappings))
	for i := range mappings {
		m := &mappings[i]
		m.From, m.To = strings.TrimSpace(m.From), strings.TrimSpace(m.To)
		if m.From == "" || m.To == "" || strings.ContainsAny(m.From+m.To, " \t\r\n") || strings.Contains(m.To, "*") || strings.Contains(strings.TrimSuffix(m.From, "*"), "*") {
			return fmt.Errorf("%s.mappings[%d] requires model IDs; only a trailing * is allowed in from", fieldPath, i)
		}
		if seen[m.From] {
			return fmt.Errorf("%s has duplicate source %q", fieldPath, m.From)
		}
		seen[m.From] = true
	}
	if requireMappings && len(mappings) == 0 {
		return fmt.Errorf("%s requires at least one mapping when enabled", fieldPath)
	}
	return nil
}

// Target prefers an exact match, then the longest matching family prefix.
// The caller removes reasoning suffixes before lookup.
func (c ClaudeFallbackConfig) Target(model string) string {
	return fallbackMappingTarget(c.Mappings, model)
}

func fallbackMappingTarget(mappings []ClaudeFallbackMapping, model string) string {
	var target string
	longest := -1
	for _, m := range mappings {
		if m.From == model {
			return m.To
		}
		if prefix, ok := strings.CutSuffix(m.From, "*"); ok && len(prefix) > longest && strings.HasPrefix(model, prefix) {
			target, longest = m.To, len(prefix)
		}
	}
	return target
}
