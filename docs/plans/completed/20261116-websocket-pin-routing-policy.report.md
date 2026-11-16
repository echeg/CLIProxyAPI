# Report: Responses websocket pins follow the routing profile policy
plan: `docs/plans/20261116-websocket-pin-routing-policy.md` · branch: `websocket-pin-routing-policy` · base: `main` · mode: full · executor: claude · task model: claude:opus:high · review model: claude:opus:xhigh · started: 2026-11-16T15:56:11Z · finished: 2026-11-16T17:08:40Z

## Summary
On the Responses websocket, `routing.providers.<provider>.fallback.target: api` now works the same way as on HTTP, in both directions:

- **Leaving the API key once a subscription is back.** A websocket pin on a credential that the current routing profile policy rejects is released at the start of the next turn, provided another target can serve that turn. A typical case is the Codex API key chosen by the API fallback leg. The handler then does one of three things:
  - An incremental turn gets the `1012` "upstream requires HTTP replay" close.
  - A full `response.create` goes through a fresh selection and lands on the subscription.
  - In HTTP mode, the turn is rebuilt locally.
  While no subscription is available, the pin stays and turns stay incremental on the key, so there is no replay loop.
- **Reaching the API key when a pinned subscription fails.** `HasAlternativeTargetForModel` now also counts an available `api` fallback leg. A native-passthrough subscription pin that fails therefore reaches the API key:
  - A full create that gets 401 or 429 fails over in place.
  - An incremental turn that gets 429 or a quota error gets the replay close.
- **Hot reload.** Switching `mode` in a hot reload, and a pin restored after a provider switch, both go through the same release check.

Supporting changes:
- New exported method `Manager.RoutingPolicyAllowsAuth`.
- The candidate search was factored into the helper `hasAvailableTargetForModel`.
- Docs updated: `AGENTS.md`, `config.example.yaml`, `docs/management-api-v8.md`.
- Backlog entry `websocket-pin-keeps-api-fallback-key.md` closed.

**Fixes from review:**
- Internal review added the same policy check for the compaction replay target.
- The Codex review fix makes a model-less full create after a pin release inherit the connection's model.

**Run shape:**
- 6 task iterations, 0 failed retries.
- Internal review: 1 loop iteration, ended by `review_done`.
- Post-review: 1 iteration.

**Phase durations:**

| phase | duration_ms | approx. |
|---|---:|---|
| tasks | 947092 | 15m 47s |
| internal review | 2870787 | 47m 51s |
| external review | 222349 | 3m 42s |
| evaluation | 309180 | 5m 09s |
| other | 0 | 0s |

The total is 4349408 ms (about 72m 29s), which matches the span from start to finish. Finalize did not run, so there was no base sync, and the diff is against local `main`.

## Change scope
16 files changed: +1168 / −86, across 9 commits.

**Production code (3 files)**
- `sdk/cliproxy/auth/provider_policy.go`: new method `Manager.RoutingPolicyAllowsAuth`.
- `sdk/cliproxy/auth/conductor_selection.go`: `HasAlternativeTargetForModel` now counts the `api` fallback leg. The shared candidate search moved to the `hasAvailableTargetForModel` helper.
- `sdk/api/handlers/openai/openai_responses_websocket.go`:
  - the `releasePolicyRejectedAuth` turn-start check;
  - the same check for the compaction replay target;
  - model inheritance after a pin is released.

**Tests (5 files)**
- New `sdk/api/handlers/openai/openai_responses_websocket_policy_pin_test.go` (684 lines). Six test functions:
  - `TestResponsesWebsocketReleasesFallbackPinOnceSubscriptionRecovers`
  - `TestResponsesWebsocketReleasesRestoredFallbackPinAfterProviderSwitch`
  - `TestResponsesWebsocketCompactionReplayTargetFollowsRoutingPolicy`
  - `TestResponsesWebsocketReleasesPinAfterHotReloadModeSwitch`
  - `TestResponsesWebsocketPinnedSubscriptionFailureReachesAPIFallback`
  - `TestResponsesWebsocketPinnedSubscriptionFailureWithoutAPIFallback`
- New `sdk/cliproxy/auth/alternative_target_test.go` (184 lines). Four `TestHasAlternativeTargetForModel*` tests.
- `sdk/cliproxy/auth/provider_policy_test.go`: new `TestRoutingPolicyAllowsAuth`.
- `internal/pluginhost/rpc_client_error_test.go`: fixes the Retry-After expectation.
- `internal/pluginhost/host_test.go`: removes a `sync.Once` copy that vet flagged.

**Docs, config and backlog (8 files)**
- `AGENTS.md`
- `config.example.yaml` (comment only)
- `docs/management-api-v8.md`
- the plan file
- backlog:
  - added `websocket-alternative-check-ignores-rebind-never-binding.md`
  - modified `websocket-duplex-steering-bypasses-pin-policy.md` and `websocket-http-mode-pin-no-api-fallback.md` (stale line references replaced with symbol names)
  - deleted `websocket-pin-keeps-api-fallback-key.md`

**Commits (newest first)**
- `1671b009` fix: address codex review findings
- `cb92f2a8` docs: add backlog entry
- `6dcf470c` fix: address code review findings
- `453893d3` feat: document websocket pin routing policy and close backlog entry
- `93745a94` feat: verify websocket pin routing policy acceptance criteria
- `e49962ea` feat: test pinned subscription failure reaching the API fallback
- `d114ef66` feat: release policy-rejected websocket pins at turn start
- `d30c2058` feat: count available API fallback leg in HasAlternativeTargetForModel
- `18aec642` feat: add Manager.RoutingPolicyAllowsAuth

