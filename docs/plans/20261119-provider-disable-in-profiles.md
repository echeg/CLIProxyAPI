# Disable Claude or Codex in a routing profile

## Overview
- Every routing profile (the shared `routing.providers` and each `routing.key-profiles.<key>.providers`) gets an explicit on/off switch per provider: `providers.claude.disabled` and `providers.codex.disabled`.
- Goal: a client key through which only Codex can be used. A request that needs a disabled provider is refused with a clear 403 instead of an `auth_not_found` workaround.
- Disabling keeps the provider's other settings (mode, api-credential, fallback, speed), so re-enabling restores them.
- The switch is enforced at the entry that every execute request passes (`executeWithProviderFallback`), so fallback legs, pinned requests and the count-tokens path cannot bypass it, and in the three public auth-selection methods that Codex search, live and realtime use directly.
- A disable applies to new requests and connections; an already open Codex duplex websocket keeps running until it closes.
- This is plan 1 of 2. Plan 2 adds the toggle to the CPA-Manager-Plus profile editor.

## Success Criteria
- A key profile with `claude.disabled: true`: `/v1/messages` with a Claude model returns 403 whose client-facing `error.message` is exactly `claude is disabled by the routing profile of this API key` (no error-code prefix); `/v1/responses` with a Codex model works.
- A key profile with `codex.disabled: true`: Codex search (`/v1/alpha/search`) and live/realtime (`/v1/live`) are refused with 403.
- `disabled: true` in the shared profile applies to every key without its own profile, with the message `claude is disabled by the routing profile`.
- A disabled Codex blocks the Claude -> Codex fallback leg in both `auto` and `always` modes.
- Streaming, non-streaming and count-tokens requests are refused the same way; a Responses websocket pinned to an auth of a disabled provider is refused or released, never served.
- A model served by both providers runs on the enabled one when the other is disabled.
- Configs without `disabled` behave exactly as before and are written back byte-stable. A management PATCH with `disabled: false` re-enables the provider (the file keeps an explicit `disabled: false`); `DELETE /v8/management/config/routing/providers/<p>/disabled` (or the key-profile path) removes the key; a typed save of the shared profile drops a cleared `disabled` key, as it does for the Codex speed fields.
- Home mode and a plugin scheduler are unaffected.
- `go build ./...`, `go vet ./...`, `go test ./...` pass; `go test -race ./sdk/cliproxy/auth/...` passes.

