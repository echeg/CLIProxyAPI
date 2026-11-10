# Per-provider subscription/API mode and API-key fallback in routing profiles

## Overview
- A routing profile (the shared `routing.*` profile and every `routing.key-profiles.<key>`) gets a `providers` block with a `claude` and a `codex` sub-block. Each sub-block sets a **mode** (`subscription` or `api`), an **API credential** (the `auth_index` of an `api-keys.claude` / `api-keys.codex` entry), and a **fallback target** (`off`, `codex` for Claude only, or `api`).
- Problems it solves:
  1. Upstream API keys of Claude and Codex sit in the same pool as OAuth subscriptions today, so a paid API key can be selected whenever the pipeline picks it. With this change an API key is used only through an explicit `mode: api` or an explicit `fallback.target: api`.
  2. A client key can name its own upstream API key as fallback, so API spend is attributable per client key (usage events already carry `api_key_hash` and `auth_index`).
  3. A client key can skip subscriptions entirely (`mode: api`).
  4. Codex gets the same controls as Claude (mode, API credential, fallback to its API key).
- Integration: the existing per-key `routingProfile` is resolved once per execution and carried in the context; the new policy is one more field on it. The Claude -> Codex fallback wrapper is generalized to a per-provider wrapper. Native standalone routing only; Home mode and scheduler plugins keep today's behavior.

## Decisions
- **Context**: `api-keys.claude` / `api-keys.codex` entries become `Auth` values with `Provider` `claude`/`codex` and `AuthKind() == apikey` (`internal/watcher/synthesizer/config.go:185`, `sdk/cliproxy/auth/classification.go`). The selector is always `PipelineSelector` (`sdk/cliproxy/service_config.go:69`) and takes candidates from every priority tier (`availableAuthsForPipeline`, `sdk/cliproxy/auth/conductor_selection.go:702`), so nothing keeps API keys out of the primary pool. The only fallback is Claude -> Codex (`sdk/cliproxy/auth/claude_fallback.go`), hard-wired to provider `codex` with `withRequiredAuthKind(ctx, AuthKindOAuth)`.
- **Chosen approach — config schema**:
  ```yaml
  routing:
    providers:
      claude:
        mode: subscription             # subscription (default) | api
        api-credential: "<auth_index>" # required when mode: api or fallback.target: api
        fallback:
          target: codex                # off (default) | codex | api
          mode: auto                   # auto | always; only for target: codex
          mappings: [{from: "claude-opus-*", to: "gpt-6-astra"}]  # only for target: codex
      codex:
        mode: subscription
        api-credential: "<auth_index>"
        fallback:
          target: api                  # off | api
    key-profiles:
      "client-key-a":
        providers: { ...same shape... }
  ```
  - A key profile still replaces the shared profile entirely; an omitted `providers` block in a key profile means `subscription` mode and fallback `off` for both providers.
  - Legacy `claude-fallback: {enabled, mode, mappings}` stays readable everywhere it is accepted today. On load it converts to `providers.claude.fallback` (`enabled: true` -> `target: codex`; `enabled: false` -> `target: off`, mode and mappings kept). When a profile carries both blocks, `providers.claude.fallback` wins and the legacy block is ignored with a warning. Saving writes the new block and drops the legacy one for that profile; a config file that is loaded and not written stays byte-identical.
  - Legacy writes (a config PATCH whose profile carries `claude-fallback` and no `providers`, which is what the current panel sends) are merged, not treated as a full replacement of `providers`: the stored `providers` of that profile is kept, and only `providers.claude.fallback` is updated from the legacy block. `enabled: true` sets `target: codex`. `enabled: false` sets `target: off` unless the stored target is `api`, which is kept (the legacy view cannot express `api`). This keeps an old panel from wiping API settings.
- **Chosen approach — validation** (error on load, `422 invalid_config` on write; key-profile paths use the 12-hex key hash, e.g. `routing.key-profiles.3f2a9c1b7d4e.providers.claude.api-credential`):
  - unknown `mode` or `target` value -> error;
  - `mode: api` or `fallback.target: api` without `api-credential` -> error;
  - `codex.fallback.target: codex` -> error;
  - `mode: api` together with `fallback.target` other than `off` -> error;
  - `target: codex` keeps today's mapping rules (`ClaudeFallbackConfig.Normalize`: at least one mapping when enabled, trailing `*` only in `from`, no duplicates);
  - `mode`/`mappings` under `target: api` are kept but ignored at runtime, with a warning on load;
  - `api-credential` is trimmed; whether it names a live API-key credential of the same provider is NOT a config error. The management write path logs a warning when it does not (the key may be added later), the same leniency as `preferred-accounts`.
