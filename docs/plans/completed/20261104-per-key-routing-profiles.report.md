# Report: Per-key routing profiles with an explicit selection pipeline
Plan: `docs/plans/20261104-per-key-routing-profiles.md` · Branch: `per-key-routing-profiles` · Base: `main` · Mode: full · Executor: claude · Task model: claude:opus:high · Review model: claude:opus:high · Started: 2026-11-04T18:34:43Z · Finished: 2026-11-04T20:43:44Z

## Summary
The branch makes credential selection configurable per client API key. Each entry in `access.api-keys` can have a profile under `routing.key-profiles` with four blocks: session affinity, preferred accounts per provider (claude, codex), an explicit `selection {primary, tiebreak}`, and a Claude -> Codex fallback. A key without a profile uses the shared profile, which is the global routing configuration. A key with a profile uses that profile completely. Fields are not inherited from the shared profile.

The main changes:
- **Profile resolution.** The profile is resolved once in the public `Execute`, `ExecuteCount` and `ExecuteStream` calls (`sdk/cliproxy/auth/routing_profile.go`). It travels in the context through retries and through the Codex fallback leg. It replaces `preferredAccountsContextKey` as the only context carrier for preferences.
- **New `PipelineSelector`** (`sdk/cliproxy/auth/selection_pipeline.go`). Selection runs in a fixed order:
  1. Hard-only availability across every priority tier.
  2. The profile's preferred account, which now wins even at soft reserve levels 1-3.
  3. The primary and tiebreak slots. Narrowers are `priority`, `reserves`, `earliest-reset` and `most-remaining`. Pickers are `round-robin`, `weighted-round-robin` and `fill-first`; they hand off to the existing selectors in a picker-only context.
  4. Round-robin for any ties left over.
- **Sticky sessions per key.** The affinity wrapper skips lookup and binding when sticky is off for a key. Explicit session bindings are kept separate for each key that has its own profile.
- **Fallback.** The Codex leg uses the same key's Codex preference and selection. Each selection inside the leg logs `routing_stage=fallback`.
- **Management API.** There is a new `GET /v8/management/routing/profiles`. Writes go through the v8 config PATCH. A profile for a key that is not in `access.api-keys` is rejected with 400; invalid slot values return 422. The comment-preserving saver removes deleted and rotated keys from the file, keeps empty `{}` profiles, and removes cleared fields. The api-key list handlers move or drop profiles to match the key list.
- **Docs.** `docs/management-api-v8.md`, `config.example.yaml` and `AGENTS.md` describe the pipeline and how a legacy config now behaves differently.

Phase durations (ms): tasks 3510523 (~58m 31s), internal review 3341956 (~55m 42s), evaluation 646542 (~10m 47s), external review 242029 (~4m 02s), other 0. They add up to ~2h 09m, which matches the time from start to finish. The run used 11 task iterations with 0 failed retries, 2 internal review loop iterations (ended by `review_done`) and 1 post-review iteration.

Finalize did not run, so no base sync happened. The diff is against local `main`.

## Change scope
- **Totals:** 64 files, 7847 additions, 406 deletions, 19 commits on top of `main`.
- **Commits before this plan's work started:** two of the 19 already existed when the plan was written. The plan cites them as context:
  - `af9850d9` feat(quota): staged soft reserves (dated 2026-10-13)
  - `f7a5cd17` feat(routing): Claude to Codex subscription fallback

  They account for about 26 files and ~1,950 added lines of the totals, including the new `sdk/cliproxy/auth/claude_fallback.go` and `quota_reserve_stages_test.go`, and the first version of `test/claude_codex_fallback_test.go`.
- **Housekeeping commits:**
  - `7272c6ae` tracks `docs/plans` and `docs/backlog` despite the docs ignore rule (`.gitignore`).
  - `eca0182f` adds the backlog entry.
  - `f0b7d161` saves the plan.
