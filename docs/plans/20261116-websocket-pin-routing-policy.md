# Responses websocket pins follow the routing profile policy

## Overview
- With `routing.providers.codex.fallback.target: api`, an HTTP request returns to the subscriptions on the next request because the API fallback leg runs without session affinity. A Responses websocket connection does not: when a turn is served by a Codex API key over an upstream websocket, the handler pins that credential (`rememberPinnedAuth`, `sdk/api/handlers/openai/openai_responses_websocket.go:1123`, `:1175`, `:1184`). Pinned executions skip the provider policy (`authSelectionEligibilityForRequest`, `sdk/cliproxy/auth/conductor_selection.go:111-126`) and the fallback (`providerFallbackFor`, `sdk/cliproxy/auth/provider_fallback.go:34`), so every later turn of that connection bills the API key even after the subscriptions recover (backlog `docs/backlog/websocket-pin-keeps-api-fallback-key.md`, major, billing).
- Reverse problem, also in scope: a turn pinned to a Codex subscription in native websocket passthrough mode that fails with 401/429 on a full `response.create`, or with 429/quota on an incremental turn, when no other subscription exists returns the error to the client instead of reaching the profile's API-key fallback, because the replay guard's alternative-target check sees only primary-policy credentials (`openai_responses_websocket.go:1063`, `:1069-1071`). An incremental turn that fails with 401 already gets the replay close today (`:1069` checks for an alternative only on 429/quota).
- Out of scope (filed in the backlog): successor turns forwarded by the Codex duplex executor under `codex.response-steering` (`docs/backlog/websocket-duplex-steering-bypasses-pin-policy.md`), and pins in HTTP upstream mode after a local compaction turn, which still return the error for the failing turn (`docs/backlog/websocket-http-mode-pin-no-api-fallback.md`).
- Goal: on the Responses websocket, `fallback.target: api` behaves like on HTTP in both directions.
- Integration: one new exported manager method, an extension of `HasAlternativeTargetForModel` (whose only caller is the websocket handler), and one turn-start check in the websocket handler that reuses the existing replay-close and fresh-selection paths.

## Decisions
- **Context**: the user chose the base branch `merge-upstream-20261116` (local upstream merge of 2026-11-16, which touches the same websocket handler and `sdk/cliproxy/auth` files) and the scope "both directions" (API pin release and subscription -> API fallback for pinned turns). The mechanism decisions below were decided from repository evidence and accepted by the user as a whole.
- **Chosen approach**:
  1. New exported `Manager.RoutingPolicyAllowsAuth(ctx context.Context, authID string) bool` in `sdk/cliproxy/auth/provider_policy.go`: `profile := routingProfileFromContext(m.ensureRoutingProfile(ctx))`; true when `profile == nil || !profile.providerPolicy` (Home resolves to a nil profile and a scheduler plugin to `providerPolicy = false`, `routing_profile.go:113-125`); look up the auth under `m.mu.RLock`, true for an empty or unknown ID (the handler already drops pins whose credential is gone, `openai_responses_websocket.go:706-716`, and a Home-runtime credential lives only in the execution session, `:568-572`); otherwise `providerPolicyAllows(profile, auth)`. It never applies `requiredKind`, `credentialPolicy`, or `disallowFreeAuth`.
  2. `HasAlternativeTargetForModel` (`sdk/cliproxy/auth/conductor_selection.go:2841`) also counts an available API fallback leg: when no primary candidate exists, it resolves `providerFallbackFor` and, only for target `api`, re-runs the candidate search in the context of `withRoutingFallbackLeg(withRoutingProfile(ctx, profile.apiFallbackProfile(provider)), target)` with only that provider. `excludedAuthID` still applies. Target `codex` (and its `always` mode) is not counted: the only caller checks pins in native websocket passthrough mode, which exists only for `codex`/`xai` credentials (`openai_responses_websocket.go:583`, `:735`, `:1276`), and a Codex pin under a Claude model is dropped by the model check (`:713`). The candidate search is factored into a helper, not duplicated.
  3. Websocket handler, at turn start, after the pinned-auth validation and restoration (`openai_responses_websocket.go:706-730`, so a pin restored from `pinnedAuthByProvider` is checked too) and before the passthrough decision (`:731`): when `pinnedAuthID != ""`, the pinned credential is rejected by `RoutingPolicyAllowsAuth`, and `responsesWebsocketHasAlternativeReplayTarget(executionParent, requestModelName, payload, pinnedAuthID)` is true, call `forgetPinnedAuth()` (clears `pinnedAuthID` and `pinnedAuthByProvider`) and log at info with the credential's `auth_index` (never keys). Plugin executor routes need no extra condition: `HasAlternativeExecutionTarget` already returns false for them (`sdk/api/handlers/handlers_stream.go:40-42`). The existing code then handles the turn: an incremental turn (`previous_response_id` or `type: append`) closes the socket with the HTTP-replay-required close (`:745-756`, close code 1012); a full `response.create` goes through a fresh selection and lands on the subscriptions. With no alternative the turn stays incremental on the API key.
  4. Reverse direction needs no extra handler code for native passthrough pins: the checks at `:1063` and `:1070` now see the fallback leg, so a full create fails over in place (the unpinned wrapper runs the API leg) and an incremental 429/quota turn gets a replay close.
  - Data flow: turn N -> subscription 429 -> API fallback leg -> handler pins the API key -> turn N+1: the policy rejects the pin -> subscriptions still cooling down: stay on the API key; subscriptions available: release -> replay -> subscription.
  - Errors: after a replay, a subscription that fails again falls back to the API key as on HTTP; a fallback leg that cannot select a credential makes the alternative check false, so the client gets the original error as today.
