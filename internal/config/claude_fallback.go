package config

import (
	"fmt"
	"strings"
)

// ClaudeFallbackConfig is opt-in and applies only to native Claude routing.
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
	switch c.Mode {
	case "", "auto", "always":
	default:
		return fmt.Errorf("routing.claude-fallback.mode must be auto or always")
	}
	seen := make(map[string]bool, len(c.Mappings))
	for i := range c.Mappings {
		m := &c.Mappings[i]
		m.From, m.To = strings.TrimSpace(m.From), strings.TrimSpace(m.To)
		if m.From == "" || m.To == "" || strings.ContainsAny(m.From+m.To, " \t\r\n") || strings.Contains(m.To, "*") || strings.Contains(strings.TrimSuffix(m.From, "*"), "*") {
			return fmt.Errorf("routing.claude-fallback.mappings[%d] requires model IDs; only a trailing * is allowed in from", i)
		}
		if seen[m.From] {
			return fmt.Errorf("routing.claude-fallback has duplicate source %q", m.From)
		}
		seen[m.From] = true
	}
	if c.Enabled && len(c.Mappings) == 0 {
		return fmt.Errorf("routing.claude-fallback requires at least one mapping when enabled")
	}
	return nil
}

// Target prefers an exact match, then the longest matching family prefix.
// The caller removes reasoning suffixes before lookup.
func (c ClaudeFallbackConfig) Target(model string) string {
	var target string
	longest := -1
	for _, m := range c.Mappings {
		if m.From == model {
			return m.To
		}
		if prefix, ok := strings.CutSuffix(m.From, "*"); ok && len(prefix) > longest && strings.HasPrefix(model, prefix) {
			target, longest = m.To, len(prefix)
		}
	}
	return target
}
