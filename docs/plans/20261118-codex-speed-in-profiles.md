# Codex speed and client-tier policy in routing profiles

## Overview
- Today one global flag, `codex.fast-mode` (v8 path `oauth.providers.codex.fast-mode`), makes the proxy add `service_tier=priority` to Codex subscription requests that carry no tier. It is the same for every client key and knows only the Fast tier.
- OpenAI released GPT-6.2 on 2026-11-17 with a new Ultrafast speed. The Codex client catalog advertises `service_tiers` `priority` and `ultrafast` for `gpt-6.2-sol`, and only `priority` for `gpt-6.2-astra` and older models.
- This plan moves the speed into the routing profile (the shared profile and every per-key profile) and adds a per-profile switch that decides whether a tier sent by the client is respected:
  - `providers.codex.speed`: `standard` | `fast` | `ultrafast`
  - `providers.codex.ignore-client-tier`: bool
- The policy covers every Codex request-building path: the four `response.create` paths (HTTP and websocket, plain and streaming) and `/responses/compact`. `response.steer` control frames only get the client tier removed.
- The global `codex.fast-mode` flag is deleted.
- Integration: two fields on the existing per-provider policy block, one exported context accessor in `sdk/cliproxy/auth`, one replaced function in the Codex executor, no new endpoints (the existing `GET /v8/management/routing/profiles` and v8 config PATCH carry the fields).
- The panel (CPA-Manager-Plus) gets its own plan afterwards and is not part of this one.

## Success Criteria
1. A profile with speed `ultrafast` yields `service_tier=ultrafast` on a model that advertises it, `priority` on a model that advertises only Fast, and no tier on a model without tiers.
2. With `ignore-client-tier: true` and speed `standard`, a client request carrying `service_tier: priority` goes upstream without a tier, on `response.create`, `/responses/compact` and `response.steer`.
3. With `ignore-client-tier: false`, the tier the client sent passes through unchanged.
4. A key with its own profile and neither field behaves as `standard` with the client tier respected.
5. Two keys with different settings get different tiers in one process.
6. A changed setting applies to the next request without a restart, and a config that still contains `oauth.providers.codex.fast-mode` loads without edits.

## Decisions
- **Context**: the design was brainstormed and approved by the user on 2026-11-18 (version 3). Direction answers from the user: the profile imposes a tier on requests without one; `ultrafast` on a model without that tier downgrades to Fast; the legacy flag is deleted instead of kept; a flag to ignore the client's tier is in scope. The remaining mechanism decisions were made from repository evidence and accepted by the user as a whole. A grill round on 2026-11-18 revised the plan; see `## Decision Log`.
- **Chosen approach**:
  1. Config: `ProviderRoutingConfig` (`internal/config/provider_routing.go:32`) gains `Speed string` (`speed`) and `IgnoreClientTier bool` (`ignore-client-tier`). The stored speed stays empty when unset. The effective view fills `standard` for Codex only, in `effectiveProviders` (`provider_routing.go:149`), not in `applyDefaults`: `applyDefaults` runs for Claude too and its result is validated again (`provider_routing.go:114-115`, `:154`, `:190-191`), so a default there would make every config fail the Claude check. Under `providers.claude` a non-empty speed or a true flag is a config error; an empty speed and a false flag are accepted, so a profile read from the API can be written back unchanged. JSON tags carry no `omitempty`, like the other fields of the block. Paths: `routing.providers.codex.*` for the shared profile and `routing.key-profiles.<key>.providers.codex.*` for a key profile.
  2. Tier resolution: one function replaces `applyCodexFastModeDoc` (`internal/runtime/executor/codex_executor_request.go:589`):
     - no `service_tier` in the request -> tier from the profile speed;
     - `service_tier` present and `ignore-client-tier` false -> untouched;
     - `service_tier` present and `ignore-client-tier` true -> the field is removed, then tier from the profile speed.
     - Tier from speed: `ultrafast` -> `ultrafast` when the model's catalog entry advertises it, else `priority` when advertised, else none; `fast` -> `priority` when advertised, else none; `standard` -> none.
     - Unchanged: API-key Codex auths are never touched, also with `ignore-client-tier`; payload rules run later in the finalizer and keep precedence.
     - The function runs on five paths: the four existing call sites and `executeCompact`. `response.steer` frames get only the removal step.
     - The function sees the request after translation. The OpenAI Responses translator already maps `fast` to `priority` and drops values it does not know, such as `auto` and `flex` (`internal/translator/codex/openai/responses/codex_openai-responses_request.go:47-62`). A dropped value counts as "no tier", so the profile speed applies even with `ignore-client-tier` false. This is the behavior fast-mode has today; the plan documents it and does not change translators.
  3. Profile access: an exported accessor in `sdk/cliproxy/auth` returns the Codex speed settings of the routing profile carried in the context. When the context has no profile (Home, direct executor calls), the executor uses the shared profile of its own config, so under Home key profiles do not apply. The Claude -> Codex fallback leg uses the Codex settings of the same profile. An open duplex websocket session keeps the profile it started with until it reconnects.
  4. Legacy flag: `codex.fast-mode` is removed (field, management path, example config, docs, tests). Its value is not migrated. A config file that still spells the key must keep loading and must keep accepting management writes.
  5. Management API: `GET /v8/management/routing/profiles` returns both fields in the effective `providers` view. Writes go through the existing v8 config PATCH. A narrow PATCH of one field must not reset the other fields of an existing profile.