- **Rejected alternatives**: none rejected by the user. The alternatives of the decided mechanism forks (marking the selection in the API leg, releasing every turn, keeping a transcript in the handler, a handler-level reverse check, limiting the rule to fallback API keys, an unexported wrapper through `BaseAPIHandler`) were not chosen on repository evidence and stay open to critique.
- **Verified facts**:
  - `rememberPinnedAuth` / `forgetPinnedAuth` closures: `openai_responses_websocket.go:589-609`; pin restoration from `pinnedAuthByProvider`: `:717-730`; native passthrough requires pinned == upstream auth (`responsesWebsocketNativePassthroughAllowed`, `:1275`).
  - In WS upstream mode the handler drops its transcript (`lastRequest = nil`, `:1187`), so a turn cannot be rebuilt locally; the replay-required close path exists (`:745-756`).
  - `HasAlternativeTargetForModel` is cooldown-aware (`availableAuthsForSelector`, `conductor_selection.go:~2903`) and its only non-test caller chain is `responsesWebsocketHasAlternativeReplayTarget` (`openai_responses_websocket_session.go:91-95`) -> `BaseAPIHandler.HasAlternativeExecutionTarget` (`sdk/api/handlers/handlers_stream.go:28-74`), which strips pinned/excluded/session/callback metadata before calling the manager.
  - `apiFallbackProfile` (`provider_fallback.go:126`), `withRoutingFallbackLeg` / `routingFallbackTarget` (`routing_profile.go:186-205`), `providerPolicyAllows` (`provider_policy.go:16`) exist.
  - The selected-auth callback carries only the auth ID (`conductor_execution.go:1980`).
  - Request-level `credentialPolicy` comes only from the Codex Alpha Search route (`internal/api/server_routes.go:418`), not from the websocket handler.
  - Docs describe the current behavior as an exception: `docs/management-api-v8.md` ~:303-306 (API leg section) and ~:314 (policy skipped for pinned executions); `config.example.yaml` ~:224.
  - Known flaky test filed in `docs/backlog/flaky-websocket-pinned-auth-replay-close.md`; do not chase it.
  - Test fixtures: `newAPIFallbackFixture`, `apiFallbackPolicy`, `apiFallbackConfig` (`sdk/cliproxy/auth/provider_fallback_test.go:35-88`), `withAPIModeRouting` (`provider_policy_helpers_test.go:10`). The handler package has no `withAPIModeRouting`; handler tests set `cfg.Routing.Providers.Codex = ...`, give the API key `Attributes["api_key"]` and `Index = <auth index>`, and call `manager.SetConfig(cfg)` (`sdk/api/handlers/openai/openai_responses_steering_error_test.go:240-250`). Cooldown is expressed with `ModelStates[model]` (`Unavailable: true`, `NextRetryAfter` in the future or the past), changed between turns through `manager.Update` / `manager.Modify` (`sdk/cliproxy/auth/conductor_lifecycle.go:295`, `:303`); `HasAlternativeTargetForModel` uses `time.Now()` and takes no clock, so no `time.Sleep`.