- **Feature commits, one per plan task (Tasks 1-10):** `629683b1`, `66624468`, `7dc1f57c`, `aafab7c6`, `1c4addc9`, `fdc63be1`, `611517b6`, `21b818df`, `92ffdd3f`, `850eb22b`, `4892f9fd`.
- **Review-fix commits:** `3e452f03` and `9b126ed8` (internal code review), `1b86ea60` (Codex external review).
- **Areas touched:**
  - `internal/config`: new `routing_profile.go`. Changes to config types, load, parse, `routing_preferences.go` and the YAML saver (`config_yaml.go`).
  - `sdk/cliproxy/auth`: new pipeline, profile, and candidate code. Changes to the selectors, `conductor_selection.go`, `conductor_execution.go`, the quota reserve and quota selector, and preferred accounts. About 14 test files added or changed.
  - `sdk/cliproxy`: `builder.go` and `service_config.go` for wiring, plus `executor/types.go` for `SessionAffinityScopeMetadataKey`.
  - `internal/api/handlers/management`: new `routing_profiles.go`. Changes to `config_v8.go` and `config_lists.go` (key reconciliation, `*Locked` helpers). Changes to the auth-files quota-reserve and claude-fallback handlers.
  - `internal/api/server_management_v8.go`: route registration.
  - `internal/watcher/diff/config_diff.go`: redacted diff of key profiles.
  - Integration tests: `test/key_routing_profiles_test.go` and `test/claude_codex_fallback_test.go`.
  - Docs: `docs/management-api-v8.md`, `config.example.yaml`, `AGENTS.md`, the plan, and one backlog file.

## Evidence
The orchestrator recorded no validation runs (0 commands, 0 runs). The only before/after proof in the facts comes from the agents' own reports.

- **Codex review iteration 1 fixes.** The evaluator says each new regression test failed with its fix reverted and passed with the fix in place:
  - `TestClaudeFallbackHonorsRequestScopedStop`: before the fix, a Claude 429 or 503 matching a `stop` or `stop-and-cooldown` rule still fell back to Codex. Now there is one Claude call, no Codex call, and the original error, on all three execute paths.
  - `TestSessionAffinityExplicitBindingsAreScopedPerKeyProfile`: before the fix, key B reused key A's explicit session binding and got A's account. Now each key lands on its own preferred account.
  - `TestSaveConfigKeepsEmptyKeyProfilesAndDropsClearedFields`: the evaluator reports all five cases failed on the old code. Rotating an empty `{}` profile lost it on disk, and clearing a profile left stale fields that came back after a reload.

  After the fixes, the evaluator reports `gofmt`, `go vet`, the server build and `go test ./...` all passing. Codex iteration 2 returned "NO ISSUES FOUND"; it read the code and did not run tests.
- **Task 9 report.** `gofmt -l .` prints nothing. The build, `go vet ./...` and `go test ./...` pass. Coverage is 81.1% for `sdk/cliproxy/auth` and 82.0% for `internal/config`.
- **Change in default behavior,** pinned by `TestPipelineSelectorEqualResetsRotate` and `TestPipelineSelectorPreferredBeforeSoftReserveThroughManager`:
  - Legacy `earliest-reset` ties used to resolve to the lowest ID; they now rotate round-robin.
  - The shared preference used to be skipped at soft reserve levels; it now wins there.

## Risk
**high**
- **Default behavior changes on upgrade.** This rewrites the selection path that every proxied request takes. A config with no new fields still behaves differently in three ways, as documented:
  - The implicit Priority tier no longer applies unless `priority` is chosen as a slot.
  - Equal `earliest-reset` ties rotate instead of always going to the lowest ID.
  - The shared `preferred-accounts` now wins at soft reserve levels 1-3.

  Deployments that rely on manual credential priority will silently route differently.
- **Public APIs.** There is a new `GET /v8/management/routing/profiles`. `PATCH /v8/management/config` gains a strict 400 check for unknown keys. The `/v0/management/api-keys` list handlers now reconcile profiles (allowed, since the existing handlers needed it). The SDK gains new exports: `NewPipelineSelector`, `Manager.SharedSelection`, `SessionAffinityScopeMetadataKey`, and the `routingProfile` context. The existing exported selector types are kept.
- **Data schemas.** There are no database changes. Affinity binding keys for keys with their own profile now carry a `::key:<hash>` suffix.
- **Configuration.** New `routing.selection` and `routing.key-profiles`, keyed by the raw client API key. The comment-preserving saver now removes key-profile entries and cleared fields from disk, which changes how existing config files are rewritten.
- **Concurrency.**
  - The api-key list helpers now mutate config under `h.mu` (the `*Locked` variants).
  - One shared pipeline selector reads the slots at `Pick` time.
  - A request that is in flight keeps the profile pointer it resolved when a key is rotated.
  - Turning sticky on or off rebuilds the selector and resets all bindings.

  All of these have tests, but they sit on a hot path.