- **Rejected alternatives**: none rejected by the user with a stated reason. The alternatives of the decided mechanism forks are listed in `## Technical Details` and stay open to critique.
- **Verified facts**:
  - `applyCodexFastModeDoc` is called from four places: `codex_executor_execute.go:158`, `codex_executor_stream.go:81`, `codex_websockets_execute.go:79`, `codex_websockets_stream.go:924` (inside `prepareCodexWebsocketStream`, which duplex follow-up turns also use: `codex_websockets_duplex.go:612`). `executeCompact` (`codex_executor_execute.go:432`) does not call it; commit 6d512af7 states "compact requests are unchanged".
  - `X-Codex-Routing-Hint` derives its tier from the final upstream body (`applyCodexRoutingHint`, `codex_executor_request.go:620`), so it follows the new logic without changes. It is skipped for SIWC requests (`:621`), and on websockets it is a handshake header (`codex_websockets_stream.go:972`), so it describes the first turn of a connection.
  - The private `routingProfile` (`sdk/cliproxy/auth/routing_profile.go:15`) already stores the effective `providers` block and is put in the context by the public `Execute*` (`conductor_execution.go:213`, `:295`, `:364`). Under Home the resolved profile is nil; under a scheduler plugin it is the shared profile with `providerPolicy = false` (`routing_profile.go:113-125`). `ensureRoutingProfile` keeps a profile that is already in the context (`routing_profile.go:165-167`), and a duplex session derives its context once (`codex_websockets_duplex.go:45`).
  - "A key profile replaces the shared profile entirely; no field is inherited" (`internal/config/routing_profile.go:38-39`).
  - The load path is lenient about unknown fields (`NormalizeConfigLayout(..., false)` at `internal/config/config_load.go:83`; comment at `config_v8.go:583`). Management writes validate strictly (`ValidateV8Config`, `config_v8.go:916`) after the on-disk document was normalized with `migrate = true`, which comments unknown fields out (`commentUnknownV8Sections`, `config_v8.go:669`; `TestConfigV8CommentsUnknownLegacySectionsOnWrite`, `internal/api/handlers/management/config_v8_test.go:117`). An unknown key under `oauth.providers.codex` is already rejected on PUT (`config_v8_test.go:231`). This was read, not run, for the exact key `fast-mode`; Task 5 locks it with tests.
  - `TestV8ExampleLoadsAndRoundTrips` (`internal/config/config_v8_test.go:18`) validates `config.example.yaml` strictly, so the example must change in the same task as the config struct.
  - The live deployment (checked 2026-11-18) has `oauth.providers.codex.fast-mode: false`, 7 client keys, no key profiles and no payload rules. No data migration is needed.
  - The fast-mode feature is fork-only (commit 6d512af7, 2026-10-04); upstream router-for-me has no such flag.
  - Usage records already carry the requested, effective and response service tier (`internal/runtime/executor/helps/usage_helpers.go:728-730`).
  - The `service_tier` copied into duplex response-settings snapshots (`codex_websockets_duplex.go:528`, `:962`) is never sent upstream: the snapshots are read for `instructions` (`:416`, `:442`) and for usage reporting (`:975`, `:1151`); prepared turns are built by `prepareCodexWebsocketStream` (`:612`) and local compaction goes through `e.Execute` (`codex_executor_execute.go:404`).
  - `response.steer` frames are built from the incoming payload and only run the payload finalizer (`codex_websockets_duplex.go:735-741`); "control frames bypass response.create translations and built-in defaults" (`:764`).
  - Existing executor tests take the `priority` tier of `gpt-5.5` from the embedded catalog (`codex_executor_fast_mode_test.go:32`). Upstream commit a5e8f69e removes that tier from `gpt-5.5`, and the embedded catalog on fork `main` has no model with `ultrafast`. `CodexClientModelSupportsServiceTier` is generic over the tier id (`internal/registry/codex_client_models.go:62-82`), so the registry needs no change.