## Context (from discovery)
- Files/components involved:
  - `sdk/cliproxy/auth/provider_policy.go` (new method), `conductor_selection.go` (`HasAlternativeTargetForModel`), `provider_fallback.go` (read: `providerFallbackFor`, `apiFallbackProfile`)
  - `sdk/api/handlers/openai/openai_responses_websocket.go` (turn-start check), `openai_responses_websocket_session.go` (read)
  - `docs/management-api-v8.md`, `config.example.yaml`, `docs/backlog/websocket-pin-keeps-api-fallback-key.md`
- Related patterns: per-key `routingProfile` resolved via `ensureRoutingProfile` and carried in the context; `authSelectionEligibility.allows` as the single policy gate; websocket tests with fake executors and two-turn flows in `openai_responses_websocket_test.go` / `openai_responses_websocket_loss_test.go`; `withAPIModeRouting` test helpers (AGENTS.md).
- Dependencies: none new.

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Make small, focused changes; follow `AGENTS.md` (KISS, English comments, `errX :=` shadow naming, logrus structured logging without secrets, moego `json`/`ojson` only, no `time.Sleep` in cooldown tests — use explicit timestamps / `nowFunc`)
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new and modified functions
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- After Go changes in every task: `gofmt -w` on touched files, then `go build -o test-output ./cmd/server && rm test-output`
- Existing websocket tests must keep passing without changes to their expectations

## Testing Strategy
- **Unit tests**: required for every task. Run the touched packages per task: `go test ./sdk/cliproxy/auth/... ./sdk/api/handlers/...`
- **Full suite**: `go test ./...` in the verification task
- **E2E tests**: the project has no UI e2e suite; the websocket two-turn tests in `sdk/api/handlers/openai` are the end-to-end coverage for this change

## Progress Tracking
- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Update plan if implementation deviates from original scope
- Keep plan in sync with actual work done

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): tasks achievable within this codebase - code changes, tests, documentation updates
- **Post-Completion** (no checkboxes): manual testing against a real Codex client, deployment
- **Checkbox placement**: Checkboxes belong only in Task sections. Do not put checkboxes in Overview, Context, Decisions, or Decision Log.

