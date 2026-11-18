# Disable Claude or Codex in a routing profile

## Overview
- Every routing profile (the shared `routing.providers` and each `routing.key-profiles.<key>.providers`) gets an explicit on/off switch per provider: `providers.claude.disabled` and `providers.codex.disabled`.
- Goal: a client key through which only Codex can be used. A request that needs a disabled provider is refused with a clear 403 instead of an `auth_not_found` workaround.
- Disabling keeps the provider's other settings (mode, api-credential, fallback, speed), so re-enabling restores them.
- The switch is enforced at the one entry that every request passes (`executeWithProviderFallback`), so fallback legs, pinned requests and the count-tokens path cannot bypass it.
- This is plan 1 of 2. Plan 2 adds the toggle to the CPA-Manager-Plus profile editor.

## Success Criteria
- A key profile with `claude.disabled: true`: `/v1/messages` with a Claude model returns 403 with `claude is disabled by the routing profile of this API key`; `/v1/responses` with a Codex model works.
- `disabled: true` in the shared profile applies to every key without its own profile, with the message `claude is disabled by the routing profile`.
- A disabled Codex blocks the Claude -> Codex fallback leg in both `auto` and `always` modes.
- Streaming, non-streaming and count-tokens requests are refused the same way; a Responses websocket pinned to an auth of a disabled provider is refused or released, never served.
- A model served by both providers runs on the enabled one when the other is disabled.
- Configs without `disabled` behave exactly as before and are written back byte-stable; re-enabling a provider in the shared profile removes the key from the file.
- Home mode and a plugin scheduler are unaffected.
- `go build ./...`, `go vet ./...`, `go test ./...` pass; `go test -race ./sdk/cliproxy/auth/...` passes.

## Decisions
- **Context**: a client key cannot be limited to one provider today. Client keys in `access.api-keys` are plain strings (`internal/config/sdk_config.go:100`); per-key behavior lives only in key profiles, whose provider `mode` is `subscription` or `api` (`internal/config/provider_routing.go:11-15`). The only workaround is `mode: api` with a non-existent `api-credential`, which answers `auth_not_found` with a misleading message.
- **Chosen approach**: a `disabled` boolean on `ProviderRoutingConfig`, enforced by a gate at the start of `executeWithProviderFallback` that removes disabled `claude`/`codex` from the request's provider list and refuses the request with a request-scoped 403 when nothing remains. `providerFallbackFor` builds no Codex fallback when Codex is disabled, and the selection filter also rejects auths of a disabled provider so pinned websocket sessions release them.
- **User direction**: a disabled provider means refused. When Claude is disabled and a Claude -> Codex fallback is configured, a Claude request gets 403; the fallback settings are kept but inactive.
- **Rejected alternatives**: `mode: off` (loses the previous mode and api-credential on every toggle); `enabled: *bool` (a tri-state pointer for a two-state switch); enforcing only in the selection filter `providerPolicyAllows` (skipped for pinned requests and required-kind legs, `sdk/cliproxy/auth/conductor_selection.go:121`); answering `auth_not_found` (triggers fallbacks and reads as a credential problem); a 422 for a Claude -> Codex fallback while Codex is disabled (toggling Codex off would then fail the save of an unrelated Claude setting); enforcing under Home or a plugin scheduler (the key profile is not resolved there, `sdk/cliproxy/auth/routing_profile.go:137-141`).
- **Verified facts**:
  - Normalization: `provider_routing.go:119-174`; effective defaults: `:180-191`; empty values stay empty in the stored config so saves are byte-stable: `:142-143`; kept-but-ignored settings log a warning: `:167-170`.
  - All three public entries `Execute`, `ExecuteCount`, `ExecuteStream` resolve the routing profile and call `executeWithProviderFallback` (`sdk/cliproxy/auth/provider_fallback.go:215-254`); the handlers call only these (`sdk/api/handlers/handlers_execution.go:116`, `handlers_stream.go:427`, `:635`).
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
  - `internal/api/handlers/management` tests for `GET /v8/management/routing/profiles` and `PATCH /v8/management/config`
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

## Implementation Steps

### Task 1: Add `disabled` to the provider routing config
- [ ] add `Disabled bool` with `yaml:"disabled,omitempty" json:"disabled"` to `ProviderRoutingConfig` in `internal/config/provider_routing.go`; document it in the struct comment (valid for both providers; keeps the other fields)
- [ ] keep `normalize` validating every other field of a disabled provider exactly as today; allow both providers disabled
- [ ] in `normalizeProfileProviders`, log a warning (not an error) when the effective Claude fallback target is `codex` while `codex.disabled` is true, with the field path of the Claude fallback
- [ ] extend `dropClearedSharedCodexSpeedFields` in `internal/config/config_yaml.go` (rename to fit, e.g. `dropClearedSharedProviderFields`) so a cleared `disabled` key is removed from `routing.providers.claude` and `routing.providers.codex`; keep its rules: a key holding a zero value is kept, an emptied provider mapping and an emptied `providers` mapping are removed
- [ ] write tests in `provider_routing_test.go`: `disabled` accepted for claude and codex, both disabled valid, a disabled provider with an invalid mode/fallback still fails, the Claude-fallback-into-disabled-Codex case loads with a warning
- [ ] write tests in `provider_routing_save_test.go`: a config without `disabled` saves byte-stable; setting `disabled: true` writes only that key; clearing it in the shared profile removes the key (and the emptied mapping); clearing it in a key profile removes it; a file holding `disabled: false` keeps it
- [ ] run `go test ./internal/config/...` - must pass before task 2

