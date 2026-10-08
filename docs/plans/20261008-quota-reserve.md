# Per-credential quota reserve for shared subscriptions

## Overview
- Some Codex and Claude subscriptions are also used by services that do not go through this proxy. The proxy should avoid exhausting their rate limits so those services keep working.
- Add an optional per-credential `quota_reserve` (`{"percent": 25, "mode": "soft"|"hard"}`) stored in the auth JSON next to `priority`. When the latest quota observation shows that any subscription window has less remaining than `percent`, the selector stops choosing that credential for **new** selections:
  - `hard`: the credential is excluded.
  - `soft`: the credential is used only when no credential above its reserve is available at any priority tier.
- Existing session-affinity bindings keep their credential, so a session lives on.
- Reserve data comes from quota observations the proxy already records (`QuotaState.Signals`). Subscription limits are account-wide, so upstream rate-limit headers already include usage by external services. The first proxy request refreshes a stale observation. There are no background probes.
- Management API: write the reserve through `PATCH /v8/management/credentials/fields`. Read config plus the selector's verdict (`quota_reserve_active`, `quota_reserve_until`) through `GET /v8/management/credentials`.
- Panel UI ships in the CPAMC plan `docs/plans/20261008-quota-reset-expiry-ledger.md` (separate repo).

## Decisions
- **Context**:
  - Selection order today: availability (`collectAvailableByPriority`, `sdk/cliproxy/auth/selector.go:469`) → manual priority tier → strategy (`round-robin`, `weighted-round-robin`, `fill-first`, `earliest-reset`) → preferred accounts (`sdk/cliproxy/auth/preferred_accounts.go`) and session affinity (`SessionAffinitySelector.Pick`, `selector.go:981`).
  - Quota observations are single snapshots of one upstream response (`QuotaState.Signals` + `ObservedAt`, `sdk/cliproxy/auth/types.go:175`). They are read by `subscriptionResetForAuth`/`quotaSignal` (`sdk/cliproxy/auth/quota_selector.go`).
- **Chosen approach** (approved in brainstorming):
  1. **Storage.** `quota_reserve` lives in the credential JSON, like `priority`. It is copied into attributes `quota_reserve_percent` / `quota_reserve_mode` on load and hot-reload, following `ApplyAuthPriorityMetadata` (`sdk/cliproxy/auth/priority.go`; callers `internal/watcher/synthesizer/file.go`, `sdk/auth/filestore.go`). Invalid values are ignored with a warn log (no reserve).
  2. **One percent for all windows**: Codex primary + secondary, Claude 5h + 7d. The reserve is active when `100 - used < percent` in ANY window. It is inactive at `remaining == percent`.
  3. A window whose reset time has passed is ignored. A missing or malformed snapshot means "available". Providers other than codex and claude do not support a reserve.
  4. **Only new selections.** A credential already bound by session affinity keeps serving that session.
  5. **hard** excludes the credential. When every candidate is excluded or cooling down, return the existing model-cooldown 429 with `Retry-After` until the earliest relevant reset. **soft** moves the credential to a "last resort" pool, used only if no non-reserved credential is available in any priority tier; the configured strategy picks inside the pool. A preferred account below its reserve behaves as unavailable (hard: excluded; soft: pool).
  6. The reserve is a selection filter only. It never writes cooldown state.
  7. **No background probes.** Overshoot is bounded by requests already in flight.
  8. **API**:
     - `PATCH /v8/management/credentials/fields` accepts `quota_reserve` (object or `null` to delete). It returns 400 for invalid values or unsupported providers.
     - `GET /v8/management/credentials` exposes `quota_reserve`, `quota_reserve_active` and `quota_reserve_until` (RFC3339 reset of the window that triggers it).
- **Rejected alternatives**:
  - Reserve in routing config keyed by `auth_index` (lost when a credential is re-issued).
  - A global threshold plus a flag.
  - Separate short/long percentages (more knobs than needed).
  - Weekly window only (the 5h window can still block external services).
  - Pacing/budget over time (complex, YAGNI).
  - A background usage probe or a pessimistic stale-observation rule. The first request refreshes headers, and overshoot of one in-flight request is acceptable.
  - Migrating bound sessions when the reserve trips (cache loss; the user prefers sessions living on).
  - Soft = end of own tier (weaker protection).