## Evidence
The run facts record no validation command (`runs: 0`), so no harness-recorded run shows a test failing before this change and passing after it. The only before/after proof comes from the external-review evaluator's report:

- **Codex finding, inherited model after a pin release.** The evaluator added the subtest `full create without model inherits the connection model` to `TestResponsesWebsocketReleasesFallbackPinOnceSubscriptionRecovers`.
  - Before the fix, it reproduced exactly the reported failure: `400 "missing model in response.create request"`.
  - After the fix in `openai_responses_websocket.go`, which sets `model` through `ojson` before normalization when neither passthrough nor `lastRequest` supplies it, the subtest passes.
  - The evaluator then reports `go test ./...` exiting 0 with 105 packages ok, a clean `go vet ./sdk/api/handlers/...`, empty `gofmt -l`, and a successful `go build -o test-output ./cmd/server && rm test-output`.
- **Pre-existing failure, recorded in plan Task 5.** `TestPluginErrorCarriesRetryAfterIntoCooldownAndHTTPHeader` expected `Retry-After: 7`, but the manager clamps quota cooldowns to a 10s floor. The expectation was corrected; the final diff uses 37s, which is above the floor.

The main-feature tests in the diff describe the new behavior, for example "incremental turn gets the replay close" and "full create 429 fails over in place to the API key". No recorded run shows them failing on `main`.

## Risk
**medium**

- **Public APIs.**
  - `Manager.RoutingPolicyAllowsAuth` is a new, additive exported SDK method.
  - `HasAlternativeTargetForModel` keeps its signature but now returns true in more cases (an available `api` fallback leg). Its only in-repo caller is the websocket handler, but embedding SDK users who call it directly will see the wider result.
- **Behavior visible to clients.** Long-lived Codex websocket connections now get `1012` replay closes when a subscription recovers or a hot reload changes `mode`. They also fail over to the API key instead of surfacing 401 or 429. This relies on the client handling `1012` mid-conversation; the plan lists that as manual post-completion verification against a real Codex CLI.
- **Data schemas and migrations:** none.
- **Configuration:** no new keys. Only a comment in `config.example.yaml` and the docs changed.
- **Concurrency.**
  - The new method reads `m.auths` under `m.mu.RLock`.
  - The handler check runs inside the per-connection turn loop and changes only connection-local pin state.
  - No new goroutines or timeouts.
- **Known gap.** Internal review filed a major backlog item: under `session-affinity-rebind-on: never`, pinned API-key turns are refused with 429, the same as on `main`. The change does not make this worse. Duplex steering successor turns and HTTP-mode compaction pins remain out of scope and are in the backlog.

## Merge danger
**Door:** two-way
A plain revert restores the previous pin behavior. There is no data or config format change, and the new exported method is not in a release yet.

**Blast radius:** websocket
Only Responses websocket connections under routing profiles with a provider policy (Codex/Claude `subscription`/`api` modes, `fallback.target: api`) are affected. HTTP routing is untouched.

## Migrations and operational steps
none

## Plan deviation
- **Added:**
  - ➕ The full-create 401 case in `TestResponsesWebsocketPinnedSubscriptionFailureReachesAPIFallback`; originally only 429 was covered. It is now a 401/429 table of subtests.
- **Blocked:** none
- **Skipped:** none

Other differences from the written plan:
- **Test harness.** Task 3 tests use a per-credential recording fake `codex` executor (`websocketPolicyPinExecutor`) instead of two `httptest` upstreams. This is noted in the plan; the handler paths under test are the same.
- **Outside the plan's file list.** Task 5 changed `internal/pluginhost`: the pre-existing Retry-After test fix, and removing a `sync.Once` copy flagged by vet. The internal-review fix commit later adjusted the expectation again, to 37s.
- **Added in internal review (`6dcf470c`).**
  - The compaction replay target (`observedCompactionReplayAuthID`) goes through the same policy release, with `TestResponsesWebsocketCompactionReplayTargetFollowsRoutingPolicy` (three subtests, including an HTTP-mode rebuild).
  - Stale line references in two backlog entries were replaced with symbol names.
  - The plan said "everything after is existing code" for the turn-start check; this goes beyond it.
- **Added in external review (`1671b009`).** Model inheritance for model-less full creates after a pin release or drop.

## Backlog
- `docs/backlog/websocket-alternative-check-ignores-rebind-never-binding.md` ("Pinned API-key websocket turns are refused by rebind-on never session bindings"): filed in internal review, major, left open for follow-up.
- Also touched:
  - `websocket-pin-keeps-api-fallback-key.md` was deleted; this plan resolves it.
  - `websocket-duplex-steering-bypasses-pin-policy.md` and `websocket-http-mode-pin-no-api-fallback.md` got reference updates and remain open, out of scope.

## External review
### codex:gpt-6-astra:xhigh
- label: codex
- iterations: 2
- duration_ms: 531503
- ended by: done
- had findings: true

Findings:
- Iteration 1: [P2] Preserve the inherited model when releasing a pin. After an API-key turn and a subscription recovery, a full `response.create` without `model` returned `400 "missing model in response.create request"`. -> fixed (`1671b009`, with the regression subtest `full create without model inherits the connection model`)
- Iteration 2: NO ISSUES FOUND.

## Validation
- Commands: none
- Timings: duration_ms: 0, runs: 0