## Decisions
- **Context**: a client key cannot be limited to one provider today. Client keys in `access.api-keys` are plain strings (`internal/config/sdk_config.go:100`); per-key behavior lives only in key profiles, whose provider `mode` is `subscription` or `api` (`internal/config/provider_routing.go:11-15`). The only workaround is `mode: api` with a non-existent `api-credential`, which answers `auth_not_found` with a misleading message.
- **Chosen approach**: a `disabled` boolean on `ProviderRoutingConfig`, enforced by a gate at the start of `executeWithProviderFallback` that removes disabled `claude`/`codex` from the request's provider list and refuses the request with a request-scoped 403 when nothing remains. `providerFallbackFor` builds no Codex fallback when Codex is disabled, and the selection filter also rejects auths of a disabled provider so pinned websocket sessions release them.
- **User direction**: a disabled provider means refused. When Claude is disabled and a Claude -> Codex fallback is configured, a Claude request gets 403; the fallback settings are kept but inactive.
- **Rejected alternatives**: `mode: off` (loses the previous mode and api-credential on every toggle); `enabled: *bool` (a tri-state pointer for a two-state switch); enforcing only in the selection filter `providerPolicyAllows` (skipped for pinned requests and required-kind legs, `sdk/cliproxy/auth/conductor_selection.go:121`); answering `auth_not_found` (triggers fallbacks and reads as a credential problem); a 422 for a Claude -> Codex fallback while Codex is disabled (toggling Codex off would then fail the save of an unrelated Claude setting); enforcing under Home or a plugin scheduler (the key profile is not resolved there, `sdk/cliproxy/auth/routing_profile.go:137-141`).
- **Verified facts**:
  - Normalization: `provider_routing.go:119-174`; effective defaults: `:180-191`; empty values stay empty in the stored config so saves are byte-stable: `:142-143`; kept-but-ignored settings log a warning: `:167-170`.
  - All three public entries `Execute`, `ExecuteCount`, `ExecuteStream` resolve the routing profile and call `executeWithProviderFallback` (`sdk/cliproxy/auth/provider_fallback.go:215-254`); the generic handlers call these (`sdk/api/handlers/handlers_execution.go:116`, `handlers_stream.go:427`, `:635`), and so do images, compact, speech and videos through the `Execute*WithAuthManager` helpers.
  - Codex search (`internal/api/server_routes.go:418`) and Codex live/realtime (`internal/client/codex/live/live.go:499`) select an auth through `SelectAuthWithCredentialPolicy` / `SelectAuthByKind` (`sdk/cliproxy/auth/conductor_selection.go:2604`, `:2628`, `:2652` with `SelectAuthExcluding`) and never reach `executeWithProviderFallback`; the provider policy is skipped for them because they set a required kind or credential policy (`conductor_selection.go:121`). The gin context carries `userApiKey` there too, so the key profile resolves.
  - `Error.Error()` prefixes the code (`sdk/cliproxy/auth/errors.go:125-132`), so `NewRequestScopedError` alone would render `request_scoped: <message>` to clients that use `err.Error()` (`sdk/api/handlers/claude/code_handlers.go:687`).
  - The shared profile also carries the caller's `keyHash` (`routing_profile.go:151`, `:159`); only `fromKey` tells a key profile from the shared one.
  - `GET /v8/management/routing/profiles` builds providers from the struct through `effectiveProvidersView` (`internal/api/handlers/management/routing_profiles.go:38-46`, `routing_api_credentials.go:60-69`), so a non-`omitempty` JSON tag is enough.
  - A management PATCH merges scalars into the YAML document (`internal/api/handlers/management/config_v8.go:190`, `:387`): a PATCH with `false` or `null` stores that value; a DELETE of the field path removes it (`docs/management-api-v8.md:432-436`).
  - An open Codex duplex websocket forwards follow-up creates and steering frames inside the existing stream (`internal/runtime/executor/codex_websockets_duplex.go:361`, `:666`) without a new selection.
  - The Codex fallback leg calls the inner `execute` directly (`provider_fallback.go:186`), so a gate in the wrapper does not see it; `hasProviderFallbackAfterError` (`provider_fallback.go:117-120`) also reads `providerFallbackFor`.
  - `NewRequestScopedError(message, httpStatus)` exists (`sdk/cliproxy/auth/errors.go:167`); request-scoped errors skip credential cooldown.
  - `shouldFallbackProvider` treats 403 as a fallback trigger (`provider_fallback.go:102`), so the gate must return before the fallback decision.
  - The Responses websocket releases a pinned auth when `RoutingPolicyAllowsAuth` rejects it (`sdk/api/handlers/openai/openai_responses_websocket.go:678-690`), which calls `providerPolicyAllows` (`sdk/cliproxy/auth/provider_policy.go:16-26`, `:34-53`).
  - Saving the shared profile never deletes keys outside pruned subtrees, so a cleared `omitempty` field stays in the file unless removed explicitly, as `dropClearedSharedCodexSpeedFields` does for `speed` and `ignore-client-tier` (`internal/config/config_yaml.go:984-1014`). Key profile subtrees are pruned to the generated keys (`config_yaml.go:975`).
  - `/v1/models` is global, not per key (`sdk/api/handlers/openai/openai_handlers.go:56`).