## Decision Log
- 2026-11-16 brainstorm: **accepted** - recognize a fallback pin by a turn-start check of the pinned credential against the current profile policy through a new `Manager.RoutingPolicyAllowsAuth`; it also catches hot-reload mode switches. Evidence: the policy lives in `authSelectionEligibility.allows` (`provider_policy.go:16`, AGENTS.md); the selected-auth callback carries only the auth ID (`conductor_execution.go:1980`).
- 2026-11-16 brainstorm: **accepted** - while subscriptions are unavailable, stay incremental on the API key and release only when `HasAlternativeTargetForModel` excluding that key finds an available subscription. Evidence: the existing guard does not replay without an alternative (`openai_responses_websocket.go:1069-1071`); `HasAlternativeTargetForModel` is cooldown-aware (`availableAuthsForSelector`).
- 2026-11-16 brainstorm: **accepted** - return to subscriptions through `forgetPinnedAuth()` and the existing paths (replay close for incremental turns, fresh selection for a full create). Evidence: WS mode drops the transcript (`openai_responses_websocket.go:1187`); the replay path exists (`:745-756`).
- 2026-11-16 brainstorm: **accepted** - for the reverse direction, `HasAlternativeTargetForModel` counts an available fallback leg (via `providerFallbackFor` and the derived profile); the existing failover-in-place / replay path does the rest. Evidence: its only caller is the websocket (`openai_responses_websocket_session.go:95` via `handlers_stream.go:74`), and pin metadata is already stripped there (`handlers_stream.go:28-50`).
- 2026-11-16 brainstorm: **accepted** - the release rule applies to any pinned credential the current policy rejects (fallback API key, or a credential after a hot-reload `mode` change); Home and scheduler plugins are untouched because the policy does not apply there. Evidence: request-level `credentialPolicy` comes only from the Alpha Search route (`internal/api/server_routes.go:418`), not the websocket.
- 2026-11-16 brainstorm: **accepted** - ⚠️ add the exported, additive SDK method `Manager.RoutingPolicyAllowsAuth` in `sdk/cliproxy/auth`. Evidence: handler packages reach the manager only through exported methods, like `HasAlternativeTargetForModel`.
- 2026-11-16 grill: **accepted** - `RoutingPolicyAllowsAuth` (Decisions 1, Task 1) checks the profile first and returns true for an unknown ID; it calls `providerPolicyAllows` directly instead of `allows` - decided by grill: Home resolves to a nil profile and a plugin to `providerPolicy = false` (`routing_profile.go:113-125`), Home-runtime pins live only in the execution session (`openai_responses_websocket.go:568-572`), and `allows` also applies `requiredKind`/`credentialPolicy`/`disallowFreeAuth` (`conductor_selection.go:128-142`); returning false for an unknown ID would release Home pins on every turn
- 2026-11-16 grill: **accepted** - `HasAlternativeTargetForModel` counts only an `api` fallback leg; the `codex` target and its `always` rule are dropped from Decisions 2, Task 2, and Technical Details - decided by grill: the only caller checks native passthrough pins, which exist only for `codex`/`xai` (`openai_responses_websocket.go:583`, `:735`, `:1276`), and a Codex pin under a Claude model is dropped by the model check (`:713`)
- 2026-11-16 grill: **accepted** - the turn-start release check moved after pin validation and restoration (before ~:731) and covers a pin restored from `pinnedAuthByProvider`; added a provider-switch round-trip test (Decisions 3, Task 3) - decided by grill: the restoration block can install a provider pin after the active one is cleared (`openai_responses_websocket.go:713-730`)
- 2026-11-16 grill: **accepted** - dropped the separate plugin-route condition from the turn-start check - decided by grill: `HasAlternativeExecutionTarget` returns false for plugin executor routes (`sdk/api/handlers/handlers_stream.go:40-42`)
- 2026-11-16 grill: **accepted** - Overview and Task 4 state that incremental 401 already replays and only 429/quota changes there; Task 4 adds a 401 regression check - decided by grill: `openai_responses_websocket.go:1069` checks for an alternative only on 429/quota
- 2026-11-16 grill: **accepted** - Task 2 adds baseline tests before the refactor and runs `./sdk/api/handlers/...` too - decided by grill: no test calls `HasAlternativeTargetForModel` directly and its only caller is the websocket handler (`handlers_stream.go:74`)
- 2026-11-16 grill: **accepted** - Tasks 1-4 name the existing fixtures and the cooldown idiom, Task 3 specifies the shared websocket setup (separate upstreams, `SetConfig`, `ModelStates` moved with `Update`/`Modify`), case (a) asserts close 1012 and no turn-2 message on the API-key upstream, the hot-reload test names its steps and negative case, and Task 4 replaces the trace step with assertions - decided by grill: `provider_fallback_test.go:35-88`, `provider_policy_helpers_test.go:10`, `openai_responses_steering_error_test.go:240-250`, `openai_responses_websocket_test.go:4351`, `conductor_lifecycle.go:295-303`; a new socket picks the subscription regardless of the release
- 2026-11-16 grill: **accepted** - Task 6 also updates `docs/management-api-v8.md` ~:426 and ~:682 and makes the `AGENTS.md` change unconditional - decided by grill: both passages describe pinned executions ignoring the policy, and the `AGENTS.md` `sdk/cliproxy/auth/` line says the policy is "skipped for pinned requests"
- 2026-11-16 grill: **accepted** - duplex successor turns under `codex.response-steering` are out of scope, filed as `docs/backlog/websocket-duplex-steering-bypasses-pin-policy.md` and noted in the Overview and docs task
- 2026-11-16 grill: **accepted** - the reverse-direction promise is narrowed to native websocket passthrough pins; HTTP-mode pins after local compaction are filed as `docs/backlog/websocket-http-mode-pin-no-api-fallback.md` and noted in the Overview and docs task

