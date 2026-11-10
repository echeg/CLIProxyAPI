package config

import (
	"fmt"
	"slices"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Provider modes decide which credential kind serves the primary leg of a provider.
const (
	ProviderModeSubscription = "subscription"
	ProviderModeAPI          = "api"
)

// Provider fallback targets. ProviderFallbackCodex is valid only for claude.
const (
	ProviderFallbackOff   = "off"
	ProviderFallbackCodex = "codex"
	ProviderFallbackAPI   = "api"
)

// ProvidersRoutingConfig holds the per-provider mode and fallback of a routing profile.
type ProvidersRoutingConfig struct {
	Claude ProviderRoutingConfig `yaml:"claude,omitempty" json:"claude"`
	Codex  ProviderRoutingConfig `yaml:"codex,omitempty" json:"codex"`
}

// ProviderRoutingConfig is the routing policy of one provider inside a profile.
// APICredential is the auth_index of an api-keys entry of the same provider.
type ProviderRoutingConfig struct {
	Mode          string                 `yaml:"mode,omitempty" json:"mode"`
	APICredential string                 `yaml:"api-credential,omitempty" json:"api-credential"`
	Fallback      ProviderFallbackConfig `yaml:"fallback,omitempty" json:"fallback"`
}

// ProviderFallbackConfig selects what runs after a failed primary round.
// Mode and Mappings apply only to target codex.
type ProviderFallbackConfig struct {
	Target   string                  `yaml:"target,omitempty" json:"target"`
	Mode     string                  `yaml:"mode,omitempty" json:"mode"`
	Mappings []ClaudeFallbackMapping `yaml:"mappings,omitempty" json:"mappings"`
}

// IsZero reports whether no fallback field is set.
func (f ProviderFallbackConfig) IsZero() bool {
	return f.Target == "" && f.Mode == "" && len(f.Mappings) == 0
}

// Clone returns a copy of p that shares no mutable state with it.
func (p ProvidersRoutingConfig) Clone() ProvidersRoutingConfig {
	p.Claude.Fallback.Mappings = slices.Clone(p.Claude.Fallback.Mappings)
	p.Codex.Fallback.Mappings = slices.Clone(p.Codex.Fallback.Mappings)
	return p
}

// ProviderFallbackFromLegacy converts a legacy claude-fallback block:
// enabled maps to target codex, disabled to target off; mode and mappings are kept.
func ProviderFallbackFromLegacy(c ClaudeFallbackConfig) ProviderFallbackConfig {
	target := ProviderFallbackOff
	if c.Enabled {
		target = ProviderFallbackCodex
	}
	return ProviderFallbackConfig{Target: target, Mode: c.Mode, Mappings: slices.Clone(c.Mappings)}
}

// LegacyClaudeFallback returns the legacy claude-fallback view of f.
// Enabled is true only for target codex; mode and mappings are copied.
func (f ProviderFallbackConfig) LegacyClaudeFallback() ClaudeFallbackConfig {
	return ClaudeFallbackConfig{Enabled: f.Target == ProviderFallbackCodex, Mode: f.Mode, Mappings: slices.Clone(f.Mappings)}
}

// MappingTarget returns the Codex model mapped from model: an exact match first,
// then the longest matching family prefix. The caller removes reasoning suffixes.
func (f ProviderFallbackConfig) MappingTarget(model string) string {
	return fallbackMappingTarget(f.Mappings, model)
}

func (p *ProviderRoutingConfig) applyDefaults() {
	if p.Mode == "" {
		p.Mode = ProviderModeSubscription
	}
	if p.Fallback.Target == "" {
		p.Fallback.Target = ProviderFallbackOff
	}
}

// Normalize validates both provider blocks in place. fieldPath is the path of
// the providers block, for example "routing.providers". Warnings describe kept
// but ignored settings; callers log them.
func (p *ProvidersRoutingConfig) Normalize(fieldPath string) ([]string, error) {
	warningsClaude, errClaude := p.Claude.normalize("claude", fieldPath+".claude")
	if errClaude != nil {
		return nil, errClaude
	}
	warningsCodex, errCodex := p.Codex.normalize("codex", fieldPath+".codex")
	if errCodex != nil {
		return nil, errCodex
	}
	return append(warningsClaude, warningsCodex...), nil
}

func (p *ProviderRoutingConfig) normalize(provider, fieldPath string) ([]string, error) {
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	p.APICredential = strings.TrimSpace(p.APICredential)
	p.Fallback.Target = strings.ToLower(strings.TrimSpace(p.Fallback.Target))
	p.Fallback.Mode = strings.ToLower(strings.TrimSpace(p.Fallback.Mode))

	// Empty values stay empty in the stored config so saves remain byte-stable;
	// validation runs on the effective values.
	effective := *p
	effective.applyDefaults()
	switch effective.Mode {
	case ProviderModeSubscription, ProviderModeAPI:
	default:
		return nil, fmt.Errorf("%s.mode has unsupported value %q", fieldPath, effective.Mode)
	}
	target := effective.Fallback.Target
	switch target {
	case ProviderFallbackOff, ProviderFallbackAPI:
	case ProviderFallbackCodex:
		if provider != "claude" {
			return nil, fmt.Errorf("%s.fallback.target codex is only supported for claude", fieldPath)
		}
	default:
		return nil, fmt.Errorf("%s.fallback.target has unsupported value %q", fieldPath, target)
	}
	if p.APICredential == "" && (effective.Mode == ProviderModeAPI || target == ProviderFallbackAPI) {
		return nil, fmt.Errorf("%s.api-credential is required when mode or fallback.target is api", fieldPath)
	}
	if effective.Mode == ProviderModeAPI && target != ProviderFallbackOff {
		return nil, fmt.Errorf("%s.fallback.target must be off when mode is api", fieldPath)
	}
	if target == ProviderFallbackAPI {
		if p.Fallback.Mode != "" || len(p.Fallback.Mappings) > 0 {
			return []string{fieldPath + ".fallback: mode and mappings are ignored when target is api"}, nil
		}
		return nil, nil
	}
	return nil, normalizeFallbackRules(p.Fallback.Mode, p.Fallback.Mappings, target == ProviderFallbackCodex, fieldPath+".fallback")
}

// effectiveProviders returns providers with the legacy claude-fallback block
// converted when providers.claude.fallback is unset, and default mode and
// target filled in. It does not validate.
func effectiveProviders(providers ProvidersRoutingConfig, legacy ClaudeFallbackConfig) ProvidersRoutingConfig {
	out := providers.Clone()
	if out.Claude.Fallback.IsZero() && !legacy.IsZero() {
		out.Claude.Fallback = ProviderFallbackFromLegacy(legacy)
	}
	out.Claude.applyDefaults()
	out.Codex.applyDefaults()
	return out
}

// EffectiveProviders returns the providers policy of the profile: the legacy
// claude-fallback block is converted when providers.claude.fallback is unset, and
// an empty mode or target means subscription and off. Use it wherever the policy
// is read; the stored Providers keeps only what the config file spells out.
func (p RoutingProfile) EffectiveProviders() ProvidersRoutingConfig {
	return effectiveProviders(p.Providers, p.ClaudeFallback)
}

// normalizeProfileProviders validates the providers block and the legacy
// claude-fallback block of one profile. When both define the Claude fallback,
// providers wins: the legacy block is dropped with a warning, so the next save
// writes only providers. legacyPrefix wraps legacy validation errors;
// providersPath prefixes the others.
func normalizeProfileProviders(providers *ProvidersRoutingConfig, legacy *ClaudeFallbackConfig, providersPath, legacyPrefix string) error {
	if !providers.Claude.Fallback.IsZero() && !legacy.IsZero() {
		log.Warnf("%s.claude.fallback is set; ignoring the legacy claude-fallback block", providersPath)
		*legacy = ClaudeFallbackConfig{}
	}
	if errLegacy := legacy.Normalize(); errLegacy != nil {
		if legacyPrefix != "" {
			return fmt.Errorf("%s: %w", legacyPrefix, errLegacy)
		}
		return errLegacy
	}
	// Normalize the stored block in place first: it trims and lower-cases the
	// stored values, mappings included, which the effective copy below clones.
	if _, errProviders := providers.Normalize(providersPath); errProviders != nil {
		return errProviders
	}
	// Validate the effective policy too, so a converted legacy block is checked
	// against the providers block (for example mode api with target codex).
	effective := effectiveProviders(*providers, *legacy)
	warnings, errEffective := effective.Normalize(providersPath)
	if errEffective != nil {
		return errEffective
	}
	for _, warning := range warnings {
		log.Warn(warning)
	}
	return nil
}
