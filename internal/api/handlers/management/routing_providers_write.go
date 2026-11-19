package management

import (
	"bytes"
	"errors"
	"io"
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
//   - a claude-fallback node the write sends next to no
//     providers.claude.fallback, or next to one that leaves the merged node
//     zero ({}, null, empty fields), which the load path reads as unset, such as
//     a profile read back and written whole (see convertsLegacyClaudeFallback), is
//     converted into providers.claude.fallback: enabled -> target codex, disabled
//     -> target off unless the stored target is api, which is kept; mode and
//     mappings are copied. Without providers.claude.fallback in the write, only
//     the fields the write carries are read, never a stored legacy block that
//     providers.claude.fallback overrides. A merge applies them over the stored
//     effective fallback, so a partial patch such as {"enabled": false} keeps
//     the stored mode and mappings even after the legacy node was converted,
//     and a null field or block resets that field. A write that names the node
//     converts the merged legacy block, which load would read, rather than the
//     stored fallback it cleared (see convertLegacyClaudeFallback);
//   - a written legacy block with a field it does not know is left in place
//     for validation to reject, whether or not it would be converted;
//   - the legacy node is dropped once providers.claude.fallback is non-zero or
//     the write names it; a stored zero node ({}, null, empty fields) the write
//     does not name leaves the legacy block in place, since the load path reads
//     such a node as unset (see dropsLegacyClaudeFallback).
//
// root is the merged document, before the stored one, and patch the written
// value placed at its config path; patch must not share nodes with root. merge
// is set for a PATCH and for a PUT below a claude-fallback block, which replaces
// one field of it (see writesInsideClaudeFallback).
func mergeLegacyClaudeFallbackWrites(root, before, patch *yaml.Node, merge bool) {
	written := configV8Node(patch, []string{"routing"})
	if written == nil || written.Kind != yaml.MappingNode {
		return
	}
	merged := configV8Node(root, []string{"routing"})
	stored := configV8Node(before, []string{"routing"})
	mergeLegacyClaudeFallbackProfile(merged, stored, written, merge)
	writtenProfiles := configV8Node(written, []string{"key-profiles"})
	if writtenProfiles == nil || writtenProfiles.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(writtenProfiles.Content); i += 2 {
		key := []string{"key-profiles", writtenProfiles.Content[i].Value}
		mergeLegacyClaudeFallbackProfile(configV8Node(merged, key), configV8Node(stored, key), writtenProfiles.Content[i+1], merge)
	}
}

func mergeLegacyClaudeFallbackProfile(merged, stored, written *yaml.Node, merge bool) {
	if merged == nil || merged.Kind != yaml.MappingNode || written == nil || written.Kind != yaml.MappingNode {
		return
	}
	if configV8Node(written, []string{"providers"}) == nil && configV8Node(merged, []string{"providers"}) == nil {
		if previous := configV8Node(stored, []string{"providers"}); previous != nil {
			setConfigV8Child(merged, "providers", cloneConfigV8Node(previous))
		}
	}
	writtenLegacy := configV8Node(written, []string{"claude-fallback"})
	if writtenLegacy != nil {
		// A misspelled legacy field such as {enabeld: false} would be ignored by
		// the conversion, or dropped with the legacy node when a non-zero
		// providers.claude.fallback wins, so the profile is left as merged for
		// ValidateV8Config to reject whether or not the block is converted.
		var submitted config.ClaudeFallbackConfig
		if decodeConfigV8Strict(writtenLegacy, &submitted) != nil {
			return
		}
	}
	if writtenLegacy != nil && convertsLegacyClaudeFallback(merged, written) {
		fallback, ok := convertLegacyClaudeFallback(merged, stored, writtenLegacy, configV8Node(written, []string{"providers", "claude", "fallback"}) != nil, merge)
		if !ok {
			return
		}
		var node yaml.Node
		if node.Encode(fallback) != nil {
			return
		}
		providers := ensureConfigV8Mapping(merged, "providers")
		setConfigV8Child(ensureConfigV8Mapping(providers, "claude"), "fallback", &node)
	}
	if dropsLegacyClaudeFallback(merged, written) {
		deleteConfigV8Path(merged, []string{"claude-fallback"})
	}
}

// convertLegacyClaudeFallback returns the providers.claude.fallback that a
// touched profile's written legacy claude-fallback block converts into, and
// false when a block fails to decode.
//   - A write that names providers.claude.fallback left the merged node unset
//     (see convertsLegacyClaudeFallback), so load would read the merged legacy
//     block: that block is converted as load reads it. The stored fallback is
//     not consulted, since the write just cleared it.
//   - Otherwise only the submitted legacy fields are read: the merged node may
//     still carry a stored legacy block that providers.claude.fallback
//     overrides. A merge applies them as YAML over the stored legacy view, so a
//     null field or block resets to the zero value, as it did before the
//     conversion, and a disabled block keeps a stored api target.
func convertLegacyClaudeFallback(merged, stored, writtenLegacy *yaml.Node, namesNode, merge bool) (config.ProviderFallbackConfig, bool) {
	var legacy config.ClaudeFallbackConfig
	if namesNode {
		source := configV8Node(merged, []string{"claude-fallback"})
		if source == nil {
			source = writtenLegacy
		}
		if decodeConfigV8Strict(source, &legacy) != nil {
			return config.ProviderFallbackConfig{}, false
		}
		return config.ProviderFallbackFromLegacy(legacy), true
	}
	var previous config.RoutingProfile
	if stored != nil && stored.Kind == yaml.MappingNode && stored.Decode(&previous) != nil {
		return config.ProviderFallbackConfig{}, false
	}
	storedFallback := previous.EffectiveProviders().Claude.Fallback
	source := writtenLegacy
	if merge {
		var view yaml.Node
		if view.Encode(storedFallback.LegacyClaudeFallback()) != nil {
			return config.ProviderFallbackConfig{}, false
		}
		mergeConfigV8Patch(&view, writtenLegacy)
		source = &view
	}
	if decodeConfigV8Strict(source, &legacy) != nil {
		return config.ProviderFallbackConfig{}, false
	}
	fallback := config.ProviderFallbackFromLegacy(legacy)
	if !legacy.Enabled && strings.EqualFold(strings.TrimSpace(storedFallback.Target), config.ProviderFallbackAPI) {
		fallback.Target = config.ProviderFallbackAPI
	}
	return fallback, true
}

// dropsLegacyClaudeFallback reports whether a touched profile's legacy
// claude-fallback node must be deleted. The load path reads a zero
// providers.claude.fallback as unset and uses the legacy block, so a stored
// zero node the write does not name keeps it. A non-zero node, a node that
// fails to decode, or a node the write itself names drops it: an empty one
// named without the legacy block turns the fallback off, and one named next to
// the legacy block was replaced by its conversion above.
func dropsLegacyClaudeFallback(merged, written *yaml.Node) bool {
	path := []string{"providers", "claude", "fallback"}
	node := configV8Node(merged, path)
	if node == nil {
		return false
	}
	return configV8Node(written, path) != nil || !unsetClaudeFallback(node)
}

// convertsLegacyClaudeFallback reports whether a touched profile's written
// legacy claude-fallback block is converted into providers.claude.fallback. A
// write without providers.claude.fallback, as older panels send, is always
// converted. A write that names it is converted only when the merged node is
// unset, so load would read the legacy block of the merged profile: a PATCH
// such as {"target": null} over a stored node with a non-empty mode or
// mappings keeps those fields rather than taking the stored target back from
// the legacy view, while over a node with only a target it leaves the merged
// node unset and the merged legacy block is converted.
func convertsLegacyClaudeFallback(merged, written *yaml.Node) bool {
	path := []string{"providers", "claude", "fallback"}
	return configV8Node(written, path) == nil || unsetClaudeFallback(configV8Node(merged, path))
}

// unsetClaudeFallback reports whether a providers.claude.fallback node is
// absent or decodes to a zero fallback ({}, null, all fields empty), which the
// load path reads as unset. A node that fails to decode counts as set. The
// decode is strict, as ValidateV8Config is, so a node with a misspelled field
// such as {targte: off} counts as set and reaches validation rather than being
// replaced by a converted legacy block.
func unsetClaudeFallback(node *yaml.Node) bool {
	if node == nil {
		return true
	}
	var fallback config.ProviderFallbackConfig
	return decodeConfigV8Strict(node, &fallback) == nil && fallback.IsZero()
}

// decodeConfigV8Strict decodes node into out and rejects fields out does not
// know, as ValidateV8Config does. An empty node decodes to the zero value.
func decodeConfigV8Strict(node *yaml.Node, out any) error {
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err = decoder.Decode(out); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// writesInsideClaudeFallback reports whether a write path points below a
// profile's legacy claude-fallback block, such as routing/claude-fallback/mode
// or routing/key-profiles/<key>/claude-fallback/enabled.
func writesInsideClaudeFallback(parts []string) bool {
	n := len(parts)
	if n < 3 || parts[0] != "routing" {
		return false
	}
	return parts[1] == "claude-fallback" || (n >= 5 && parts[1] == "key-profiles" && parts[3] == "claude-fallback")
}

// deleteConvertedClaudeFallback handles a DELETE of a profile's legacy
// claude-fallback path (routing/claude-fallback or
// routing/key-profiles/<key>/claude-fallback). Deleting the legacy block used to
// turn the Claude -> Codex fallback off, and an earlier write may have converted
// it into providers.claude.fallback, so that block is deleted too, unless its
// target is api, which the legacy view cannot express (the enabled: false rule).
// It reports whether it deleted anything.
func deleteConvertedClaudeFallback(root *yaml.Node, parts []string) bool {
	n := len(parts)
	isShared := n == 2 && parts[0] == "routing"
	isKey := n == 4 && parts[0] == "routing" && parts[1] == "key-profiles"
	if (!isShared && !isKey) || parts[n-1] != "claude-fallback" {
		return false
	}
	path := append(append([]string{}, parts[:n-1]...), "providers", "claude", "fallback")
	node := configV8Node(root, path)
	if node == nil {
		return false
	}
	var current config.ProviderFallbackConfig
	if node.Decode(&current) != nil || strings.EqualFold(strings.TrimSpace(current.Target), config.ProviderFallbackAPI) {
		return false
	}
	return deleteConfigV8Path(root, path)
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
