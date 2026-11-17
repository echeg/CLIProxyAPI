# Codex speed and client-tier policy in routing profiles

## Overview
- Today one global flag, `codex.fast-mode` (v8 path `oauth.providers.codex.fast-mode`), makes the proxy add `service_tier=priority` to Codex subscription requests that carry no tier. It is the same for every client key and knows only the Fast tier.
- OpenAI released GPT-6.2 on 2026-11-17 with a new Ultrafast speed. The Codex client catalog advertises `service_tiers` `priority` and `ultrafast` for `gpt-6.2-sol`, and only `priority` for `gpt-6.2-astra` and older models.
- This plan moves the speed into the routing profile (the shared profile and every per-key profile) and adds a per-profile switch that decides whether a tier sent by the client is respected:
  - `providers.codex.speed`: `standard` | `fast` | `ultrafast`
  - `providers.codex.ignore-client-tier`: bool
- The global `codex.fast-mode` flag is deleted.
- Integration: two fields on the existing per-provider policy block, one exported context accessor in `sdk/cliproxy/auth`, one replaced function in the Codex executor, no new endpoints (the existing `GET /v8/management/routing/profiles` and v8 config PATCH carry the fields).
- The panel (CPA-Manager-Plus) gets its own plan afterwards and is not part of this one.

## Decisions
- **Context**: the design was brainstormed and approved by the user on 2026-11-18 (version 3). Direction answers from the user: the profile imposes a tier on requests without one; `ultrafast` on a model without that tier downgrades to Fast; the legacy flag is deleted instead of kept; a flag to ignore the client's tier is in scope. The remaining mechanism decisions were made from repository evidence and accepted by the user as a whole.
- **Chosen approach**:
  1. Config: `ProviderRoutingConfig` (`internal/config/provider_routing.go:32`) gains `Speed string` (`speed`) and `IgnoreClientTier bool` (`ignore-client-tier`). Empty speed means `standard`; the stored value stays empty and the effective view fills the default, like `mode` and `fallback.target` (`applyDefaults`, `provider_routing.go:84`). Either field under `providers.claude` is a config error. Paths: `routing.providers.codex.*` for the shared profile and `routing.key-profiles.<key>.providers.codex.*` for a key profile.
  2. Tier resolution: one function replaces `applyCodexFastModeDoc` (`internal/runtime/executor/codex_executor_request.go:589`):
     - no `service_tier` in the request -> tier from the profile speed;
     - `service_tier` present and `ignore-client-tier` false -> untouched;
     - `service_tier` present and `ignore-client-tier` true -> the field is removed (any value, including `flex` and `auto`), then tier from the profile speed.
     - Tier from speed: `ultrafast` -> `ultrafast` when the model's catalog entry advertises it, else `priority` when advertised, else none; `fast` -> `priority` when advertised, else none; `standard` -> none.
     - Unchanged: API-key Codex auths are never touched, also with `ignore-client-tier`; payload rules run later in the finalizer and keep precedence.
  3. Profile access: an exported accessor in `sdk/cliproxy/auth` returns the Codex speed settings of the routing profile carried in the context. When the context has no profile (Home, direct executor calls), the executor uses the shared profile of its own config. The Claude -> Codex fallback leg uses the Codex settings of the same profile.
  4. Legacy flag: `codex.fast-mode` is removed (field, management path, example config, docs, tests). Its value is not migrated. A config file that still spells the key must keep loading and must keep accepting management writes.
  5. Management API: `GET /v8/management/routing/profiles` returns both fields in the effective `providers` view. Writes go through the existing v8 config PATCH. A narrow PATCH of one field must not reset the other fields of an existing profile.