- **Chosen approach — runtime**:
  - **Primary-leg filter** in `authSelectionEligibility.allows(auth)`, keyed by the candidate's provider (modes are per provider and a request may select from a mixed pool):
    - provider `claude`/`codex`, `mode: subscription` -> only `AuthKind() == oauth`;
    - provider `claude`/`codex`, `mode: api` -> only the API-key credential whose `candidatePreferenceIndex` equals `api-credential`;
    - any other provider -> unchanged.
  - The filter is skipped for pinned executions (`pinnedAuthIDFromMetadata`), for requests that carry their own `requiredKind` or `credentialPolicy` (e.g. Codex Alpha Search with an API key, the Codex leg of Claude -> Codex), and whenever the policy is unsupported (`ClaudeFallbackSupported()` is false: Home mode or a scheduler plugin).
  - **Default is `subscription`** when no `providers` block exists. This is a behavior change: API-key credentials of `claude`/`codex` are never selected in the primary leg for any client key. When a `subscription`-mode provider has no OAuth credential at all, the error is `auth_not_found` with a message such as `no subscription credentials for claude; set routing providers.claude.mode: api`.
  - **`mode: api`**: subscriptions are skipped; session affinity works as usual within that one credential; a cooling-down key returns its cooldown error with `Retry-After`. No other API key is ever substituted (billing pin).
  - **Fallback** is a per-provider generalization of `executeWithClaudeFallback`:
    - `target: codex` (Claude only): unchanged behavior, including `auto`/`always`, model mappings, and the OAuth-only Codex leg.
    - `target: api`: after a failed primary round with the same triggers as `shouldFallbackClaude` (401, 403, 408, 429, 5xx, `auth_not_found`/`auth_unavailable`, transient transport errors; never invalid requests, cancellation, or request-stop), re-execute the SAME model on the SAME provider under a derived profile: that provider in `mode: api` with the profile's `api-credential`, session affinity off, fallback off. So the API leg neither reads nor writes session bindings and the next request starts from subscriptions again.
    - When the API credential cannot be selected (cooldown, disabled, removed, unknown index), the client gets the primary error with its status and `Retry-After` (today's `isFallbackSelectionError` rule).
    - No chains: a fallback leg calls the inner `execute`, never the wrapper.
    - Streaming: only before the response starts, as today.
    - When both `claude` and `codex` are among a request's providers, `claude` is checked first.
    - A completed primary round prefers the fallback to sleeping for a cooldown (today's `hasClaudeFallbackAfterError`), for both targets.
  - **Logging**: the `Routing profile selection` info line gets `routing_stage=api` for a `mode: api` selection and `fallback_target=codex|api` on the fallback stage; the fallback info message names the target. Raw keys are never logged.
- **Chosen approach — management API**:
  - `GET /v8/management/routing/profiles`: `shared` and each `keys[].profile` carry `providers` in effective form (always present, both sub-blocks, legacy block already converted) and still carry the legacy `claude-fallback` view derived from it, so the current panel keeps working. New field `api_credentials: {"claude": [{"auth_index", "label", "base_url", "disabled"}], "codex": [...]}` lists API-key credentials per provider; the key itself is never included. (`GET /v8/management/credentials` entries carry `provider`, `label`, `runtime_only` but no auth kind — `internal/api/handlers/management/auth_files.go:690-700`.)
  - Writes go through `PATCH /v8/management/config` as today.
  - `GET /v8/management/routing/claude-fallback` stays and returns the legacy view derived from `providers.claude.fallback` of the shared profile (`enabled` is true only for `target: codex`).
- **Rejected alternatives**:
  - Manual credential `priority` plus the `priority` selection slot to demote API keys: only works when configured, session affinity keeps a session on the API key, and there is no per-client-key billing pin.
  - One mode per client key instead of per provider: a client key serves both Claude and Codex models.
  - Conditional exclusion (API keys return to the primary pool when a provider has no enabled subscription): a silent billing surprise.
  - Substituting another API key when the profile's `api-credential` is unavailable, or an ordered list of API credentials: billing leak / YAGNI.
  - Binding the session on the API fallback leg, or a separate short TTL for it: spend would continue on the paid API after subscriptions recover.
  - Codex -> Claude subscription fallback with reverse model mapping: out of scope.
- **Verified facts**:
  - `sdk/cliproxy/auth/routing_profile.go`: `routingProfile{keyHash, fromKey, sticky, preferred, selection, fallback}`; `ensureRoutingProfile` injects the profile unless the context already carries one, so a derived profile placed in the context survives nested calls. `resolveRoutingProfile` returns nil under Home and the shared profile under a plugin scheduler.
  - `authSelectionEligibilityForRequest(ctx, opts)` has the context, so it can read `routingProfileFromContext(ctx)`. Call sites of `eligibility.allows` found during planning: `conductor_selection.go` lines ~1716, ~1824 (cooldown wait / retry), ~2389, ~2681, ~2763, ~2847, ~3032, ~3155 (candidate collection paths), and `scheduler.go` ~412, ~499 via `scheduledAuthPredicate`. `claude_fallback.go:23` reads `requiredKind` to refuse rerouting non-OAuth requests.
  - `sdk/cliproxy/auth/selector.go:~1287`: a profile with `sticky == false` neither reads nor writes session bindings (`SessionAffinityDisabledMetadataKey`), which is what the API leg's derived profile relies on.
  - `internal/config/routing_profile.go`: `RoutingProfile{SessionAffinity, PreferredAccounts, Selection, ClaudeFallback}`, `RoutingConfig.Normalize(apiKeys)`, `SharedProfile()`, `Clone()`. `Normalize` runs in `config_load.go:248` and `parse.go:64`.
  - `internal/config/config_yaml.go`: save path with key-profile pruning (`pruneMappingToGeneratedKeys`, `isKeyProfileSubtreePath`); `internal/watcher/diff/config_diff.go:207-216` logs `routing.claude-fallback: updated` and redacted key-profile changes.
  - `internal/api/handlers/management/config_v8.go`: PATCH validation and the `invalid_config` / `unknown_api_key` responses; `routing_profiles.go` (`GetRoutingProfiles`, `reconcileKeyProfiles`); `claude_fallback.go` (`GetClaudeFallback`); routes in `internal/api/server_management_v8.go`.
  - Existing tests to extend, not replace: `sdk/cliproxy/auth/{claude_fallback,routing_profile,routing_profile_edge,selection_pipeline,selection_pipeline_wiring,selector_pipeline_candidates}_test.go`, `internal/config/{claude_fallback,routing_profile,routing_preferences}_test.go`, `internal/api/handlers/management/{claude_fallback,routing_profiles,config_subscription_routing}_test.go`.
  - `.gitignore` un-ignores `docs/plans/` and `docs/backlog/`, so this plan is tracked normally.
  - Known unrelated flaky tests are filed in `docs/backlog/` (`plugin-oauth-tests-fail-with-repeated-runs.md`, `flaky-websocket-pinned-auth-replay-close.md`); do not chase them in this plan.

## Context (from discovery)
- Files/components involved:
  - `internal/config/`: `routing_profile.go`, `claude_fallback.go`, new `provider_routing.go`, `config_types.go` (`RoutingConfig`), `config_yaml.go`, `config_load.go`, `parse.go`
  - `internal/watcher/diff/config_diff.go`
  - `sdk/cliproxy/auth/`: `routing_profile.go`, `claude_fallback.go` (to become the provider fallback wrapper), `conductor_selection.go` (eligibility), `scheduler.go` (predicate), `selector.go` (sticky bypass, read only)
  - `internal/api/handlers/management/`: `routing_profiles.go`, `claude_fallback.go`, `config_v8.go`
  - `docs/management-api-v8.md`, `config.example.yaml`, `AGENTS.md` (architecture line for `sdk/cliproxy/auth/`)
- Related patterns: per-key profile resolved once and carried in context; context markers (`withRoutingFallbackLeg`, `withRequiredAuthKind`); table-driven tests with fake executors in `claude_fallback_test.go`; key identified in errors and logs by `config.KeyHashPrefix`.
- Dependencies: none new.

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Make small, focused changes; follow `AGENTS.md` (KISS, English comments, `errX :=` shadow naming, logrus structured logging without secrets, no `time.Sleep` in tests — use `nowFunc` / explicit timestamps)
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new and modified functions
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- After Go changes in every task: `gofmt -w` on touched files, then `go build -o test-output ./cmd/server && rm test-output`
- Maintain backward compatibility for configs without a `providers` block, except for the documented default change (API keys leave the primary leg)

## Testing Strategy
- **Unit tests**: required for every task. Run the touched packages per task: `go test ./internal/config/... ./sdk/cliproxy/auth/... ./internal/api/handlers/management/... ./internal/watcher/...`
- **Full suite**: `go test ./...` in the verification task
- **E2E tests**: the project has no UI e2e suite; `test/` holds cross-module integration tests — add one there only if a behavior cannot be covered at package level

## Progress Tracking
- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Update plan if implementation deviates from original scope
- Keep plan in sync with actual work done

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): tasks achievable within this codebase - code changes, tests, documentation updates
- **Post-Completion** (no checkboxes): manual testing, deployment, the panel plan
- **Checkbox placement**: Checkboxes belong only in Task sections. Do not put checkboxes in Overview, Context, Decisions, or Decision Log.

## Decision Log
- 2026-11-10 brainstorm: **accepted** - Codex gets "fallback to its own API key" only; fallback to another subscription stays Claude -> Codex.
- 2026-11-10 brainstorm: **accepted** - mode and fallback are per provider (`providers.claude` / `providers.codex`) inside a profile.
- 2026-11-10 brainstorm: **accepted** - strict primary leg: `subscription` mode never selects an API key; with no subscription credential the request fails with a pointer to `mode: api`.
- 2026-11-10 brainstorm: **accepted** - an unavailable profile API key is an error to the client; no substitution by another key.
- 2026-11-10 brainstorm: **accepted** - the API fallback leg does not bind the session.
- 2026-11-10 brainstorm: **rejected** - "demote API keys with credential priority" - not a guarantee, affinity pins sessions to the API key, no per-key billing.
- 2026-11-10 brainstorm: **rejected** - "one API/Subscription switch per client key" - a key serves both providers.
- 2026-11-10 brainstorm: **rejected** - "API keys rejoin the pool when no subscription is enabled" - silent billing surprise.
- 2026-11-10 brainstorm: **rejected** - "ordered list of API credentials" - YAGNI, one key per provider per client key today.
- 2026-11-10 planning (decided while writing the plan, not discussed in the dialogue): **accepted** - legacy `claude-fallback` writes merge into the stored `providers` instead of replacing it, so the current panel cannot wipe API settings; `providers` wins when both blocks are present; the provider policy is skipped under Home mode and scheduler plugins.

## Implementation Steps

### Task 1: Add provider routing config types, validation, and legacy conversion
- [x] create `internal/config/provider_routing.go` with `ProviderRoutingConfig{Mode, APICredential, Fallback ProviderFallbackConfig}`, `ProviderFallbackConfig{Target, Mode, Mappings []ClaudeFallbackMapping}`, constants for modes (`subscription`, `api`) and targets (`off`, `codex`, `api`), and `ProvidersRoutingConfig{Claude, Codex}` with yaml/json tags `providers` -> `claude` / `codex` -> `mode`, `api-credential`, `fallback` -> `target`, `mode`, `mappings`
- [x] implement `Normalize(fieldPath string)` covering every rule in Decisions/validation (trim and lower-case enums, empty mode -> `subscription`, empty target -> `off`, provider-specific targets, required `api-credential`, `mode: api` with non-`off` target, mapping rules reused from `ClaudeFallbackConfig.Normalize` for `target: codex`); return the list of warnings (ignored `mode`/`mappings` under `target: api`) separately from errors so callers can log them
- [x] add conversion helpers: legacy `ClaudeFallbackConfig` -> `ProviderFallbackConfig` and back (`LegacyClaudeFallback()` returns `enabled` true only for `target: codex`), plus `Target(model)` lookup delegating to the existing longest-prefix logic
- [x] add `Providers ProvidersRoutingConfig` to `RoutingProfile` and to `RoutingConfig` (shared), update `SharedProfile()` and `Clone()` (deep-copy mappings), and call the new `Normalize` from `RoutingConfig.Normalize` for the shared block (`routing.providers`) and every key profile (`routing.key-profiles.<hash>.providers`); apply the "both blocks present -> `providers` wins, warn" rule and the legacy -> new conversion here so the in-memory profile always has an effective `Providers`
- [x] write table-driven tests in `internal/config/provider_routing_test.go` for every validation rule (success and error, error text contains the field path with the key hash, never the raw key)
- [x] write tests for legacy conversion both ways and for precedence when both blocks are present; extend `routing_profile_test.go` for `SharedProfile()`/`Clone()` independence
- [x] run `go test ./internal/config/...`, gofmt, compile check - must pass before task 2

### Task 2: Persist the providers block (load, save, legacy writes, config diff)
- [x] read `internal/config/config_yaml.go` save path and `internal/api/handlers/management/config_v8.go` PATCH path first; note in this plan (Technical Details) how a key-profile entry is replaced on PATCH and where the pre-write config is available
- [x] make saving write `providers` for the shared profile and each key profile and drop the legacy `claude-fallback` node of a profile once that profile is written with `providers`; a config that is loaded and saved without routing changes must keep its bytes (extend the existing round-trip tests)
- [x] implement the legacy-write merge in the PATCH path: for the shared profile and for each key profile in the patch that carries `claude-fallback` and no `providers`, keep the stored `providers` of that profile and update only `providers.claude.fallback` per the rule in Decisions (`enabled: true` -> `codex`; `enabled: false` -> `off` unless the stored target is `api`)
- [x] update `internal/watcher/diff/config_diff.go` to report `routing.providers: updated` for the shared block (no values) and keep key-profile changes redacted
- [x] write tests: YAML round-trip (legacy-only file untouched; file with `providers` stable; legacy file after a providers write has no legacy node), hot-reload normalize, legacy-write merge matrix (stored target off/codex/api × legacy enabled true/false, shared and key profile), config diff output without keys or credential indexes
- [x] write tests for error cases: invalid `providers` in a PATCH returns `422 invalid_config` with the hashed path and leaves the file unchanged
- [x] run `go test ./internal/config/... ./internal/api/handlers/management/... ./internal/watcher/...`, gofmt, compile check - must pass before task 3

### Task 3: Apply the per-provider mode filter in credential eligibility
- [x] audit every selection path before coding and record the result under Technical Details: all `eligibility.allows` call sites, `scheduledAuthPredicate`, `SelectAuthByKind`, `SelectAuthWithCredentialPolicy`, the Home variants, and any candidate loop that does not go through `allows`; add ➕ tasks if a native-routing path bypasses the eligibility
- [x] carry the effective `ProvidersRoutingConfig` on `routingProfile` (`sdk/cliproxy/auth/routing_profile.go`, `newRoutingProfile`) and add a `providerPolicy` to `authSelectionEligibility`, filled in `authSelectionEligibilityForRequest` from `routingProfileFromContext(ctx)` only when the request has no own `requiredKind`/`credentialPolicy`, is not pinned, and the manager supports the policy (`ClaudeFallbackSupported()`); because the function is not a `Manager` method today, pass the support flag through the context or the profile (set it when the profile is resolved)
- [x] extend `allows(auth)`: for provider `claude`/`codex` apply the mode rule (subscription -> OAuth only; api -> API-key credential whose `candidatePreferenceIndex` equals `api-credential`); other providers untouched
- [x] return the dedicated `auth_not_found` message when a `subscription`-mode provider has API-key credentials but no OAuth credential at all, and a dedicated message when `mode: api` names a credential that does not exist; keep the existing cooldown error when OAuth credentials exist but are cooling down
- [x] log `routing_stage=api` in `logRoutingProfileSelection` for selections made under `mode: api`
- [x] write table-driven tests: provider × mode × credential kind; mixed-provider pool; other providers untouched; pinned request exemption; own `requiredKind` and `credentialPolicy` exemptions; Home mode and plugin scheduler leave selection unchanged; retry/cooldown-wait paths count only eligible credentials
- [x] write tests for the error cases: subscription mode with only API keys, `mode: api` with unknown index, `mode: api` with the key cooling down (cooldown error with `Retry-After`), default profile (no `providers`) never selects an API key
- [x] update existing tests that relied on an API key being selected in the primary leg for `claude`/`codex` (set `mode: api` or adjust expectations) and list each such test in this plan
- [x] run `go test ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 4

### Task 4: Generalize the fallback wrapper and add the API target
- [x] rename/reshape `executeWithClaudeFallback` into a per-provider wrapper (keep the file focused; a rename to `provider_fallback.go` is fine) that resolves the fallback for the first matching provider (`claude`, then `codex`) from `profile.providers`; `Execute`, `ExecuteCount`, `ExecuteStream` call it as today
- [x] keep `target: codex` behavior byte-for-byte: `auto`/`always`, model mapping with suffix preservation, OAuth-only Codex leg, primary error preferred when the Codex leg cannot select, request-stop handling, stream bootstrap errors
- [x] implement `target: api`: after a failed primary round (same triggers as `shouldFallbackClaude`, renamed to a provider-neutral name), re-run the inner `execute` with the same model and providers under a derived profile (that provider in `mode: api` with the profile's `api-credential`, `sticky: false`, fallback `off`), marked as a fallback leg for logging; return the primary error when the leg fails with a selection error
- [x] generalize `hasClaudeFallbackAfterError` so a completed primary round prefers either fallback target to sleeping for a cooldown, and log `fallback_target` in the routing info line and the fallback message
- [x] write tests for `target: api` on Claude and on Codex: each trigger status, no-credential trigger, transient transport error; never on invalid request, cancellation, request-stop
- [x] write tests: the API leg neither reads nor writes session bindings and the next request of the same session selects a subscription again; unavailable/unknown/disabled API key returns the primary error with status and `Retry-After`; no chain after a failed API leg; streaming before and after the first byte; `always` mode still applies only to `target: codex`
- [x] confirm the existing `claude_fallback_test.go` cases pass with changes limited to renames and config construction
- [x] run `go test ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 5

### Task 5: Expose providers and API credentials in the management API
- [x] `GetRoutingProfiles` (`internal/api/handlers/management/routing_profiles.go`): emit effective `providers` (both sub-blocks, `mappings` as `[]` not null) for `shared` and every key profile, keep emitting the legacy `claude-fallback` view derived from it, and add `api_credentials` built from `authManager.List()` filtered to `AuthKind() == apikey` and provider `claude`/`codex` (`auth_index`, `label`, `base_url` attribute, `disabled`); never include `api_key`
- [x] `GetClaudeFallback`: return the legacy view derived from the shared `providers.claude.fallback`
- [x] on a successful config write, log one warning per profile whose `api-credential` does not match a live API-key credential of that provider (hashed key in the field, no raw key, no credential secret)
- [x] write handler tests: response shape for shared and key profiles (legacy-only config, new config, mixed), `api_credentials` content and absence of secrets, `supported: false` under Home/plugin scheduler, `Cache-Control: no-store` preserved
- [x] write tests for the write side through the handler: valid `providers` PATCH persists and is returned; each validation error returns `422` with the hashed path; unknown `api-credential` is accepted with a logged warning
- [x] run `go test ./internal/api/handlers/management/...`, gofmt, compile check - must pass before task 6

### Task 6: Verify acceptance criteria
- [x] verify every Overview goal against the code: default profile never selects a Claude/Codex API key; per-key API fallback; `mode: api`; Codex parity
- [x] verify edge cases: mixed-provider request, pinned Codex websocket continuation under `subscription` mode with an API-key pin, hot reload switching a key between modes mid-session (existing bindings to a now-ineligible credential fail over instead of sticking)
- [x] run the full suite `go test ./...` (known flaky tests listed in `docs/backlog/` may be re-run once; do not modify them)
- [x] run `gofmt -l .` (no output) and the compile check `go build -o test-output ./cmd/server && rm test-output`
- [x] run `go vet ./internal/config/... ./sdk/cliproxy/auth/... ./internal/api/handlers/management/...` and fix findings in touched code
- ➕ verification notes: Overview goals map to `providerPolicyAllows` (eligibility, default `subscription` excludes Claude/Codex API keys), `executeWithProviderFallback` with `target: api` (shared and key profiles, `TestAPIFallback*`), `mode: api` (`TestProviderPolicyExecutionSelection`), and Codex parity (every policy/fallback test runs both providers). Added regression tests for the edge cases that had no explicit coverage: `pinned codex api key is exempt` and `mixed claude and codex under codex api mode` rows in `TestProviderPolicyExecutionSelection`, and `TestProviderPolicyHotReloadMovesBoundSessions` (a session bound under one mode moves to an eligible credential after a reload to the other mode, both providers). `go test ./...` passed on the first run (no flaky re-run needed); `gofmt -l .`, the compile check, and `go vet` on the listed packages are clean.

### Task 7: Update documentation
- [ ] `docs/management-api-v8.md`: new section "Per-provider mode and API fallback" (schema, validation, runtime rules, logging fields), update the routing stages list, the profiles response example (`providers`, `api_credentials`), the writing-profiles rules (legacy-write merge), and add a prominent note on the default change (Claude/Codex API keys leave the primary leg; set `mode: api` to use them)
- [ ] `config.example.yaml`: document `routing.providers` and the key-profile form next to the existing `claude-fallback` example, mark `claude-fallback` as the legacy spelling
- [ ] `AGENTS.md`: extend the `sdk/cliproxy/auth/` architecture line with the provider policy in eligibility and the per-provider fallback wrapper
- [ ] note in `docs/management-api-v8.md` that CLIProxyAPIHome does not implement the provider policy

## Technical Details
- **Types** (`internal/config/provider_routing.go`):
  ```go
  type ProvidersRoutingConfig struct {
      Claude ProviderRoutingConfig `yaml:"claude,omitempty" json:"claude"`
      Codex  ProviderRoutingConfig `yaml:"codex,omitempty" json:"codex"`
  }
  type ProviderRoutingConfig struct {
      Mode          string                 `yaml:"mode,omitempty" json:"mode"`
      APICredential string                 `yaml:"api-credential,omitempty" json:"api-credential"`
      Fallback      ProviderFallbackConfig `yaml:"fallback,omitempty" json:"fallback"`
  }
  type ProviderFallbackConfig struct {
      Target   string                  `yaml:"target,omitempty" json:"target"`
      Mode     string                  `yaml:"mode,omitempty" json:"mode"`
      Mappings []ClaudeFallbackMapping `yaml:"mappings,omitempty" json:"mappings"`
  }
  ```
- **Task 1 implementation notes (deviations from the original wording)**:
  - The stored `Providers` keeps only what the config spells out: `Normalize` trims and lower-cases enums and validates the effective values, but does not write the `subscription` / `off` defaults and does not copy the legacy `claude-fallback` block into it. This keeps `routing: null` equal to `RoutingConfig{}`, empty key profiles equal to `RoutingProfile{}`, and a load/save without routing changes byte-stable.
  - The effective policy is read through `RoutingProfile.EffectiveProviders()` (defaults filled, legacy block converted when `providers.claude.fallback` is unset). `SharedProfile()` copies `Providers` as stored, so callers in Tasks 3-5 must use `EffectiveProviders()` on both the shared and key profiles, not the raw field.
  - Precedence: when `providers.claude.fallback` and the legacy block are both set, `Normalize` logs a warning and clears the legacy block in memory (an invalid ignored legacy block does not fail the load). Task 2 must make the save drop the legacy node for the shared `routing` mapping too (only key-profile subtrees are pruned today).
  - The effective policy (legacy block combined with `providers`) is validated as a whole, e.g. `providers.claude.mode: api` + legacy `enabled: true` is rejected at `...providers.claude.fallback.target`.
  - Shared legacy validation moved from `config_load.go` / `parse.go` into `RoutingConfig.Normalize`; legacy error texts are unchanged (`ClaudeFallbackConfig.Normalize` now delegates to the shared `normalizeFallbackRules`).
  - The mapping lookup is `ProviderFallbackConfig.MappingTarget(model)` because the struct already has a `Target` field.
- **Eligibility rule** (pseudo):
  ```text
  if request is pinned, has its own requiredKind/credentialPolicy, or policy unsupported: existing rules only
  p := policy[auth.Provider]            // only claude and codex have entries
  mode subscription: allow iff auth.AuthKind() == oauth
  mode api:          allow iff auth.AuthKind() == apikey && candidatePreferenceIndex(auth) == p.APICredential
  ```
- **API fallback leg**: `ctx' = withRoutingFallbackLeg(withRoutingProfile(ctx, derived))` where `derived` copies the request profile with `providers[P].mode = api`, `sticky = false`, both fallback targets `off`. `ensureRoutingProfile` keeps a profile already in the context, so retries inside the leg use `derived`.
- **Legacy view**: `claude-fallback.enabled == (providers.claude.fallback.target == "codex")`; `mode` and `mappings` are copied.
- **Selection-path audit result** (Task 3): every native candidate loop goes through `authSelectionEligibility.allows`, and each builds the eligibility from a context that carries the execution's profile, so no ➕ task was needed.
  - Candidate collection: `pickNextLegacy`, `pickNext` (fast-path pre-scan, then `scheduler.pickSingle` via `scheduledAuthPredicate`), `pickNextMixedLegacy`, `pickNextMixed` (pre-scan, then `scheduler.pickMixed` via `scheduledAuthPredicate`), `HasAlternativeTargetForModel`. All call `ensureRoutingProfile` first; executions reach them only through `pickNextMixed`.
  - Retry and cooldown wait: `retryAllowedForRequest` and `closestCooldownWaitWithAttemptedForRequest` take the eligibility built in `shouldRetryAfterErrorWithAttempted` from the execution context; `warnLogAuthUnavailable` builds its own from the same context. The session-affinity bound-auth pin there is a local variable, so the policy still applies to it.
  - Exempt by design: `SelectAuthByKind` (requiredKind; Codex live client), `SelectAuthWithCredentialPolicy` (Codex Alpha Search), pinned executions (Codex websocket continuation). `SelectAuth` has no non-test caller and gets the policy.
  - Home: `SelectHome*`, `pickNextViaHome` and the `conductor_home.go` loops run without a profile (`resolveRoutingProfile` returns nil), so no policy. Plugin scheduler: the profile is built with `providerPolicy: false`.
  - The selectors (session affinity, pipeline stages, preferred accounts) pick only from the candidate list they receive, so a binding to a credential that a hot reload made ineligible fails over. The remaining `range m.auths` loops (cooldown reconciliation, `AvailableProviders`, `HasProviderAuth`, `LookupSessionAffinity`, `requestRetryRoundExclusions`, model alias tables) do not select credentials.
- **Task 3 implementation notes (deviations from the original wording)**:
  - `subscription` mode excludes `AuthKind() == apikey` instead of requiring `AuthKind() == oauth`: a credential with no detectable kind (no `auth_kind` attribute, no `api_key`, no OAuth metadata) is not an API key and stays eligible, so an unusual subscription credential cannot be locked out. `mode: api` is strict (API-key kind and matching `candidatePreferenceIndex`).
  - The profile carries the effective `providers` and a `providerPolicy` flag (true unless a scheduler plugin owns routing); `authSelectionEligibility.providerPolicy` points at the profile. Helpers live in `sdk/cliproxy/auth/provider_policy.go`.
  - The dedicated `auth_not_found` messages are produced by `providerPolicySelectionError`, applied in `pickNextMixed` (now a thin wrapper around `selectNextMixed`) to an `auth_not_found` pick error: `no subscription credentials for <provider>; set routing providers.<provider>.mode: api` when the provider has enabled API keys and no other credential, and `no enabled <provider> API key matches routing providers.<provider>.api-credential "<index>"` in `mode: api`. A cooling-down credential keeps the existing `model_cooldown` error with `Retry-After`.
  - `logRoutingProfileSelection` reports `routing_stage=api` for an API-key selection of a provider in `mode: api` (any selection stage).
- **Task 4 implementation notes**:
  - `sdk/cliproxy/auth/claude_fallback.go` is renamed to `provider_fallback.go`. `providerFallbackFor` resolves a `providerFallback{provider, target, mode, model}` for the first listed provider (claude, then codex) whose fallback applies; `executeWithProviderFallback` replaces `executeWithClaudeFallback`, `shouldFallbackProvider` replaces `shouldFallbackClaude`, and `hasProviderFallbackAfterError` replaces `hasClaudeFallbackAfterError` in the three retry loops of `conductor_execution.go`.
  - `routingProfile.fallback` (the legacy view) is removed; the Codex target reads `providers.claude.fallback` directly. `ClaudeFallbackSupported()` keeps its name because the management handlers call it.
  - `withRoutingFallbackLeg(ctx, target)` now stores the leg's target. `providerFallbackFor` returns no fallback inside any fallback leg, so neither leg chains and the retry loop of a leg is not short-circuited (previously the Codex leg was protected only because its providers were `["codex"]`; a `codex.fallback.target: api` would otherwise end the Codex leg's retries early).
  - `target: api` is skipped when the request carries its own `requiredKind` or `credentialPolicy`, is pinned, or the provider is already in `mode: api`; its mode is always `auto`, so `fallback.mode: always` is ignored for it. The API leg derives the profile with `apiFallbackProfile` (provider in `mode: api`, both fallbacks off, `sticky: false`, `providerPolicy: true`) and runs the inner `execute` with the same model, providers, and options.
  - Logging: the fallback message is `Routing <provider> request to API key fallback` (fields `provider`, `source_model`, `fallback_target`, `primary_status`); the Codex message keeps its text and gains `fallback_target`. `Routing profile selection` lines inside a leg carry `fallback_target`.