## Context (from discovery)
- Files/components involved:
  - `internal/config/provider_routing.go` (+ `provider_routing_test.go`, `provider_routing_save_test.go`)
  - `internal/config/config_yaml.go` (shared-profile save cleanup)
  - `sdk/cliproxy/auth/provider_fallback.go` (+ `provider_fallback_test.go`)
  - `sdk/cliproxy/auth/provider_policy.go` (+ `provider_policy_test.go`, `provider_policy_helpers_test.go`)
  - `sdk/cliproxy/auth/routing_profile.go` (+ `routing_profile_test.go`, `routing_profile_edge_test.go`)
  - `sdk/cliproxy/auth/conductor_selection.go` (`SelectAuthExcluding`, `SelectAuthByKind`, `SelectAuthWithCredentialPolicy`)
  - handler tests: `sdk/api/handlers` (messages, streaming, count-tokens), `internal/api/server_codex_search_siwc_test.go`, `internal/client/codex/live/live_test.go`, Responses websocket tests in `sdk/api/handlers/openai`
  - `internal/api/handlers/management` tests for `GET /v8/management/routing/profiles` and `PATCH`/`DELETE /v8/management/config` (new `routing_provider_disabled_test.go`, pattern `routing_codex_speed_test.go`)
  - `docs/management-api-v8.md`
- Related patterns found: Codex speed fields added in `docs/plans/completed/20261118-codex-speed-in-profiles.md` (field + normalize + save cleanup + management lock tests + docs); provider policy errors with client-facing messages in `providerPolicySelectionError` (`provider_policy.go:59-101`).
- Dependencies identified: the panel (plan 2) detects support by the presence of `disabled` in each provider block of `GET /v8/management/routing/profiles`, so the JSON tag must not use `omitempty`.

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- Run tests after each change
- Maintain backward compatibility

## Testing Strategy
- **Unit tests**: required for every task (see Development Approach above)
- **E2E tests**: the project has no UI e2e tests; request-level behavior is covered with Manager-level tests using fake executors, as the existing `provider_fallback_test.go` does.

## Progress Tracking
- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Update plan if implementation deviates from original scope
- Keep plan in sync with actual work done

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code changes, tests, documentation updates in this repository
- **Post-Completion** (no checkboxes): live-instance verification and the panel plan