## Context (from discovery)
- Files/components involved:
  - `internal/config/provider_routing.go`, `internal/config/routing_profile.go`, `internal/config/config_types.go`, `internal/config/config_yaml.go` (pruning, `shouldPruneNestedMappingKeys` at `:947`)
  - `sdk/cliproxy/auth/routing_profile.go`
  - `internal/runtime/executor/codex_executor_request.go`, `codex_executor_execute.go`, `codex_executor_stream.go`, `codex_websockets_execute.go`, `codex_websockets_stream.go`, `codex_websockets_duplex.go`
  - `internal/api/handlers/management/routing_profiles.go`, `routing_api_credentials.go` (`effectiveProvidersView` at `:60`), `config_v8.go`
  - `config.example.yaml` (`:1343-1345`), `docs/management-api-v8.md` (`:43` and the routing sections), `AGENTS.md`
- Related patterns found:
  - per-provider validation with a provider-specific error: `provider_routing.go:143` ("fallback.target codex is only supported for claude")
  - exported context accessor in `sdk/cliproxy/auth`: `RequestScopedErrorConfigFromContext` (`conductor_request_scoped_errors.go:32`)
  - stored-empty, effective-default values: `effectiveProviders` and `EffectiveProviders` (`provider_routing.go:149`, `:163`)
- Test helpers to reuse:
  - `internal/config/provider_routing_save_test.go`: `writeLoad` (`:13`), `saveRead` (`:27`), `routingNode` (`:40`)
  - `sdk/cliproxy/auth/routing_profile_test.go`: `ginContextWithKey` (`:25`), `routingProfileTestConfig` (`:34`)
  - `internal/runtime/executor`: `newCodexRoutingHintServer`, `codexOAuthTestAuth` (non-SIWC), `codexAPIKeyTestAuth` (`codex_executor_routing_hint_test.go:26`, `:45`, `:53`), `newCodexWebsocketHintServer` (`codex_websockets_routing_hint_test.go:30`), the manager-driven pattern of `TestCodexDuplexSummaryCredentialFailuresThroughManager` (`codex_websockets_duplex_health_test.go:94`), the duplex tests in `codex_websockets_duplex_test.go`, the compact test at `codex_executor_compact_test.go:1365`
  - `internal/api/handlers/management`: `newRoutingProfilesTestHandler` and the table in `TestConfigV8InvalidProvidersWriteRejected` (`routing_providers_write_test.go:322`)
- Dependencies identified: none new. JSON goes through `github.com/MoeForge/moego/ojson` only.

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- Follow `AGENTS.md`: comments in English, no `encoding/json`/`gjson`/`sjson`, mutate request bodies through `*ojson.Doc`, executor helpers under `internal/runtime/executor/helps/`, `nowFunc` in time-dependent tests, no standalone changes to `internal/translator/`
- After Go changes in every task: `gofmt -w` on touched files, then `go build -o test-output ./cmd/server && rm test-output`
- Do not merge upstream as part of this plan

## Testing Strategy
- **Unit tests**: required for every task. Run the touched packages per task, for example `go test ./internal/config/... ./sdk/cliproxy/auth/... ./internal/runtime/executor/... ./internal/api/handlers/management/...`
- **Full suite**: `go test ./...` in the verification task. Known flaky tests listed in `docs/backlog/` may be re-run once; do not modify them.
- **Catalog in executor tests**: tests must not depend on which tiers the embedded catalog gives to a real model. They install a fixture lookup with three models (ultrafast + priority, priority only, no tiers) through the package-level variable added in Task 3.
- **Shape of executor tests**: the full combination table runs against the resolution function on an `*ojson.Doc`; each transport gets one end-to-end smoke case. All call sites share one function, so crossing the table with every transport adds no coverage.
- **E2E tests**: the project has no UI e2e tests; none are added.

## Progress Tracking
- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Update plan if implementation deviates from original scope
- Keep plan in sync with actual work done

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code changes, tests, documentation in this repository
- **Post-Completion** (no checkboxes): deploy, live checks against OpenAI, the panel plan
- **Checkbox placement**: only in Task sections