- **Rejected alternatives**: none rejected by the user with a stated reason. The alternatives of the decided mechanism forks are listed in `## Technical Details` and stay open to critique.
- **Verified facts**:
  - `applyCodexFastModeDoc` is called from four places: `codex_executor_execute.go:158`, `codex_executor_stream.go:81`, `codex_websockets_execute.go:79`, `codex_websockets_stream.go:924` (inside `prepareCodexWebsocketStream`, which duplex follow-up turns also use: `codex_websockets_duplex.go:612`).
  - `X-Codex-Routing-Hint` derives its tier from the final upstream body (`applyCodexRoutingHint`, `codex_executor_request.go:620`), so it follows the new logic without changes.
  - The private `routingProfile` (`sdk/cliproxy/auth/routing_profile.go:15`) already stores the effective `providers` block and is put in the context by the public `Execute*` (`conductor_execution.go:213`, `:295`, `:364`). Under Home the resolved profile is nil; under a scheduler plugin it is the shared profile with `providerPolicy = false` (`routing_profile.go:113-125`).
  - "A key profile replaces the shared profile entirely; no field is inherited" (`internal/config/routing_profile.go:38-39`).
  - The load path is lenient about unknown fields (`NormalizeConfigLayout(..., false)` at `internal/config/config_load.go:83`; comment at `config_v8.go:583`: "Existing unknown fields are ignored by the runtime"). Management writes validate strictly (`ValidateV8Config`, `config_v8.go:916`, `KnownFields(true)` at `:972`) after the on-disk document was normalized with `migrate = true`, which comments unknown fields out (`commentUnknownV8Sections`, `config_v8.go:669`; `TestConfigV8CommentsUnknownLegacySectionsOnWrite`, `internal/api/handlers/management/config_v8_test.go:117`). This was read, not run, for the exact path `oauth.providers.codex.fast-mode`; Task 5 locks it with tests.
  - The live deployment (checked 2026-11-18) has `oauth.providers.codex.fast-mode: false`, 7 client keys, no key profiles and no payload rules. No data migration is needed.
  - The fast-mode feature is fork-only (commit 6d512af7, 2026-10-04); upstream router-for-me has no such flag.
  - Usage records already carry the requested, effective and response service tier (`internal/runtime/executor/helps/usage_helpers.go:728-730`).
  - The duplex websocket path copies `service_tier` into response-settings snapshots separately: from the raw next request payload (`codex_websockets_duplex.go:528`) and from the prepared body (`:962`). Whether the first copy can send a client tier upstream that the policy removed is not verified; Task 4 traces it.
  - Existing executor tests take the `priority` tier of `gpt-5.5` from the embedded catalog (`codex_executor_fast_mode_test.go:32`). Upstream commit a5e8f69e removes that tier from `gpt-5.5`, and the embedded catalog on fork `main` has no model with `ultrafast`.

## Context (from discovery)
- Files/components involved:
  - `internal/config/provider_routing.go`, `internal/config/routing_profile.go`, `internal/config/config_types.go`, `internal/config/config_yaml.go` (key-profile pruning, `shouldPruneNestedMappingKeys` at `:947`)
  - `sdk/cliproxy/auth/routing_profile.go`, `sdk/cliproxy/auth/provider_fallback.go` (`apiFallbackProfile` at `:126`)
  - `internal/runtime/executor/codex_executor_request.go`, `codex_executor_execute.go`, `codex_executor_stream.go`, `codex_websockets_execute.go`, `codex_websockets_stream.go`, `codex_websockets_duplex.go`
  - `internal/registry/codex_client_models.go` (`CodexClientModelSupportsServiceTier` at `:62`)
  - `internal/api/handlers/management/routing_profiles.go`, `routing_api_credentials.go` (`effectiveProvidersView` at `:60`), `routing_providers_write.go`, `config_v8.go`
  - `config.example.yaml` (`:1343-1345`), `docs/management-api-v8.md` (`:43` and the routing sections), `AGENTS.md`
- Related patterns found:
  - per-provider validation with a provider-specific error: `provider_routing.go:143` ("fallback.target codex is only supported for claude")
  - exported context accessor in `sdk/cliproxy/auth`: `RequestScopedErrorConfigFromContext` (`conductor_request_scoped_errors.go:32`)
  - stored-empty, effective-default values: `applyDefaults` and `EffectiveProviders` (`provider_routing.go:84`, `:163`)
