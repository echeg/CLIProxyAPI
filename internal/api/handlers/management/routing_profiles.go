package management

import (
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

type routingKeyProfileView struct {
	Key     string                `json:"key"`
	Profile config.RoutingProfile `json:"profile"`
}

// GetRoutingProfiles returns the shared profile, every key profile, and the slot
// values a selection accepts. Writes go through the v8 config PATCH.
func (h *Handler) GetRoutingProfiles(c *gin.Context) {
	h.mu.Lock()
	manager := h.authManager
	cfg := h.cfg
	if cfg == nil {
		cfg = &config.Config{}
	}
	shared := cfg.Routing.SharedProfile()
	keys := make([]routingKeyProfileView, 0, len(cfg.Routing.KeyProfiles))
	seen := make(map[string]bool, len(cfg.APIKeys))
	// Follow access.api-keys order so the listing is stable across requests.
	for _, key := range cfg.APIKeys {
		profile := cfg.Routing.KeyProfiles[key]
		if profile == nil || seen[key] {
			continue
		}
		seen[key] = true
		view := routingKeyProfileView{Key: key, Profile: profile.Clone()}
		view.Profile.Selection = profile.EffectiveSelection()
		if view.Profile.ClaudeFallback.Mappings == nil {
			view.Profile.ClaudeFallback.Mappings = []config.ClaudeFallbackMapping{}
		}
		keys = append(keys, view)
	}
	h.mu.Unlock()
	if shared.ClaudeFallback.Mappings == nil {
		shared.ClaudeFallback.Mappings = []config.ClaudeFallbackMapping{}
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{
		"supported": manager.ClaudeFallbackSupported(),
		"shared":    shared,
		"keys":      keys,
		"selection_options": gin.H{
			"narrowing": config.SelectionNarrowingSlots,
			"pickers":   config.SelectionPickerSlots,
		},
	})
}

// reconcileKeyProfiles keeps routing.key-profiles in step with an api-keys list
// change. Profiles of removed keys are dropped; when exactly one key was removed
// and exactly one added, the change is a rotation and the profile moves to the
// new key. A pure append moves nothing.
func reconcileKeyProfiles(before, after []string, profiles map[string]*config.RoutingProfile) {
	if len(profiles) == 0 {
		return
	}
	afterSet := make(map[string]bool, len(after))
	for _, key := range after {
		afterSet[key] = true
	}
	beforeSet := make(map[string]bool, len(before))
	var removed []string
	for _, key := range before {
		if beforeSet[key] {
			continue
		}
		beforeSet[key] = true
		if !afterSet[key] {
			removed = append(removed, key)
		}
	}
	var added []string
	addedSet := make(map[string]bool)
	for _, key := range after {
		if !beforeSet[key] && !addedSet[key] {
			addedSet[key] = true
			added = append(added, key)
		}
	}
	if len(removed) == 1 && len(added) == 1 {
		if profile, ok := profiles[removed[0]]; ok {
			profiles[added[0]] = profile
		}
	}
	for _, key := range removed {
		delete(profiles, key)
	}
}

// countUnknownKeyProfiles counts routing.key-profiles entries that carry a profile
// for a key absent from access.api-keys. Entries unchanged from the previous
// document are tolerated: load-time normalization already ignores them and the
// saver prunes them, so they must not block unrelated edits. Null entries delete.
func countUnknownKeyProfiles(root, before *yaml.Node) int {
	profiles := configV8Node(root, []string{"routing", "key-profiles"})
	if profiles == nil || profiles.Kind != yaml.MappingNode {
		return 0
	}
	known := knownAPIKeys(root)
	previous := configV8Node(before, []string{"routing", "key-profiles"})
	count := 0
	for i := 0; i+1 < len(profiles.Content); i += 2 {
		key, value := profiles.Content[i].Value, profiles.Content[i+1]
		if known[key] || value == nil || value.Tag == "!!null" {
			continue
		}
		if old := configV8Node(previous, []string{key}); old != nil && sameYAMLValue(old, value) {
			continue
		}
		count++
	}
	return count
}

// pruneSavedKeyProfiles removes routing.key-profiles entries that are null (a null
// in a write deletes the profile) or whose key is absent from access.api-keys, and
// the key-profiles node itself when it ends up empty.
func pruneSavedKeyProfiles(root *yaml.Node) {
	profiles := configV8Node(root, []string{"routing", "key-profiles"})
	if profiles == nil || profiles.Kind != yaml.MappingNode {
		return
	}
	known := knownAPIKeys(root)
	kept := profiles.Content[:0]
	for i := 0; i+1 < len(profiles.Content); i += 2 {
		value := profiles.Content[i+1]
		if value == nil || value.Tag == "!!null" {
			continue
		}
		if known[profiles.Content[i].Value] {
			kept = append(kept, profiles.Content[i], profiles.Content[i+1])
		}
	}
	profiles.Content = kept
	if len(profiles.Content) == 0 {
		deleteConfigV8Path(root, []string{"routing", "key-profiles"})
	}
}

// knownAPIKeys returns the access.api-keys entries of a v8 document as a set.
func knownAPIKeys(root *yaml.Node) map[string]bool {
	known := make(map[string]bool)
	if keys := configV8Node(root, []string{"access", "api-keys"}); keys != nil && keys.Kind == yaml.SequenceNode {
		for _, key := range keys.Content {
			known[key.Value] = true
		}
	}
	return known
}

func sameYAMLValue(a, b *yaml.Node) bool {
	var left, right any
	if a.Decode(&left) != nil || b.Decode(&right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}