- **Verified facts**:
  - `priority` is parsed by `ApplyAuthPriorityMetadata`, synced on PATCH by `syncAuthFilePriorityAttribute` (`internal/api/handlers/management/auth_files_fields.go:688`), and exposed by `ListAuthFiles` (`internal/api/handlers/management/auth_files.go:~747-762`).
  - `PATCH /credentials/fields` → `PatchAuthFileFields` (`auth_files_fields.go:258`), with validation in `normalizeAuthFilePatchFields` (`:433`) and `syncAuthFileMetadataFields` (`:603`).
  - The route is `internal/api/server_management_v8.go:50`.
  - Codex signals come from `X-Codex-Primary-/Secondary-{Used-Percent,Window-Minutes,Reset-At,Reset-After-Seconds}`. Claude signals come from `Anthropic-Ratelimit-Unified-{5h,7d}-{Utilization,Reset}` (utilization is a 0-1 fraction). See `internal/api/handlers/management/api_tools_quota.go:78-169` and `quota_selector.go:127-181`.
  - The model-cooldown 429 with `Retry-After` is `newModelCooldownError` (`selector.go:91-125`).
  - AGENTS.md:
    - no `time.Sleep` in TTL/ordering tests (use `nowFunc`)
    - no `log.Fatal`
    - `/v0/management` is frozen
    - wrap errors
    - run `gofmt` and the compile check after changes
    - check CLIProxyAPIHome impact

## Context (from discovery)
- Files/components involved:
  - `sdk/cliproxy/auth/priority.go` (pattern), new `sdk/cliproxy/auth/quota_reserve.go`
  - `sdk/cliproxy/auth/selector.go` (`collectAvailableByPriority`, `getSelectorAvailableAuths*`, `SessionAffinitySelector.Pick`)
  - `sdk/cliproxy/auth/quota_selector.go` (signal helpers), `sdk/cliproxy/auth/preferred_accounts.go`
  - `internal/watcher/synthesizer/file.go`, `sdk/auth/filestore.go` (metadata → attributes)
  - `internal/api/handlers/management/auth_files_fields.go`, `internal/api/handlers/management/auth_files.go`
  - `docs/management-api-v8.md`, `config.example.yaml`
- Related patterns found:
  - attribute sync for `priority`/`weight`/`websockets`
  - table-driven selector tests with `nowFunc` (`quota_selector_test.go`, `preferred_accounts_test.go`)
  - management handler tests (`internal/api/handlers/management/*_test.go`)
- Dependencies identified: none external. CPAMC consumes the new GET fields and the PATCH field.

## Development Approach
- **Testing approach**: TDD. Write failing table-driven tests first in each task, using controllable clocks (`nowFunc`) and no `time.Sleep`.
- Complete each task fully before moving to the next.
- Make small, focused changes. Keep the canonical selector pipeline intact. The reserve is a filter at the availability stage, never a new strategy.
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task.
  - tests are not optional - they are a required part of the checklist
  - write unit tests for new functions/methods
  - write unit tests for modified functions/methods
  - add new test cases for new code paths
  - update existing test cases if behavior changes
  - tests cover both success and error scenarios
- **CRITICAL: all tests must pass before starting next task** - no exceptions
- **CRITICAL: update this plan file when scope changes during implementation**
- After Go changes, run `gofmt -w` on the touched files, `go build -o test-output ./cmd/server && rm test-output`, and the focused `go test` packages.
- Maintain backward compatibility: credentials without `quota_reserve` behave exactly as today.

## Testing Strategy
- **Unit tests**: required for every task. Use Go table-driven tests in the packages that are touched.
- **E2E tests**: none in this repo for selection. `test/` integration suites must keep passing (`go test ./...`).

## Progress Tracking
- Mark completed items with `[x]` immediately when done
- Add newly discovered tasks with ➕ prefix
- Document issues/blockers with ⚠️ prefix
- Update plan if implementation deviates from original scope
- Keep plan in sync with actual work done

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code, tests, docs in this repo
- **Post-Completion** (no checkboxes): live verification, CLIProxyAPIHome follow-up, panel deployment

## Decision Log
- 2026-10-08 brainstorm: **rejected** - "background usage probe / pessimistic stale rule" - the user noted that one proxy request refreshes the observation. Expired windows are ignored, so a hard-reserved credential recovers after its window reset.
- 2026-10-08 brainstorm: **accepted** - bound sessions live on; the reserve affects only new selections.
- 2026-10-08 brainstorm: **accepted** - backend and panel UI in parallel. The UI is appended to the not-yet-run CPAMC resets plan to avoid file conflicts.