- Dependencies identified: none new. JSON goes through `github.com/MoeForge/moego/ojson` only.

## Development Approach
- **Testing approach**: Regular (code first, then tests)
- Complete each task fully before moving to the next
- Make small, focused changes
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- Follow `AGENTS.md`: comments in English, no `encoding/json`/`gjson`/`sjson`, mutate request bodies through `*ojson.Doc`, executor helpers under `internal/runtime/executor/helps/`, `nowFunc` in time-dependent tests
- After Go changes in every task: `gofmt -w` on touched files, then `go build -o test-output ./cmd/server && rm test-output`
- Do not merge upstream as part of this plan

## Testing Strategy
- **Unit tests**: required for every task. Run the touched packages per task, for example `go test ./internal/config/... ./sdk/cliproxy/auth/... ./internal/runtime/executor/... ./internal/api/handlers/management/...`
- **Full suite**: `go test ./...` in the verification task. Known flaky tests listed in `docs/backlog/` may be re-run once; do not modify them.
- **Catalog in executor tests**: tests must not depend on which tiers the embedded catalog gives to a real model. They use a fixture with three models (ultrafast + priority, priority only, no tiers).
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

## Implementation Steps

### Task 1: Add `speed` and `ignore-client-tier` to the provider routing config
- [ ] in `internal/config/provider_routing.go` add constants `CodexSpeedStandard`, `CodexSpeedFast`, `CodexSpeedUltrafast` and the fields `Speed string` (`yaml:"speed,omitempty" json:"speed"`) and `IgnoreClientTier bool` (`yaml:"ignore-client-tier,omitempty" json:"ignore-client-tier"`) on `ProviderRoutingConfig`
- [ ] in `normalize` trim and lower-case `Speed`, keep the stored value empty when unset, and validate the effective value: unknown values return `"<path>.speed has unsupported value %q"`
- [ ] in `normalize` reject a non-empty `Speed` or a true `IgnoreClientTier` for a provider other than `codex` with `"<path>.speed is only supported for codex"` / `"<path>.ignore-client-tier is only supported for codex"`
- [ ] make `applyDefaults` fill `Speed` with `standard` so `EffectiveProviders` and the management view return a concrete value
- [ ] confirm `Clone`, `SharedProfile`, `effectiveProviders` and `apiFallbackProfile` (`sdk/cliproxy/auth/provider_fallback.go:126`) carry both fields without extra code; add code only where a field is dropped
- [ ] write tests in `internal/config`: table for `normalize` (valid values, case and whitespace, unknown value, both Claude errors, defaults in the effective view, stored value stays empty)
- [ ] write tests for the YAML round trip: shared profile and key profile keep both fields on save; clearing a field in a key profile removes it from the file (key-profile pruning, `config_yaml.go:947`); a config without the fields saves byte-identical
- [ ] run `go test ./internal/config/...`, gofmt, compile check - must pass before task 2

