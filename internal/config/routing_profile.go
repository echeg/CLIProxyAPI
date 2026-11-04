package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
)

// Selection slot values. Narrowing criteria keep the best group of candidates;
// pickers choose exactly one credential.
const (
	SelectionPriority           = "priority"
	SelectionReserves           = "reserves"
	SelectionEarliestReset      = "earliest-reset"
	SelectionMostRemaining      = "most-remaining"
	SelectionRoundRobin         = "round-robin"
	SelectionWeightedRoundRobin = "weighted-round-robin"
	SelectionFillFirst          = "fill-first"
)

// SelectionNarrowingSlots lists the narrowing criteria in display order.
var SelectionNarrowingSlots = []string{SelectionPriority, SelectionReserves, SelectionEarliestReset, SelectionMostRemaining}

// SelectionPickerSlots lists the pickers in display order.
var SelectionPickerSlots = []string{SelectionRoundRobin, SelectionWeightedRoundRobin, SelectionFillFirst}

// SelectionConfig is the explicit pool strategy: a primary slot and an optional tiebreak.
type SelectionConfig struct {
	Primary  string `yaml:"primary,omitempty" json:"primary"`
	Tiebreak string `yaml:"tiebreak,omitempty" json:"tiebreak"`
}

// RoutingProfile is the full routing configuration of one client API key.
// A key profile replaces the shared profile entirely; no field is inherited.
type RoutingProfile struct {
	SessionAffinity   bool                 `yaml:"session-affinity,omitempty" json:"session-affinity"`
	PreferredAccounts map[string]string    `yaml:"preferred-accounts,omitempty" json:"preferred-accounts,omitempty"`
	Selection         SelectionConfig      `yaml:"selection,omitempty" json:"selection"`
	ClaudeFallback    ClaudeFallbackConfig `yaml:"claude-fallback,omitempty" json:"claude-fallback"`
}

// IsPickerSlot reports whether slot chooses a single credential instead of narrowing.
func IsPickerSlot(slot string) bool {
	switch slot {
	case SelectionRoundRobin, SelectionWeightedRoundRobin, SelectionFillFirst:
		return true
	}
	return false
}

func isSelectionSlot(slot string) bool {
	switch slot {
	case SelectionPriority, SelectionReserves, SelectionEarliestReset, SelectionMostRemaining:
		return true
	}
	return IsPickerSlot(slot)
}

// Normalize trims and lower-cases both slots and rejects unknown values.
// fieldPath prefixes error messages, for example "routing.selection".
func (s *SelectionConfig) Normalize(fieldPath string) error {
	s.Primary = strings.ToLower(strings.TrimSpace(s.Primary))
	s.Tiebreak = strings.ToLower(strings.TrimSpace(s.Tiebreak))
	if s.Primary != "" && !isSelectionSlot(s.Primary) {
		return fmt.Errorf("%s.primary has unsupported value %q", fieldPath, s.Primary)
	}
	if s.Tiebreak != "" && !isSelectionSlot(s.Tiebreak) {
		return fmt.Errorf("%s.tiebreak has unsupported value %q", fieldPath, s.Tiebreak)
	}
	if s.Primary == "" && s.Tiebreak != "" {
		return fmt.Errorf("%s.primary is required when tiebreak is set", fieldPath)
	}
	return nil
}

// DefaultSelection is used when no selection is configured and no legacy strategy applies.
func DefaultSelection() SelectionConfig {
	return SelectionConfig{Primary: SelectionReserves, Tiebreak: SelectionRoundRobin}
}

// normalizeLegacyStrategy maps routing.strategy and its aliases to a selection slot.
func normalizeLegacyStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		return SelectionWeightedRoundRobin
	case "fill-first", "fillfirst", "ff":
		return SelectionFillFirst
	case "earliest-reset":
		return SelectionEarliestReset
	default:
		return SelectionRoundRobin
	}
}