- **Task 5 implementation notes**:
  - Helpers live in `internal/api/handlers/management/routing_api_credentials.go`: `routingAPICredentials` (both providers always present as lists, sorted by `auth_index`, `disabled` covers `Disabled` and `StatusDisabled`), `effectiveProvidersView` (mappings `[]`), `warnUnknownRoutingAPICredentials`.
  - The write warning runs after every successful `ConfigV8` write (JSON and YAML), one entry per profile with fields `profile` (`shared` or the key hash prefix) and `<provider>_api_credential` (the auth_index, not a secret). A disabled credential counts as not matching, like the runtime `no enabled ... API key` error. Nothing is logged when the policy is unsupported (Home, plugin scheduler). A key added in the same write is not yet in the manager (reload is async), so it can produce a one-off warning.
- **How a profile is replaced on PATCH, and where the pre-write config is available** (Task 2):
  - `ConfigV8` works on the YAML node tree, not on `Config`. `before := cloneConfigV8Node(root)` is the stored document (v8 layout) taken before the write; PATCH deep-merges the body into `root` (`mergeConfigV8Patch`, so a key profile body merges field by field and a stored `providers` node survives), PUT replaces the node at the path. The tree is then validated through `ParseConfigBytes` (`422 invalid_config` on a routing error) and written as is with `WriteConfig`; `SaveConfigPreserveComments` is not involved.
  - The current panel (CPA-Manager-Plus `routingProfiles.ts`) writes one complete profile per PATCH to `/config` with `claude-fallback` and no `providers`.
  - The legacy-write merge is `mergeLegacyClaudeFallbackWrites` (`internal/api/handlers/management/routing_providers_write.go`), called for every non-YAML, non-DELETE write after the merge. It inspects a copy of the request value placed at its path (`configV8PatchRoot`), so it works for PATCH and PUT at any routing path. Per touched profile: a `providers` node the write omits is restored from `before` (PUT); a `claude-fallback` without `providers.claude.fallback` in the write is converted into `providers.claude.fallback` (the trigger is "no `providers.claude.fallback`", slightly wider than "no `providers`", so a write that sets only `providers.codex` plus the legacy block is not ignored); the legacy node is dropped once `providers.claude.fallback` is set. When the stored target `api` is kept, `mode`/`mappings` are still copied from the legacy block (ignored at runtime, warned on load) so the panel reads back what it wrote. YAML document writes (`/config.yaml`) stay authoritative and are not rewritten.
  - `SaveConfigPreserveComments` (v0/in-memory saves): key-profile subtrees were already pruned to the generated keys; `dropSupersededLegacyClaudeFallback` now removes the shared `routing.claude-fallback` node when the generated routing carries `providers.claude.fallback` and no legacy block.
  - Added so the runtime and the panel stay consistent before Tasks 4/5: `newRoutingProfile` reads the Codex fallback from `EffectiveProviders().Claude.Fallback.LegacyClaudeFallback()`, and `GetRoutingProfiles` / `GetClaudeFallback` emit the legacy view derived from the effective providers. `TestClaudeFallbackManagementSaveAndDisable` now reads the saved fallback through that view.