## Decision Log
- 2026-11-18 brainstorm: **accepted** - the fields live in the profile's per-provider block as `providers.codex.speed` and `providers.codex.ignore-client-tier`; evidence: `internal/config/provider_routing.go:32` is the existing per-provider policy of a profile.
- 2026-11-18 brainstorm: **accepted** - speed values are `standard` / `fast` / `ultrafast`, empty means `standard`; evidence: the Codex catalog names them in `additional_speed_tiers`, and empty-means-default follows `applyDefaults` (`provider_routing.go:84`).
- 2026-11-18 brainstorm: **accepted** - `ultrafast` on a model that advertises only Fast downgrades to `priority`; evidence: user's answer.
- 2026-11-18 brainstorm: **accepted** - `codex.fast-mode` is deleted and its value is not migrated, while a config that still spells the key keeps loading; evidence: user's instruction, the live value is `false`, lenient load at `config_load.go:83`.
- 2026-11-18 brainstorm: **accepted** - a per-profile bool `ignore-client-tier` (default false) removes any tier sent by the client; evidence: user's request.
- 2026-11-18 brainstorm: **accepted** - the flag and the speed apply to subscription (OAuth) requests only, API-key Codex auths stay untouched; evidence: the existing rule in `codex_executor_request.go:590`.
- 2026-11-18 brainstorm: **accepted** - either field under `providers.claude` is a config error; evidence: Claude speed is chosen by the client (`internal/runtime/executor/claude_executor.go:467`), validation pattern at `provider_routing.go:143`.
- 2026-11-18 brainstorm: **accepted** - the executor reads the settings through an exported accessor in `sdk/cliproxy/auth` and falls back to the shared profile of its config; evidence: the profile is put in the context at `conductor_execution.go:213`, accessor pattern at `conductor_request_scoped_errors.go:32`.
- 2026-11-18 brainstorm: **accepted** - no retry and no downgrade when upstream rejects ultrafast for an account without the entitlement; evidence: the error shape is unknown, and usage already records the tiers (`helps/usage_helpers.go:728`).
- 2026-11-18 brainstorm: **accepted** - executor tests use a fixture catalog instead of the embedded one; evidence: `codex_executor_fast_mode_test.go:32` depends on a tier upstream removed in a5e8f69e.
- 2026-11-18 brainstorm: **accepted** - backend plan now, panel plan second, upstream merge not included; evidence: loopai runs one plan per repository.
- 2026-11-18 grill: **accepted** - `/responses/compact` gets the full rule (client tier removed under the flag, profile speed injected) as a fifth call site in Task 3, and `response.steer` frames get only the removal in Task 4.
- 2026-11-18 grill: **accepted** - the `standard` default is filled for Codex only in `effectiveProviders`, `applyDefaults` stays untouched, and the Claude check accepts an empty speed and a false flag (Task 1) - decided by grill: `applyDefaults` runs for Claude and its result is re-validated (`provider_routing.go:114-115`, `:154`, `:190-191`), so the original wording would fail every config load.
- 2026-11-18 grill: **accepted** - the new JSON tags carry no `omitempty`, and Task 6 tests that a profile body from GET can be sent back as a PATCH - decided by grill: the existing tags of the block have none (`provider_routing.go:33-35`) and `effectiveProvidersView` returns the struct unchanged (`routing_api_credentials.go:60-69`).
- 2026-11-18 grill: **accepted** - Task 4 no longer traces the duplex snapshots; the result is recorded under Verified facts and the task keeps the regression tests - decided by grill: the snapshot tier is read only for instructions and usage reporting (`codex_websockets_duplex.go:416`, `:442`, `:975`, `:1151`).
- 2026-11-18 grill: **accepted** - `config.example.yaml` edits moved into Task 1 (new fields) and Task 5 (removal), and both tasks run `TestV8ExampleLoadsAndRoundTrips` - decided by grill: that test validates the example strictly (`config_v8_test.go:18-32`), so it would fail between Task 5 and the documentation task.
- 2026-11-18 grill: **accepted** - the two-key, context-precedence and hot-reload checks are one manager-driven test in `internal/runtime/executor` (Task 3); the manager-level tests and the `apiFallbackProfile` case of Task 2 and the "confirm Clone..." checkbox of Task 1 are dropped - decided by grill: the fixture variable is unexported so `test/` cannot install it, executor tests already drive a `Manager` (`codex_websockets_duplex_health_test.go:94`), `withRoutingProfile` is unexported, and per-key resolution with a fresh config snapshot is existing behavior (`sdk/cliproxy/auth/routing_profile.go:117`, `:131`).
- 2026-11-18 grill: **accepted** - the executor combination table runs against the function on an `*ojson.Doc`, with one smoke case per transport (Task 3) - decided by grill: every call site calls the same function.
- 2026-11-18 grill: **accepted** - the registry `ultrafast` test is dropped - decided by grill: `CodexClientModelSupportsServiceTier` is generic over the tier id (`internal/registry/codex_client_models.go:62-82`) and the plan does not change it.
- 2026-11-18 grill: **accepted** - Task 5 drops the "PUT returns 400" test and loads one case (`fast-mode: true`); the old test file is rewritten instead of deleted - decided by grill: an unknown key under `oauth.providers.codex` is already rejected in `config_v8_test.go:231`, and the value is not migrated, so `true` and `false` take the same path.
- 2026-11-18 grill: **accepted** - Task 6 loses its two "confirm" checkboxes, merges the GET and PATCH tests, and adds the rejected writes to the existing table - decided by grill: the view and merge code is field-agnostic (`routing_api_credentials.go:60-66`) and the table exists at `routing_providers_write_test.go:322`.
- 2026-11-18 grill: **accepted** - Task 1 tests clearing a field of the shared profile through the save path, with a conditional pruning fix - decided by grill: `shouldPruneNestedMappingKeys` prunes only key-profile subtrees (`internal/config/config_yaml.go:947-967`).
- 2026-11-18 grill: **accepted** - the tasks name the existing test helpers (section "Test helpers to reuse") - decided by grill: the helpers exist at the cited lines.
- 2026-11-18 grill: **accepted** - a `## Success Criteria` section lists the six criteria, and Task 7 maps each to a test instead of re-verifying by hand - decided by grill: the plan referred to numbered criteria it never listed.
- 2026-11-18 grill: **accepted** - translator normalization of the client tier is documented and covered by one test, with no translator change - decided by grill: the translator drops `auto`/`flex` before the executor runs (`codex_openai-responses_request.go:47-62`), fast-mode behaves this way today, and `AGENTS.md` forbids standalone translator changes.
- 2026-11-18 grill: **accepted** - the docs state that Home ignores key profiles and that an open duplex session keeps its starting profile - decided by grill: `routing_profile.go:114-116`, `:165-167` and `codex_websockets_duplex.go:45`.
- 2026-11-18 grill: **accepted** - websocket hint assertions are scoped to a fresh connection and the fixture auth is the non-SIWC `codexOAuthTestAuth` - decided by grill: the hint is a handshake header (`codex_websockets_stream.go:972`) and is skipped for SIWC (`codex_executor_request.go:621`).
- 2026-11-18 grill: **accepted** - Post-Completion checks that the active catalog advertises the expected tiers before a live result is interpreted - decided by grill: the lookup needs an exact advertised tier (`codex_client_models.go:62`) and the catalog refresh can be disabled (`internal/registry/catalog_sources.go:61`).

