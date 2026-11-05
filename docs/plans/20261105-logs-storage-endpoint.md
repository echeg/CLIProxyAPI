# Logs directory storage endpoint

## Overview
- Add `GET /v8/management/observability/logs/storage`. It reports how much disk the CPA logs directory uses, how much of that the `logs-max-total-size-mb` cleaner manages, the oldest and newest file times, and the related logging config flags.
- Problem: enabling `observability.logs.request-log` writes one file per proxied request (client request/response plus upstream API request/response). From token stats that is an estimated 1-10 GB/day (12-25k requests/day, 90M-1.3B input tokens/day). No API or UI shows the logs directory size today, so the user cannot see the cost or check that the cap works.
- Integration: a read-only scan helper in `internal/logging`, a management handler next to the other log handlers, and one v8 route. The CPA-Manager-Plus panel adds a read-only "CPA logs" card on `#/usage-maintenance` in a separate plan that is created after this one.

## Decisions
- **Context**: the user asked to see the logs directory size on the panel page `#/usage-maintenance`, next to the SQLite size card. In brainstorming on 2026-11-05 the user chose a read-only display: no inline limit editing and no clear button. Work is split into two loopai plans with the backend first; this is plan 1.
- **Chosen approach** (approved in brainstorming on 2026-11-05):
  1. `internal/logging`: new `ScanLogDir(dir string) (LogDirStats, error)`. It walks the logs directory recursively and counts every regular file into `TotalBytes`/`TotalFiles`, including the `request-log-parts-*/` subdirectories and `.tmp` files. Separately it counts `CappedBytes`/`CappedFiles`: top-level regular files that pass the existing `isLogFileName` (`log_dir_cleaner.go:159`), which are exactly the files the size cleaner counts (`log_dir_cleaner.go:108-130`). It records the oldest and newest mtime across all counted files. A missing directory gives zero stats with `Present=false`, the same way the cleaner treats `IsNotExist` (`log_dir_cleaner.go:87`). The cleaner is not refactored, and nothing is cached: the cleaner already does a full `ReadDir` on every tick.
  2. Management handler `GetLogStorage` in `internal/api/handlers/management/logs.go` takes the directory from `h.logDirectory()` (`logs.go:485`). It returns snake_case JSON: `directory_present`, `total_bytes`, `total_files`, `capped_bytes`, `capped_files`, `oldest_modified_at`, `newest_modified_at` (RFC3339 UTC, or `null` when no files were counted), `limit_mb` (`cfg.LogsMaxTotalSizeMB`), `request_log` (`cfg.RequestLog`), `logging_to_file` (`cfg.LoggingToFile`). Scan errors other than not-exist return 500 with an `{"error": ...}` body, like the sibling handlers.
  3. Route `v8.GET("/observability/logs/storage", s.mgmt.GetLogStorage)` in `internal/api/server_management_v8.go` next to lines 38-42. It is v8 only, with no v0 alias.
  4. Docs: one row in the endpoint table of `docs/management-api-v8.md` (around line 555) plus the response fields.
- **Rejected alternatives** (trade-offs only; the user did not judge them):
  - A v0 alias, or extending `GET /observability/logs`: that endpoint requires `logging-to-file` (`logs.go:49`) and returns log lines, and the panel already has a v8 client.
  - Total only: then nobody can tell what the cap actually manages, because launchd's `stdout.log` and the temporary request-log parts are counted differently.
  - Caching: the scan is cheap next to the cleaner, and the panel requests it only on page load and refresh.
- **Verified facts**:
  - `logs-max-total-size-mb` is a total cap over the logs directory. The cleaner runs whether or not `logging-to-file` is set (`internal/logging/global_logger.go:240`), protects only `main.log`, and counts only top-level `*.log`/`*.log.gz` regular files (`log_dir_cleaner.go:108-130,159-166`).
  - The request logger is created from `cfg.RequestLog` (`internal/api/server_options.go:38`). `GET /request-error-logs` returns an empty list while request-log is on (`logs.go:206`).
  - `Handler.logDir` is set through `SetLogDirectory` (`internal/api/handlers/management/handler.go:255-259`); `logDirectory()` falls back to `logging.ResolveLogDirectory(h.cfg)`.
  - The panel consumes v8 through `createQuotaRequestConfig` (CPA-Manager-Plus `apps/web/src/features/personal-quota-support/services/api/client.ts:38`) and treats 404 as an unsupported backend.
  - Route registration is asserted in `internal/api/server_management_v8_test.go:90-105`.
  - An adjacent issue is in the backlog and out of scope: launchd's `stdout.log` sits inside the capped logs directory (`docs/backlog/launchd-stdout-log-in-capped-logs-dir.md`, commit 81f8903b).
