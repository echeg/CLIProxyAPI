# Report: 2026 11 18 codex speed in profiles
plan: /Users/echeg/Projects/CPA/CLIProxyAPI-migration/.loopai/worktrees/codex-speed-in-profiles/docs/plans/20261118-codex-speed-in-profiles.md | branch: codex-speed-in-profiles | base: main | mode: full | executor: claude | task model: claude:opus:high | review model: claude:opus:xhigh | started: 2026-11-18T14:03:22Z | finished: 2026-11-18T14:29:36Z

## Summary
- task iterations: 1
- failed retries: 0
- external reviewers: 1
- post-review iterations: 0
| phase | duration_ms |
|---|---:|
| evaluation | 19048 |
| external review | 125951 |
| internal review | 1414204 |
| other | 0 |
| tasks | 14997 |

## Change scope
- commits: 10
- files: 30
- additions: 1829
- deletions: 228
| status | path |
|---|---|
| M | AGENTS.md |
| M | README.md |
| M | config.example.yaml |
| M | docs/management-api-v8.md |
| M | docs/plans/20261118-codex-speed-in-profiles.md |
| M | examples/plugin/README.md |
| M | examples/plugin/README_CN.md |
| M | examples/plugin/codex-service-tier/README.md |
| M | internal/api/handlers/management/config_codex_fast_mode_test.go |
| M | internal/api/handlers/management/routing_api_credentials_test.go |
| A | internal/api/handlers/management/routing_codex_speed_test.go |
| M | internal/api/handlers/management/routing_providers_write_test.go |
| M | internal/config/config_types.go |
| M | internal/config/config_yaml.go |
| M | internal/config/provider_routing.go |
| M | internal/config/provider_routing_save_test.go |
| M | internal/config/provider_routing_test.go |
| M | internal/runtime/executor/codex_executor_execute.go |
| D | internal/runtime/executor/codex_executor_fast_mode_test.go |
| M | internal/runtime/executor/codex_executor_request.go |
| A | internal/runtime/executor/codex_executor_service_tier_test.go |
| M | internal/runtime/executor/codex_executor_stream.go |
| M | internal/runtime/executor/codex_websockets_duplex.go |
| M | internal/runtime/executor/codex_websockets_duplex_test.go |
| M | internal/runtime/executor/codex_websockets_execute.go |
| M | internal/runtime/executor/codex_websockets_routing_hint_test.go |
| M | internal/runtime/executor/codex_websockets_stream.go |
| M | sdk/cliproxy/auth/claude_fallback_test.go |
| M | sdk/cliproxy/auth/routing_profile.go |
| M | sdk/cliproxy/auth/routing_profile_test.go |

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
- ➕ review: the same test runs on all five paths (HTTP plain and streaming, compact, websocket plain and streaming), as AGENTS.md requires for request-building paths
- ➕ `docs/management-api-v8.md`: state that a PATCH with `null` for `speed` or `ignore-client-tier` stores `null` (read as the default) and that DELETE of the field path removes it from the file
- ➕ `README.md`: an upgrade note that `oauth.providers.codex.fast-mode` was removed and the profile speed replaces it
- ➕ `examples/plugin/README.md`, `README_CN.md` and `codex-service-tier/README.md`: a tier the example plugin sets counts as the client tier, so `ignore-client-tier: true` removes it
### Blocked
- ⚠️ the cleared keys survived; `shouldPruneNestedMappingKeys` prunes a whole mapping, so the fix is a dedicated `dropClearedSharedCodexSpeedFields` in `internal/config/config_yaml.go` that drops only these two keys before the merge. It keeps a key that already holds the default (`null`, `false`, `""`) and removes a `codex` mapping, and then a `providers` mapping, that the removal leaves empty. `TestGetRoutingProfilesProvidersView` (management) now expects `speed: standard` in the effective Codex view.
- ⚠️ the v8 PATCH keeps an explicit `null` by design (`mergeConfigV8Patch`, `config_v8.go:385`: "DELETE is the explicit field-removal operation"), so a `null` PATCH stores `speed: null`, which loads as empty and returns the view to `standard`; the test locks that and checks that DELETE of the field path removes it from the file. No management change was made.
### Skipped
- none

## Backlog
- none

## External review
### codex:gpt-6-astra:xhigh
- label: codex
- iterations: 1
- duration_ms: 144909
- ended by: done
- had findings: false
#### Iteration 1
- truncated: false
Reviewer output:
```
NO ISSUES FOUND

```
Evaluator response:
```
Codex found no issues, so I didn't need to evaluate or fix anything.

- I ran git status --porcelain and git diff HEAD, and both came back empty. No tracked files are modified, no new files are untracked, and nothing is staged, including any backlog entry.
- So I made no commit. Earlier review iterations had already committed their fixes (794468df and f8bdb16a).
- The server still compiles (go build ./cmd/server). I didn't run the test suite this time.

<<<RALPHEX:EXTERNAL_REVIEW_DONE>>>
```

## Validation
- none
- duration_ms: 0
- runs: 0