- **Affected existing tests** (Task 3): tests that expected a Claude/Codex API-key credential in the primary leg without a `providers` block.
  - Switched to `mode: api` with an explicit `Index` (the test is about API-key behavior): `TestSelectedCodexConfigurationUpdateCapabilityExecutionModelOverride`, `TestManagerExecute_APIKeyAliasForceMappingRewritesResponse`, `TestManagerExecuteStream_APIKeyAliasForceMappingRewritesResponse`, `TestManager_Issue6416_APIKeyRotation_LateResultDoesNotCooldownNewAPIKey` (it hung: the request goroutine never reached the executor), `TestServerCodexAPIKeyResponsesStreamingRequestLog`, and the API-key case of `TestResponsesWebsocketClosesOnIdleCodexDisconnect`. The `sdk/cliproxy/auth` helper is `withAPIModeRouting` (`provider_policy_helpers_test.go`).
  - Fixtures that stand in for subscription accounts (several Codex accounts failing over to each other, websocket/steering/interrupt mechanics) are marked `auth_kind: oauth` and keep their `api_key` attribute; the Codex executor keys its transport off the `api_key` attribute (`codexAuthUsesAPIKey`), so the upstream traffic is unchanged. `test/`: `TestCodexIncompleteStreamIsNotTypedAsInvalidRequest`, `TestCodexTerminalQuotaCoolsAccountAcrossModels`, `TestCodexModelLevelCoolingPreservesSiblingModel`, `TestCodexStreamDisconnectAfterHeadersFailsOver`, `TestCodexStreamDisconnectAfterOutputDoesNotFailOver`, `TestCodexStreamAbnormalDisconnectBeforeOutputFailsOver`. `sdk/api/handlers/openai`: `TestResponsesInterruptInFlight`, `TestResponsesInterruptStopsHTTPUpstream`, `TestResponsesLateInterruptDoesNotPoisonNextCreate`, `TestResponsesSteeringDisabledAccountCannotSendAnotherFrame`, `TestResponsesSteeringSubsequentWebsocketMessagesRunRequestInterceptors`, `TestResponsesSteeringErrorRecoveryIntegration`, `TestResponsesSteeringFullDuplexIntegration`, `TestResponsesWebsocketIsCompatLocalCompactionIntegration_Issue7406`, `TestResponsesWebsocketExistingWSCompactionRejectsStaleParent_Issue7406`, `TestResponsesSteerInFlightWebSocket`, `TestResponsesSteeringLocalValidationRecovery`, `TestResponsesInterruptRacingWebsocketLossIsNotAnsweredLocally`, `TestResponsesWebsocketUpstreamLossKeepsDownstreamOpen`, `TestResponsesWebsocketIdleUpstreamEOFIsSilent`, `TestResponsesWebsocketDuplexLossReportsEveryOperation` (hung), plus the other Codex fixtures in the same files.
  - `internal/runtime/executor` (helper `withAPIModeRouting` in `provider_routing_helpers_test.go`) and `executor/helps`: `mode: api` for `TestApplyPatchBridgeLiveHTTPPreviewMatrix`, `TestApplyPatchHTTPGatewayErrorMatrix` (Claude subtests), `TestClaudeExecutor_AuthManager_FableOnlyRejectionDoesNotBlockOpus`, `TestClaudeExecutor_AuthManager_OverageSpendCapReachedDoesNotBlockOpus_Issue5915`, `TestClaudeExecutor_AuthManager_ModelLevelCoolingConfigScopesSharedRejection`, `TestClaudeExecutor_AuthManager_OverageSpendCapWithRetryAfter_Issue5920`, `TestClaudeExecutor_AuthManager_OverageSpendCapFirstRequestAllowsOpus_Issue5920`, `TestClaudeExecutor_AuthManager_FableOnlyRejectionWithout5hHeadersAllowsOpus_Issue7197`, `TestClaudeExecutor_AuthManager_CredentialScopeBlocksAllModelsAndAliases`, `TestClaudeExecutor_AuthManager_OrdinaryModel429DoesNotBlockSiblingModels`, `TestClaudeExecutor_Issue7000ManagerRetryKeepsAuto`, `TestClaudeExecutorManagerRetriesSilentTLSHandshakeThroughConnectProxy`, `TestCodexDuplexConnectionTimeoutDoesNotCoolHealthyAccount`, `TestCodexDuplexSummaryCredentialFailuresThroughManager`, `TestV8SharedUpstreamRequestSettingsAffectBothAuthKinds`, `TestApplyRequestThinkingConfigurationUpdateSelectedCodexModel`, `TestApplyRequestThinkingUsesExactClaudeModeForSummaryOnlyRequest`, `TestApplyRequestThinkingUsesSelectedPrefixedAPIKeyModel`; `auth_kind: oauth` stand-ins (several accounts failing over; assertions do not depend on the API-key kind) for `TestClaudeExecutor_RateLimit_FastModeAuthoritativeRejectionHeadersOverrideBody`, `TestClaudeExecutor_AuthManager_AlternativeCredentialCanBeSelected`, `TestClaudeExecutor_AuthManager_MultiModelPoolStreamStopsProbingOn429`, `TestCodexDuplexBootstrapPreservesFollowup`, `TestCodexDuplexLaterCredentialFailure`, `TestCodexDuplexInitialFailure` (failover branch only).
  - Pre-existing flake fixed on the way: `TestServerResponsesWebsocketReauthorizesEachTurn` (`internal/api`) failed about 3 of 4 package runs on the base commit because the hijacked websocket handler wrote its request log into the test's TempDir after the test returned; the test now waits for the handler before cleanup.

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- On a copy of the live config with a throwaway `api-keys.claude` entry: confirm a default profile never selects it; set `providers.claude.fallback.target: api` for one client key, force a subscription failure (disable the Claude credentials or use a model on cooldown) and confirm the request is served by the API key, the usage event carries that `auth_index`, and the next request returns to a subscription.
- Confirm `mode: api` for one client key and that the panel's current Routing profiles modal can still save a profile without wiping `providers`.

**Deployment**:
- Build from the branch in a temp worktree, smoke-start on a config copy with another port and an empty auth-dir, back up binary + `config.yaml` + plist, install and `launchctl kickstart -k gui/$(id -u)/local.clipproxyapi`, then verify `/v8/management/routing/profiles` through the CPAMP proxy.

**Follow-up plan (panel, repo CPA-Manager-Plus)** after this plan is merged and deployed:
- per-provider Subscription/API switch in the profile editor, API-key picker from `api_credentials`, three-state fallback (Off / Codex / API), and a third Guide tab with the API-fallback diagram (`/Users/echeg/Projects/CPA/diagrams/key-routing-fallback.workflow.json`, to be re-authored in English).

**CLIProxyAPIHome**:
- Home does not implement the provider policy; a cluster deployment needs matching selector support there before using these settings.