## Implementation Steps

### Task 1: Add `speed` and `ignore-client-tier` to the provider routing config
- [x] in `internal/config/provider_routing.go` add constants `CodexSpeedStandard`, `CodexSpeedFast`, `CodexSpeedUltrafast` and the fields `Speed string` (`yaml:"speed,omitempty" json:"speed"`) and `IgnoreClientTier bool` (`yaml:"ignore-client-tier,omitempty" json:"ignore-client-tier"`) on `ProviderRoutingConfig`
- [x] in `normalize` trim and lower-case `Speed` and validate the stored value: empty or one of the three constants, otherwise `"<path>.speed has unsupported value %q"`
- [x] in `normalize` reject, for a provider other than `codex`, a non-empty `Speed` (`"<path>.speed is only supported for codex"`) and a true `IgnoreClientTier` (`"<path>.ignore-client-tier is only supported for codex"`); an empty speed and a false flag stay valid there
- [x] leave `applyDefaults` unchanged; in `effectiveProviders` (`provider_routing.go:149`) set `out.Codex.Speed` to `standard` when it is empty, so the Claude block stays empty through the second validation at `:190-191`
- [x] document both fields in the `routing.providers.codex` and `routing.key-profiles` examples of `config.example.yaml`, including the downgrade rule and the subscription-only scope
- [x] write a table test for `normalize`: valid values, case and whitespace, unknown value, both Claude errors, Claude with an empty speed and a false flag accepted, stored value stays empty
- [x] write a test that a config without the new fields passes `normalizeProfileProviders` for the shared profile and a key profile, and that `EffectiveProviders` returns `standard` for Codex and an empty speed for Claude
- [x] write save tests with `writeLoad`, `saveRead` and `routingNode`: a key profile keeps both fields; a config without them saves byte-identical; clearing a field of a key profile removes it from the file
- [x] write a save test that clears `routing.providers.codex.speed` and `ignore-client-tier` on the shared profile, saves, reloads, and finds the keys gone; if a cleared key survives, extend `shouldPruneNestedMappingKeys` (`internal/config/config_yaml.go:947`) to these two keys under `routing.providers.codex`
  - ⚠️ the cleared keys survived; `shouldPruneNestedMappingKeys` prunes a whole mapping, so the fix is a dedicated `dropClearedSharedCodexSpeedFields` in `internal/config/config_yaml.go` that drops only these two keys before the merge. `TestGetRoutingProfilesProvidersView` (management) now expects `speed: standard` in the effective Codex view.
- [x] run `go test ./internal/config/...` (includes `TestV8ExampleLoadsAndRoundTrips`), gofmt, compile check - must pass before task 2

