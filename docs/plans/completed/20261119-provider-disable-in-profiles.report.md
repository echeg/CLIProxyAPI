# Report: 2026 11 19 provider disable in profiles
plan: /Users/echeg/Projects/CPA/CLIProxyAPI-migration/.loopai/worktrees/provider-disable-in-profiles/docs/plans/20261119-provider-disable-in-profiles.md | branch: provider-disable-in-profiles | base: main | mode: full | executor: claude | task model: claude:opus:high | review model: claude:opus:xhigh | started: 2026-11-19T10:02:24Z | finished: 2026-11-19T13:52:12Z

## Summary
- task iterations: 1
- failed retries: 0
- external reviewers: 1
- post-review iterations: 1
| phase | duration_ms |
|---|---:|
| evaluation | 7278069 |
| external review | 2937046 |
| internal review | 3517561 |
| other | 0 |
| tasks | 55235 |

## Change scope
- commits: 11
- files: 53
- additions: 5489
- deletions: 267
| status | path |
|---|---|
| M | AGENTS.md |
| M | config.example.yaml |
| A | docs/backlog/auto-model-ignores-routing-profile.md |
| A | docs/backlog/model-listing-counts-alias-pool-entries.md |
| A | docs/backlog/suffixed-only-registration-unservable.md |
| A | docs/backlog/websocket-pin-matcher-ignores-alias-selection-key.md |
| M | docs/management-api-v8.md |
| M | docs/plans/20261119-provider-disable-in-profiles.md |
| M | internal/api/handlers/management/routing_codex_speed_test.go |
| A | internal/api/handlers/management/routing_provider_disabled_test.go |
| A | internal/api/server_codex_search_provider_disabled_test.go |
| M | internal/client/codex/live/capabilities.go |
| M | internal/client/codex/live/capabilities_test.go |
| M | internal/client/codex/live/live.go |
| A | internal/client/codex/live/live_provider_disabled_test.go |
| M | internal/config/config_yaml.go |
| M | internal/config/provider_routing.go |
| M | internal/config/provider_routing_save_test.go |
| M | internal/config/provider_routing_test.go |
| M | internal/registry/model_registry.go |
| A | internal/registry/model_registry_first_available_test.go |
| M | internal/util/provider.go |
| A | sdk/api/handlers/claude/code_handlers_provider_disabled_test.go |
| M | sdk/api/handlers/gemini/gemini_handlers.go |
| A | sdk/api/handlers/gemini/gemini_stream_provider_disabled_test.go |
| M | sdk/api/handlers/gemini/interactions_handlers.go |
| M | sdk/api/handlers/handlers.go |
| A | sdk/api/handlers/handlers_auto_model_provider_disabled_test.go |
| M | sdk/api/handlers/handlers_execution.go |
| M | sdk/api/handlers/handlers_model_router_test.go |
| M | sdk/api/handlers/handlers_routing.go |
| M | sdk/api/handlers/handlers_speech_only_test.go |
| M | sdk/api/handlers/handlers_stream.go |
| M | sdk/api/handlers/model_execution.go |
| M | sdk/api/handlers/model_execution_test.go |
| M | sdk/api/handlers/openai/openai_handlers.go |
| M | sdk/api/handlers/openai/openai_images_handlers.go |
| M | sdk/api/handlers/openai/openai_responses_handlers.go |
| A | sdk/api/handlers/openai/openai_responses_provider_disabled_test.go |
| M | sdk/api/handlers/openai/openai_responses_websocket.go |
| M | sdk/api/handlers/openai/openai_responses_websocket_forward.go |
| A | sdk/api/handlers/openai/openai_responses_websocket_provider_disabled_test.go |
| M | sdk/api/handlers/openai/openai_responses_websocket_session.go |
| M | sdk/api/handlers/openai/openai_responses_websocket_test.go |
| A | sdk/api/handlers/openai/openai_stream_provider_disabled_test.go |
| M | sdk/api/handlers/openai_responses_stream_error.go |
| M | sdk/cliproxy/auth/alternative_target_test.go |
| M | sdk/cliproxy/auth/conductor_selection.go |
| M | sdk/cliproxy/auth/provider_fallback.go |
| M | sdk/cliproxy/auth/provider_fallback_test.go |
| M | sdk/cliproxy/auth/provider_policy.go |
| M | sdk/cliproxy/auth/provider_policy_test.go |
| M | sdk/cliproxy/service_executionregistry_test.go |

## Evidence
_assessment unavailable_

## Risk
_assessment unavailable_

## Merge danger
_assessment unavailable_

## Migrations and operational steps
_assessment unavailable_

## Plan deviation
_assessment unavailable_
### Added
- ➕ refuse a Claude Thread continuation owned by a disabled Claude when claude was dropped from the list or `auto` resolved away from it (`requestedAutoModel`); tests `TestDisabledClaudeRefusesOwnedThreadContinuation`, `TestDisabledClaudeRefusesOwnedThreadContinuationResolvedFromAuto`
- ➕ keep `type: permission_error` and `code: provider_disabled` on OpenAI-format refusals, including in-band stream errors after a keep-alive (`BuildErrorResponseBodyWithError`, `ResponsesStreamErrorTextWithError`); tests in `sdk/api/handlers/openai/openai_stream_provider_disabled_test.go` and `sdk/api/handlers/gemini/gemini_stream_provider_disabled_test.go`
- ➕ resolve `model: auto` with the request's profile (`BaseAPIHandler.AutoModelProviderFilter`, `util.ResolveAutoModelMatching`, registry `GetModelCountMatching`/`GetFirstAvailableModelMatching`/`GetFirstRegisteredModelMatching`, alias-pool entry counts in `clientModelCounts`); tests `sdk/api/handlers/handlers_auto_model_provider_disabled_test.go`, `internal/registry/model_registry_first_available_test.go`
- ➕ let the hangup of an open realtime call select its pinned credential through `WithOpenSessionPin`, and give realtime refusals the OpenAI type and code (`writeSelectionError`); tests `TestHandleHangupForwardsOpenCallAfterCodexDisabled`, `TestOpenSessionPinSkipsOnlyTheDisabledSwitch`, `TestHandlerRefusesDisabledCodex`, `TestRealtimeEntryPointsRefuseDisabledCodex`
- ➕ expose the refusal on the Responses websocket: `shouldExposeResponsesUpstreamError` closed the socket without an error event for a 403 (only terminal-auth, 429/cooldown and request faults were exposed), so the client saw an abnormal close and would reconnect in a loop; added the exported `coreauth.IsProviderDisabledError` and exposed it there (test `TestResponsesWebsocketPinnedTurnRefusedWhenCodexDisabled` in `openai_responses_websocket_provider_disabled_test.go`, full create and incremental)
- ➕ resolve `auto` per websocket turn with the key's profile (`responsesWebsocketResolvedModel`) and leave auths of disabled providers out of the replay capability checks (`responsesWebsocketAvailableAuthsForModel`); tests `TestResponsesWebsocketAutoModelKeepsCodexPinWhenProfileDisablesClaude`, `TestResponsesWebsocketCompactionReplayIgnoresDisabledProvider`, `TestResponsesWebsocketReleasesDisabledPinWhenAnotherProviderServes`
### Blocked
- none
### Skipped
- none