- **Migrations.** None are needed. Without the new fields, old configs load and use a selection derived from them.

## Merge danger
**Door:** two-way
This is unreleased branch work. A plain revert restores the previous selector, saver and endpoints, and any `key-profiles` or `selection` entries already written are simply ignored by the old code.

**Blast radius:** global
Every request's credential choice goes through the new pipeline, so a regression affects all keys and providers, not only keys that have a profile.

## Migrations and operational steps
- No database or data migration.
- Configuration: optional for most deployments, but required to keep the old behavior when it matters:
  - Operators who rely on manual credential priority should set `routing.selection: {primary: reserves, tiebreak: priority}`. This keeps the old order: reserves, then priority, then round-robin.
  - Operators on `strategy: earliest-reset` should expect equal-reset ties to rotate.
- Deployment follow-up from the plan's Post-Completion section:
  1. Deploy to the local launchd service and keep the previous binary for rollback.
  2. Check by hand with two keys and two Claude Code sessions, watching the `Routing profile selection` log lines.
  3. Force a 429 on both Claude credentials and confirm the request reaches the key's preferred Codex credential.
  4. Confirm the existing panel still reads and writes the shared fields.
- External: a separate CPA-Manager-Plus plan covers the panel UI. CLIProxyAPIHome needs no change (checked in Task 9).

## Plan deviation
All 67 checklist items are checked, and nothing was skipped (Skipped: none).

**Added (➕):**
- `RoutingProfile.EffectiveSelection()`. An empty profile selection means `{reserves, round-robin}`. A tiebreak without a primary is rejected. `KeyHashPrefix`, `SelectionNarrowingSlots` and `SelectionPickerSlots` are exported.
- `ensureRoutingProfile` injects a profile only when the context has none. It runs in the public `Execute*`, the private `execute*`, and the `pickNext`, `pickNextMixed` and legacy entry points. A profile supplied by the caller is respected.
- Availability details:
  - `availableForPipeline` keeps hard-reserved credentials under `withQuotaReserveSkipped`.
  - The legacy pickers pass candidates through unchanged under picker-only.
  - `preferredAvailableAuth` matches each candidate's own provider for `mixed` requests.
  - A shared index lookup moved into `candidatePreferenceIndex`.
- Pipeline details:
  - A `withPipelineCandidates` marker tells the selector its candidates are already filtered.
  - An empty profile selection falls back to the shared selection, then the default.
  - Under `withQuotaReserveSkipped`, the `reserves` slot does not narrow.
  - Under weighted round-robin, zero-weight credentials are dropped after the preference step.
- Extraction regressions are covered by the unchanged existing suites, plus `TestQuotaReserveLevelFromWindows` and a lowest-ID assertion.
- `routingRuntimeState` no longer carries `strategy`. The shared slots come from the exported `Manager.SharedSelection()`, and the builder creates the manager before the pipeline.
- `availableAuthsForPipeline` keeps the legacy view (soft filter, top tier) for plugin schedulers.
- `reserveSelectors()` also runs the reserve suites through the pipeline. The priority and affinity tests use explicit pipeline fallbacks.
- `RoutingConfig.StickyAnywhere()` was added. The shared profile is sticky when `session-affinity` is true, or when no key profile is sticky. The stage names are constants. The sticky tests live in `sdk/cliproxy/service_config_sticky_test.go`.
- The fallback leg carries a marker (`withRoutingFallbackLeg`) and logs `routing_stage=fallback` along with the stage that decided it (`fallback_stage`).
- The 400 check tolerates an unknown-key profile that is unchanged from the file on disk, and accepts `null` entries for unknown keys.
- A new edge-case file, `routing_profile_edge_test.go`, covers a pinned execution ignoring the key preference, a key rotated while a request is in flight, unknown quota across the whole primary x tiebreak matrix, and several uncovered branches.
- Task 9 results and coverage are recorded. The only uncovered branches are nil-context guards, an unreachable `default`, and a nil-candidate skip.
- Docs placement: the new docs section is placed between the affinity and quota-reserve sections, `/routing/profiles` was added to the endpoints table, and stale comments in `config.example.yaml` were updated.