### Task 2: Export the Codex speed settings of the request's routing profile
- [ ] in `sdk/cliproxy/auth/routing_profile.go` add an exported accessor (for example `CodexSpeedFromContext(ctx context.Context) (speed string, ignoreClientTier bool, ok bool)`) that reads `routingProfileFromContext(ctx).providers.Codex`; `ok` is false when the context carries no profile
- [ ] the accessor must not depend on `providerPolicy`: a scheduler-plugin profile (`providerPolicy = false`) still returns the shared profile's settings
- [ ] write tests: no profile in the context, a key profile, the shared profile, a scheduler-plugin profile, and a profile derived by `apiFallbackProfile`
- [ ] write a manager-level test: two client keys with different key profiles executed through `Manager.Execute` with a capturing fake executor see different values in the executor context, and a key without a profile sees the shared values
- [ ] write a manager-level test for hot reload: after the manager's runtime config is replaced, the next execution sees the new values
- [ ] run `go test ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 3

### Task 3: Resolve the Codex service tier from the profile in the executor
- [ ] in `internal/runtime/executor/codex_executor_request.go` replace `applyCodexFastModeDoc` with a function that takes the request context and implements the rules from `## Technical Details` (client tier kept or removed, tier from speed with the ultrafast -> priority -> none downgrade, API-key auths untouched)
- [ ] read the settings through the Task 2 accessor; when it reports no profile, use `cfg.Routing.SharedProfile().EffectiveProviders().Codex`; stop reading `cfg.Codex.FastMode`
- [ ] route the catalog lookup through a package-level function variable that defaults to `registry.CodexClientModelSupportsServiceTier`, so tests can install a fixture without mutating the global catalog
- [ ] update the four call sites (`codex_executor_execute.go:158`, `codex_executor_stream.go:81`, `codex_websockets_execute.go:79`, `codex_websockets_stream.go:924`) and their comments; the call stays before the payload-rule finalizer
- [ ] replace `codex_executor_fast_mode_test.go` with a table test over speed x fixture model x client tier x `ignore-client-tier` for the HTTP non-stream, HTTP stream, websocket execute and websocket stream paths, asserting the upstream body and `X-Codex-Routing-Hint`
- [ ] write tests for the unchanged rules: API-key auth is untouched in every combination, a payload filter on `service_tier` still wins, a payload override still wins
- [ ] write a test that the context profile takes precedence over the config's shared profile (an external test package or `test/` that drives `Manager.Execute` with the real Codex executor against an `httptest` upstream for two client keys)
- [ ] write a registry test that `CodexClientModelSupportsServiceTier` reports `ultrafast` for a catalog loaded with `loadCodexClientModelsFromBytes`, restoring the original catalog afterwards
- [ ] run `go test ./internal/runtime/executor/... ./internal/registry/... ./sdk/cliproxy/auth/...`, gofmt, compile check - must pass before task 4

### Task 4: Keep an ignored client tier out of the duplex websocket path
- [ ] trace every use of the `service_tier` copied into response-settings snapshots in `codex_websockets_duplex.go` (written at `:528` from the raw next request payload and at `:962` from the prepared body; read around `:408-450`, `:520-585`, `:746-753`, `:926`) and record in this plan which copies can reach an upstream payload
- [ ] where a snapshot can send a tier upstream that did not pass through the Task 3 function, take the tier from the prepared body or apply the same function, so a client tier removed by `ignore-client-tier` cannot return and a profile speed is not lost
- [ ] write a duplex regression test: with `ignore-client-tier` true and speed `standard`, a client turn carrying `service_tier: priority` and its follow-up or compaction request go upstream without a tier
- [ ] write a duplex test for the opposite setting: with `ignore-client-tier` false the client tier of each turn is sent as before
- [ ] run `go test ./internal/runtime/executor/...`, gofmt, compile check - must pass before task 5

### Task 5: Remove `codex.fast-mode`
- [ ] delete the `FastMode` field (`internal/config/config_types.go:362-364`) and `internal/api/handlers/management/config_codex_fast_mode_test.go`; `grep -rn -iE 'FastMode|fast-mode'` over Go sources must then match only Claude and Devin code
- [ ] write a load test: a v8 config containing `oauth.providers.codex.fast-mode: false` (and one with `true`) loads through `config.LoadConfig`, keeps its other `oauth.providers.codex` settings, and yields speed `standard` for the shared profile
- [ ] write a management test: on a config file that still contains the key, a PATCH of an unrelated field returns 200, the saved file has no active `fast-mode` key, and its other settings are preserved
- [ ] if either test shows the leftover key breaking load or writes, drop the key explicitly in `NormalizeConfigLayout` with one warning that names `routing.providers.codex.speed`, and keep both tests
- [ ] write a management test: `PUT /v8/management/config/oauth/providers/codex/fast-mode` now returns 400
- [ ] run `go test ./internal/config/... ./internal/api/handlers/management/...`, gofmt, compile check - must pass before task 6