## Implementation Steps

### Task 1: Parse and sync `quota_reserve` into credential attributes
- [ ] write failing table tests in `sdk/cliproxy/auth/quota_reserve_test.go` for `ApplyAuthQuotaReserveMetadata(auth, metadata)`:
  - a valid `{percent: 25, mode: "hard"}` sets the attributes `quota_reserve_percent=25` and `quota_reserve_mode=hard`
  - a missing mode defaults to `soft`
  - invalid inputs remove the attributes: not an object, percent 0/100/negative/fractional/string, unknown mode, unsupported provider
  - a missing key removes stale attributes
- [ ] implement `sdk/cliproxy/auth/quota_reserve.go` with `ApplyAuthQuotaReserveMetadata` and a parsed accessor `authQuotaReserve(auth) (percent int, hard bool, ok bool)`, mirroring `ApplyAuthPriorityMetadata`. Log invalid values with logrus at warn level without secrets.
- [ ] call it wherever `ApplyAuthPriorityMetadata` is called (`internal/watcher/synthesizer/file.go`, `sdk/auth/filestore.go`), so load and hot-reload pick it up
- [ ] write tests that a synthesized auth from JSON with `quota_reserve` carries the attributes, and that a hot-reload with a changed or removed reserve updates them (extend the existing synthesizer/filestore tests)
- [ ] run `gofmt`, the compile check, and `go test ./sdk/cliproxy/auth/... ./internal/watcher/... ./sdk/auth/...` - must pass before task 2

### Task 2: Compute the reserve verdict from quota observations
- [ ] write failing table tests for `quotaReserveVerdict(auth, now) (active bool, until time.Time)`:
  - Codex: primary and secondary each trip on their own
  - Claude: 5h and 7d (utilization is a 0-1 fraction) each trip on their own
  - boundary `remaining == percent` is inactive; `remaining < percent` is active
  - a window whose reset has passed is ignored
  - missing or malformed signals are inactive
  - no reserve configured is inactive
  - `until` = the reset time of the tripping window; with several tripping windows, use the latest reset (the reserve holds until all tripping windows recover)
- [ ] implement it in `sdk/cliproxy/auth/quota_reserve.go`, reusing the signal helpers in `quota_selector.go` (`subscriptionWindowPrefixes`, `quotaSignal`, the reset parsing) on a single snapshot, never merged across responses
- [ ] run `go test ./sdk/cliproxy/auth/...` - must pass before task 3

### Task 3: Apply the reserve in selection (new selections only)
- [ ] write failing selector tests (`sdk/cliproxy/auth/quota_reserve_selector_test.go`) with `nowFunc`:
  - hard reserve excluded
  - all candidates hard-reserved → model-cooldown error with `Retry-After` until the earliest `until`
  - soft-reserved credential skipped while any non-reserved credential exists in ANY priority tier; picked from the last-resort pool when none exists; with several pool members, the configured strategy chooses
  - behavior identical under `round-robin`, `fill-first`, `earliest-reset`, `weighted-round-robin`
  - a preferred account below its reserve falls back (hard excluded / soft pool)
  - credentials without a reserve unchanged
  - no cooldown fields mutated
- [ ] write a failing session-affinity test: a session already bound to a credential that then trips its reserve keeps that credential on the next request, while a NEW session is not bound to it
- [ ] implement the filter at the availability stage used for new selections (`collectAvailableByPriority` / `getSelectorAvailableAuthsWithPriorityMode` in `selector.go`): split candidates into non-reserved and soft pools, and drop hard ones. Keep the bound-session validation path in `SessionAffinitySelector.Pick` (`selector.go:981+`) and the preferred-account path consistent with the decisions. Verify which availability helper each path uses and make sure the reserve is NOT applied when re-validating an existing binding.
- [ ] make sure `earliest-reset` (`quota_selector.go`) and `preferredOrHighestPriorityAuths` operate on the filtered set, and that the 429 path reuses `newModelCooldownError`
- [ ] run `gofmt`, the compile check, and `go test ./sdk/cliproxy/... ./internal/...` - must pass before task 4

