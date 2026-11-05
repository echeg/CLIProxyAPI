# Report: Logs directory storage endpoint

Plan: `docs/plans/20261105-logs-storage-endpoint.md` · Branch: `logs-storage-endpoint` · Base: `main` · Mode: full · Executor: claude · Task model: claude:opus:high · Review model: claude:opus:xhigh · Started: 2026-11-05T13:57:09Z · Finished: 2026-11-05T14:50:55Z

## Summary
The branch adds a read-only endpoint, `GET /v8/management/observability/logs/storage`. It reports how much disk the CPA logs directory uses:
- `total_bytes` / `total_files`: every regular file in the tree.
- `capped_bytes` / `capped_files`: the top-level `*.log` / `*.log.gz` files that the `logs-max-total-size-mb` cleaner counts.
- The oldest and newest mtime, in UTC RFC 3339, or `null` when no file was counted.
- The effective `limit_mb`, `request_log` and `logging_to_file`.

The branch also moves the default request logger into the shared logs directory. `defaultRequestLoggerFactory` now passes the absolute path from `ResolveLogDirectory(cfg)`, so request logs, `main.log`, the cleaner and the management handlers all use one directory. Before, request logs went under the config file's directory. A new `logging.ScanLogDir` helper does the counting with the same rules as the cleaner. The endpoint is documented in `docs/management-api-v8.md`.

Run shape:
- 6 task iterations with 0 failed retries.
- 1 internal review loop iteration, ended by `review_done`.
- 1 post-review iteration.
- 2 external review iterations (codex). The second one found no issues.

Phase durations (wall clock 53m46s):

| phase | duration_ms | ≈ |
|---|---:|---|
| tasks | 517184 | 8m37s |
| internal review | 1604115 | 26m44s |
| external review | 718374 | 11m58s |
| evaluation | 386131 | 6m26s |
| other | 0 | 0s |

Finalize did not run, so there was no base sync. Everything in this report comes from the branch's own commits.

## Change scope
15 files changed, 893 additions and 26 deletions, in 9 commits.

**Production code**
- `internal/api/server_options.go`: `defaultRequestLoggerFactory` turns `ResolveLogDirectory(cfg)` into an absolute path, falling back to the raw value if `Abs` fails. This keeps `NewFileRequestLogger` from rebasing the path onto the config directory.
- `internal/logging/log_dir_stats.go` (new): `LogDirStats` and `ScanLogDir`.
  - Stats the root with `os.Stat`, so a symlinked root is followed. A missing or blank root gives zero stats.
  - Reads the top level with `os.ReadDir` and walks only subdirectories with `WalkDir`.
  - Counts only regular files and skips unreadable entries below the root.
- `internal/api/handlers/management/logs.go`: `GetLogStorage` and `logStorageResponse`.
  - Copies the config fields it needs while holding `h.mu`, then scans with the lock released.
  - Sets `request_log` to `RequestLog && !CommercialMode`.
  - Guard responses: 500 for a nil handler, 503 for a nil config, 500 for a blank directory, 500 on a scan error.
- `internal/api/server_management_v8.go`: one v8-only route.
- `internal/api/server.go` and `config.example.yaml`: comments only, describing how the logs directory is resolved.

**Tests**
- `internal/logging/log_dir_stats_test.go` (new): 12 `ScanLogDir` tests.
- `internal/api/handlers/management/logs_test.go`: 7 `GetLogStorage` tests, including a race test.
- `internal/api/server_test.go`: a factory test where the working directory differs from the config directory, and an end-to-end `NewServer` test.
- `internal/api/server_management_v8_test.go`: the route is added to the access-control loop, with a temp log directory, and to the route list.

**Docs**
- `docs/management-api-v8.md`: a table row plus a "Logs directory storage" subsection.
- The plan's checkboxes.
- 3 new backlog entries.

**Commits**
- `1fda2b6c` feat: put request logs in the shared logs directory
- `4adfc9ae` feat: add logs directory scan helper
- `c41a6fe1` feat: add GetLogStorage management handler
- `240279ef` feat: register v8 logs storage route and document it
- `70ff0ab2` feat: verify logs storage endpoint acceptance criteria
- `5f72327a` feat: confirm logs storage endpoint documentation
- `56d7699c` fix: address code review findings
- `b3d2339d` docs: add backlog entry
- `84224e31` fix: address codex review findings

Local `main` (`4fa559fe`) is 22 commits ahead of `origin/main` (`af9850d9`). The diff above is against local `main`. A PR opened against `origin/main` would also show those 22 commits until they are pushed.

## Evidence
The harness recorded no validation runs (0 commands, 0 runs). The facts contain one before/after result, and it comes from the codex evaluator's transcript, not from a harness-recorded run:
- `TestGetLogStorageConcurrentSetConfigIsRaceFree` runs `SetConfig` in a loop while requests hit `GetLogStorage`.
  - Against the old handler, which read `h.cfg` without the lock, `-race` reported "4 DATA RACE reports and a FAIL".
  - With the fix in `84224e31`, the evaluator reported that it passes, along with `go test ./...` exiting 0.

The diff adds regression tests for the directory move. No run of them failing before the fix is recorded:
- `TestDefaultRequestLoggerFactory_UsesWorkingDirectoryLogsNotConfigDir` asserts that a forced error log lands in `<cwd>/logs` and not in `<configDir>/logs`. The old rebasing behavior wrote to `<configDir>/logs`.
- `TestNewServerLogStorageCountsRequestLogs` asserts that the endpoint of a real server counts the request log that server just wrote (`capped_files == 1`).