### Task 6: Serve and accept the fields through the management API
- [ ] confirm `GET /v8/management/routing/profiles` returns `speed` (with the `standard` default) and `ignore-client-tier` under `providers.codex` for the shared profile and each key profile through `effectiveProvidersView` (`routing_api_credentials.go:60`); add code only if a field is missing
- [ ] confirm the write helpers in `routing_providers_write.go` and `mergeLegacyClaudeFallbackWrites` do not drop the new fields when a `providers` block is written
- [ ] write tests for GET: defaults, explicit values on the shared profile, explicit values on a key profile
- [ ] write tests for PATCH on the shared profile: setting `routing.providers.codex.speed` alone keeps `mode`, `api-credential`, `fallback` and the Claude block; clearing it returns the view to `standard`
- [ ] write tests for PATCH on an existing key profile: setting only `providers.codex.speed` or only `providers.codex.ignore-client-tier` keeps `session-affinity`, `preferred-accounts`, `selection` and the rest of `providers`
- [ ] write tests for rejected writes: an unknown speed value, `speed` under `providers.claude`, `ignore-client-tier` under `providers.claude`
- [ ] run `go test ./internal/api/handlers/management/... ./internal/config/...`, gofmt, compile check - must pass before task 7

### Task 7: Verify acceptance criteria
- [ ] verify criterion 1: speed `ultrafast` gives `ultrafast` on the fixture model that advertises it, `priority` on the priority-only model, and no tier on the model without tiers
- [ ] verify criteria 2 and 3: `ignore-client-tier` true with speed `standard` strips a client `service_tier: priority`; false passes it through unchanged
- [ ] verify criteria 4 and 5: a key profile without the fields behaves as `standard` with the client tier respected; two keys with different settings get different tiers in one process
- [ ] verify criterion 6: a PATCH changes the behavior of the next request without a restart, and the live config shape (with the leftover `fast-mode: false`) loads without edits
- [ ] run the full suite `go test ./...` (known flaky tests in `docs/backlog/` may be re-run once; do not modify them)
- [ ] run `gofmt -l .` (no output) and the compile check `go build -o test-output ./cmd/server && rm test-output`
- [ ] run `go vet` on the touched packages and fix findings in touched code

### Task 8: Update documentation
- [ ] `config.example.yaml`: remove the `fast-mode` block (`:1343-1345`) and document `speed` and `ignore-client-tier` in the `routing.providers.codex` and `routing.key-profiles` examples, including the downgrade rule and the subscription-only scope
- [ ] `docs/management-api-v8.md`: remove the `/config/oauth/providers/codex/fast-mode` row (`:43`), add both fields to the routing profile sections and the `GET /routing/profiles` description, and state that a PATCH for a key without a profile creates a profile holding only the written fields, so a client must send a full profile when it creates one
- [ ] `AGENTS.md`: add one sentence to the `sdk/cliproxy/auth` paragraph naming the accessor and the rule that the Codex executor reads the speed policy from the context profile

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
- Merging upstream router-for-me (v8.0.77)
- Panel work and ultrafast pricing in monitoring (panel backlog: `docs/backlog/ultrafast-service-tier-priced-as-standard.md` in CPA-Manager-Plus)

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**:
- After deploy, send one request for `gpt-6.2-sol` through a key whose profile has `speed: ultrafast` and check the usage record: requested, effective and response tier. Then watch whether the credential entered a cooldown. An account whose plan lacks Ultrafast may answer with an error; the shape of that error decides whether a follow-up plan is needed.
- Repeat with `gpt-6.2-astra` and confirm the request went out with `priority`.
- Send a request with a client `service_tier` through a profile with `ignore-client-tier: true` and confirm the usage record shows the profile's tier.

**External system updates**:
- The live config keeps `fast-mode: false` until the first management write comments it out; no manual edit is required.
- Until the panel plan ships, the Fast toggle in the deployed panel returns an error; set the speed with a config PATCH.
- Panel plan (CPA-Manager-Plus): speed control and the flag in the profile editor; a speed table opened from the toolbar with one row per shared profile and client key, applied on click, creating a key profile as a full copy of the shared profile for a key without one; removal of the Fast toolbar toggle.
- The embedded Codex catalog gets `gpt-6.2-*` only with the upstream merge; a running instance already receives it from the remote catalog refresh.