### Task 4: Management API — PATCH and GET support
- [ ] write failing handler tests in `internal/api/handlers/management` for `PATCH /v8/management/credentials/fields`:
  - `{"quota_reserve": {"percent": 25, "mode": "soft"}}` persists to the auth JSON and syncs the attributes
  - `null` deletes the field and the attributes
  - invalid values return 400 with a clear error
  - a non-codex/claude credential returns 400 "quota reserve is supported for codex and claude"
- [ ] write failing tests for `GET /v8/management/credentials`: entries expose `quota_reserve` when set, `quota_reserve_active` (bool) and `quota_reserve_until` (RFC3339, only when active), computed with an injectable clock
- [ ] implement validation in `normalizeAuthFilePatchFields` (`auth_files_fields.go:433`), a new `syncAuthFileQuotaReserveAttribute` invoked from `syncAuthFileMetadataFields` (`:603`), and the list fields in `ListAuthFiles` (`auth_files.go:~747`). Do not touch `/v0/management`.
- [ ] run `gofmt`, the compile check, and `go test ./internal/api/...` - must pass before task 5

### Task 5: Verify acceptance criteria
- [ ] verify every Overview requirement: storage, verdict, hard/soft, new-selection-only, preferred fallback, no cooldown writes, API read/write
- [ ] verify the edge cases: expired windows ignored, missing observations available, unsupported providers ignored, invalid JSON values ignored with warn, all-hard 429 `Retry-After`
- [ ] run `gofmt -l .` (must print nothing for changed files), `go build -o test-output ./cmd/server && rm test-output`, and `go test ./...` - all must pass
- [ ] confirm that the new code in `quota_reserve.go` and the selector filter has tests that cover every branch

### Task 6: [Final] Update documentation
- [ ] add a "Quota reserve for shared subscriptions" section to `docs/management-api-v8.md` next to "Subscription priority and session affinity". Cover:
  - format and modes
  - one percent for all windows
  - new-selection-only and affinity behavior
  - preferred fallback
  - overshoot of in-flight requests
  - no background probes
  - the PATCH/GET fields
  - the Home-mode note
- [ ] document the credential JSON field in `config.example.yaml`, in the comment block where `priority` is documented for credentials

## Technical Details
- **Credential JSON**: `"quota_reserve": {"percent": 25, "mode": "soft"}`. `percent` is an int 1..99; `mode` is `soft` (default) or `hard`.
- **Attributes**: `quota_reserve_percent` = `"25"`, `quota_reserve_mode` = `"soft"|"hard"`.
- **Verdict**:
  - per window `remaining = 100 - usedPercent`; Codex `Used-Percent` is 0..100, Claude `Utilization` is 0..1 and gets ×100
  - active when `remaining < percent` and the window's reset is after `now`
  - `until` = the latest reset among the tripping windows
- **Selection** (new selections):

  ```
  candidates (available, not cooling down)
    → drop hard-reserved
    → split: normal pool (by priority tier, as today) | soft pool
    → if normal pool non-empty: existing tier+strategy+preferred logic on normal pool
    → else if soft pool non-empty: strategy on soft pool
    → else: model-cooldown 429 (Retry-After = earliest of cooldown/reserve recovery)
  ```

  Bound sessions skip the reserve check.
- **API**:
  - `PATCH /v8/management/credentials/fields` `{"auth_index":"…","fields":{"quota_reserve":{…}|null}}`
  - `GET /v8/management/credentials` entries add `quota_reserve`, `quota_reserve_active` and `quota_reserve_until`

## Post-Completion
*Items requiring manual intervention or external systems - no checkboxes, informational only*

**Manual verification**
- On the running proxy, set `{"percent": 30, "mode": "hard"}` on a Claude credential that has about 64% 7d remaining. Selection is unchanged.
- Then raise the percent to 70. New sessions go to other accounts, and an already bound session continues. `GET /v8/management/credentials` shows `quota_reserve_active: true` and `quota_reserve_until`.
- Use only 1-2 short requests, and never spend reset credits.

**External system updates**
- CLIProxyAPIHome uses its own selector, and local management is disabled in Home mode. It needs matching reserve support before this works in a cluster. Note it in that repo's backlog.
- Panel UI: CPAMC plan `docs/plans/20261008-quota-reset-expiry-ledger.md` (Ledger reserve badge and meter tick, Auth Files reserve editor). Deploy the backend binary before checking the UI live.