// EffectiveSelection returns routing.selection when set, otherwise the selection
// derived from the legacy strategy: {reserves, <strategy>}.
func (r RoutingConfig) EffectiveSelection() SelectionConfig {
	if r.Selection.Primary != "" {
		return r.Selection
	}
	return SelectionConfig{Primary: SelectionReserves, Tiebreak: normalizeLegacyStrategy(r.Strategy)}
}

// EffectiveSelection returns the profile selection, or DefaultSelection when the
// profile leaves it empty. The shared selection is never inherited.
func (p RoutingProfile) EffectiveSelection() SelectionConfig {
	if p.Selection.Primary != "" {
		return p.Selection
	}
	return DefaultSelection()
}

// KeyHashPrefix returns the first 12 hex characters of the sha256 of key.
// Use it wherever a client API key has to be identified in logs or errors.
func KeyHashPrefix(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:12]
}

// normalizePreferredAccountsMap validates and copies subscription preferences.
// Only codex and claude are supported; empty values are dropped.
func normalizePreferredAccountsMap(accounts map[string]string, fieldPath string) (map[string]string, error) {
	if len(accounts) == 0 {
		return nil, nil
	}
	normalized := make(map[string]string, len(accounts))
	for provider, index := range accounts {
		if provider != "codex" && provider != "claude" {
			return nil, fmt.Errorf("%s only supports codex and claude", fieldPath)
		}
		if index = strings.TrimSpace(index); index != "" {
			normalized[provider] = index
		}
	}
	return normalized, nil
}

// Normalize validates routing.selection and every key profile. It is lenient about
// key-profile membership: null entries and entries for keys absent from apiKeys are
// dropped with a warning that identifies the key only by its hash prefix.
func (r *RoutingConfig) Normalize(apiKeys []string) error {
	if errSelection := r.Selection.Normalize("routing.selection"); errSelection != nil {
		return errSelection
	}
	if len(r.KeyProfiles) == 0 {
		r.KeyProfiles = nil
		return nil
	}
	known := make(map[string]struct{}, len(apiKeys))
	for _, key := range apiKeys {
		known[key] = struct{}{}
	}
	for key, profile := range r.KeyProfiles {
		if profile == nil {
			delete(r.KeyProfiles, key)
			continue
		}
		hash := KeyHashPrefix(key)
		if _, ok := known[key]; !ok {
			log.WithField("routing_key", hash).Warn("routing.key-profiles: dropping profile for a key missing from access.api-keys")
			delete(r.KeyProfiles, key)
			continue
		}
		path := "routing.key-profiles." + hash
		if errSelection := profile.Selection.Normalize(path + ".selection"); errSelection != nil {
			return errSelection
		}
		if errFallback := profile.ClaudeFallback.Normalize(); errFallback != nil {
			return fmt.Errorf("%s: %w", path, errFallback)
		}
		preferred, errPreferred := normalizePreferredAccountsMap(profile.PreferredAccounts, path+".preferred-accounts")
		if errPreferred != nil {
			return errPreferred
		}
		profile.PreferredAccounts = preferred
	}
	if len(r.KeyProfiles) == 0 {
		r.KeyProfiles = nil
	}
	return nil
}

// SharedProfile builds the profile used by keys without their own profile from
// the global routing fields. The returned value shares no mutable state with r.
func (r RoutingConfig) SharedProfile() RoutingProfile {
	profile := RoutingProfile{
		SessionAffinity: r.SessionAffinity,
		Selection:       r.EffectiveSelection(),
		ClaudeFallback:  r.ClaudeFallback,
	}
	if len(r.PreferredAccounts) > 0 {
		profile.PreferredAccounts = make(map[string]string, len(r.PreferredAccounts))
		for provider, index := range r.PreferredAccounts {
			profile.PreferredAccounts[provider] = index
		}
	}
	if r.ClaudeFallback.Mappings != nil {
		profile.ClaudeFallback.Mappings = append([]ClaudeFallbackMapping(nil), r.ClaudeFallback.Mappings...)
	}
	return profile
}
