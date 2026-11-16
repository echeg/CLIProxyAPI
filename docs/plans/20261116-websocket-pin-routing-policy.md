# Responses websocket pins follow the routing profile policy

## Overview
- With `routing.providers.codex.fallback.target: api`, an HTTP request returns to the subscriptions on the next request because the API fallback leg runs without session affinity. A Responses websocket connection does not: when a turn is served by a Codex API key over an upstream websocket, the handler pins that credential (`rememberPinnedAuth`, `sdk/api/handlers/openai/openai_responses_websocket.go:1123`, `:1175`, `:1184`). Pinned executions skip the provider policy (`authSelectionEligibilityForRequest`, `sdk/cliproxy/auth/conductor_selection.go:111-126`) and the fallback (`providerFallbackFor`, `sdk/cliproxy/auth/provider_fallback.go:34`), so every later turn of that connection bills the API key even after the subscriptions recover (backlog `docs/backlog/websocket-pin-keeps-api-fallback-key.md`, major, billing).
- Reverse problem, also in scope: a turn pinned to a Codex subscription that fails with 401/429 when no other subscription exists returns the error to the client instead of reaching the profile's API-key fallback, because the replay guard's alternative-target check sees only primary-policy credentials (`openai_responses_websocket.go:1063`, `:1069-1071`).
- Goal: on the Responses websocket, `fallback.target: api` behaves like on HTTP in both directions.
- Integration: one new exported manager method, an extension of `HasAlternativeTargetForModel` (whose only caller is the websocket handler), and one turn-start check in the websocket handler that reuses the existing replay-close and fresh-selection paths.

## Decisions
- **Context**: the user chose the base branch `merge-upstream-20261116` (local upstream merge of 2026-11-16, which touches the same websocket handler and `sdk/cliproxy/auth` files) and the scope "both directions" (API pin release and subscription -> API fallback for pinned turns). The mechanism decisions below were decided from repository evidence and accepted by the user as a whole.
- **Chosen approach**:
  1. New exported `Manager.RoutingPolicyAllowsAuth(ctx context.Context, authID string) bool` in `sdk/cliproxy/auth/provider_policy.go`: resolves the profile via `ensureRoutingProfile`, builds the eligibility without a pin, and applies `allows`. Returns true when no policy applies (Home, scheduler plugin, no profile, provider other than `claude`/`codex`); false for an unknown auth ID.
  2. `HasAlternativeTargetForModel` (`sdk/cliproxy/auth/conductor_selection.go:2841`) also counts an available fallback leg: when no primary candidate exists, it resolves `providerFallbackFor`; for target `api` it re-runs the candidate search in the context of `apiFallbackProfile(provider)` with only that provider; for target `codex` with `requiredKind` oauth and the mapped model. `excludedAuthID` still applies. Mode `always` is not relevant here (that fallback already runs first). The candidate search is factored into a helper, not duplicated.
  3. Websocket handler, at turn start (before the pinned-auth restoration, around `openai_responses_websocket.go:706`): when the pinned credential is rejected by `RoutingPolicyAllowsAuth` AND `responsesWebsocketHasAlternativeReplayTarget(ctx, model, payload, pinnedAuthID)` is true, call `forgetPinnedAuth()` (clears `pinnedAuthID` and `pinnedAuthByProvider`) and log at info with the credential's `auth_index` (never keys). The existing code then handles the turn: an incremental turn (`previous_response_id` or `type: append`) closes the socket with the HTTP-replay-required close (`:745-756`, close code 1012); a full `response.create` goes through a fresh selection and lands on the subscriptions. With no alternative the turn stays incremental on the API key.
  4. Reverse direction needs no extra handler code: the checks at `:1063` and `:1070` now see the fallback leg, so a full create fails over in place (the unpinned wrapper runs the API leg) and an incremental turn gets a replay close.
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

## Implementation Steps

### Task 1: Add `Manager.RoutingPolicyAllowsAuth`
- [ ] add `func (m *Manager) RoutingPolicyAllowsAuth(ctx context.Context, authID string) bool` to `sdk/cliproxy/auth/provider_policy.go`: nil manager or empty ID -> false for unknown ID / true when no policy applies; look up the auth under `m.mu.RLock`; return true when `!m.ClaudeFallbackSupported()`; resolve `m.ensureRoutingProfile(ctx)`; build `authSelectionEligibilityForRequest(ctx, cliproxyexecutor.Options{})` and return `eligibility.allows(auth)` restricted to the provider policy (do not let `disallowFreeAuth` or other unrelated eligibility rules leak in — document the choice in the doc comment)
- [ ] keep the doc comment precise: true when no policy applies (Home, scheduler plugin, no profile, provider without a policy), false for an unknown auth ID
- [ ] write table-driven tests in `sdk/cliproxy/auth/provider_policy_test.go`: subscription mode with OAuth (true) and API key (false); api mode with the named API key (true), another API key (false), OAuth (false); provider without policy (true); unknown ID (false); Home enabled and plugin scheduler (true); no `providers` block (default subscription: API key false)
- [ ] run `go test ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 2

### Task 2: Count an available fallback leg in `HasAlternativeTargetForModel`
- [ ] factor the candidate collection and availability check of `HasAlternativeTargetForModel` (`conductor_selection.go:2841-2905`) into an unexported helper taking ctx, providers, model, opts, excludedAuthID, so the primary and fallback checks share one path
- [ ] after a primary check that finds nothing (native routing only; Home keeps `hasAlternativeHomeTargetForModel`), resolve `m.providerFallbackFor(ctx, providers, req, opts)` with `req.Model = model`; for target `api` call the helper with `withRoutingFallbackLeg(withRoutingProfile(ctx, profile.apiFallbackProfile(provider)), target)` and providers `[provider]`; for target `codex` with `withRequiredAuthKind(ctx, AuthKindOAuth)`, providers `["codex"]`, and the mapped fallback model; `excludedAuthID` applies to both
- [ ] skip the fallback check when `fallback.mode == "always"` (already the first leg) and when no fallback is resolved
- [ ] write tests in `sdk/cliproxy/auth/provider_fallback_test.go` (or a new `alternative_target_test.go`): codex subscription mode, only one subscription excluded, `target: api` with the API key available (true), cooling down (false), disabled (false), the API key itself excluded (false), no fallback configured (false); Claude `target: codex` with an available Codex OAuth credential (true); a primary alternative still returns true without consulting the fallback
- [ ] confirm existing `HasAlternativeTargetForModel` tests pass unchanged
- [ ] run `go test ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 3

