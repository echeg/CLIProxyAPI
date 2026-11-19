# A write that removes providers.claude.fallback revives an ignored legacy claude-fallback

- found: 2026-11-19, plan: 20261119-config-write-etag-and-legacy-fallback, phase: internal review
- severity: minor
- area: internal/api/handlers/management/routing_providers_write.go

A profile can hold a non-zero `providers.claude.fallback` next to an enabled legacy
`claude-fallback` block. Only a hand edit or an older version leaves that state, because any routing
write that keeps the node drops the legacy block. Load reads the node and ignores the legacy block
with a warning.

Two write paths can remove or empty that node and leave the legacy block in place. The next load
then reads the zero node as unset and uses the legacy block, so a write that never mentioned the
legacy fallback turns Claude -> Codex on. Probes started from
`providers: {claude: {fallback: {target: off}}}` next to `claude-fallback: {enabled: true, ...}` and
reported effective target `off` before the write and `codex` after it:

- PUT/PATCH: a write that replaces the Claude node without a `fallback`, for example
  `PUT /v8/management/config/routing/providers/claude` with `{"mode":"subscription"}`.
  `dropsLegacyClaudeFallback` in `routing_providers_write.go` finds no merged node and returns
  false, so the legacy block stays.
- DELETE: `DELETE /v8/management/config/routing/providers/claude/fallback` or
  `.../claude/fallback/target`. DELETE never reaches `mergeLegacyClaudeFallbackWrites`, because
  `ConfigV8` skips it for DELETE (the `c.Request.Method != http.MethodDelete` guard in
  `config_v8.go`). The only legacy handling on DELETE is `deleteConvertedClaudeFallback`, which
  covers a delete of the legacy block itself, not of the modern node.

This predates the branch. On main the delete was also gated on the merged node being present, and
DELETE was already skipped.

Suggested fix:
- PUT/PATCH: pass the stored profile node into `dropsLegacyClaudeFallback` and also drop the legacy
  block when the stored `providers.claude.fallback` was non-zero, because the legacy block was being
  ignored.
- DELETE: next to `deleteConvertedClaudeFallback` in `ConfigV8`, when the path is at or below a
  profile's `providers/claude` and the stored `providers.claude.fallback` was non-zero, delete that
  profile's legacy `claude-fallback` too. This applies to the shared profile and to key profiles.
- Add a sub-path PUT case and both DELETE cases to `TestConfigV8LegacyClaudeFallbackWriteEdgeCases`.