### Task 2: Export the Codex speed settings of the request's routing profile
- [x] in `sdk/cliproxy/auth/routing_profile.go` add an exported accessor (for example `CodexSpeedFromContext(ctx context.Context) (speed string, ignoreClientTier bool, ok bool)`) that reads `routingProfileFromContext(ctx).providers.Codex`; `ok` is false when the context carries no profile
- [x] the accessor must not depend on `providerPolicy`: a scheduler-plugin profile (`providerPolicy = false`) still returns the shared profile's settings
- [x] write accessor tests with `ginContextWithKey` and `routingProfileTestConfig`: no profile in the context, a key profile, the shared profile, and a profile with `providerPolicy = false`
- [x] run `go test ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 3

### Task 3: Resolve the Codex service tier from the profile in the executor
- [x] in `internal/runtime/executor/codex_executor_request.go` replace `applyCodexFastModeDoc` with a function that takes the request context and implements the rules from `## Technical Details` (client tier kept or removed, tier from speed with the ultrafast -> priority -> none downgrade, API-key auths untouched)
- [x] read the settings through the Task 2 accessor; when it reports no profile, use `cfg.Routing.SharedProfile().EffectiveProviders().Codex`; stop reading `cfg.Codex.FastMode`
- [x] route the catalog lookup through a package-level function variable that defaults to `registry.CodexClientModelSupportsServiceTier`, so tests can install a fixture without mutating the global catalog
- [x] update the four call sites (`codex_executor_execute.go:158`, `codex_executor_stream.go:81`, `codex_websockets_execute.go:79`, `codex_websockets_stream.go:924`) and their comments; the call stays before the payload-rule finalizer
- [x] add the call to `executeCompact` (`codex_executor_execute.go:432`) after `ApplyRequestThinkingDoc` and before the payload finalizer, so `/responses/compact` follows the same rule
- [x] rewrite `codex_executor_fast_mode_test.go` (renamed to `codex_executor_service_tier_test.go`): a table test that calls the function on an `*ojson.Doc` over speed x fixture model x client tier (absent, `priority`, `ultrafast`) x `ignore-client-tier` x auth kind (OAuth, API key), with the settings set on `cfg.Routing.Providers.Codex`
- [x] write one end-to-end smoke case per transport (HTTP non-stream, HTTP stream, websocket execute, websocket stream) with `newCodexRoutingHintServer` / `newCodexWebsocketHintServer` and `codexOAuthTestAuth`, asserting the upstream body and `X-Codex-Routing-Hint`; assert the websocket hint on a fresh connection only
- [x] write compact tests modelled on `codex_executor_compact_test.go:1365`: the flag strips a client `service_tier`, speed `fast` injects `priority`, an API-key auth is untouched
- [x] write tests that payload rules still win on a create path and on compact: a filter on `service_tier` removes the injected tier, an override replaces it (the rule names no protocol: create paths report `codex`, compact reports `openai-response`)
- [x] write a test for translator normalization: an OpenAI Responses request with `service_tier: "auto"` and `ignore-client-tier` false goes upstream with the profile's tier
- [x] write a manager-driven test in `internal/runtime/executor` (pattern of `TestCodexDuplexSummaryCredentialFailuresThroughManager`): `cliproxyauth.NewManager`, the real Codex executor, `SetConfig`, key contexts built like `ginContextWithKey`; two keys with different key profiles and one key without a profile get different tiers, the key settings differ from the shared ones, and after `SetConfig` with a changed speed the next `Execute` uses the new value
- [x] run `go test ./internal/runtime/executor/... ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 4

### Task 4: Apply the client-tier removal to duplex steering and lock the duplex path
- [x] in `codex_websockets_duplex.go` (`:735-741`) delete `service_tier` from the `response.steer` document before `FinalizePayloadDoc` when the auth is not an API key and the profile's `ignore-client-tier` is true; do not inject a speed tier into steer frames
- [x] write a duplex test in `codex_websockets_duplex_test.go`: with `ignore-client-tier` true and speed `standard`, a client turn carrying `service_tier: priority` and its follow-up turn reach the upstream socket without a tier (assert on the captured frames)
- [x] write a duplex test for steering: a `response.steer` frame carrying `service_tier` reaches upstream without it when the flag is true and unchanged when the flag is false
- [x] run `go test ./internal/runtime/executor/...`, gofmt, compile check - must pass before task 5

### Task 5: Remove `codex.fast-mode`
- [x] delete the `FastMode` field (`internal/config/config_types.go:362-364`) and the `fast-mode` block in `config.example.yaml` (`:1343-1345`); `grep -rn -iE 'FastMode|fast-mode'` over Go sources must then match only Claude and Devin code (the leftover-key tests spell the removed key on purpose; the executor fixture `tierFixtureUltrafastModel` was renamed to `tierFixtureUltraModel` so it no longer matches)
- [x] rewrite `internal/api/handlers/management/config_codex_fast_mode_test.go` into the leftover-key tests below, reusing its router setup (`:25-27`)
- [x] write a load test: a v8 config containing `oauth.providers.codex.fast-mode: true` loads through `config.LoadConfig`, keeps its other `oauth.providers.codex` settings, and yields speed `standard` for the shared profile
- [x] write a management test: on a config file that still contains the key, a PATCH of an unrelated field returns 200, the saved file has no active `fast-mode` key, and its other settings are preserved
- [x] if either test shows the leftover key breaking load or writes, drop the key explicitly in `NormalizeConfigLayout` with one warning that names `routing.providers.codex.speed`, and keep both tests (not needed: both tests pass; the lenient load ignores the key and the write path comments it out through `commentUnknownV8Sections`)
- [x] run `go test ./internal/config/... ./internal/api/handlers/management/...` (includes `TestV8ExampleLoadsAndRoundTrips`), gofmt, compile check - must pass before task 6

### Task 6: Lock the management API behavior for the new fields
- [x] write a GET test for `/v8/management/routing/profiles` with `newRoutingProfilesTestHandler`: Codex shows `speed: standard` and `ignore-client-tier: false` by default, explicit values appear for the shared profile and a key profile, and the Claude block shows an empty speed
- [x] write a PATCH test: setting only `providers.codex.speed` (and, separately, only `ignore-client-tier`) on the shared profile and on an existing key profile changes the GET view and the saved file while `session-affinity`, `preferred-accounts`, `selection` and the rest of `providers` stay intact; clearing the field with `null` removes it from the file and returns the view to `standard`
  - ⚠️ the v8 PATCH keeps an explicit `null` by design (`mergeConfigV8Patch`, `config_v8.go:385`: "DELETE is the explicit field-removal operation"), so a `null` PATCH stores `speed: null`, which loads as empty and returns the view to `standard`; the test locks that and checks that DELETE of the field path removes it from the file. No management change was made.
- [x] write a round-trip test: the profile body returned by GET is sent back as a PATCH and returns 200
- [x] add three cases to `TestConfigV8InvalidProvidersWriteRejected` (`routing_providers_write_test.go:322`): an unknown speed value, `speed` under `providers.claude`, `ignore-client-tier: true` under `providers.claude`
- [x] fix the management code only where one of these tests fails
- [x] run `go test ./internal/api/handlers/management/... ./internal/config/...`, gofmt, compile check - must pass before task 7

### Task 7: Verify acceptance criteria
- [x] for each of the six items in `## Success Criteria`, name the test that proves it in a short list under `## Technical Details`; add a test where one is missing
  - criterion 4 had no end-to-end case: `TestCodexProfileSpeedThroughManager` gained a key whose profile sets neither field, and its reload step now also turns on the shared `ignore-client-tier`