## Backlog
| status | path | title |
|---|---|---|
| A | docs/backlog/auto-model-ignores-routing-profile.md | `model: auto` ignores the key's provider mode |
| A | docs/backlog/model-listing-counts-alias-pool-entries.md | Model listing counts alias-pool entries instead of credentials |
| A | docs/backlog/suffixed-only-registration-unservable.md | A model registered only under its suffixed name cannot be served |
| A | docs/backlog/websocket-pin-matcher-ignores-alias-selection-key.md | Responses websocket drops a pin that selection accepts through an OAuth alias |

## External review
### codex:gpt-6.2-astra:xhigh
- label: codex
- iterations: 10
- duration_ms: 10215074
- ended by: max_iterations
- had findings: true
#### Iteration 1
- truncated: true
Reviewer output:
```
- **[P2] `auto` can return 403 despite an available enabled model.** `sdk/api/handlers/handlers_routing.go:185` selects an exact registry ID, but `sdk/api/handlers/handlers_routing.go:214` looks up its providers again after stripping the thinking suffix. If disabled Claude registers `shared` and enabled Codex registers `shared(high)`, `auto` can select the Codex alias and then route only to Claude. It returns 403 without trying another available Codex model. Resolve the model and its eligible providers together.
- **[P2] `auto` can return 503 instead of the promised 403.** When only a disabled provider has registrations, `internal/util/provider.go:120` returns its newest model so execution can issue the refusal. If that model is `gpt-image-2`, a `/v1/responses` request hits image-only validation at `sdk/api/handlers/handlers_routing.go:203` before reaching the disable gate. The client gets an endpoint error instead of `provider_disabled`. Preserve the all-providers-disabled result through validation.
- **[P2] A Responses websocket can lose its credential pin after switching models.** `sdk/api/handlers/openai/openai_responses_websocket.go:804` counts all registered providers when deciding whether to restore a remembered pin. If Claude is disabled and a model is shared by Claude and Codex, switching to an xAI model and back skips restoration because the set has two entries. Round-robin can then select a different Codex account even though the original is still available. Count only profile-enabled providers for this decision.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external findings with technical rigor.Still evaluating the three findings: I've read the auto-resolution and websocket pin-restore code and am now checking the provider-set helper.Checking how the auth manager keys model support when a model ID carries a thinking suffix (relevant to finding 1).Next I'll reuse an existing auto/disabled-provider test setup to check finding 1 empirically.I've confirmed all three findings against the code. Now checking ProviderEnabledForRequest semantics before deciding the fixes.Weighing fix options for finding 1; checking how the registry tests use the matching functions to size the change.Writing the failing tests. First a look at how existing handler tests assert the 403 through execution.Writing the three regression tests now (handler auto shadowing, /v1/responses auto refusal through the image check, websocket pin restore).Now the /v1/responses test for finding 2 and the websocket test for finding 3.Expected: that test already targets the new registry signature. Temporarily using the old signature to watch the behavior failures first.Now update the registry tests' call sites and add a registry-level test for the model filter.Now util.ResolveAutoModelMatching.Now the handler: a shared routing lookup, the route-aware model filter, and one resolver used by both execution and the websocket.Now the websocket: delegate auto resolution to the shared resolver, and count only enabled providers when restoring a remembered pin.Now the pin-restore fix (finding 3).The websocket test still fails, so restoring the pin isn't the only gap. Tracing how a full `response.create` uses the pin.Both pass. Now confirming the websocket test actually fails without the restore fix (temporarily disabling the filter loop).The test catches the regression, and the fix is restored. Now the existing handlers test call site that uses the old registry signature.Running the full affected packages, then the whole suite.Full suite passes. Running vet and checking for a configured linter.No `.golangci` config in the repo; checking whether CI runs a linter before deciding what the 58 default-config reports mean.go vet is clean, and CI only builds and tests. Before touching AGENTS.md, checking what the `agents-md-guard` workflow enforces.The branch already maintains this AGENTS.md sentence, and it now names the wrong entry point, so I'm updating it to match.Checking whether user-facing docs describe this auto behavior too.The handler diff reads cleanly. Checking the management-api doc's description of auto resolution.The user-facing doc stays accurate at its level of detail, so I'm leaving it. Checking whether earlier review iterations recorded decisions in the plan's log.Earlier reviews recorded their decisions in the plan's log, so I'm adding one entry for this round.Final verification: format, compile per AGENTS.md, and the full suite once more after the last edits.All three findings were real. I reproduced each one with a failing test before fixing it, and each new test fails without its fix. The fixes are uncommitted for the next review pass, and no backlog entries were filed.

Finding 1 (P2, auto returns 403 although an enabled model is available): fixed.
- Cause: auto counted availability on the exact registry ID, but routing looks providers up by the base model with the thinking suffix removed. So with Claude disabled and registering "shared", and Codex registering "shared(high)" plus another model, auto picked "shared(high)", routed it only to Claude, and got the 403.
- Fix: auto now counts a model onl
[truncated]
```
#### Iteration 2
- truncated: true
Reviewer output:
```
The three original scenarios are fixed, including websocket pin restoration. Three related issues remain:

- **[P2] `auto` can count a healthy credential that routing cannot use.** `sdk/api/handlers/handlers_routing.go:203` checks whether *any* routed provider is enabled, while `internal/registry/model_registry.go:2231` counts availability on the exact registered ID. If disabled Claude and cooling-down Gemini register `shared`, while healthy Codex registers the newest `shared(high)` and an older `other`, the two checks pass on different providers. Execution routes only to Gemini after Claude is removed and fails instead of choosing `other`. Check availability on the enabled providers that will actually receive the request.
- **[P2] `auto(high)` can still return a false 403.** `sdk/api/handlers/handlers_routing.go:203` filters candidates before `sdk/api/handlers/handlers_routing.go:208` appends the caller’s suffix. If disabled Claude registers `x` and enabled Codex registers only `x(v2)`, the filter rejects `x(v2)` because it looks up `x`. But the final name would be `x(v2)(high)`, which routes to Codex via `x(v2)`. The resolver instead falls back to Claude and returns 403. Filter the same name execution will route.
- **[P2] A config reload can bypass endpoint validation.** `sdk/api/handlers/handlers_routing.go:242` skips image and speech checks on the assumption that execution will refuse the request. `sdk/cliproxy/auth/provider_fallback.go:295` resolves the profile again. If Codex is re-enabled between those reads, a `/v1/responses` request for `auto` that resolved to `gpt-image-2` can reach the executor with neither the 403 nor the image-endpoint check. Return the typed refusal at the first decision, or carry the same profile and provider set through execution.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external findings rigorously.Still evaluating the three findings: I'm reading the refusal gate in the auth package to see whether the handler can return the same 403 directly.Now checking util.GetProviderName and how auth selection matches a suffixed model, to decide where availability should be counted.Checking the stream paths and provider reordering before choosing the fix for the reload race.All three findings target code added in this review loop (uncommitted), so they're in scope. Reading the existing tests to follow their setup.Implementing. First the registry: count availability on the model routing serves, not the listed ID.Now util.ResolveAutoModelMatching.Now the Manager method that returns the typed refusal, placed next to `ProviderEnabledForRequest`.Now the handler: resolve with the suffixed name and the routed registration, and refuse at the first decision.Moving the refusal inside the non-Home branch so Home requests skip the profile read entirely.Updating the registry test to the new signature and adding a case for counting on the routed registration.Giving the suffixed ID a strictly newer timestamp so the unstable sort can't flip the last assertion.Now the handler tests: update the disabled-only case to expect the 403 from the routing step, and add regressions for findings 1 and 2.All pass. Verifying the two new regressions actually catch the old behavior by temporarily reverting the route mapping.Tests pass. Now updating AGENTS.md and the plan's decision log to describe the new behavior.The previous decision-log line from this loop names the removed `routedModelProviders`; renaming it so the doc doesn't point at a function that no longer exists.Now the full verification: build, vet on touched packages, and the whole test suite.Build, vet and the full suite are clean. Checking whether the repo has a golangci-lint config to run too.Seven issues in code new relative to main; listing all of them.Only the SA1029 test-key notes remain, which are required by the production lookup. Final look at this iteration's core diff.All three findings were valid and are fixed. Nothing is committed and no signal is emitted, since the next Codex pass needs to check the fixes. All three were in code this branch added during this review loop, so none was out of scope and nothing went to the backlog.

Finding 1 (P2, auto counted a healthy client that routing can't use): valid, fixed.
- Cause: resolution checked that some routed provider was enabled, but counted availability on the exact listed ID. For a Codex-registered "shared(high)", routing sends the request to the clients of "shared", where Claude is disabled and Gemini is cooling down. Auth selection (authSupportsRouteModel / canonicalModelKey) matches on that base model too.
- Fix: internal/registry/model_registry.go GetFirstAvailableModelMatching now takes routeModel func(modelID) (string, bool) instead of acceptModel. It maps a listed model to the registration routing uses and counts the enabled providers' available clients there.
- internal/util/provider.go ResolveAutoModelMatching passes routeModel through, and gives GetFirstRegisteredModelMatching only its accept bit.
- sdk/api/handlers/handlers_routing.go: routedModelProviders became routedModel, which returns both the routed model ID and its providers.
- Tests: TestAutoModelCountsClientsOfRoutedModel covers the reviewer's exact scenario and now resolves to "other" on codex. TestGetFirstAvailableModelMatchingCountsRoutedModel covers the registry side.

Finding 2 (P2, auto(high) false 403): valid, fixe
[truncated]
```
#### Iteration 3
- truncated: true
Reviewer output:
```
The three previously reported scenarios are fixed, including the endpoint-validation bypass. Four findings remain:

- **[P2] `auto` changes behavior even when no provider is disabled.** `sdk/api/handlers/handlers_routing.go:196` enables routed-model counting whenever the predicate is non-nil, but `ProviderEnabledForRequest` returns a predicate even for an unrestricted profile or plugin scheduler. If the newest `x(high)` registration is cooling down, `other` is next-newest, and an older `x` is healthy, `auto` now selects `x(high)` instead of `other`. This is separate from the alias-pool change the plan accepts.
- **[P2] Mixed-case model names can be counted against a nonexistent registration.** `sdk/api/handlers/handlers_routing.go:220` returns `X` as the registry key for `X(high)` even when `GetProviderName` found its providers through the lowercase fallback to `x` at `internal/util/provider.go:68`. The count is case-sensitive, so `auto` can skip the newest runnable model for an older one. Return the registry key that actually matched.
- **[P2] A profile reload can still produce a false 403.** `sdk/api/handlers/handlers_routing.go:234` passes the original context to `RefuseDisabledProviders`, which resolves the profile again at `sdk/cliproxy/auth/provider_policy.go:122`. If a reload switches a key from Codex-only to Claude-only after `auto` selects Codex, the handler refuses the request even though either profile, used consistently, has a healthy target. The endpoint checks are no longer bypassed; the remaining issue is that selection and refusal use different snapshots.
- **[P3] One refused WebSocket turn can produce two refusal logs.** `sdk/api/handlers/handlers_routing.go:234` logs even when called by the alternative-target probe at `sdk/api/handlers/handlers_stream.go:44`. On an `auto` turn pinned to the only Codex account after Codex is disabled, the probe logs a refusal and returns `false`; actual execution then logs the same refusal again. Keep the probe side-effect free so the log counts refused requests, not routing checks.

```
Evaluator response:
```
Reviewing the four Codex findings against the code now. Next I'll check the plan, the profile resolution path, and the websocket probe caller.Status: findings 1, 3 and 4 look valid after tracing. I'm checking finding 2's registry-key lookup and the probe call path before editing.Now the util change for finding 2: expose the registry key that actually matched.Now the auth package: a profile-pinning helper, a "disables any provider" check, and a non-logging refusal for probes.Now the handler side: probe flag, single snapshot, nil filter for unrestricted profiles, and the matched registry key.Now update the existing tests to the new signature and the nil filter for an unrestricted profile.Now the auth-level tests: disables-any check, probe variant without logging, and one-snapshot pinning across a reload.Now the handler tests for findings 1, 2 and 4.Tests pass. Checking that each new test actually fails without its fix, so they're real regression guards.The mutated run printed nothing, which is suspicious. Rerunning with full output.All three new tests fail without their fixes and pass with them. Now updating AGENTS.md and the plan's Decision Log.Now the full verification: gofmt, vet, build, and the whole test suite.The exit code didn't print under zsh, so I'm confirming there are no failures directly.Full suite passes (exit 0, 105 packages ok, no failures). Checking for a configured linter.golangci-lint (default config; the repo has no lint config) reports 112 issues across these packages. Checking whether any are in files or lines this branch touched.No CI workflow runs golangci-lint, staticcheck or vet, so the project's gate is gofmt plus build and tests, and all are clean. A final look at the handler diff:All four findings were real, and I fixed them. Nothing is committed and I'm not sending the done signal, so the loop can run Codex again on the uncommitted diff.

- [P2] auto changed for profiles that disable nothing. Valid. The plan says configs without disabled behave as before, except for the alias-pool count fix. Routed counting was switched on whenever the filter existed, and ProviderEnabledForRequest always returns one, so your x(high) / other / x case went the wrong way. Fix: AutoModelProviderFilter now returns nil unless the new Manager.DisablesProviderForRequest says the profile disables a provider. Under Home or a plugin scheduler it is false too. With nil, ResolveAutoModelMatching takes the old unfiltered path. Test: TestAutoModelUnfilteredWhenProfileDisablesNothing (your scenario resolves to other on gemini). TestAutoModelSkipsModelOfDisabledProvider now expects a nil filter for the plain key.

- [P2] Mixed-case names were counted against a nonexistent registration. Valid. GetProviderName found the providers under the lowercase key, but routedModel returned the original-case key, and the case-sensitive count found nothing. Fix: a new util.GetRegisteredModelProviders returns the registry key that actually matched plus its providers. GetProviderName now calls it, so its behaviour is unchanged, and routedModel uses the returned key in both branches. Test: TestAutoModelCountsLowercaseRoutedModel (Codex Auto-Model-Case-X(high) routed to Gemini auto-model-case-x is chosen over the older Codex model).

- [P2] A reload between choosing auto and refusing it could give a false 403. Valid. The two steps each resolved the profile on their own. Fix: a new Manager.WithRequestRoutingProfile (wraps ensureRoutingProfile) stores the profile in the context once. getRequestDetailsWithOptions now resolves auto and runs the refusal on that same context, and AutoModelPro
[truncated]
```
#### Iteration 4
- truncated: true
Reviewer output:
```
The unrestricted-profile, mixed-case lookup, and duplicate refusal-log fixes are correct. The snapshot fix makes the handler’s two checks agree, but two end-to-end issues remain:

- **[P2] A reload can still return a false 403 for `auto`.** `sdk/api/handlers/handlers_routing.go:237` pins the profile only in the helper’s local context. `sdk/api/handlers/handlers_execution.go:116` passes the original context to `Execute`, which resolves the profile again. If a key switches from Codex-only to Claude-only after `auto` selects Codex, execution refuses it even though either profile could have served the request. Pin the profile in the caller and carry it through resolution and execution, including streaming and count requests.
- **[P2] `auto` can select a suffixed model that credential selection rejects.** If Codex registers `x(high)` but not `x`, `sdk/api/handlers/handlers_routing.go:224` counts the full registration as available. The default selection path strips the suffix and checks for `x` at `sdk/cliproxy/auth/conductor_selection.go:1533`, so it can reject the healthy credential instead of serving an older available model. WebSocket pin matching likewise checks `x` at `sdk/api/handlers/openai/openai_responses_websocket_session.go:196`. Use the registry key that actually matched for credential and pin checks, while preserving the requested model for the upstream payload.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external review findings with technical rigor.Still evaluating the two findings: reading how credential selection matches a suffixed model before deciding whether the second one is real.I'm checking where the routing profile gets read, to see whether pinning it in the handler before `Execute` changes anything.Writing a throwaway test to check finding 2 directly: does credential selection reject a credential that registered only `x(high)` when the request asks for `x(high)`?Checking whether handler tests already have a before-auth interceptor fake I can reuse to land a config reload between `auto` resolution and execution.Now appending the reload test (non-streaming, streaming, and count) and a test for finding 2 to the handler test file.Now the four execution entry points: non-streaming, count, streaming, and the alternative-target probe.All handler tests pass, and both new tests failed before the fix. Running the wider affected packages and the build while I update the docs.Checking the backlog for an existing entry on the suffixed-registration mismatch before filing one.No existing entry covers it, so I'm filing a new one with current line references.Waiting for the full test suite to finish.The entry-point change is four one-line additions. Still waiting on the full suite.Both findings were real, and both are now fixed in the uncommitted changes. The full test suite passes, the server builds, and gofmt and go vet report nothing. Each new test failed before its fix.

Finding 1 (P2, a config reload can give auto a false 403) - valid, fixed.
- Confirmed: the handler pinned the routing profile only inside getRequestDetailsWithOptions. Manager.Execute, ExecuteStream and ExecuteCount then read the profile again through ensureRoutingProfile.
- Reproduced it: a before-auth interceptor reloads the config after auto has picked the Codex model. Non-streaming, streaming and count requests all returned "403 codex is disabled by the routing profile of this API key".
- Fix: a new helper, BaseAPIHandler.withRequestRoutingProfile, pins the profile in the caller's context before providersForExecution. It runs in executeWithAuthManagerFormats, executeCountWithAuthManager, executeStreamWithAuthManagerFormats and HasAlternativeExecutionTarget.
- Since ensureRoutingProfile keeps a profile already in the context, resolution, the handler refusal and execution (with its retries and fallback legs) now read the same profile. Under Home the resolved profile is nil and withRoutingProfile ignores it, so Home is unchanged. Plugin-executor routes are not affected because the pin comes after that branch.
- Test: TestAutoModelExecutionKeepsProfileAcrossReload covers execute, stream and count.

Finding 2 (P2, auto picks a model credential selection rejects) - valid, fixed for the filtered auto; the broader mismatch is filed to the backlog.
- Reproduced at the selection layer: with a Codex client that registers only x(high), Manager.Execute for x(high) returns "auth_not_found: no auth available" and no executor runs. authSupportsRouteModel strips the suffix and requires the client to register x.
- In scope: the filtered auto resolution (used when a profile disables a provider) is this branch's code, and it counted x(high) through routedModel's full-name fallback.
- Fix: in ResolveAutoModelForRequest, routeModel now counts a listed model only on its base model's registration via util.GetRegisteredModelProviders, which is what selection matches. So x(high) without x is skipped for an older model the key can run. The websocket pin matcher che
[truncated]
```
#### Iteration 5
- truncated: true
Reviewer output:
```
The two reported cases are fixed: HTTP execute, stream, and count now retain one profile snapshot, and filtered `auto` skips `x(high)` when the credential registers neither `x` nor an equivalent base name. Two related issues remain:

- **[P2] A Responses WebSocket turn can still return a false 403 after a reload.** `sdk/api/handlers/openai/openai_responses_websocket.go:833` checks whether to retain the credential pin using a turn context with no profile snapshot. If a self-contained `auto` turn retains its Codex pin and the key switches to Claude-only before stream execution, execution selects Claude but rejects the stale Codex pin at `sdk/cliproxy/auth/provider_fallback.go:197`. Either profile alone could serve the turn. Pin the profile on `executionParent` before model resolution and reuse it through execution, once per turn rather than once per connection.
- **[P3] Filtered `auto` can skip a runnable mixed-case model.** At `sdk/api/handlers/handlers_routing.go:223`, a healthy Codex credential registering both `x(high)` and `X`, but not lowercase `x`, fails the base-registration lookup for `x(high)`. Yet explicit routing finds its full name at `sdk/api/handlers/handlers_routing.go:239`, and credential selection accepts `X` as the base via `EqualFold` at `internal/registry/model_registry.go:1481`. If `x(high)` is newest, filtered `auto` therefore selects an older model unnecessarily. The full-name fallback should count only routed credentials that actually support the base, using the same comparison as selection.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external review findings with technical rigor.Checking the two findings now: the websocket per-turn profile snapshot and the mixed-case base lookup in filtered auto.Tracing whether the websocket turn's API key (read from the gin context) is settled before executionParent is built, so pinning the profile there resolves the right key profile.Finding 1 is confirmed (per-turn websocket checks each re-resolve the profile). Now reading the registry counter to see how to count only clients that support the base model for finding 2.Finding 1 fix in place. Now looking at the existing reload-race tests from the last iteration so the websocket regression test follows the same pattern.Now the registry changes: extract the locked count, add the per-client route check, and switch both first-model lookups to the new router hook.Now the util and handler side.Builds cleanly. Updating the registry tests for the new hook signature and adding a per-client base-support test.Registry tests pass. Now a handler-level test that runs filtered auto end to end through credential selection, so it shows the mixed-case model is actually servable.Verifying the new test actually fails with the old base-only lookup:The test fails without the fix and passes with it. Now a websocket regression test for finding 1. Looking for a hook that can land a config reload mid-turn from the openai test package.Still working on finding 1's regression test: the reload has to land between the turn's pin checks and execution, and there is no seam there, so I'm looking for a deterministic trigger point.Found a usable seam: a plugin model router runs at turn start, right after the new snapshot point, so it can land a reload mid-turn. Reading the existing router-host test helper.Passes. Checking it fails without the snapshot line:Both regression tests fail without their fix and pass with it. Updating AGENTS.md, the plan, and the existing backlog entry so they describe the new behavior.Docs updated and the backlog entry restaged. Running gofmt, the compile check, and the full test suite.Full suite passes. Running the new and touched tests under the race detector too, since the websocket test reloads config from the handler goroutine:Everything passes, race detector included. One last read of the new registry code:Both findings were valid and both are fixed. Neither went to the backlog, since both are in code this branch wrote. Nothing is committed.

Finding 1 [P2], websocket false 403 after a reload: valid, fixed.
- Each turn's pin checks (resolving auto, the pin matcher, the provider check when restoring a pin, the policy release) re-read the routing profile on their own. Execution then read it again. A reload in between could keep a Codex pin under the old profile and then refuse it under the new one.
- Fix in sdk/api/handlers/openai/openai_responses_websocket.go: each turn now takes one profile snapshot when executionParent is built. That is after the turn's authentication sets the key and before model routing and auto resolution. The turn's execution context derives from executionParent, so the same snapshot covers the turn's execution and its retries. executionParent is rebuilt for every message, so the snapshot is per turn, not per connection.
- New test TestResponsesWebsocketTurnKeepsProfileAcrossReload: a model-router hook switches the key from Codex-only to Claude-only during turn 2. Turn 2 stays on its Codex pin and turn 3 runs on Claude. With the snapshot line removed, the test fails (turn 2 runs on Claude, so the turn ignored its own starting profile
[truncated]
```
#### Iteration 6
- truncated: true
Reviewer output:
```
Both previously reported cases are fixed: the Responses WebSocket now uses one routing profile per turn, and filtered `auto` recognizes `x(high)` when the same credential also registers `X`. Two related issues remain:

- **[P2] A policy change can still skip a remembered WebSocket credential.** `sdk/api/handlers/openai/openai_responses_websocket.go:810` restores a remembered pin only when the current pin is already empty. If a connection switches from xAI to Codex, then Codex is disabled before a full `response.create` for a model both serve, the Codex pin still matches the model. It is released at `sdk/api/handlers/openai/openai_responses_websocket.go:840`, after restoration has run. With two eligible xAI accounts, the turn can select a different account instead of restoring the remembered one.
- **[P3] Filtered `auto` still skips models that credential selection can serve.** `internal/registry/model_registry.go:2239` requires one credential to register both the routed ID and its literal base. If Codex credential A registers `x(high)` and Codex credential B registers `X`, an explicit `x(high)` routes to Codex and can run on B: selection checks the provider and base model, not whether B advertised the full name (`sdk/cliproxy/auth/conductor_selection.go:2997`). Filtered `auto` nevertheless skips `x(high)` even when it is newest. The same literal-base check also misses a forked OAuth alias `x(high)` whose credential registers its upstream target `y`, which selection accepts at `sdk/cliproxy/auth/conductor_selection.go:1540`.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external review findings rigorously.Writing the fix now. Then a regression test where a higher-priority second xAI account would win a fresh selection, so the test fails unless the remembered pin is restored.Now the regression test, placed after the existing restore test.Passes with the fix. Now confirming it fails without the fix by temporarily disabling the new restore condition.Registry side done. Now the manager method that supplies per-credential selection keys, placed next to `authSupportsRouteModel` so the two stay in sync.The build passes. Now updating the registry tests for the new router type and adding cases for both of the reviewer's scenarios.Registry tests pass. Next, handler-level tests that run real execution for both reviewer cases. Checking how an OAuth alias is attached to a credential first.The request ran on the alias credential. The alias resolver keeps the request's `(high)` suffix on the upstream model, which is existing behavior, so I'll fix the test's expectation.Both pass. Now confirming they fail under the old rule, by temporarily putting back the "member of the routed ID, literal base only" restriction.Updating AGENTS.md, the plan's review log, and the backlog entry's description of the filtered `auto` rule.The full suite passes. Checking whether the repo has a linter configured.golangci-lint has no repo config, and with its defaults it reports 112 issues across these large packages. Checking which of them, if any, this branch introduced.Both findings were valid and both are fixed. Nothing is committed, and the full test suite passes.

- [P2] Remembered WebSocket pin skipped after a policy change: valid, fixed in sdk/api/handlers/openai/openai_responses_websocket.go.
  - Cause: the restore step only ran when no pin was kept. A Codex pin that still matched the model survived the pin check, so restore never ran. The policy release then cleared it, and the turn picked an account afresh instead of the remembered xAI one.
  - Fix: the pin check now notes when the kept pin's provider is disabled for the turn, and the restore step also runs in that case, before the policy release. The restore drops disabled providers, so it switches to the remembered pin of the one enabled provider serving the model. The release that follows still checks whichever pin is current. If nothing can be restored, the disabled pin stays and the release or 403 path runs as before.
  - The replaced Codex pin stays remembered, so re-enabling Codex later can restore it.
  - New test TestResponsesWebsocketRestoresPinInPlaceOfDisabledProviderPin covers the reviewer's exact scenario. It gives the other xAI account priority 10 under a priority-first selection, so a fresh selection is certain to pick it. The test fails without the fix (xai auth IDs = [a, b]) and passes with it.

- [P3] Filtered auto skips models that credential selection can serve: valid, fixed. This was the branch's own code (routedModelClients), so it is in scope.
  - Cause: the registry check only counted clients that registered both the routed ID and its literal base. Selection (authSupportsRouteModel) accepts any client of the routed providers that registers the base model in any case, or the model its OAuth alias or prefix maps the request to.
  - registry.ModelRouter now returns a registry.ModelRoute with the routed ID, the base model, and a SelectionKeys map from client to alias/prefix target.
  - routedModelClients now counts every client of the routed registration's accepted providers that registers the base model or its selection key, compa
[truncated]
```
#### Iteration 7
- truncated: true
Reviewer output:
```
The remembered-pin fix is correct for the reported provider switch, and filtered `auto` now recognizes the mixed-case and forked-alias credentials from the prior review. Two related correctness issues remain:

- **[P2] A valid WebSocket continuation can still lose its pin.** `sdk/api/handlers/openai/openai_responses_websocket_session.go:166` checks only the literal base model. With the new forked-alias case, a WebSocket-capable Codex credential registering `x(high)` and `y`, but not `x`, can serve the first `auto` turn through `x(high) → y`. On the next turn, the matcher rejects that same credential because it does not register `x`. A request carrying `previous_response_id` then closes with “upstream requires HTTP replay” even though the original upstream is still usable. The pin and capability checks should use the same credential-specific model match as execution.
- **[P2] Filtered `auto` can check the wrong model’s cooldown.** `internal/registry/model_registry.go:2254` uses the routed registration’s state whenever the client registers that ID. If one Codex credential registers `x`, `x(high)`, and `y`, with `x(high) → y`, the request routes through `x` but execution checks `y` for cooldown (`sdk/cliproxy/auth/conductor_selection.go:903`). When `x` is healthy and `y` is cooling down, `auto` can select the newest `x(high)` and return a cooldown error instead of choosing an older healthy model. Availability needs to follow the credential’s resolved selection key, not just the registration used to find its provider.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the Codex findings rigorously.Checking both Codex findings against the code: tracing the WebSocket pin matcher and the registry availability check against what credential selection actually does.Still tracing finding 2: checking who actually writes the registry's per-client cooldown state that filtered auto reads.Writing the fix for finding 2 now; first confirming how a test can drive a real cooldown through the manager so the registry projection is exercised end to end.The listing keeps a quota-cooling model, so an end-to-end test can tell the old behavior from the new one. Adding the tests now.Now the end-to-end handler test that drives a real 429 through `MarkResult`, so the manager's projection, not the test, decides the registry state.All pass. Now confirming the two new tests actually catch the bug by running them against the old state selection temporarily.Both new tests fail on the old logic with exactly the failure Codex described, and pass with the fix. Next, updating AGENTS.md and the plan's decision log, then filing finding 1.Now filing finding 1 to the backlog, with exact line references.The build is clean, gofmt and vet report nothing. Running the full test suite in the background.The full suite passes with no failures. Quick check for a configured linter:golangci-lint isn't configured for this repo (AGENTS.md lists gofmt as the required formatter), but I'll check whether any of its default-ruleset findings land in code this branch touched.My hunk parser was too coarse: blame shows those lines come from older commits. Checking every flagged line with blame to be sure:Finding 2 is fixed. Finding 1 is real, but it was already broken on main, so I filed it to the backlog. Nothing is committed and I'm not emitting the done signal, so the loop can run Codex again on these changes.

Finding 2 (filtered auto checked the wrong model's cooldown): valid, fixed.
- Cause: the registry doesn't track cooldowns on its own. After each result, the auth manager writes per-client state for every model a client registers (MarkResult, then clientModelProjectionForAuth). For each registered model it uses the cooldown of the model that credential selection would check, after aliases and prefixes. So the "x(high)" entry mirrors the cooldown of "y", the alias's upstream model, while the "x" entry mirrors "x". Execution checks "y" (selectionModelForAuth, conductor_selection.go:903). The old routedModelClients read the routed registration "x", so with "y" cooling down and "x" healthy, auto picked "x(high)" and the request failed with a cooldown error.
- Fix (internal/registry/model_registry.go, routedModelClients): ModelRoute has a new Model field holding the requested model with its suffix; the handler fills it in handlers_routing.go. A client's state now comes from, in order:
  1. its entry for the requested model, whose state comes from exactly the check execution makes;
  2. otherwise its entry for the model selection checks (its selection key, or the base model when it has none);
  3. otherwise the model it was accepted for.
  
  It no longer reads the routed base entry by default.
- Tests:
  - New: TestFirstAvailableModelMatchingChecksSelectionKeyState covers both cases, the client registering the requested model and the client registering only the selection key.
  - New: TestAutoModelChecksCooldownOfForkedAliasUpstream is end to end. A real 429 goes through MarkResult, so the manager, not the test, writes the registry state. It checks that auto resolves to the older model and that execution runs on it.
  - Existing r
[truncated]
```
#### Iteration 8
- truncated: true
Reviewer output:
```
- **[P2] Filtered `auto(high)` can still select a cooling model with chained aliases.** At `internal/registry/model_registry.go:2268`, when `b(high)` is not registered literally, the code reads the registration for its selection key, `a`. With aliases `b → a` and `a → c`, that registration reflects **`c`’s** cooldown, while execution of `b(high)` checks **`a`**. If `b` is newest, `a` is cooling, and `c` is healthy, `auto(high)` picks `b` and returns a cooldown error instead of using an older healthy model. The availability check should use the credential’s state for the resolved selection key, not assume the registration named by that key represents the same state.
- **[P3] Disabled-only routes trigger unnecessary full credential scans.** `sdk/api/handlers/handlers_routing.go:236` computes selection keys before checking whether any routed provider is enabled. For a Codex-only profile, each newer Claude-only model therefore scans every auth under the manager read lock at `sdk/cliproxy/auth/conductor_selection.go:1563`, only to be discarded. Skip routes with no enabled provider before computing selection keys.

The previous fix is correct for the reported forked-alias case. Deferring the WebSocket pin mismatch is reasonable: `main` already uses the same literal-base check for explicit model requests.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external findings with technical rigor.Checking how alias resolution and registry state projection interact, to verify the chained-alias claim.Both findings target code this branch added, so they are in scope. Reading the remaining registry test that builds SelectionKeys.The chain claim depends on how MarkResult keys and migrates alias states, so I'll reproduce the scenario with a handler test before changing anything.Writing a reproduction test for the chained-alias case (b → a, a → c, request auto(high)).Now the auth manager side: report the credential's state for its selection key.Now finding 2: skip a route with no enabled provider before scanning auths, and pass only enabled providers to the scan.The provider slice is freshly allocated per call, so in-place DeleteFunc is safe. Adding the slices import and updating the registry tests to the new SelectionKey type.The vet error is the expected test fallout from the type change. Updating the two registry tests to the new SelectionKey contract.Now cleaning up the reproduction test into a proper regression test (drop the debug logging, assert execution succeeds on the older model).The new regression test passes with the fix (it failed before it, in the reproduction run). Formatting, then running the full suite and the required compile check.Build, vet, and all tests pass. Checking for a configured linter.golangci-lint isn't configured for this repo (no config file; AGENTS.md names gofmt/build/test, and go vet passes). Checking whether any of its default-linter reports touch lines this branch changed.The existing directive uses " -- " which golangci misparses (that's the "unknown linters" warning above). I'll use the valid `//nolint:staticcheck // reason` form on the three lines.golangci caps repeated reports at three, so more were hidden. Listing all of them.Branch-changed lines now lint clean. Updating AGENTS.md and the plan's review log to describe the new availability source.Final verification: format, compile, vet, full test suite, lint on branch-changed lines.I fixed both findings. Build, go vet and the full go test suite pass, gofmt is clean, and golangci-lint reports 0 issues on lines this branch changed. Nothing is committed, as instructed.

