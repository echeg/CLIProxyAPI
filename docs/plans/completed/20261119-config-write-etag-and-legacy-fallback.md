# Config write: ETag of the written bytes, and keep a legacy Claude fallback next to an empty node

## Overview
- Two defects in the management config write path (`/v8/management/config`), both found while the panel's Codex speed table was reviewed. The panel works around them today.
- **ETag**: after a write, `ConfigV8` reads the config file again and answers with the hash of that read. An edit of the file by hand or by another tool between the write and the read makes the answer name the other writer's file, while CPA has loaded the version the handler wrote. A client that chains the tag into its next `If-Match` then passes the check against a file it never saw.
- **Legacy fallback**: a routing write deletes a profile's legacy `claude-fallback` block whenever a `providers.claude.fallback` node exists in the merged profile. The load path reads a zero node (`{}`, `null`, all fields empty) as unset and uses the legacy block. So a write that only changes, say, the Codex speed turns the Claude -> Codex fallback off for a profile that holds an empty node next to an enabled legacy block.
- After the change, the write answers with the hash of the bytes it wrote, and a zero `providers.claude.fallback` node that the write itself does not name no longer deletes the legacy block.

## Decisions
- **Context**: both fixes are described in the panel backlog (`CPA-Manager-Plus/docs/backlog/cpa-patch-etag-names-reread-file.md`, `cpa-empty-claude-fallback-node-drops-legacy-block.md`). The user chose to fix both in one backend plan.
- **Chosen approach, ETag**: split `WriteConfig` into an unexported `writeConfigBytes(path, data) ([]byte, error)` that returns the bytes it wrote, and keep `WriteConfig` as a wrapper with its current signature. `ConfigV8` hashes the returned bytes and no longer reads the file back.
  - `WriteConfig` is re-exported by the SDK (`sdk/api/management.go:75`), so its signature must not change.
- **Chosen approach, legacy fallback**: in `mergeLegacyClaudeFallbackProfile`, delete the legacy node only when the merged `providers.claude.fallback` is not zero, or when the write itself names `providers.claude.fallback` for that profile. A stored zero node that the write does not name leaves the legacy block in place, and the file keeps both nodes as they were.
  - A write that names the node keeps today's behaviour: writing an empty `providers.claude.fallback` on purpose still drops the legacy block and so turns the fallback off.
- **Alternatives not taken**:
  - Convert the legacy block into `providers.claude.fallback` when the node is zero. It rewrites the Claude part of a profile during a write that did not touch it; the chosen rule changes nothing the write did not name.
  - Read the file back and compare its hash with the hash of the written bytes, dropping the `ETag` on a mismatch. The read is not needed once the written bytes are known.
  - A test-only hook between the write and the answer to simulate the race. The fix removes the second read, so nothing is left to race; the tests assert the tag against the written bytes.
- **Verified facts**:
  - `ConfigV8` writes with `WriteConfig` (`internal/api/handlers/management/config_v8.go:212`) and then hashes `os.ReadFile(h.configFilePath)` (`:222-224`).
  - `WriteConfig` normalizes the bytes before writing (`NormalizeConfigLayout`, `NormalizeCommentIndentation`, `internal/api/handlers/management/config_basic.go:101-127`), so the handler's own `data` is not what lands on disk.
  - Other callers of `WriteConfig`: `PutConfigYAML` (`config_basic.go:169`) and the SDK wrapper. Neither sets an `ETag`.
  - The read side hashes the file per request (`config_v8.go:30-37`), so a tag of the written bytes equals the tag the next `GET` returns when nobody else wrote.
  - `effectiveProviders` uses the legacy block when `Claude.Fallback.IsZero()` (`internal/config/provider_routing.go:182`); `ProviderFallbackConfig.IsZero` is `Target == "" && Mode == "" && len(Mappings) == 0` (`:63`).
  - The delete is unconditional on the node's presence (`internal/api/handlers/management/routing_providers_write.go:91-93`). The function runs for the shared profile on every write whose body has `routing`, and for each key profile the body names (`:31-46`). It does not run for `DELETE` or for `config.yaml` writes (`config_v8.go:159-162`).
  - The conversion branch (`:57-90`) always writes a non-zero node (`target: codex` or `off`), so it still drops the legacy block.
  - `docs/management-api-v8.md` does not mention `ETag` or `If-Match` at all.

## Context (from discovery)
- Files/components involved:
  - `internal/api/handlers/management/config_basic.go` (`WriteConfig`)
  - `internal/api/handlers/management/config_v8.go` (`ConfigV8`)
  - `internal/api/handlers/management/routing_providers_write.go` (`mergeLegacyClaudeFallbackProfile`)
  - tests: `config_v8_etag_test.go` (`TestConfigV8ETagIfMatch`), `routing_providers_write_test.go` (`TestConfigV8LegacyClaudeFallbackWriteMergesIntoProviders`, `TestConfigV8LegacyClaudeFallbackWriteEdgeCases`), `routing_codex_speed_test.go`, `routing_provider_disabled_test.go`
  - docs: `docs/management-api-v8.md` (legacy write rules near line 797)
- Related patterns found: handler tests build a `Handler{cfg, configFilePath}` on a temp file, register `ConfigV8` on a gin router and check the file with `config.LoadConfig`.
- Dependencies identified: none outside the package. The panel (`CPA-Manager-Plus`) consumes both behaviours; see Post-Completion.

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- Run `gofmt -w .` after Go changes and `go build -o test-output ./cmd/server && rm test-output` to verify the build (`AGENTS.md`)
- Maintain backward compatibility: `WriteConfig` keeps its signature; a write that names `providers.claude.fallback` behaves as before

## Testing Strategy
- **Unit tests**: handler tests in `internal/api/handlers/management`, run with `go test ./internal/api/handlers/management/`
- **E2E tests**: the repository has none for the management API