- [x] run the full suite `go test ./...` (known flaky tests in `docs/backlog/` may be re-run once; do not modify them)
- [x] run `gofmt -l .` (no output) and the compile check `go build -o test-output ./cmd/server && rm test-output`
- [x] run `go vet` on the touched packages and fix findings in touched code

### Task 8: Update documentation
- [x] `docs/management-api-v8.md`: remove the `/config/oauth/providers/codex/fast-mode` row (`:43`) and add both fields to the routing profile sections and the `GET /routing/profiles` description
- [x] `docs/management-api-v8.md`: describe the behavior a client must know: a PATCH for a key without a profile creates a profile holding only the written fields, so a full profile must be sent when one is created; `/responses/compact` follows the same rule and `response.steer` only loses the client tier; a tier the translator drops (`auto`, `flex`) counts as absent; Home ignores key profiles; an open duplex websocket session keeps its starting profile until it reconnects
- [x] ➕ `docs/management-api-v8.md`: state that a PATCH with `null` for `speed` or `ignore-client-tier` stores `null` (read as the default) and that DELETE of the field path removes it from the file
- [x] `AGENTS.md`: add one sentence to the `sdk/cliproxy/auth` paragraph naming the accessor and the rule that the Codex executor reads the speed policy from the context profile
- [x] re-read the `config.example.yaml` comments written in Tasks 1 and 5 against the final behavior and correct them if they drifted; re-run `go test ./internal/config/...` when the file changes

## Technical Details

### Fields
| Path | Type | Default | Notes |
|---|---|---|---|
| `routing.providers.codex.speed` | string | `standard` (stored empty) | `standard`, `fast`, `ultrafast` |
| `routing.providers.codex.ignore-client-tier` | bool | `false` | removes any client `service_tier` |
| `routing.key-profiles.<key>.providers.codex.speed` | string | `standard` | not inherited from the shared profile |
| `routing.key-profiles.<key>.providers.codex.ignore-client-tier` | bool | `false` | not inherited from the shared profile |

### Resolution
1. Skip when the auth is a Codex API key.
2. Read `speed` and `ignore-client-tier` from the context profile; without a profile, from the shared profile of the executor config.
3. If the body has `service_tier` and `ignore-client-tier` is false, stop.
4. If the body has `service_tier` and `ignore-client-tier` is true, delete the field.
5. Choose the tier: `ultrafast` -> first advertised of [`ultrafast`, `priority`]; `fast` -> `priority` when advertised; `standard` -> none.
6. Set `service_tier` when a tier was chosen. Payload rules run afterwards.

### Where the rule runs
| Path | Removal under the flag | Speed injection |
|---|---|---|
| HTTP `response.create`, plain and streaming | yes | yes |
| Websocket `response.create`, plain and streaming, duplex follow-up turns | yes | yes |
| `/responses/compact` | yes | yes |
| `response.steer` control frames | yes | no |
| Codex API-key auths, any path | no | no |