## Decision Log
- 2026-11-19 brainstorm: **accepted** - config field `providers.<p>.disabled: true` with `yaml:"disabled,omitempty"`, valid for claude and codex; evidence: byte-stable saves `provider_routing.go:142-143`, kept-but-ignored pattern `:167-170`.
- 2026-11-19 brainstorm: **accepted** - gate at the start of `executeWithProviderFallback`, before the fallback decision; evidence: `provider_fallback.go:215-254` (every public entry), `conductor_selection.go:121` (the selection filter is skipped for pinned and required-kind requests).
- 2026-11-19 brainstorm: **accepted** - refusal is `NewRequestScopedError(msg, 403)` with a message naming the provider and whether the shared or the key profile disabled it, never the key or its hash; evidence: `errors.go:167`.
- 2026-11-19 brainstorm: **accepted** - a request whose model is served by both providers drops the disabled one silently and runs on the other.
- 2026-11-19 brainstorm: **accepted** - `providerFallbackFor` builds no Claude -> Codex fallback when Codex is disabled; normalization logs a warning for that combination instead of failing; evidence: `provider_fallback.go:186` (the leg bypasses the wrapper), `provider_routing.go:167-170`.
- 2026-11-19 brainstorm: **accepted** - the other fields of a disabled provider are still validated; both providers disabled is allowed.
- 2026-11-19 brainstorm: **accepted** - Home mode and a plugin scheduler do not enforce `disabled`, like `mode`; evidence: `routing_profile.go:137-141`.
- 2026-11-19 brainstorm: **accepted** - one log line per refusal with fields `provider` and `profile` (`shared` or the key hash); evidence: Logging section `docs/management-api-v8.md:438`.
- 2026-11-19 brainstorm: **accepted** - `/v1/models` stays global; evidence: `openai_handlers.go:56`.
- 2026-11-19 planning: **accepted** - `providerPolicyAllows` also rejects every auth of a disabled provider, so `RoutingPolicyAllowsAuth` releases a pinned websocket auth of a disabled provider; evidence: `openai_responses_websocket.go:678-690`.
- 2026-11-19 planning: **accepted** - the shared-profile save removes a cleared `disabled` key under `routing.providers.claude` and `routing.providers.codex`, extending the existing cleanup for the Codex speed fields; evidence: `config_yaml.go:984-1014`.
- 2026-11-19 user: **rejected** - "Translate Claude requests to Codex when Claude is disabled and a Codex fallback is configured" - the user chose that disabled must mean refused.
- 2026-11-19 grill: **accepted** - Task 3 (new) refuses `SelectAuthExcluding`/`SelectAuthByKind`/`SelectAuthWithCredentialPolicy` for a disabled provider, with handler tests for `/v1/alpha/search` and `/v1/live`; Success Criteria and Verified facts updated - decided by grill: Codex search and live select auths directly and skip `executeWithProviderFallback` (`server_routes.go:418`, `live.go:499`, `conductor_selection.go:121`)
- 2026-11-19 grill: **accepted** - Task 2 requires the client-facing message without the `request_scoped: ` prefix and handler tests for messages, streaming bootstrap and count-tokens - decided by grill: `Error.Error()` prefixes the code (`errors.go:125-132`) and the Claude handler renders `err.Error()` (`code_handlers.go:687`)
- 2026-11-19 grill: **accepted** - Task 2 picks the message and the `profile` log field by `fromKey`, not `keyHash`; test that a key without its own profile logs `profile=shared` - decided by grill: the shared profile also carries the caller's key hash (`routing_profile.go:151`, `:159`)
- 2026-11-19 grill: **accepted** - the flag is read through the existing `providerMode` instead of a new `providerDisabled` method - decided by grill: `providerMode` already handles nil, `providerPolicy` false and other providers (`routing_profile.go:96-107`)
- 2026-11-19 grill: **accepted** - Task 2 also refuses a request pinned to an auth of a disabled provider, and the pinned test moved from Task 4 to Task 2 - decided by grill: pinned requests skip the selection filter (`conductor_selection.go:121`), so only the entry gate covers them
- 2026-11-19 grill: **accepted** - Task 4 adds tests for `hasProviderFallbackAfterError`, rows in `TestRoutingPolicyAllowsAuth`, and a websocket no-alternative turn that gets 403; the open-ended `providerPolicySelectionError` check bullet is removed - decided by grill: `conductor_execution.go:352`, `:472`, `:619`; `provider_policy_test.go:414`; `openai_responses_websocket.go:678-681`; `provider_policy.go:59-69` only sees the filtered list
- 2026-11-19 grill: **accepted** - Task 1 extends the save cleanup with per-provider key lists instead of a rename, adds log assertions via `captureStandardLogs`, and covers the legacy `claude-fallback` form of the warning - decided by grill: `config_yaml.go:992-1014`, `routing_profile_test.go:13`, `provider_routing.go:182-183`
- 2026-11-19 grill: **accepted** - Task 5 drops the hand-built-view check, names `routing_provider_disabled_test.go` modeled on `routing_codex_speed_test.go`, round-trips the GET `providers` block (not the whole GET body), and specifies PATCH `false` keeps `disabled: false` while DELETE removes it; Success Criteria updated - decided by grill: `routing_profiles.go:38-46`, `config_v8.go:93-99`, `:387`, `docs/management-api-v8.md:432-436`
- 2026-11-19 grill: **accepted** - Task 6 replaces test-name bookkeeping with "confirm each criterion maps to a test" - decided by grill: Tasks 1-5 list a test per criterion
- 2026-11-19 grill: **accepted** - a disable applies to new requests and connections; an open Codex duplex websocket runs until it closes (Overview, Success Criteria via docs, Task 7)

## Implementation Steps

