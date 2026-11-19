# A write that removes providers.claude.fallback revives an ignored legacy claude-fallback

- found: 2026-11-19, plan: 20261119-config-write-etag-and-legacy-fallback, phase: internal review
- severity: minor
- area: internal/api/handlers/management/routing_providers_write.go

A profile can hold a non-zero `providers.claude.fallback` next to an enabled legacy
`claude-fallback` block. Only a hand edit or an older version leaves that state, because any routing
write that keeps the node drops the legacy block. Load reads the node and ignores the legacy block
with a warning.

A write that replaces the Claude node without a `fallback` removes the node, for example
`PUT /v8/management/config/routing/providers/claude` with `{"mode":"subscription"}`.
`dropsLegacyClaudeFallback` (`routing_providers_write.go:110-115`) then finds no merged node and
returns false, so the legacy block stays. The next load reads the zero node as unset and uses the
legacy block. A probe starting from `providers: {claude: {fallback: {target: off}}}` next to
`claude-fallback: {enabled: true, ...}` reported effective target `off` before the PUT and
`codex` after it. A write that never mentioned the fallback turned Claude -> Codex on.

This predates the branch. On main the delete was also gated on the merged node being present.

Suggested fix: pass the stored profile node into `dropsLegacyClaudeFallback` and also drop the
legacy block when the stored `providers.claude.fallback` was non-zero, because the legacy block was
being ignored. Then add a sub-path PUT case to `TestConfigV8LegacyClaudeFallbackWriteEdgeCases`.