- P2, chained aliases: valid and reproduced. With aliases b → a (fork) and a → c (fork), a cooling "a" and a healthy "c", auto(high) picked b(high) and the request failed with 429 model_cooldown. The cause: selection maps an alias only one step, so b(high) is checked against the state of "a". But the auth manager writes onto each registration the state of the model that registration maps to, so the client's "a" registration carried the state of "c".
  Fix: Manager.RouteModelSelectionKeys (sdk/cliproxy/auth/conductor_selection.go) now returns a registry.SelectionKey {Model, Unavailable} per auth. Unavailable comes from isAuthBlockedForModel(auth, selectionKey, now), the same check selection makes. For a client that has a selection key, routedModelClients (internal/registry/model_registry.go) uses that flag and no longer reads a registration's state. For other clients nothing changes: it reads their registration of the requested model, else of the base model.
  Tests: new regression test TestAutoModelChecksCooldownOfChainedAliasSelectionKey, which failed before the fix and now resolves to older(high) and runs it. I rewrote TestFirstAvailableModelMatchingChecksSelectionKeyState and adjusted TestFirstModelMatchingCountsAcceptedClientsOfRoutedProviders to the new type. The earlier forked-alias tests still pass
[truncated]
```
#### Iteration 9
- truncated: true
Reviewer output:
```
The two previous findings are fixed: chained aliases now use the credential’s state for the selection key, and routes with no enabled provider skip the credential scan. Two availability mismatches remain:

- **[P2] Filtered `auto` checks the wrong token for native Codex clients.** `sdk/cliproxy/auth/conductor_selection.go:1579` checks alias availability without the request context. For a `use_siwc` credential, that checks the nested token, while native Codex execution checks the outer token at `sdk/cliproxy/auth/conductor_selection.go:904`. With a valid nested token and an expired outer token, `auto` can pick an alias that execution rejects instead of an older model on a healthy credential. Pass the request context through to the availability check.
- **[P2] A case-sensitive lookup can read another model’s cooldown.** `internal/registry/model_registry.go:2282` looks up the full requested name with `slices.Index`, although OAuth alias matching is case-insensitive. For example, if `x` maps to upstream `y` and `x(HIGH)` maps to upstream `x`, execution checks `x` for `x(high)`. The lookup misses `x(HIGH)` and falls back to the `x` registration, which carries `y`’s cooldown. With `y` cooling and `x` healthy, `auto(high)` skips a servable model. Match the full registration case-insensitively before falling back to the base.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the external review findings with technical rigor.Status: reading the code behind both findings; both look plausible so far, and I'm checking how the request context changes Codex SIWC token checks.Both are valid and in scope (this branch wrote both lines). Applying fix 1 first: `RouteModelSelectionKeys` gets the request context so it checks the same token that execution checks.Now the fix in `conductor_selection.go`:Fix 2: the registry's state lookup now matches the full requested name case-insensitively, the way OAuth alias resolution does.Updating the doc comment on `routedModelClients` to say the lookup ignores case:Now the registry regression test for finding 2, next to the selection-key test:The test passes. To confirm it actually catches the bug, I'll put the case-sensitive lookup back for a moment and run it again:Writing both handler-level tests in the existing style:Both pass. Confirming each one fails with its fix temporarily undone:The plan keeps a log of external-review decisions, so I'll add an entry for this round and adjust the AGENTS.md sentence that describes the same behavior.Everything passes except `internal/cmd`, and its log mentions port 3000 already in use. Checking whether that failure is environmental or a real regression:It passed when run alone, so this looks like a port collision between packages that `go test ./...` runs in parallel. Finding which test binds port 3000 before deciding whether it needs a fix:That message comes from an OAuth error string in `internal/auth`, not from `internal/cmd`. Rerunning the full suite to catch the real `internal/cmd` failure:The full suite passes on the rerun. The first `internal/cmd` failure had no visible `--- FAIL` line (`tail -40` may have cut it off), so I'll run that package repeatedly to see whether it's flaky:Sixteen runs in a row pass. Checking whether this branch touched `internal/cmd` at all, and running the suite once more under full parallel load:Two more full runs pass, and the branch doesn't touch `internal/cmd`. One last full run with all output saved to a file, so a recurrence would show the failing test name:Exit 0. A last look at this iteration's code changes:Both findings were valid and are fixed. Nothing is committed and no signal is output, because there were fixes to make. Both are defects in this branch's own code, so neither was filed to the backlog.