### Task 1: Add `disabled` to the provider routing config
- [x] add `Disabled bool` with `yaml:"disabled,omitempty" json:"disabled"` to `ProviderRoutingConfig` in `internal/config/provider_routing.go`; document it in the struct comment (valid for both providers; keeps the other fields)
- [x] keep `normalize` validating every other field of a disabled provider exactly as today; allow both providers disabled
- [x] in `normalizeProfileProviders`, log a warning (not an error) when the effective Claude fallback target is `codex` (including one converted from the legacy `claude-fallback` block) while `codex.disabled` is true, with the field path of the Claude fallback
- [x] extend `dropClearedSharedCodexSpeedFields` in `internal/config/config_yaml.go` with a per-provider key list (`codex`: `speed`, `ignore-client-tier`, `disabled`; `claude`: `disabled`) and run its loop for `routing.providers.claude` too; keep its rules (a key holding a zero value is kept; an emptied provider mapping and an emptied `providers` mapping are removed); rename only if the name becomes misleading and update the doc comment (renamed to `dropClearedSharedProviderFields` with a `sharedProviderClearableKeys` table)
- [x] write tests in `provider_routing_test.go`: `disabled` accepted for claude and codex; both disabled valid; a disabled provider with an invalid mode/fallback still fails; the Claude-fallback-into-disabled-Codex case loads, and `captureStandardLogs` (`internal/config/routing_profile_test.go:13`) shows the warning with `routing.providers.claude.fallback` (and the key-profile path for a key profile); the legacy `claude-fallback.enabled: true` with `codex.disabled: true` also loads with the warning
- [x] write tests in `provider_routing_save_test.go`: a config without `disabled` saves byte-stable; setting `disabled: true` writes only that key; clearing it in the shared profile removes the key (and the emptied mapping) for claude and for codex; clearing it in a key profile removes it; a file holding `disabled: false` keeps it
- [x] run `go test ./internal/config/...` - must pass before task 2

### Task 2: Refuse execute requests to a disabled provider at the request entry
- [ ] read the flag through the existing `profile.providerMode(p)` (`sdk/cliproxy/auth/routing_profile.go:96-107`: `policy, ok := profile.providerMode(p); ok && policy.Disabled`), which already returns false for a nil profile, `providerPolicy` false and other providers; add no new accessor
- [ ] add the refusal error: a request-scoped 403 (no credential cooldown) whose client-facing message is exactly `<provider> is disabled by the routing profile of this API key` when `profile.fromKey`, else `<provider> is disabled by the routing profile`; `Error()` must return the message without the `request_scoped: ` prefix that `Error.Error()` adds (`sdk/cliproxy/auth/errors.go:125-132`), e.g. a small error type with `StatusCode() 403` that is recognized as request-scoped, or the existing `Error` with whatever field keeps the prefix off; never include the key or its hash
- [ ] add a helper in `sdk/cliproxy/auth/provider_fallback.go` (e.g. `filterDisabledProviders(ctx, providers) ([]string, error)`) that drops disabled `claude`/`codex` (compared trimmed and lower-cased, as `normalizeProviders` does) and leaves other providers in place; when the input was non-empty and the result is empty, return the refusal naming the first disabled provider of the request; also refuse when the request is pinned (`pinnedAuthIDFromMetadata(opts.Metadata)`) to an auth whose provider is disabled
- [ ] call it first in `executeWithProviderFallback`, before `providerFallbackFor`, and pass the filtered list on; return the refusal without calling `execute`
- [ ] log one Info line per refusal through `logEntryWithRequestID(ctx)` with fields `provider` and `profile`, where `profile` is `shared` when `!profile.fromKey` and `profile.keyHash` otherwise
- [ ] write tests in `provider_fallback_test.go` (fake executors, as existing tests do): key profile with Claude disabled refuses a Claude request for `Execute`, `ExecuteStream` and `ExecuteCount`; status 403 and `err.Error()` equals the key-profile message exactly; the executor is never called; no auth is put into cooldown
- [ ] write tests: shared-profile disable applies to a key without its own profile, uses the shared message and logs `profile=shared`; a key profile that does not disable Claude is not affected by a shared disable (key profile replaces shared); a request listing both providers runs on the enabled one and the disabled provider's executor is never called; a request listing `claude, codex` with both disabled returns 403 naming `claude`; a request for another provider (e.g. gemini) is untouched; a request pinned to an auth of a disabled provider is refused with 403
- [ ] write tests with `newStandardLogHook` (`sdk/cliproxy/auth/routing_profile_test.go:216`): exactly one Info entry per refusal with `provider` and `profile` (`shared` or the key hash) and no raw key in any field
- [ ] write tests: Home mode and a plugin scheduler (`providerPolicy` false) do not refuse
- [ ] write handler tests that the client receives HTTP 403 with exactly the message in `error.message` for `/v1/messages` (non-streaming), a streaming request (bootstrap error), and count-tokens
- [ ] run `go test ./sdk/cliproxy/auth/... ./sdk/api/...` - must pass before task 3