## Risk
**low**

- **Public APIs:** the HTTP change only adds something. It is a new v8 route with no v0 alias, and no existing endpoint changes. The SDK `NewFileRequestLogger(..., configDir)` signature and behavior are unchanged.
  - The visible behavior change: for deployments that use the default factory and run with a working directory other than the config directory, request and error logs now go to `<cwd>/logs` (or `$WRITABLE_PATH/logs` / `<auth-dir>/logs`) instead of `<configDir>/logs`.
  - In those deployments, the request logs now fall under the `logs-max-total-size-mb` cap, as intended.
  - The live launchd service sets `WorkingDirectory` to the config directory, so its logs do not move.
- **Data schemas:** none. The JSON response is new, and nothing is persisted.
- **Configuration:** no new or renamed keys. `config.example.yaml` only gains a comment.
- **Concurrency:**
  - The handler copies the config fields under `h.mu` and scans without holding the lock, so a hot reload never waits on a directory walk. A `-race` test covers this.
  - The scan is not atomic while logging continues, which the docs state.
  - The sibling log handlers still read `h.cfg` without the lock. That problem predates this branch and is in the backlog.
- **Migrations:** none.
- **Other:**
  - Each request does a full, uncached walk of the directory. The plan estimates tens of milliseconds even under heavy request logging.
  - `request_log` can disagree with what is written to disk after a hot reload of `commercial-mode`. That problem predates this branch and is in the backlog.

## Merge danger
**Door:** two-way
A plain revert removes the new route and restores the old factory path. There is no data or schema migration, and the panel that will consume the endpoint is still a separate, unstarted plan.

**Blast radius:** observability
Request logs move only where the working directory differs from the config directory. The rest is a new read-only management endpoint.

## Migrations and operational steps
none

Optional, not required: in deployments where the working directory differs from the config directory, request logs written before this change stay in `<configDir>/logs`. The cleaner never covered that directory, so operators may want to archive or delete them. Deploying to the local launchd service, as the plan's Post-Completion section describes, is a separate manual step.

## Plan deviation
The supplied plan drift lists no added, blocked or skipped items. All checkboxes in Tasks 1–6 are marked done, and Task 6 notes that AGENTS.md is unchanged. The diff shows these refinements beyond the plan text, all from review fixes:
- `request_log` is `cfg.RequestLog && !cfg.CommercialMode`, where the plan said plain `cfg.RequestLog`. This came from internal review (`56d7699c`), and the docs describe it.
- `GetLogStorage` copies the config under `h.mu` before scanning. This came from external review (`84224e31`).
- Tests beyond the plan:
  - `GetLogStorage`: nil handler, commercial mode, and the concurrent race test.
  - `ScanLogDir`: a path under a regular file (skipped on Windows), an unreadable root, and capped bytes matching the cleaner's total.
  - The end-to-end `TestNewServerLogStorageCountsRequestLogs`.
- The access-control test now points the handler at a temp log directory, so the scan never touches the host's real logs.
- Comments about directory resolution in `config.example.yaml` and `internal/api/server.go`.
- The docs have a full subsection instead of only a table row. It covers the fields, logical bytes, the non-atomic scan, the missing-directory behavior and the error behavior that the plan asked for.

The plan's Decision Log still contains an early entry mentioning `directory_present:false`. A later grill entry overrides it, and the code correctly leaves the field out.

## Backlog
- `docs/backlog/commercial-mode-hot-reload-request-logging.md`: "Hot-reloading commercial-mode does not add or remove request logging middleware". Found in internal review, minor, and it predates this branch. Filed and left open; not fixed on this branch.
- `docs/backlog/log-handlers-read-config-without-lock.md`: "Log management handlers read h.cfg without h.mu". Found in evaluation, minor. Filed and left open: `GetLogStorage` was fixed here, but the older handlers predate the branch and are out of scope.
- `docs/backlog/plugin-oauth-tests-fail-with-repeated-runs.md`: "Plugin OAuth management tests fail under go test -count>1". Found in internal review, minor, in files this branch does not touch. Filed and left open.

## External review
### codex:gpt-6-astra:xhigh
- label: codex
- iterations: 2
- duration_ms: 1104466
- ended by: done
- had findings: true

Iteration 1:
- [P2] Config reads race with reloads: `GetLogStorage` read `h.cfg` without `h.mu` -> fixed. The config fields are now copied under `h.mu` and the scan runs unlocked, with the regression test `TestGetLogStorageConcurrentSetConfigIsRaceFree`, in `84224e31`.
- [P2] New test assumes Unix filesystem errors: on Windows, `file\logs` counts as missing -> fixed. `TestScanLogDir_PathUnderRegularFileReturnsError` now skips on Windows, which matches the cleaner treating any not-exist root as empty. Fixed in `84224e31`.
- While tracing the first finding, the evaluator also found the same unlocked reads in older handlers and filed them to `docs/backlog/log-handlers-read-config-without-lock.md`. Codex did not raise this.

Iteration 2:
- NO ISSUES FOUND.

## Validation
- Commands: none
- Timings: duration_ms 0, runs 0

The checks the agents described in review transcripts (gofmt, go vet, the `-race` runs, `go test ./...`, the compile check, the `GOOS=windows` test compile) were not recorded as harness validation runs.