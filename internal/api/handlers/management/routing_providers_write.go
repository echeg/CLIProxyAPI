package management

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"gopkg.in/yaml.v3"
)

// mergeLegacyClaudeFallbackWrites keeps the providers block of every routing
// profile a write touches. Older panels send claude-fallback and no providers;
// such a write must not wipe the stored providers nor be ignored because
// providers.claude.fallback wins over the legacy block. For each touched profile
// (the shared routing node and every key profile present in the write):
//   - a providers node the write omits is restored from the stored profile;
//   - a claude-fallback node without providers.claude.fallback in the write is
//     converted into providers.claude.fallback: enabled -> target codex, disabled
//     -> target off unless the stored target is api, which is kept; mode and
//     mappings are copied;
//   - the legacy node is dropped once providers.claude.fallback is set.
//
// root is the merged document, before the stored one, and patch the written
// value placed at its config path; patch must not share nodes with root.
func mergeLegacyClaudeFallbackWrites(root, before, patch *yaml.Node) {
	written := configV8Node(patch, []string{"routing"})
	if written == nil || written.Kind != yaml.MappingNode {
		return
	}
	merged := configV8Node(root, []string{"routing"})
	stored := configV8Node(before, []string{"routing"})
	mergeLegacyClaudeFallbackProfile(merged, stored, written)
	writtenProfiles := configV8Node(written, []string{"key-profiles"})
	if writtenProfiles == nil || writtenProfiles.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(writtenProfiles.Content); i += 2 {
		key := []string{"key-profiles", writtenProfiles.Content[i].Value}
		mergeLegacyClaudeFallbackProfile(configV8Node(merged, key), configV8Node(stored, key), writtenProfiles.Content[i+1])
	}
}

func mergeLegacyClaudeFallbackProfile(merged, stored, written *yaml.Node) {
	if merged == nil || merged.Kind != yaml.MappingNode || written == nil || written.Kind != yaml.MappingNode {
		return
	}
	if configV8Node(written, []string{"providers"}) == nil && configV8Node(merged, []string{"providers"}) == nil {
		if previous := configV8Node(stored, []string{"providers"}); previous != nil {
			setConfigV8Child(merged, "providers", cloneConfigV8Node(previous))
		}
	}
	if configV8Node(written, []string{"claude-fallback"}) != nil && configV8Node(written, []string{"providers", "claude", "fallback"}) == nil {
		var legacy config.ClaudeFallbackConfig
		var previous config.RoutingProfile
		if node := configV8Node(merged, []string{"claude-fallback"}); node == nil || node.Decode(&legacy) != nil {
			return
		}
		if stored != nil && stored.Kind == yaml.MappingNode && stored.Decode(&previous) != nil {
			return
		}
		fallback := config.ProviderFallbackFromLegacy(legacy)
		storedTarget := strings.TrimSpace(previous.EffectiveProviders().Claude.Fallback.Target)
		if !legacy.Enabled && strings.EqualFold(storedTarget, config.ProviderFallbackAPI) {
			fallback.Target = config.ProviderFallbackAPI
		}
		var node yaml.Node
		if node.Encode(fallback) != nil {
			return
		}
		providers := ensureConfigV8Mapping(merged, "providers")
		setConfigV8Child(ensureConfigV8Mapping(providers, "claude"), "fallback", &node)
	}
	if configV8Node(merged, []string{"providers", "claude", "fallback"}) != nil {
		deleteConfigV8Path(merged, []string{"claude-fallback"})
	}
}

// ensureConfigV8Mapping returns the mapping stored under key, replacing a missing
// or non-mapping value with an empty mapping.
func ensureConfigV8Mapping(parent *yaml.Node, key string) *yaml.Node {
	if child := configV8Node(parent, []string{key}); child != nil && child.Kind == yaml.MappingNode {
		return child
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setConfigV8Child(parent, key, child)
	return child
}

// setConfigV8Child sets key of a mapping node to value, keeping the key position.
func setConfigV8Child(parent *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			parent.Content[i+1] = value
			return
		}
	}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// configV8PatchRoot places a written value at its config path, so the write can
// be inspected from the document root whatever path it targeted.
func configV8PatchRoot(parts []string, value *yaml.Node) *yaml.Node {
	for i := len(parts) - 1; i >= 0; i-- {
		value = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: parts[i]}, value}}
	}
	return value
}