- **Success criteria**: with the management key, `GET /v8/management/observability/logs/storage` returns totals that match a `find`/`du` over the logs directory, and capped numbers that match the cleaner's view. It also works when the directory is missing. `gofmt`, the compile check, and `go test` for the touched packages pass.
- **Non-goals**: editing the limit, clearing logs, history or trends, fixing where `stdout.log` lives, and panel changes (separate plan).

## Context (from discovery)
- Files/components involved:
  - `internal/logging/log_dir_cleaner.go` (`isLogFileName`, cleaner scan semantics). The new helper goes in a new file `internal/logging/log_dir_stats.go`.
  - `internal/api/handlers/management/logs.go` (sibling handlers, `logDirectory()`) and `logs_test.go`.
  - `internal/api/server_management_v8.go` (v8 routes) and `internal/api/server_management_v8_test.go` (route list).
  - `docs/management-api-v8.md` (endpoint table).
- Related patterns found: the sibling log handlers return `gin.H{"error": ...}` on failure. The v8-only route `/routing/profiles` was added the same way (`server_management_v8.go:36`). The cleaner reads `entry.Info()` and skips entries it cannot stat.
- Dependencies identified: none new. Use `io/fs`/`path/filepath.WalkDir` from the standard library.

## Development Approach
- **Testing approach**: Regular (code first, then tests).
- Complete each task fully before moving to the next.
- Make small, focused changes.
- **CRITICAL: every task MUST include new/updated tests** for code changes in that task:
  - write unit tests for new functions/methods;
  - write unit tests for modified functions/methods;
  - add new test cases for new code paths;
  - update existing test cases if behavior changes;
  - cover both success and error scenarios.
- **CRITICAL: all tests must pass before starting the next task** - no exceptions.
- **CRITICAL: update this plan file when scope changes during implementation.**
- Follow AGENTS.md: run `gofmt -w` on changed Go files, run the compile check `go build -o test-output ./cmd/server && rm test-output` after changes, and use explicit timestamps (`os.Chtimes`) instead of sleeps in tests.
- Maintain backward compatibility: this change only adds an endpoint.

## Testing Strategy
- **Unit tests**: required for every task (see Development Approach above).
- **E2E tests**: none in this repository for the management API. The panel plan covers the UI.

## Progress Tracking
- Mark completed items with `[x]` immediately when done.
- Add newly discovered tasks with ➕ prefix.
- Document issues/blockers with ⚠️ prefix.
- Update the plan if implementation deviates from the original scope.

## What Goes Where
- **Implementation Steps** (`[ ]` checkboxes): code, tests, and docs in this repository.
- **Post-Completion** (no checkboxes): deploy to the local launchd service and the follow-up panel plan.