### Task 2: Refuse requests to a disabled provider at the request entry
- [ ] add a method on `*routingProfile` (e.g. `providerDisabled(provider string) bool`) in `sdk/cliproxy/auth/routing_profile.go` that returns false when the profile is nil or `providerPolicy` is false, and otherwise reads `providers.Claude.Disabled` / `providers.Codex.Disabled`
- [ ] add a helper in `sdk/cliproxy/auth/provider_fallback.go` (e.g. `filterDisabledProviders(ctx, providers) ([]string, error)`) that drops disabled `claude`/`codex` (compared trimmed and lower-cased, as `normalizeProviders` does) and leaves other providers in place; when the input was non-empty and the result is empty, return `NewRequestScopedError(msg, http.StatusForbidden)` where msg is `<provider> is disabled by the routing profile of this API key` for a key profile (`fromKey`) and `<provider> is disabled by the routing profile` for the shared one, naming the first disabled provider of the request
- [ ] call it first in `executeWithProviderFallback`, before `providerFallbackFor`, and pass the filtered list on; return the 403 without calling `execute`
- [ ] log one line per refusal at Info with fields `provider` and `profile` (`shared` or the key hash), through `logEntryWithRequestID(ctx)`
- [ ] write tests in `provider_fallback_test.go` (fake executors, as existing tests do): key profile with Claude disabled refuses a Claude request with 403 and the key-profile message for `Execute`, `ExecuteStream` and `ExecuteCount`; the executor is never called; no auth is put into cooldown
- [ ] write tests: shared-profile disable applies to a key without its own profile and uses the shared message; a key profile that does not disable Claude is not affected by a shared disable (key profile replaces shared); a request listing both providers runs on the enabled one; a request for another provider (e.g. gemini) is untouched; the message never contains the key or its hash
- [ ] write tests: Home mode and a plugin scheduler (`providerPolicy` false) do not refuse
- [ ] run `go test ./sdk/cliproxy/auth/...` - must pass before task 3

### Task 3: Keep fallbacks and pinned auths away from a disabled provider
- [ ] in `providerFallbackFor`, build no Claude -> Codex fallback when the profile disables Codex (covers `auto`, `always` and `hasProviderFallbackAfterError`)
- [ ] in `providerPolicyAllows` (`sdk/cliproxy/auth/provider_policy.go`), return false for any auth whose provider the profile disables, before the mode checks; update the function comment
- [ ] check `providerPolicySelectionError` does not replace the 403 path (it only rewrites `auth_not_found`); adjust only if a test shows a disabled provider reaching it
- [ ] write tests in `provider_fallback_test.go`: Claude enabled with fallback `target: codex` in `auto` mode and Codex disabled - a failing Claude primary returns the primary error and the Codex leg never runs; in `always` mode the Claude primary runs and Codex is never called
- [ ] write tests in `provider_policy_test.go`: `providerPolicyAllows` rejects OAuth and API-key auths of a disabled provider in both modes; `RoutingPolicyAllowsAuth` returns false for an auth of a disabled provider and true for the other provider
- [ ] write a test that a request pinned (`pinnedAuthIDFromMetadata`) to an auth of a disabled provider is refused with 403
- [ ] run `go test ./sdk/cliproxy/auth/...` and `go test -race ./sdk/cliproxy/auth/...` - must pass before task 4

### Task 4: Lock the management API behavior for `disabled`
- [ ] verify `GET /v8/management/routing/profiles` returns `disabled` (false by default) in both provider blocks of the shared profile and every key profile; add the field to any hand-built view if it is not derived from the struct
- [ ] write management tests: GET shows `disabled: false` by default and `true` after a write; `PATCH /v8/management/config` with `routing.key-profiles.<key>.providers.claude.disabled: true` succeeds and reads back; PATCH with `disabled: false` on the shared profile removes the key from the file; a GET body written back unchanged is accepted
- [ ] write a test that a PATCH disabling Codex while a Claude -> Codex fallback is configured succeeds (no 422)
- [ ] run `go test ./internal/api/...` - must pass before task 5

### Task 5: Verify acceptance criteria
- [ ] verify every item in Success Criteria is covered by a test; list the test names next to each criterion in Technical Details
- [ ] run `go build ./...`
- [ ] run `go vet ./...` - all issues must be fixed
- [ ] run `go test ./...`
- [ ] run `go test -race ./sdk/cliproxy/auth/...`

### Task 6: [Final] Update documentation
- [ ] `docs/management-api-v8.md`: add `disabled` to the YAML example of "Per-provider mode and API fallback" (~230-252) and to the key-profile default sentence (~256-259)
- [ ] `docs/management-api-v8.md` Validation (~261-285): `disabled` is a boolean for both providers; other fields are still validated; both disabled is allowed; the fallback-into-disabled-Codex warning
- [ ] `docs/management-api-v8.md` Runtime rules (~287+): the 403 and its two messages, the dropped provider for two-provider models, no fallback into a disabled Codex, pinned auths
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