## Progress Tracking
- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Update plan if implementation deviates from original scope
- Keep plan in sync with actual work done

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code, tests and docs in this repository
- **Post-Completion** (no checkboxes): deployment and the panel follow-up

## Implementation Steps

### Task 1: Answer a config write with the ETag of the written bytes
- [x] in `internal/api/handlers/management/config_basic.go`, move the body of `WriteConfig` into `writeConfigBytes(path string, data []byte) ([]byte, error)` that returns the normalized bytes it wrote; `WriteConfig` calls it and returns only the error
- [x] in `ConfigV8` (`config_v8.go:212-224`), call `writeConfigBytes`, set `ETag` from `config.ComputeSourceHash` of the returned bytes, and remove the `os.ReadFile` after the write; update the comment above it
- [x] write a test for `writeConfigBytes`: the returned bytes equal the file content for a v8 config whose layout and comment indentation get normalized, and an invalid YAML input returns an error and no bytes
- [x] extend `TestConfigV8ETagIfMatch`: after a `PUT`, a `PATCH` and a `DELETE` the response `ETag` equals the tag of the file, and a following write with that tag in `If-Match` succeeds
- [x] write a test that a write failure (config path is a directory) answers `500 write_failed` without an `ETag` header
  - ⚠️ `ConfigV8` reads the file before writing, so a directory path fails with `read_failed`; the handler test uses a read-only file instead (`TestConfigV8WriteFailureOmitsETag`), and the directory case is covered by `TestWriteConfigBytesWriteFailure`
- [x] run `go test ./internal/api/handlers/management/ ./sdk/api/...` - must pass before task 2

### Task 2: Keep the legacy claude-fallback next to a zero fallback node the write does not name
- [x] in `mergeLegacyClaudeFallbackProfile` (`routing_providers_write.go:91-93`), delete `claude-fallback` only when the merged `providers.claude.fallback` node decodes to a non-zero `config.ProviderFallbackConfig`, or when `written` carries a `providers.claude.fallback` node; a node that fails to decode still deletes, as today
- [x] update the function comment ("the legacy node is dropped once providers.claude.fallback is set") to state the rule
- [x] write tests in `routing_providers_write_test.go` for the shared profile holding `providers.claude.fallback: {}` next to an enabled legacy block: a `PATCH` of `routing.providers.codex.speed` and a `PATCH` of `routing.providers.codex.disabled` keep the legacy block, and `config.LoadConfig` of the file still reports the Claude fallback target `codex` with its mappings
- [x] write the same test for a key profile with that shape, written through `routing.key-profiles.<key>.providers.codex.speed`, and check that a write naming only that key profile also keeps the shared profile's legacy block
- [x] write tests for `providers.claude.fallback: null` and for a node with all fields empty, stored next to the legacy block
- [x] write tests that today's behaviour stays: a write that names `providers.claude.fallback` (empty, and with `target: off`) drops the legacy block; a stored non-zero node drops it on any routing write; a legacy-only write is still converted and dropped
- [x] run `go test ./internal/api/handlers/management/ ./internal/config/` - must pass before task 3

### Task 3: Verify acceptance criteria
- [x] verify both items of the Overview against the code and the new tests
- [x] run `gofmt -l .` (no output) and `go vet ./...`
- [x] run `go build -o test-output ./cmd/server && rm test-output`
- [x] run `go test ./...`
- [x] run `go test -race ./internal/api/handlers/management/`

### Task 4: [Final] Update documentation
- [x] in `docs/management-api-v8.md`, add a short section on `ETag` and `If-Match` for `/v8/management/config`: the tag is a hash of the config file, a write with a stale `If-Match` returns `412`, and a write answers with the tag of the bytes it wrote
- [x] in the legacy write rules of `docs/management-api-v8.md` (near "Writes that still use the legacy `claude-fallback` block"), state that an empty `providers.claude.fallback` the write does not name leaves the legacy block in place
- [x] update `AGENTS.md` only if it describes either behaviour (no change: it describes neither the write ETag nor the write-side legacy rule)

## Technical Details
- `writeConfigBytes` returns the bytes after `NormalizeConfigLayout` and `NormalizeCommentIndentation`, the same slice it passes to `f.Write`.
- ETag flow after the change: `data` (validated tree) -> `writeConfigBytes` -> `written` -> `ETag: "<ComputeSourceHash(written)>"`. If another writer replaces the file right after, the client's next `If-Match` carries the tag of `written`, the read side hashes the other writer's file, and the write fails with `412`.
- Legacy rule after the change, per touched profile:

  | merged `providers.claude.fallback` | write names the node | legacy `claude-fallback` |
  |---|---|---|
  | absent | - | kept (as today) |
  | zero (`{}`, `null`, empty fields) | no | kept (new) |
  | zero | yes | deleted (as today) |
  | non-zero | either | deleted (as today) |

- "The write names the node" means `configV8Node(written, []string{"providers", "claude", "fallback"}) != nil` for that profile's part of the write.

## Post-Completion

**Deployment**: build and install the backend on the local Mac as usual, then check `PATCH /v8/management/config` returns an `ETag` equal to the one of the next `GET`.

**Panel follow-up** (`CPA-Manager-Plus`, separate work):
- `docs/backlog/cpa-patch-etag-names-reread-file.md`: with this fix the panel could take an unconfirmed Default copy back safely again; decide whether to restore that, then close the entry.
- `docs/backlog/cpa-empty-claude-fallback-node-drops-legacy-block.md`: the speed table's `LegacyClaudeFallbackError` refusal is no longer needed against a backend with this fix; older backends still need it, so removing it needs a capability check or a decision to drop older backends. Then close the entry.