## Implementation Steps

### Task 1: Add `Manager.RoutingPolicyAllowsAuth`
- [x] add `func (m *Manager) RoutingPolicyAllowsAuth(ctx context.Context, authID string) bool` to `sdk/cliproxy/auth/provider_policy.go` with exactly this order: nil manager -> true; `profile := routingProfileFromContext(m.ensureRoutingProfile(ctx))`, `profile == nil || !profile.providerPolicy` -> true; look up the auth under `m.mu.RLock`, empty or unknown ID -> true; return `providerPolicyAllows(profile, auth)`. Do not build `authSelectionEligibilityForRequest` and do not call `allows` (no `requiredKind`, `credentialPolicy`, or `disallowFreeAuth`)
- [x] write the doc comment: reports whether the request's routing profile would select the credential in the primary leg under its per-provider policy; true when no policy applies (Home, scheduler plugin, no profile, provider without a policy) and for an unknown ID, so callers never release a pin they cannot judge
- [x] write table-driven tests in `sdk/cliproxy/auth/provider_policy_test.go` using `apiFallbackConfig` / `withAPIModeRouting`: subscription mode with OAuth (true) and API key (false); api mode with the named API key (true), another API key (false), OAuth (false); provider without policy (true); unknown and empty ID (true); Home enabled and plugin scheduler with an API key under subscription mode (true); no `providers` block (default subscription: API key false); a key profile from the client key in ctx overrides the shared profile
- [x] run `go test ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 2

### Task 2: Count an available fallback leg in `HasAlternativeTargetForModel`
- [x] before refactoring, add baseline tests for today's primary-only behavior in a new `sdk/cliproxy/auth/alternative_target_test.go` (no direct test of `HasAlternativeTargetForModel` exists): another eligible subscription available -> true; only the excluded credential -> false; the other subscription cooling down (`ModelStates` with a future `NextRetryAfter`) -> false
- [x] factor the candidate collection and availability check of `HasAlternativeTargetForModel` (`conductor_selection.go:2841-2905`) into an unexported helper taking ctx, providers, model, opts, excludedAuthID, so the primary and fallback checks share one path
- [x] after a primary check that finds nothing (native routing only; Home keeps `hasAlternativeHomeTargetForModel`), resolve `m.providerFallbackFor(ctx, providers, req, opts)` with `req.Model = model` on the profile-carrying ctx (`m.ensureRoutingProfile`); only for target `api`, call the helper with `withRoutingFallbackLeg(withRoutingProfile(ctx, profile.apiFallbackProfile(fallback.provider)), fallback.target)` and providers `[fallback.provider]`; any other target or no fallback -> false; `excludedAuthID` still applies
- [x] write tests with `newAPIFallbackFixture` / `apiFallbackConfig`: codex subscription mode with the only subscription excluded and `target: api` -> API key available (true), cooling down (false), disabled (false), the API key itself excluded (false); no fallback configured (false); `claude.fallback.target: codex` is not counted (false when no primary candidate); a primary alternative returns true without consulting the fallback; the same cases for `claude` with `target: api`
- [x] run `go test ./sdk/cliproxy/auth/... ./sdk/api/handlers/...` (the websocket handler is the only caller, so the existing websocket pinned-failure tests run here), gofmt, compile check - must pass before task 3

### Task 3: Release policy-rejected pins at websocket turn start
- [x] in `ResponsesWebsocket` (`openai_responses_websocket.go`), after the pinned-auth validation and restoration (~:706-730) and before `useUpstreamWebsocketPassthrough` is computed (~:731): when `pinnedAuthID != ""`, `!h.AuthManager.RoutingPolicyAllowsAuth(executionParent, pinnedAuthID)`, and `h.responsesWebsocketHasAlternativeReplayTarget(executionParent, requestModelName, payload, pinnedAuthID)` is true, call `forgetPinnedAuth()` and log at info `responses websocket: releasing pinned auth rejected by routing policy` with `id` and the credential's `auth_index` (no key material); guard against a nil `h.AuthManager`
- [x] test setup for this task and task 4 (shared helper in the new file `sdk/api/handlers/openai/openai_responses_websocket_policy_pin_test.go`): one Codex subscription and one Codex API key (`Attributes["api_key"]`, `Index = <idx>`, both with `websockets: true`) on separate `httptest` upstreams so each turn is attributed by server; `cfg.Routing.Providers.Codex = {APICredential: <idx>, Fallback: {Target: api}}` applied with `manager.SetConfig`; turn 1 falls back because the subscription is registered with `ModelStates[model] = {Unavailable: true, NextRetryAfter: now+1h}`; before turn 2 the state is moved to the past with `manager.Update`/`Modify` for "recovered", or left as is for "cooling down"; no `time.Sleep`
  - note: implemented with a per-credential recording fake `codex` executor (`websocketPolicyPinExecutor`) instead of two `httptest` upstreams; each turn is attributed by credential ID, and the handler decides WS upstream mode from the credential attributes, so the handler paths under test are the same
- [x] write two-turn websocket tests: (a) subscription recovered, incremental turn 2 -> close code 1012 with reason "upstream requires HTTP replay" (close assertion modelled on `TestResponsesWebsocketReplaysImmediatelyAfterPinnedAuthFailure`, `openai_responses_websocket_test.go:6525`; a one-off 1006 is the known flake in `docs/backlog/flaky-websocket-pinned-auth-replay-close.md`) and the API-key upstream receives no turn-2 message; (b) subscription still cooling down -> turn 2 stays incremental on the API-key upstream, no close; (c) subscription recovered, full `response.create` turn 2 -> served by the subscription upstream on the same socket
- [x] write a provider-switch round-trip test: turn 1 pins the Codex API key through the fallback, turn 2 uses a model of another provider (the active pin is cleared by the model check at ~:713 but kept in `pinnedAuthByProvider`), the subscription recovers, turn 3 returns to the Codex model -> the restored API pin is released (no turn-3 message on the API-key upstream)
- [x] write hot-reload tests: turn 1 on the subscription over the upstream websocket, then `manager.SetConfig` with Codex `{Mode: api, APICredential: <idx>}` -> incremental turn 2 gets close 1012; negative case without a mode change -> turn 2 stays on the subscription, no close, and no "releasing pinned auth" log line (logrus test hook)
- [x] run `go test ./sdk/api/handlers/... ./sdk/cliproxy/auth/...` (re-run the known flaky test once if it fails, do not modify it), gofmt, compile check - must pass before task 4

### Task 4: Reverse direction - pinned subscription failure reaches the API fallback
- [ ] write a websocket test (setup from task 3, subscription pinned over its upstream websocket in turn 1, then the subscription upstream answers turn 2 with 429): full `response.create` turn 2 -> the API-key upstream receives the turn and the client gets `response.completed` with no error event on the same socket (proves the failover iteration at ~:1129-1145 runs unpinned and reaches the API leg); if it fails, add a ➕ task for the handler change instead of widening this one
- [ ] write a websocket test: incremental turn 2 whose pinned subscription returns 429 -> close 1012, the API-key upstream receives nothing on this socket
- [ ] write regression tests: the same setups with the API key cooling down or `target: off` -> the 429 error is exposed to the client as today; an incremental turn whose pinned subscription returns 401 -> close 1012 (unchanged behavior, `:1069` checks for an alternative only on 429/quota)
- [ ] confirm `TestResponsesWebsocketPinned429ExposesErrorWhenOnlyAlternativeHasExpiredToken` and the other pinned-failure tests in `openai_responses_websocket_loss_test.go` / `openai_responses_websocket_test.go` pass unchanged
- [ ] run `go test ./sdk/api/handlers/... ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 5

