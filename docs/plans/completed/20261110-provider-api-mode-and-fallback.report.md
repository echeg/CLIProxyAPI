# Report: 2026 11 10 provider api mode and fallback
plan: /Users/echeg/Projects/CPA/CLIProxyAPI-migration/.loopai/worktrees/provider-api-mode-and-fallback/docs/plans/20261110-provider-api-mode-and-fallback.md | branch: provider-api-mode-and-fallback | base: main | mode: full | executor: claude | task model: claude:opus:high | review model: claude:opus:xhigh | started: 2026-11-10T15:38:07Z | finished: 2026-11-10T18:13:01Z

## Summary
- task iterations: 7
- failed retries: 0
- external reviewers: 1
- post-review iterations: 1
| phase | duration_ms |
|---|---:|
| evaluation | 345504 |
| external review | 365575 |
| internal review | 4138707 |
| other | 0 |
| tasks | 4444378 |

## Change scope
- commits: 13
- files: 71
- additions: 3818
- deletions: 425
| status | path |
|---|---|
| M | AGENTS.md |
| M | README.md |
| M | config.example.yaml |
| A | docs/backlog/rebind-never-sticks-to-ineligible-credential.md |
| A | docs/backlog/websocket-pin-keeps-api-fallback-key.md |
| M | docs/management-api-v8.md |
| M | docs/plans/20261110-provider-api-mode-and-fallback.md |
| M | internal/api/handlers/management/claude_fallback.go |
| M | internal/api/handlers/management/claude_fallback_test.go |
| M | internal/api/handlers/management/config_v8.go |
| A | internal/api/handlers/management/routing_api_credentials.go |
| A | internal/api/handlers/management/routing_api_credentials_test.go |
| M | internal/api/handlers/management/routing_profiles.go |
| A | internal/api/handlers/management/routing_providers_write.go |
| A | internal/api/handlers/management/routing_providers_write_test.go |
| M | internal/api/server_test.go |
| M | internal/config/claude_fallback.go |
| M | internal/config/claude_fallback_test.go |
| M | internal/config/config_load.go |
| M | internal/config/config_types.go |
| M | internal/config/config_yaml.go |
| M | internal/config/parse.go |
| A | internal/config/provider_routing.go |
| A | internal/config/provider_routing_save_test.go |
| A | internal/config/provider_routing_test.go |
| M | internal/config/routing_profile.go |
| M | internal/config/routing_profile_test.go |
| M | internal/runtime/executor/apply_patch_bridge_test.go |
| M | internal/runtime/executor/apply_patch_integration_test.go |
| M | internal/runtime/executor/claude_executor_fable_ratelimit_test.go |
| M | internal/runtime/executor/claude_executor_ratelimit_test.go |
| M | internal/runtime/executor/claude_issue7000_regression_test.go |
| M | internal/runtime/executor/claude_thinking_replay_test.go |
| M | internal/runtime/executor/claude_tls_handshake_retry_test.go |
| M | internal/runtime/executor/codex_stream_bootstrap_buffering_test.go |
| M | internal/runtime/executor/codex_websockets_duplex_bootstrap_input_test.go |
| M | internal/runtime/executor/codex_websockets_duplex_credential_failure_test.go |
| M | internal/runtime/executor/codex_websockets_duplex_health_test.go |
| M | internal/runtime/executor/codex_websockets_duplex_initial_failure_test.go |
| M | internal/runtime/executor/helps/model_capabilities_test.go |
| M | internal/runtime/executor/oauth_scope_test.go |
| A | internal/runtime/executor/provider_routing_helpers_test.go |
| M | internal/watcher/diff/config_diff.go |
| M | internal/watcher/diff/routing_profile_diff_test.go |
| M | sdk/api/handlers/openai/openai_responses_interrupt_test.go |
| M | sdk/api/handlers/openai/openai_responses_late_interrupt_test.go |
| M | sdk/api/handlers/openai/openai_responses_steering_auth_test.go |
| M | sdk/api/handlers/openai/openai_responses_steering_error_test.go |
| M | sdk/api/handlers/openai/openai_responses_steering_integration_test.go |
| M | sdk/api/handlers/openai/openai_responses_steering_test.go |
| M | sdk/api/handlers/openai/openai_responses_steering_validation_test.go |
| M | sdk/api/handlers/openai/openai_responses_websocket_disconnect_interrupt_test.go |
| M | sdk/api/handlers/openai/openai_responses_websocket_loss_test.go |
| M | sdk/api/handlers/openai/openai_responses_websocket_unknown_interrupt_test.go |
| M | sdk/cliproxy/auth/api_key_model_capabilities_test.go |
| D | sdk/cliproxy/auth/claude_fallback.go |
| M | sdk/cliproxy/auth/claude_fallback_test.go |
| M | sdk/cliproxy/auth/conductor_execution.go |
| M | sdk/cliproxy/auth/conductor_force_mapping_test.go |
| M | sdk/cliproxy/auth/conductor_issue6416_test.go |
| M | sdk/cliproxy/auth/conductor_selection.go |
| A | sdk/cliproxy/auth/provider_fallback.go |
| A | sdk/cliproxy/auth/provider_fallback_test.go |
| A | sdk/cliproxy/auth/provider_policy.go |
| A | sdk/cliproxy/auth/provider_policy_helpers_test.go |
| A | sdk/cliproxy/auth/provider_policy_test.go |
| M | sdk/cliproxy/auth/routing_profile.go |
| M | sdk/cliproxy/auth/routing_profile_test.go |
| M | test/codex_incomplete_stream_error_type_test.go |
| M | test/codex_quota_failover_test.go |
| M | test/codex_stream_disconnect_failover_test.go |

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
- ➕ verification notes: Overview goals map to `providerPolicyAllows` (eligibility, default `subscription` excludes Claude/Codex API keys), `executeWithProviderFallback` with `target: api` (shared and key profiles, `TestAPIFallback*`), `mode: api` (`TestProviderPolicyExecutionSelection`), and Codex parity (every policy/fallback test runs both providers). Added regression tests for the edge cases that had no explicit coverage: `pinned codex api key is exempt` and `mixed claude and codex under codex api mode` rows in `TestProviderPolicyExecutionSelection`, and `TestProviderPolicyHotReloadMovesBoundSessions` (a session bound under one mode moves to an eligible credential after a reload to the other mode, both providers). `go test ./...` passed on the first run (no flaky re-run needed); `gofmt -l .`, the compile check, and `go vet` on the listed packages are clean.
### Blocked
- none
### Skipped
- none