Finding 1 (P2, filtered auto checked the wrong token for native Codex clients): valid, fixed.
- Cause: RouteModelSelectionKeys (sdk/cliproxy/auth/conductor_selection.go) called isAuthBlockedForModel, which passes a nil context. With no context, IsCodexClientRequest is false, so a use_siwc credential was always judged by its nested SIWC token. Execution calls isAuthBlockedForModelForRequest(ctx, ...) at conductor_selection.go:904. For a Codex client request (the handler context sets WithCodexClientRequest at handlers.go:770), that checks the outer token instead.
- Fix: RouteModelSelectionKeys now takes the request context and calls isAuthBlockedForModelForRequest(ctx, auth, selectionKey, now). ResolveAutoModelForRequest in sdk/api/handlers/handlers_routing.go passes its ctx.
- Test: TestAutoModelChecksAliasSelectionKeyWithRequestToken. A use_siwc credential has a valid nested token and an expired outer token, and serves x(high) only through a forked alias. auto from another client still resolves to x(high). auto from a Codex client resolves to an older model on a healthy credential, and the request runs there. Without the fix, the Codex-client case resolved to x(high), as the
[truncated]
```
#### Iteration 10
- truncated: false
Reviewer output:
```
Both previous findings are fixed for the cases reported: alias selection uses the request context, and suffixed registration lookup is case-insensitive. Two availability mismatches remain:

- **[P2] Direct model registrations skip the request-specific token check.** `sdk/cliproxy/auth/conductor_selection.go:1575` skips the availability check when the selection key equals the base model. `internal/registry/model_registry.go:2297` then checks only registry cooldown and suspension. With Claude disabled, a native Codex client can select the newest directly registered model on a `use_siwc` credential whose outer token has expired. Execution rejects it instead of using an older model on a healthy credential. Apply the request-aware check to direct matches as well as aliases.
- **[P2] The global model listing can hide a healthy suffixed route.** `internal/registry/model_registry.go:2333` starts from `GetAvailableModels`, before the request suffix is applied at `sdk/api/handlers/handlers_routing.go:230`. With `x → y` and `x(HIGH) → x`, a non-quota suspension of `y` can remove the newest candidate `x` from that listing. `auto(high)` then picks an older model without ever checking the healthy `x(high)` route. For filtered `auto`, enumerate registrations first and judge availability on the routed request; leave unfiltered listing behavior unchanged.

```
Evaluator response:
```
I'm using superpowers:receiving-code-review because this task is evaluating external review feedback.Still checking the two findings. So far I've read the selection-key code and the registry lookup; now I'm reading the auth block check that finding 1 depends on.Still evaluating. Both findings look plausible so far. I'm checking what else the global model listing filters out before I change auto to enumerate registrations directly.I've confirmed both findings in the code. Next I'm writing two failing tests for them before changing anything.Both tests fail as the findings describe. Now checking the callers of the registry function before changing how filtered auto picks its candidates.Now the registry side: update the ModelRoute/SelectionKey docs, and make the filtered path enumerate registrations instead of the listing.Now the candidate enumeration for filtered auto:The two new tests and all existing auto-model tests pass. Adding a registry-level test for the candidate change, then running the full suite.Registry tests pass. Running the full build and test suite now.Full suite passes, build and vet are clean. Checking whether the repo has a linter config to run as well.The default golangci-lint run reports 112 issues. Checking whether any of them are in code this branch touched:Now the plan's review log entry:Docs updated. Last check: a race run on the changed packages, then the final diff of this iteration's code changes.Both findings were real and in scope: they are in the filtered auto resolution this branch adds. Both are fixed. Nothing is committed and there is no signal, so Codex can re-check.