### Task 5: Verify acceptance criteria
- [ ] verify each Overview goal against the code and tests: API pin released once a subscription is available (incremental: replay close; full create: subscription), no replay loop while subscriptions cool down, pinned native-passthrough subscription failure reaches the API fallback (full create 401/429, incremental 429), hot-reload mode switch and provider-switch restoration release a rejected pin
- [ ] verify Home and scheduler-plugin setups keep today's websocket pin behavior (policy not applied)
- [ ] run the full suite `go test ./...` (known flaky tests in `docs/backlog/` may be re-run once; do not modify them)
- [ ] run `gofmt -l .` (no output) and the compile check `go build -o test-output ./cmd/server && rm test-output`
- [ ] run `go vet ./sdk/cliproxy/auth/... ./sdk/api/handlers/...` and fix findings in touched code

### Task 6: Update documentation and close the backlog entry
- [ ] `docs/management-api-v8.md`: replace the websocket exception in the API-leg rules (~:303-306) with the new behavior (the pin is kept while subscriptions are unavailable and released at the next turn once one is; an incremental turn then gets the replay close), extend the pinned-execution notes (~:314, ~:426, ~:682) with the turn-start policy check and the reverse-direction failover for native websocket passthrough pins, and state the two remaining limits (duplex successor turns under `codex.response-steering`; HTTP-mode pins after local compaction return the error for the failing turn)
- [ ] `config.example.yaml` (~:224): update the comment on the websocket behavior to match
- [ ] `AGENTS.md`: in the `sdk/cliproxy/auth/` line, after "skipped for pinned requests", add that the Responses websocket handler releases a pin the policy rejects at turn start (`Manager.RoutingPolicyAllowsAuth`) and that `HasAlternativeTargetForModel` counts an available `api` fallback leg
- [ ] delete `docs/backlog/websocket-pin-keeps-api-fallback-key.md`