**Blocked / deviations (⚠️):**
- `useSchedulerFastPath` keeps a preference gate that reads the context profile, so SDK embedders using built-in selectors keep their preferences. The gate has no effect under the pipeline.
- Under the affinity wrapper, `availableAuthsForSelector` keeps passing every tier without the hard-reserve filter and does not set the pipeline marker. Task 5 hands the hard-only set to the cold path instead.
- The cold path removes hard-reserved credentials from the set it was given (`withoutHardReservedAuths`) instead of re-running `availableForPipeline`. Rechecking against the route alias would be wrong because the candidates were already checked against each credential's upstream model. It returns the same reserve error as `newSelectionAuths`.
- The Task 7 integration test uses two Codex credentials instead of one, so that the preference is visible against priority narrowing. It also checks the logged stages.
- The rule "one key removed and one added means a rotation" also applies to `{index,value}` patches and `PUT`. Any other change drops profiles. The list helpers became `*Locked` variants that save through `persistLocked`.
- A legacy config differs in three ways, not the two the plan listed. The third is that the shared preference now wins at soft levels.
- The docs place api-key reconciliation under `/v0/management/api-keys`, where those handlers are actually registered. The plan text said `/v8/management/api-keys`.

**Outside the plan's tasks:** the two prerequisite commits (`af9850d9`, `f7a5cd17`) are on the branch, so they are counted in the diff totals. The plan treats them as existing context, not deliverables.

## Backlog
- `docs/backlog/quota-probe-429-not-observed.md`: "Quota probe 429 responses are neither observed nor throttled" (minor, `internal/api/handlers/management/api_tools_quota.go`). It was filed during planning and is outside this plan's scope. It is still open, and no work was done on it in this run.

## External review
### codex:gpt-6-astra:high
- label: codex
- iterations: 2
- duration_ms: 888535
- ended by: done
- had findings: true

Iteration 1 (truncated: false):
- [P2] Claude fallback bypasses explicit stop rules (`sdk/cliproxy/auth/claude_fallback.go:78`) -> fixed
  - `execute`, `executeCount` and `executeStream` now keep the stop marker through `unwrapExecutionBoundaryKeepingStop`, and the fallback wrapper removes it after the decision.
  - Test: `TestClaudeFallbackHonorsRequestScopedStop`.
  - Included in `1b86ea60`.
- [P2] Explicit session bindings cross client-key profiles (`sdk/cliproxy/auth/selector.go:1187`) -> fixed
  - Binding keys for keys with their own profile get a `::key:<hash>` suffix. The suffix is carried to `OnResult` through `SessionAffinityScopeMetadataKey`.
  - `LookupAffinity` checks the key scopes the selector has already seen.
  - Keys on the shared profile still share bindings.
  - Documented in `docs/management-api-v8.md`.
  - Test: `TestSessionAffinityExplicitBindingsAreScopedPerKeyProfile`.
  - Included in `1b86ea60`.
- [P2] Rotating a key loses an empty override profile on disk (`internal/config/config_yaml.go:72`) -> fixed
  - Key-profile entries are no longer treated as empty defaults, and empty entries survive when a new section is written.
  - A related bug was fixed at the same time: fields left out of an entry are now removed from disk.
  - Test: `TestSaveConfigKeepsEmptyKeyProfilesAndDropsClearedFields`.
  - Included in `1b86ea60`.

Iteration 2 (truncated: false): NO ISSUES FOUND. The reviewer confirmed that all three earlier findings are addressed. It read the code and did not run tests. Nothing was filed to the backlog.

## Validation
- Commands: none
- Timings: duration_ms 0, runs 0
- The orchestrator ran no validation commands of its own. The agents report passing `gofmt`, `go vet ./...`, the server build and `go test ./...` (Task 9 and the Codex evaluator), but those are not orchestrator-recorded runs.