### Success criteria coverage
1. Ultrafast downgrade: `TestApplyCodexServiceTierDoc` (speed `ultrafast` rows over the three fixture models), end to end in `TestCodexExecutorProfileSpeedHTTP` and `TestCodexProfileSpeedThroughManager` (`internal/runtime/executor/codex_executor_service_tier_test.go`).
2. Client tier removed under the flag: `TestApplyCodexServiceTierDoc` (speed `standard` with `ignore` rows) for `response.create`; `TestCodexDuplexIgnoreClientTierOnCreateTurns` for websocket create and follow-up turns; `TestCodexExecutorCompactProfileSpeed` ("flag strips client tier") for `/responses/compact`; `TestCodexDuplexSteerClientTier` and `TestRemoveCodexClientServiceTierDoc` for `response.steer`.
3. Client tier respected without the flag: `TestApplyCodexServiceTierDoc` (rows without `ignore`), `TestCodexExecutorCompactProfileSpeed` ("client tier kept without flag"), `TestCodexDuplexSteerClientTier` (flag false).
4. Key profile without either field: `TestProvidersRoutingCodexSpeedDefaults` (`internal/config`), `TestCodexSpeedFromContext` ("key profile without codex settings is not inherited", `sdk/cliproxy/auth`), and end to end the `keyNoCodex` key in `TestCodexProfileSpeedThroughManager` (added in Task 7). That key keeps `standard` and the client tier while the shared profile is `ultrafast` with `ignore-client-tier`.
5. Two keys with different tiers in one process: `TestCodexProfileSpeedThroughManager`.
6. Hot reload and the leftover key: `TestCodexProfileSpeedThroughManager` (the `SetConfig` step, no restart), `TestConfigV8LeftoverCodexFastModeLoads` and `TestConfigV8LeftoverCodexFastModeAcceptsWrites` (`internal/api/handlers/management/config_codex_fast_mode_test.go`).

### Decision table (from the brainstorm)
| Decision | Chosen | Alternatives not taken |
|---|---|---|
| Field location | per-provider block of the profile | top-level profile field; a map outside profiles |
| Speed values | `standard` / `fast` / `ultrafast` | storing tier ids (`priority`, `ultrafast`); two bools |
| Ultrafast on a model without it | downgrade to `priority` | leave standard |
| Legacy `codex.fast-mode` | deleted, not migrated | keep it as a legacy source for the shared profile |
| Client tier | bool `ignore-client-tier` | out of scope; an enum |
| Scope | subscription requests only | also API-key requests |
| Fields under `providers.claude` | config error | silently ignored |
| Settings access | exported accessor reading the context profile | passing through `Options`; resolving the key profile again in the executor |
| Missing ultrafast entitlement | no retry, no downgrade on error | a request-scoped error class like Claude Fast |
| Catalog in tests | fixture | merge upstream first and use the embedded `gpt-6.2-sol` |

### Non-goals
- Speed or the flag for Claude
- Handling an upstream rejection of ultrafast
- Carrying the client's original tier past translator normalization
- Merging upstream router-for-me (v8.0.77)
- Panel work and ultrafast pricing in monitoring (panel backlog: `docs/backlog/ultrafast-service-tier-priced-as-standard.md` in CPA-Manager-Plus)

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- Before any live check, confirm that the active Codex catalog advertises `ultrafast` for `gpt-6.2-sol` and `priority` for `gpt-6.2-astra`. A missing advertisement is a catalog problem (disabled refresh, embedded catalog without the upstream merge), not an entitlement problem.
- After deploy, send one request for `gpt-6.2-sol` through a key whose profile has `speed: ultrafast` and check the usage record: requested, effective and response tier. Then watch whether the credential entered a cooldown. An account whose plan lacks Ultrafast may answer with an error; the shape of that error decides whether a follow-up plan is needed.
- Repeat with `gpt-6.2-astra` and confirm the request went out with `priority`.
- Send a request with a client `service_tier` through a profile with `ignore-client-tier: true` and confirm the usage record shows the profile's tier.
- Compaction now uses the profile speed, so it spends the limit at the fast rate; watch the usage after the first long session.

**External system updates**:
- The live config keeps `fast-mode: false` until the first management write comments it out; no manual edit is required.
- Until the panel plan ships, the Fast toggle in the deployed panel returns an error; set the speed with a config PATCH.
- Panel plan (CPA-Manager-Plus): speed control and the flag in the profile editor; a speed table opened from the toolbar with one row per shared profile and client key, applied on click, creating a key profile as a full copy of the shared profile for a key without one; removal of the Fast toolbar toggle.
- The embedded Codex catalog gets `gpt-6.2-*` only with the upstream merge; a running instance already receives it from the remote catalog refresh.