## Technical Details
- **New method**:
  ```go
  // RoutingPolicyAllowsAuth reports whether the routing profile in ctx would
  // select authID in the primary leg under its per-provider policy.
  func (m *Manager) RoutingPolicyAllowsAuth(ctx context.Context, authID string) bool
  ```
- **Alternative-target flow** (`HasAlternativeTargetForModel`):
  1. Home -> unchanged Home check.
  2. Primary: helper(ctx, providers, model, opts, excluded) -> true returns true.
  3. Fallback: `providerFallbackFor` -> only `api`: helper(apiLegCtx, [provider], model, opts, excluded); any other target -> false.
- **Handler turn start** (after pin validation/restoration ~:706-730, before ~:731): `pinned && !RoutingPolicyAllowsAuth && HasAlternativeReplayTarget(excluding pinned)` -> `forgetPinnedAuth()`; everything after is existing code. Plugin routes are covered because `HasAlternativeExecutionTarget` returns false for them.
- **Turn outcomes after a release**:
  | turn type | upstream mode | outcome |
  |---|---|---|
  | incremental (`previous_response_id` / `append`) | WS | replay-required close (1012); client replays on a new socket |
  | full `response.create` | any | fresh selection, subscription serves |
  | incremental | HTTP | local transcript rebuild, fresh selection |

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- With a real Codex CLI over the Responses websocket and a key profile with `providers.codex.fallback.target: api`: exhaust or disable the subscription, confirm a turn goes to the API key, re-enable the subscription, and confirm the next turn reconnects (replay) and is served by the subscription; check the usage events (`auth_index`) for the switch.
- Confirm the Codex CLI handles the 1012 replay close mid-conversation without user-visible errors.

**Deployment**:
- Builds on the upstream merge of 2026-11-16 (`merge-upstream-20261116`, fast-forwarded into the local `main`); deploy after `main` with that merge is pushed.