## Decision Log
- 2026-11-05 loopai-brainstorm-v2: **accepted** - route `GET /v8/management/observability/logs/storage`, v8 only, next to the other `observability/logs` routes (evidence: `internal/api/server_management_v8.go:36-42`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - snake_case response with `total_*` (every regular file, recursive), `capped_*` (top-level `isLogFileName` files, the cleaner's view), oldest and newest mtime, and `limit_mb`/`request_log`/`logging_to_file` (evidence: `internal/logging/log_dir_cleaner.go:108-130,159`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - the scan is a new function in `internal/logging` that reuses `isLogFileName`; the cleaner is untouched and nothing is cached (evidence: the cleaner already does a full `ReadDir` per tick, `log_dir_cleaner.go:85`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - a missing logs directory returns 200 with zero stats and `directory_present:false` (evidence: the cleaner treats `IsNotExist` as empty, `log_dir_cleaner.go:87`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - document the endpoint as a row in `docs/management-api-v8.md` (evidence: the `observability/logs` rows around line 555).

## Implementation Steps

### Task 1: Add the logs directory scan helper
- [ ] create `internal/logging/log_dir_stats.go` with a `LogDirStats` struct (`Present bool`, `TotalBytes`, `TotalFiles`, `CappedBytes`, `CappedFiles int64`, `Oldest`, `Newest time.Time`) and `ScanLogDir(dir string) (LogDirStats, error)`
- [ ] walk with `filepath.WalkDir`; count only regular files (`d.Type().IsRegular()`), skip symlinks, and ignore entries that vanish mid-walk (`fs.ErrNotExist` from `d.Info()` or a walk callback error). The cleaner may delete files concurrently
- [ ] count capped stats only for files directly in `dir` whose name passes `isLogFileName`; track the oldest and newest mtime across all counted files
- [ ] when `dir` is blank, return zero stats with `Present=false`; when the root does not exist, do the same with a nil error; wrap any other root error with context
- [ ] write tests in `internal/logging/log_dir_stats_test.go` on `t.TempDir()`: a mix of `a.log`, `b.log.gz`, `request-body-1.tmp`, `request-log-parts-x/part.bin`, and a nested subdir file. Assert total vs capped bytes and counts, and set oldest/newest via `os.Chtimes`
- [ ] write tests for edge cases: missing directory, empty directory (`Present=true`, zero times), blank path, and an uppercase `.LOG` name counted as capped (same as the cleaner)
- [ ] run `gofmt -w` on changed files and `go test ./internal/logging/` - must pass before task 2

### Task 2: Add the GetLogStorage management handler
- [ ] add `GetLogStorage` to `internal/api/handlers/management/logs.go`: nil-handler/nil-config guards like `GetRequestLogByID`, call `logging.ScanLogDir(h.logDirectory())`, return 500 `{"error": ...}` on a scan error
- [ ] build the snake_case response: `directory_present`, `total_bytes`, `total_files`, `capped_bytes`, `capped_files`, `oldest_modified_at`/`newest_modified_at` as RFC3339 UTC strings or JSON `null` when zero, `limit_mb`, `request_log`, `logging_to_file` read from `h.cfg`
- [ ] write tests in `logs_test.go` using `SetLogDirectory(t.TempDir())`: files present with config flags set; assert the whole decoded JSON, including the formatted times
- [ ] write tests for a missing directory (200, `directory_present:false`, null times) and a nil config (non-200, like sibling handlers)
- [ ] run `gofmt -w` and `go test ./internal/api/handlers/management/` - must pass before task 3

### Task 3: Register the v8 route and document it
- [ ] add `v8.GET("/observability/logs/storage", s.mgmt.GetLogStorage)` in `internal/api/server_management_v8.go` next to the other `observability/logs` routes
- [ ] add `"GET /v8/management/observability/logs/storage"` to the expected route list in `internal/api/server_management_v8_test.go`
- [ ] add the endpoint row and a short description of the response fields to `docs/management-api-v8.md` (endpoint table around line 555)
- [ ] run `gofmt -w` and `go test ./internal/api/` - must pass before task 4

### Task 4: Verify acceptance criteria
- [ ] verify every item in Overview and Decisions is implemented (route, fields, missing-dir behavior, capped semantics match `isLogFileName`)
- [ ] run the compile check `go build -o test-output ./cmd/server && rm test-output`
- [ ] run `go vet` and `go test` for `./internal/logging/ ./internal/api/...`, then `go test ./...`; all tests must pass, or pre-existing unrelated failures are noted with ⚠️ and evidence that they fail on `main` too

### Task 5: [Final] Update documentation
- [ ] confirm `docs/management-api-v8.md` matches the final response shape
- [ ] update project knowledge docs (AGENTS.md) only if a new pattern was introduced

## Technical Details
- Response example:
  ```json
  {
    "directory_present": true,
    "total_bytes": 3456789012,
    "total_files": 18234,
    "capped_bytes": 3401234567,
    "capped_files": 18220,
    "oldest_modified_at": "2026-11-05T08:00:00Z",
    "newest_modified_at": "2026-11-05T12:56:16Z",
    "limit_mb": 10000,
    "request_log": true,
    "logging_to_file": true
  }
  ```
- `total_*` measures disk usage across the whole directory tree. `capped_*` is what `logs-max-total-size-mb` is enforced against: top-level `*.log`/`*.log.gz` files, which include the protected `main.log` and launchd's `stdout.log`. The panel compares `capped_bytes` with `limit_mb * 1024 * 1024`.
- Scan cost: one directory walk per request, with no cache. At 20k+ files/day under request-log this stays in the tens of milliseconds, comparable to one cleaner tick.

## Post-Completion
**Deploy**: build from `main` and replace the local `local.clipproxyapi` binary following the CPA deploy notes (back up the binary, config, and plist; kickstart; verify `/v8/management/observability/logs/storage` with the management key, and compare it with `du -sh` and `find ... | wc -l` on `~/Library/Application Support/CLIProxyAPI/logs`).

**Follow-up**: the CPA-Manager-Plus plan for the read-only "CPA logs" card on `#/usage-maintenance`. It handles a 404 from older CPA builds as unsupported, and fixes the `logs_max_size` label in en/ru/zh-CN/zh-TW to say "total".