## Backlog
| status | path | title |
|---|---|---|
| A | docs/backlog/rebind-never-sticks-to-ineligible-credential.md | rebind-on never keeps sessions bound to a credential that became ineligible |
| A | docs/backlog/websocket-pin-keeps-api-fallback-key.md | Responses websocket stays on the API key after an API fallback turn |

## External review
### codex:gpt-6-astra:xhigh
- label: codex
- iterations: 2
- duration_ms: 711035
- ended by: done
- had findings: true
#### Iteration 1
- truncated: false
Reviewer output:
```
- **[P2] Null scalar PATCHes retain old fallback values** — [routing_providers_write.go:66](/Users/echeg/Projects/CPA/CLIProxyAPI-migration/.loopai/worktrees/provider-api-mode-and-fallback/internal/api/handlers/management/routing_providers_write.go:66). With Codex fallback enabled, `PATCH /v8/management/config/routing/claude-fallback` with `{"enabled":null}` returns success but leaves it enabled. The struct is seeded from stored settings, and yaml.v3 does not clear booleans or strings when decoding null. The existing fix covers only whole-block null; explicitly reset null scalar fields before decoding.

- **[P2] Partial writes reactivate ignored legacy settings** — [routing_providers_write.go:59](/Users/echeg/Projects/CPA/CLIProxyAPI-migration/.loopai/worktrees/provider-api-mode-and-fallback/internal/api/handlers/management/routing_providers_write.go:59). If a file contains `providers.claude.fallback.target: off` alongside an ignored legacy block with `enabled: true` and valid mappings, PATCHing only legacy `mode` re-enables Codex fallback. Decoding the merged legacy node reapplies stale fields that were absent from the request. Apply only the submitted fields over the effective fallback.

- **[P2] Partial writes misinterpret accepted target casing** — [routing_providers_write.go:67](/Users/echeg/Projects/CPA/CLIProxyAPI-migration/.loopai/worktrees/provider-api-mode-and-fallback/internal/api/handlers/management/routing_providers_write.go:67). A stored `target: CODEX` is valid and enables fallback at runtime, but `previous` is decoded without normalization. `LegacyClaudeFallback()` compares the target case-sensitively, so a legacy PATCH containing only `{"mode":"auto"}` silently changes the target to `off`. Normalize the stored policy before deriving the legacy view.

```
Evaluator response:
```
Using superpowers:receiving-code-review to evaluate the Codex findings rigorously.Reading the config handler and helpers to verify each finding before deciding; all three look plausible so far, checking yaml.v3 null semantics and the alias projection next.Writing the failing tests first.Now the handler conversion block.Targeted tests pass, including the existing null-block and partial-PATCH cases. Checking whether docs describe the legacy-write rules in a way that needs updating.Docs updated. Running gofmt, build, vet and the full test suite.Full suite passes (no failing packages). Checking for a linter config.I checked all three findings against the code and they're all real. I fixed them, the full test suite passes, and nothing is committed.

1. A null field in a PATCH kept the old value — valid, fixed.
Before conversion, a legacy PATCH wrote a field as null, and loading that file read null as "off" or empty. The branch fills the struct from the stored fallback and then decodes the merged node over it. yaml.v3 leaves a bool or string unchanged when it decodes null into it (it only resets pointers, maps and slices). So {"enabled":null} kept Codex fallback turned on. Only a whole null block was handled before.
Fix (routing_providers_write.go:57-83): the stored fallback is turned into a YAML node, the submitted node is merged onto it with the same mergeConfigV8Patch the handler already uses, and the result is decoded into an empty struct. A null field now resets just that field, and a null block still turns the fallback off.

2. Partial writes brought back an ignored legacy block — valid, fixed.
The conversion decoded the merged claude-fallback node. That node still holds any stored legacy block, even one that load ignores because providers.claude.fallback is set. So a PATCH of {"mode":"auto"} picked up a stale enabled: true and mappings and switched the target to codex.
Fix: only the node that was actually written (writtenLegacy) is read. A PATCH applies it over the effective stored fallback; a PUT of the whole block replaces it.
The same problem hit a PUT of one field, such as PUT routing/claude-fallback/mode, and the fix above would have made that PUT drop the other fields. So such a PUT is now handled like a PATCH. A new helper, writesInsideClaudeFallback, detects these paths, and its result is passed as the merge flag at config_v8.go:161.

3. A stored target in a different case was misread — valid, fixed.
Normalize lower-cases the target, so "target: CODEX" works at runtime. But the stored profile was decoded without normalizing, and LegacyClaudeFallback compared the target exactly, so a later {"mode":"auto"} PATCH set the target to off. I fixed it in LegacyClaudeFallback itself (provider_routing.go:70): it now trims spaces and ignores case, the same way Normalize reads the target. That covers every caller. The GET handlers already pass normalized config, so they behave the same.

Tests:
- New subtests in TestConfigV8LegacyClaudeFallbackWriteEdgeCases: a null enabled field, a null mode field, a stale legacy block under both PATCH and a single-field PUT, a single-field PUT keeping the other stored fields, and an upper-case stored target.
- A new assertion in TestProviderFallbackLegacyConversion for " CODEX ".
- All of these failed before the fix and pass now.

Docs: the legacy-write rules in docs/management-api-v8.md now cover these cases.

Checks:
- gofmt is clean, the build passes, go vet passes on the changed packages, and go test ./... passes.
- golangci-lint with its default settings reports 19 warnings in other files, none in the changed ones. The repo has no golangci config, and the PR workflow I checked (pr-test-build.yml) only builds, so I left those alone.

Insight: yaml.v3 skips null when decoding into a non-pointer scalar, so you can't tell "field omitted" from "field set to null" after decoding into a pre-filled struct. Merging at the node level keeps that difference until the final decode into an empty struct.
```
#### Iteration 2
- truncated: false
Reviewer output:
```
NO ISSUES FOUND.

The three prior findings are correctly fixed: null-field resets, stale legacy settings, and target casing. No additional issues found in the diff. Tests were inspected but not rerun under the read-only restriction.

```
Evaluator response:
```
Codex reported no issues, so I'm going straight to the wrap-up. First I'll review all uncommitted changes and look for untracked files.Formatting is clean, the build compiles, vet is clean, and all tests pass. Now I'll stage only the six files this loop changed and commit.Codex found no issues this round. It confirmed that the three earlier fixes are correct: null-field resets, the stale legacy block being ignored, and the stored target being read in any case.

What I did:
- Reviewed the full uncommitted diff with git diff HEAD and git status --porcelain. Six modified files, nothing untracked, and every change belongs to this loop's earlier fixes.
- Checked before committing: gofmt reports nothing, the server builds, go vet is clean, and tests pass for internal/config, internal/api/handlers/management and sdk/cliproxy/auth.
- Staged only those six files by name and committed them as 448faee2 "fix: address codex review findings". The working tree is now clean.

Insight:
- The legacy write is now merged in a single format. The stored fallback's legacy view is turned into YAML, the written fields are laid over it with the same merge routine a PATCH uses, and the result is decoded once. That is why a null field now resets only that field.
- A PUT to one legacy field (for example routing/claude-fallback/mode) now counts as a merge, so it replaces just that field. It no longer resets the mode and mappings that weren't sent.

<<<RALPHEX:EXTERNAL_REVIEW_DONE>>>
```

## Validation
- none
- duration_ms: 0
- runs: 0