Finding 1 (P2, a directly registered model skipped the request-specific token check): valid.
- Cause: RouteModelSelectionKeys only reported an auth whose selection key differed from the base model. Any other client was judged by its registry state, and that state never includes token expiry. So a Codex client's auto could pick the newest model of a use_siwc credential whose outer token had expired. Execution then refused it.
- Fix in sdk/cliproxy/auth/conductor_selection.go: RouteModelSelectionKeys now reports every auth of the route's enabled providers, including ones checked by their base model. Each is checked with isAuthBlockedForModelForRequest, the same check selection runs. In internal/registry/model_registry.go, routedModelClients falls back to the registry state only for clients the auth manager does not report.
- New test TestAutoModelChecksDirectModelWithRequestToken: auto from a Codex client now picks the older model on the healthy credential and runs there; auto from any other client still picks the newest model.

Finding 2 (P2, the global listing could hide a healthy suffixed route): valid.
- Cause: GetFirstAvailableModelMatching took its candidates from GetAvailableModels. The listing hides a model whose clients all have a non-quota suspension on its own registration. With aliases x -> y and x(HIGH) -> x, a failing y hid x, so auto(high) never tried the healthy x(high) route.
- Fix in internal/registry/model_registry.go: when a router is given (filtered auto only), every registered model is a candidate, newest first, via a new helper registeredModelsNewestFirst. GetFirstRegisteredModelMatching uses the same helper, with the same behavior as before. The unfiltered listing path is unchanged.
- New tests: TestAutoModelJudgesSuffixedRouteOfModelHiddenFromListing uses a 503 on y, which hides x from the listing; auto(high) now resolves to x(high) and runs on that credential. TestFirstAvailableModelMatchingRoutesModelHiddenFromListing covers the registry level and confirms the unrouted path still goes by the listing.

Docs: AGENTS.md and the plan's review log now describe the new behavior. Nothing was filed to the backlog.

Verification:
- gofmt is clean, the server build passes, and go vet is clean on the changed packages.
- go test ./... passes. A -race run on the registry, util and handlers packages and the disabled-provider auth tests also passes.
- golangci-lint (default linters; the repo has no config for it) reports 112 issues, none on lines this branch added. They are in older code: deprecated golang.org/x/net/context imports in the handlers, nil contexts passed to the old wrappers in conductor_selection.go, and unused helpers in selector.go. I left them alone: fixing them would be a large unrelated refactor.

Insight: the registry's per-client model state is computed in advance, without any request, so it can never hold request-specific facts such as which token a use_siwc credential uses for a Codex client. That is why the auth manager now supplies the state for every credential it knows, and the registry state is only a fallback.
```

## Validation
- none
- duration_ms: 0
- runs: 0