### Task 3: Refuse direct auth selection for a disabled provider
- [ ] in `SelectAuthExcluding`, `SelectAuthByKind` and `SelectAuthWithCredentialPolicy` (`sdk/cliproxy/auth/conductor_selection.go:2604`, `:2628`, `:2652`), resolve the profile with `ensureRoutingProfile` and return the Task 2 refusal (same message and log line) before picking when the requested provider is disabled; Home and plugin schedulers are unchanged because `providerMode` reports no policy there
- [ ] write Manager-level tests: each of the three methods refuses `codex` under a key profile and under the shared profile with the exact message, and still selects when Codex is enabled
- [ ] write handler tests: `/v1/alpha/search` (see `internal/api/server_codex_search_siwc_test.go`) and `/v1/live` (see `internal/client/codex/live/live_test.go`) return 403 for a key whose profile disables Codex
- [ ] run `go test ./sdk/cliproxy/auth/... ./internal/api/... ./internal/client/codex/...` - must pass before task 4

### Task 4: Keep fallbacks and pinned websocket auths away from a disabled provider
- [ ] in `providerFallbackFor`, build no Claude -> Codex fallback when the profile disables Codex (covers `auto`, `always` and `hasProviderFallbackAfterError`)
- [ ] in `providerPolicyAllows` (`sdk/cliproxy/auth/provider_policy.go`), return false for any auth whose provider the profile disables, before the mode checks, reading the flag through `providerMode`; update the function comment
- [ ] write tests in `provider_fallback_test.go`: Claude enabled with fallback `target: codex` in `auto` mode and Codex disabled - a failing Claude primary returns the primary error and the Codex leg never runs; in `always` mode the Claude primary runs and Codex is never called; `hasProviderFallbackAfterError` returns false for a 429 and a 503 with Codex disabled and true with Codex enabled
- [ ] add rows to `TestRoutingPolicyAllowsAuth` (`sdk/cliproxy/auth/provider_policy_test.go:414`): an OAuth and an API-key auth of a disabled provider -> false in both modes; the other provider -> true; `providerPolicy` false -> true
- [ ] write a Responses websocket test: a session pinned to a Codex auth, Codex then disabled in the key profile and no other target able to serve the turn - the next turn gets the 403 and never reaches upstream
- [ ] run `go test ./sdk/cliproxy/auth/... ./sdk/api/...` and `go test -race ./sdk/cliproxy/auth/...` - must pass before task 5

### Task 5: Lock the management API behavior for `disabled`
- [ ] no view change is needed: `disabled` reaches `GET /v8/management/routing/profiles` through `effectiveProvidersView` (`internal/api/handlers/management/routing_profiles.go:38-46`); the GET tests below cover it
- [ ] create `internal/api/handlers/management/routing_provider_disabled_test.go`, modeled on `routing_codex_speed_test.go`: GET shows `disabled: false` by default in both provider blocks of the shared profile and of a key profile, and `true` after a write; `PATCH /v8/management/config` with `routing.key-profiles.<key>.providers.claude.disabled: true` succeeds and reads back; PATCH with `disabled: false` re-enables (the file keeps `disabled: false`); `DELETE /v8/management/config/routing/providers/claude/disabled` and the key-profile path remove the key; sibling provider settings survive each write
- [ ] write a test that a PATCH disabling Codex while a Claude -> Codex fallback is configured succeeds (no 422)
- [ ] extend `TestConfigV8RoutingProfileGetBodyPatchesBack` (`routing_codex_speed_test.go:171`) so the `providers` block taken from the GET view, with `disabled` set, is PATCHed back under `routing.providers` and under `routing.key-profiles.<key>.providers`, and the effective settings and unrelated profiles stay unchanged
- [ ] run `go test ./internal/api/...` - must pass before task 6