### Task 3: Release policy-rejected pins at websocket turn start
- [ ] in `ResponsesWebsocket` (`openai_responses_websocket.go`, before the pinned-auth validation at ~:706), when `pinnedAuthID != ""`, the route is not a plugin executor route, `!h.AuthManager.RoutingPolicyAllowsAuth(executionParent, pinnedAuthID)`, and `h.responsesWebsocketHasAlternativeReplayTarget(executionParent, requestModelName, payload, pinnedAuthID)` is true: call `forgetPinnedAuth()` and log at info `responses websocket: releasing pinned auth rejected by routing policy` with `id` and the credential's `auth_index` (no key material)
- [ ] verify that after the release the existing flow runs unchanged: the restoration loop at ~:717-730 cannot restore the released pin (the map entry is gone), an incremental turn in WS upstream mode hits the replay close at ~:745-756, and a full `response.create` runs a fresh selection
- [ ] write two-turn websocket tests in `sdk/api/handlers/openai` (new file `openai_responses_websocket_policy_pin_test.go`): turn 1 served by the Codex API key through `target: api` with an upstream websocket; (a) before turn 2 the subscription cooldown has expired -> incremental turn 2 gets the replay close, and a full create on a new socket is served by the subscription; (b) the subscription is still cooling down -> turn 2 stays incremental on the API key, no close; (c) full `response.create` turn 2 with the subscription available -> served by the subscription on the same socket
- [ ] write a hot-reload test: a pinned Codex subscription, the key profile switched to `mode: api` with an available API credential -> next turn releases the pin; and a pinned credential the policy still allows is never released
- [ ] run `go test ./sdk/api/handlers/... ./sdk/cliproxy/auth/...` (re-run the known flaky test once if it fails, do not modify it), gofmt, compile check - must pass before task 4

### Task 4: Reverse direction - pinned subscription failure reaches the API fallback
- [ ] trace the failure path for a pinned Codex subscription that fails with 401/429 when no other subscription exists and `target: api` is set: confirm `replayPinnedAuthFailure` (~:1052-1075) now returns true because the alternative check sees the fallback leg; adjust the handler only if a path still exposes the error (record any change here with ➕)
- [ ] write websocket tests: full `response.create` turn whose pinned subscription returns 429 -> failover in place, served by the API key, no close; incremental turn whose pinned subscription returns 429 -> replay close; the same setups with the API key cooling down or `target: off` -> the error is exposed to the client as today
- [ ] confirm `TestResponsesWebsocketPinned429ExposesErrorWhenOnlyAlternativeHasExpiredToken` and the other pinned-failure tests in `openai_responses_websocket_loss_test.go` / `openai_responses_websocket_test.go` pass unchanged
- [ ] run `go test ./sdk/api/handlers/... ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 5

### Task 5: Verify acceptance criteria
- [ ] verify each Overview goal against the code and tests: API pin released once a subscription is available (incremental: replay close; full create: subscription), no replay loop while subscriptions cool down, pinned subscription failure reaches the API fallback, hot-reload mode switch releases a rejected pin
- [ ] verify Home and scheduler-plugin setups keep today's websocket pin behavior (policy not applied)
- [ ] run the full suite `go test ./...` (known flaky tests in `docs/backlog/` may be re-run once; do not modify them)
- [ ] run `gofmt -l .` (no output) and the compile check `go build -o test-output ./cmd/server && rm test-output`
- [ ] run `go vet ./sdk/cliproxy/auth/... ./sdk/api/handlers/...` and fix findings in touched code

### Task 6: Update documentation and close the backlog entry
- [ ] `docs/management-api-v8.md`: replace the websocket exception in the API-leg rules (~:303-306) with the new behavior (the pin is kept while subscriptions are unavailable and released at the next turn once one is; an incremental turn then gets the replay close), and extend the pinned-execution note (~:314) with the turn-start policy check and the reverse-direction failover
- [ ] `config.example.yaml` (~:224): update the comment on the websocket behavior to match
- [ ] `AGENTS.md`: if the `sdk/cliproxy/auth/` line mentions pinned requests skipping the policy, add that the websocket handler releases policy-rejected pins via `RoutingPolicyAllowsAuth`
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
  3. Fallback: `providerFallbackFor` -> `api`: helper(apiLegCtx, [provider], model, opts, excluded); `codex`: helper(oauthLegCtx, ["codex"], mappedModel, opts, excluded).
- **Handler turn start**: `pinned && !plugin && !RoutingPolicyAllowsAuth && HasAlternativeReplayTarget(excluding pinned)` -> `forgetPinnedAuth()`; everything after is existing code.
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
- Ships with the `merge-upstream-20261116` branch; deploy after that branch lands on `main`.