### Task 6: Verify acceptance criteria
- [ ] confirm each Success Criterion maps to a test from Tasks 1-5; add any missing one
- [ ] run `go build ./...`
- [ ] run `go vet ./...` - all issues must be fixed
- [ ] run `go test ./...`
- [ ] run `go test -race ./sdk/cliproxy/auth/...`

### Task 7: [Final] Update documentation
- [ ] `docs/management-api-v8.md`: add `disabled` to the YAML example of "Per-provider mode and API fallback" (~230-252) and to the key-profile default sentence (~256-259)
- [ ] `docs/management-api-v8.md` Validation (~261-285): `disabled` is a boolean for both providers; other fields are still validated; both disabled is allowed; the fallback-into-disabled-Codex warning
- [ ] `docs/management-api-v8.md` Runtime rules (~287+): the 403 and its two messages; the dropped provider for two-provider models; no fallback into a disabled Codex; pinned requests; Codex search, live and realtime; a disable applies to new requests and connections while an open Codex duplex websocket runs until it closes
- [ ] `docs/management-api-v8.md` writing rules (~424-436): PATCH `false` keeps `disabled: false`, DELETE of the field path removes it
- [ ] `docs/management-api-v8.md` Logging (~438) and "Home mode, plugins, and logging" (~747-756): the refusal log line; not enforced under Home and plugin schedulers
- [ ] mention that `/v1/models` still lists every model

## Technical Details

### Field
```yaml
routing:
  providers:
    claude:
      disabled: true        # default false; both providers; other fields are kept
  key-profiles:
    "client-key-a":
      providers:
        claude: {disabled: true}
        codex:  {speed: fast}
```
JSON (`GET /v8/management/routing/profiles`): `"disabled": false` is always present in each provider block, because the panel uses its presence to detect support.

### Request flow
1. `Execute` / `ExecuteCount` / `ExecuteStream` resolve the routing profile (`ensureRoutingProfile`).
2. `executeWithProviderFallback` filters the provider list. Nothing left -> 403 request-scoped error, logged, returned before the fallback decision.
3. `providerFallbackFor` sees the filtered list and skips a Codex fallback when Codex is disabled.
4. Selection: `providerPolicyAllows` rejects auths of a disabled provider (defense in depth; drives websocket pin release).
5. Direct selection (`SelectAuthExcluding`, `SelectAuthByKind`, `SelectAuthWithCredentialPolicy`, used by Codex search and live/realtime) refuses a disabled provider before picking.

### Messages
- key profile: `claude is disabled by the routing profile of this API key`
- shared profile: `claude is disabled by the routing profile`
- the same with `codex`.

### Non-goals
- Filtering `/v1/models` per key.
- Per-model restrictions.
- Enforcement under Home mode or a plugin scheduler.
- Panel changes (plan 2: `CPA-Manager-Plus`, toggle in the profile editor).

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- After merge and deploy to the local `local.clipproxyapi` service: create a key profile with `claude.disabled: true` through `PATCH /v8/management/config`; `/v1/messages` with that key returns 403 with the key-profile message; `/v1/responses` with a Codex model returns 200; remove the flag and confirm Claude works again and the key is gone from `config.yaml`.

**External system updates**:
- Plan 2 in `CPA-Manager-Plus`: `ProviderRouting.disabled`, support detection by the presence of `disabled`, a toggle in `ProviderRoutingBlock`, summary and speed-table handling.
