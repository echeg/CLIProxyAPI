# Logs directory storage endpoint

## Overview
- Add `GET /v8/management/observability/logs/storage`. It reports how much disk the CPA logs directory uses, how much of that the `logs-max-total-size-mb` cleaner manages, the oldest and newest file times, and the related logging config flags.
- Make the default request logger write into the same logs directory that the cleaner, `main.log`, and the management handlers use. Today it resolves the relative `logs` path against the config file's directory, while everyone else resolves it against the working directory.
- Problem: enabling `observability.logs.request-log` writes one file per proxied request (client request/response plus upstream API request/response). From token stats that is an estimated 1-10 GB/day (12-25k requests/day, 90M-1.3B input tokens/day). No API or UI shows the logs directory size today, so the user cannot see the cost or check that the cap works.
- Integration:
  - a one-line directory fix in the default request logger factory;
  - a read-only scan helper in `internal/logging`;
  - a management handler next to the other log handlers;
  - one v8 route.
- The CPA-Manager-Plus panel adds a read-only "CPA logs" card on `#/usage-maintenance` in a separate plan that is created after this one.

## Decisions
- **Context**: the user asked to see the logs directory size on the panel page `#/usage-maintenance`, next to the SQLite size card. In brainstorming on 2026-11-05 the user chose a read-only display, with no inline limit editing and no clear button. The work is split into two loopai plans with the backend first; this is plan 1.
- **Chosen approach** (approved in brainstorming on 2026-11-05, refined by the grill round the same day):
  0. **One logs directory.** `defaultRequestLoggerFactory` (`internal/api/server_options.go:35-38`) passes the absolute form of `logging.ResolveLogDirectory(cfg)` to `NewFileRequestLogger`. The config-dir rebasing in `NewFileRequestLogger` (`internal/logging/request_logger.go:162-166`) then no longer applies to the default server. The request logger, the cleaner and `main.log` (`global_logger.go:213`), and the management handlers (`server.go:218-219` with `handler.go:254-256`) all use the working-directory-relative path, which is also the path whose writability `ResolveLogDirectory` probes (`global_logger.go:191`).
     - The public SDK `NewFileRequestLogger(..., configDir)` signature and behavior stay unchanged.
     - Setups whose working directory differs from the config directory will see request logs move from `<configDir>/logs` to `<cwd>/logs`.
  1. **Scan helper.** `internal/logging` gets a new `ScanLogDir(dir string) (LogDirStats, error)`.
     - **Root handling.** It cleans the path, then calls `os.Stat` on the root, which follows a symlinked root the same way the cleaner's `os.ReadDir` does. A missing root gives zero stats and a nil error. A root that is not a directory, or any other root error, is returned wrapped.
     - **Top level.** It reads the top level with `os.ReadDir` (cleaner parity, `log_dir_cleaner.go:85`).
       - A top-level regular file counts into `TotalBytes`/`TotalFiles`. If its name also passes the existing `isLogFileName` (`log_dir_cleaner.go:159`), it counts into `CappedBytes`/`CappedFiles` too; these are exactly the files the cleaner counts (`log_dir_cleaner.go:108-130`).
       - Top-level directories are walked with `filepath.WalkDir`, and their regular files count only into the totals.
     - **Bytes** are logical file lengths (`info.Size()`), as the cleaner sums them. They are not allocated blocks.
     - **Skipped entries.** Symlinks and other non-regular entries below the root are never counted. An entry or subdirectory that cannot be stat'ed or read below the root is skipped, as the cleaner skips entries whose `Info()` fails (`log_dir_cleaner.go:116-119`). Only root errors fail the call.
     - **Times.** The oldest and newest mtime are tracked across counted files only.
     - The scan is not atomic while logging continues. The cleaner is not refactored, and nothing is cached.
  2. **Handler.** The management handler `GetLogStorage` in `internal/api/handlers/management/logs.go` takes the directory from `h.logDirectory()` (`logs.go:485`).
     - **Guards** copy `GetRequestLogByID`: 500 for a nil handler, 503 `configuration unavailable` for a nil config, 500 `log directory not configured` for a blank directory (`logs.go:261-275`).
     - **Response** is snake_case JSON:
       - `total_bytes`, `total_files`, `capped_bytes`, `capped_files`;
       - `oldest_modified_at`, `newest_modified_at`: `t.UTC().Format(time.RFC3339)`, or JSON `null` when no file was counted;
       - `limit_mb` (`cfg.LogsMaxTotalSizeMB`), `request_log` (`cfg.RequestLog`), `logging_to_file` (`cfg.LoggingToFile`).
     - Scan errors return 500 with an `{"error": ...}` body.
  3. **Route.** `v8.GET("/observability/logs/storage", s.mgmt.GetLogStorage)` goes in `internal/api/server_management_v8.go` next to lines 38-42. It is v8 only, with no v0 alias.
  4. **Docs.** Add one row in the endpoint table of `docs/management-api-v8.md` (around line 555), plus the response fields, the logical-bytes definition, and the non-atomic note.
- **Rejected alternatives** (trade-offs only; the user did not judge them):
  - A v0 alias, or extending `GET /observability/logs`: that endpoint requires `logging-to-file` (`logs.go:49`) and returns log lines. The panel already has a v8 client.
  - Totals only: they don't show what the cap actually manages.
  - Caching: the scan is cheap compared with the cleaner, and the panel calls it only on page load and refresh.
  - Anchoring every consumer on the config directory: that needs the config path inside `ConfigureLogOutput` and the cleaner, which is a larger change than moving the one outlier.
  - Aborting the scan on a descendant error: the cleaner skips unreadable entries, and one unreadable `request-log-parts-*` folder should not turn the whole card into an error.
- **Verified facts**:
  - `logs-max-total-size-mb` caps the whole logs directory. The cleaner runs whether or not `logging-to-file` is set (`internal/logging/global_logger.go:240`) and protects only `main.log`. It counts only top-level `*.log`/`*.log.gz` regular files, and sums `info.Size()` (`log_dir_cleaner.go:108-130,159-166`).
  - `ResolveLogDirectory` returns the relative `logs` when `WRITABLE_PATH` is unset and `./logs` is writable. Otherwise it returns `<auth-dir>/logs` or `$WRITABLE_PATH/logs` (`global_logger.go:184-201`). `TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory` (`internal/api/server_test.go:2607`) covers the auth-dir fallback and must keep passing.
  - The live launchd service sets `WorkingDirectory` to the config directory, so today its request logs and cleaner directory coincide.
  - The request logger is created from `cfg.RequestLog` (`internal/api/server_options.go:38`). `GET /request-error-logs` returns an empty list while request-log is on (`logs.go:206`).
  - `GET /config/<section>/<field>` renders the persisted config without runtime defaults (`docs/management-api-v8.md:23-26`). That is why the storage endpoint returns the effective `limit_mb`/`request_log`/`logging_to_file`. The live `config.yaml` omits `request-log`.
  - The panel consumes v8 through `createQuotaRequestConfig` (CPA-Manager-Plus `apps/web/src/features/personal-quota-support/services/api/client.ts:38`) and treats 404 as an unsupported backend.
  - Routes are asserted in two places in `internal/api/server_management_v8_test.go`: the access-control request loop (line 44) and the registration list (lines 90-105).
  - Adjacent issue, in the backlog and out of scope: launchd's `stdout.log` sits inside the capped logs directory (`docs/backlog/launchd-stdout-log-in-capped-logs-dir.md`, commit 81f8903b).
- **Success criteria**:
  - With the management key, `GET /v8/management/observability/logs/storage` returns `total_*` equal to the logical-size sum and count of regular files under the logs directory. Check with `find <logs> -type f -exec stat -f%z {} + | awk '{s+=$1} END {print s, NR}'` on an idle directory.
  - It returns `capped_*` equal to the same for top-level `*.log`/`*.log.gz`.
  - A missing directory returns 200 with zeros.
  - Request logs land in the directory the endpoint scans.
  - `gofmt`, the compile check, and `go test` for the touched packages pass.
- **Non-goals**: editing the limit, clearing logs, history or trends, fixing where `stdout.log` lives, and panel changes (separate plan).

## Context (from discovery)
- Files/components involved:
  - `internal/api/server_options.go` (`defaultRequestLoggerFactory`) and `internal/api/server_test.go` (factory tests).
  - `internal/logging/log_dir_cleaner.go` (`isLogFileName`, cleaner scan semantics). A new file `internal/logging/log_dir_stats.go` holds the helper.
  - `internal/api/handlers/management/logs.go` (sibling handlers, `logDirectory()`) and `logs_test.go`.
  - `internal/api/server_management_v8.go` (v8 routes) and `internal/api/server_management_v8_test.go` (route lists).
  - `docs/management-api-v8.md` (endpoint table).
- Related patterns found:
  - Sibling log handlers return `gin.H{"error": ...}` with fixed guard codes.
  - The v8-only route `/routing/profiles` was added the same way (`server_management_v8.go:36`).
  - The existing factory test switches the working directory with `os.Chdir` and restores it.
- Dependencies identified: none new. Use `os`, `io/fs`, and `path/filepath` from the standard library. Go 1.26 (`go.mod`), so `t.Chdir` is available.

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
- Follow AGENTS.md:
  - run `gofmt -w` on changed Go files;
  - run the compile check `go build -o test-output ./cmd/server && rm test-output` after changes;
  - use explicit whole-second timestamps (`os.Chtimes`) instead of sleeps in tests.
- Maintain backward compatibility: the endpoint is additive, and the SDK request-logger constructors are unchanged.

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
- **Post-Completion** (no checkboxes): deploying to the local launchd service, and the follow-up panel plan.

## Decision Log
- 2026-11-05 loopai-brainstorm-v2: **accepted** - route `GET /v8/management/observability/logs/storage`, v8 only, next to the other `observability/logs` routes (evidence: `internal/api/server_management_v8.go:36-42`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - snake_case response with `total_*` (every regular file, recursive), `capped_*` (top-level `isLogFileName` files, the cleaner's view), oldest and newest mtime, and `limit_mb`/`request_log`/`logging_to_file` (evidence: `internal/logging/log_dir_cleaner.go:108-130,159`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - the scan is a new function in `internal/logging` that reuses `isLogFileName`; the cleaner is untouched and nothing is cached (evidence: the cleaner already does a full `ReadDir` per tick, `log_dir_cleaner.go:85`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - a missing logs directory returns 200 with zero stats and `directory_present:false` (evidence: the cleaner treats `IsNotExist` as empty, `log_dir_cleaner.go:87`).
- 2026-11-05 loopai-brainstorm-v2: **accepted** - document the endpoint as a row in `docs/management-api-v8.md` (evidence: the `observability/logs` rows around line 555).
- 2026-11-05 grill: **accepted** - new Task 1 makes `defaultRequestLoggerFactory` pass the absolute `ResolveLogDirectory(cfg)`, so request logs, the cleaner, `main.log`, and the management endpoints share one directory, with a regression test where cwd differs from the config dir. The user chose to unify resolution. Anchoring on the working directory is decided by the code: three of four consumers and the writability probe already use it (`global_logger.go:191,213`, `server.go:218`), and only the factory rebases (`request_logger.go:162-166`).
- 2026-11-05 grill: **accepted** - removed `directory_present`/`Present`; a missing directory is 200 with zeros, like the cleaner (Decisions §1-2, Tasks 2-3, Technical Details).
- 2026-11-05 grill: **accepted** - kept `oldest_modified_at`/`newest_modified_at`; the panel plan will show the retention span and approximate growth rate on the card.
- 2026-11-05 grill: **accepted** - Task 2 stats the root with `os.Stat` (follows a symlinked root) and reads the top level with `os.ReadDir`, walking only subdirectories with `WalkDir`. Symlinked-root and descendant-symlink tests added - decided by grill: the cleaner opens its root with `os.ReadDir` (`log_dir_cleaner.go:85`), while `WalkDir` would `Lstat` a symlinked root and count nothing.
- 2026-11-05 grill: **accepted** - Task 2 skips unreadable or vanished entries below the root and fails only on root errors, with tests for an unreadable subdirectory and a root that is a regular file - decided by grill: the cleaner skips entries whose `Info()` fails (`log_dir_cleaner.go:116-119`).
- 2026-11-05 grill: **accepted** - Task 2 cleans the input path, decides "top level" from the `os.ReadDir` listing rather than by comparing path strings, and adds a trailing-slash test - decided by grill: the cleaner cleans its root (`log_dir_cleaner.go:83`), and `WalkDir` child paths are cleaned.
- 2026-11-05 grill: **accepted** - Task 2 fixtures name `sub/nested.log` (total, not capped) and a top-level `dir.log/` directory (not counted), and give directories far-off mtimes - decided by grill: the cleaner skips directories (`log_dir_cleaner.go:109`), and vague fixtures would let a depth-agnostic implementation pass.
- 2026-11-05 grill: **accepted** - Task 3 copies the sibling guards exactly (500 nil handler, 503 `configuration unavailable`, 500 `log directory not configured`) and adds a 500 test for a regular-file directory - decided by grill: `logs.go:261-275,290-297`.
- 2026-11-05 grill: **accepted** - times are `UTC().Format(time.RFC3339)`. Tests use whole-second non-UTC fixtures, and assert that the null keys are present - decided by grill: `RFC3339` drops sub-second digits, and filesystems store mtimes at different precisions.
- 2026-11-05 grill: **accepted** - Task 4 also adds the route to the access-control request loop - decided by grill: `internal/api/server_management_v8_test.go:44` is the only test that sends requests through the v8 middleware.
- 2026-11-05 grill: **accepted** - bytes are defined as logical file lengths, the `du` acceptance check is replaced with a `find`/`stat` sum, and the docs note that scans are non-atomic - decided by grill: the cleaner sums `info.Size()` (`log_dir_cleaner.go:124-129`), and `du` measures allocated blocks.

## Implementation Steps

### Task 1: Put request logs in the shared logs directory
- [x] in `internal/api/server_options.go` `defaultRequestLoggerFactory`, convert `logging.ResolveLogDirectory(cfg)` to an absolute path with `filepath.Abs` (fall back to the raw value if `Abs` fails) before passing it to `NewFileRequestLogger`, so the config-dir rebasing no longer applies; leave `NewFileRequestLogger` and the SDK constructors unchanged
- [x] add a test in `internal/api/server_test.go`: `t.Chdir` into temp dir A that contains a writable `logs/` directory, put the config at `B/config.yaml`, clear `WRITABLE_PATH`/`writable_path` with `t.Setenv`, write a forced error request log through the factory's logger, and assert that it lands in `A/logs` and not in `B/logs`
- [x] confirm `TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory` (auth-dir fallback) still passes unchanged
- [x] run `gofmt -w` and `go test ./internal/api/ -run 'RequestLogger'` - must pass before task 2

### Task 2: Add the logs directory scan helper
- [x] create `internal/logging/log_dir_stats.go` with a `LogDirStats` struct (`TotalBytes`, `TotalFiles`, `CappedBytes`, `CappedFiles int64`, `Oldest`, `Newest time.Time`) and `ScanLogDir(dir string) (LogDirStats, error)`
- [x] handle the root: `filepath.Clean(strings.TrimSpace(dir))`; a blank path gives zero stats; `os.Stat` (follows a symlinked root): not-exist gives zero stats and a nil error, a non-directory or any other error is returned wrapped with context
- [x] read the top level with `os.ReadDir`. A regular file counts into the totals, and also into the capped stats when `isLogFileName(name)`. A directory is walked with `filepath.WalkDir`: count only regular files (`d.Type().IsRegular()`, which skips symlinks) into the totals. Skip any entry or subdirectory whose `Info()` or read fails (return `fs.SkipDir` for directories; never dereference a nil `d`). Track the oldest and newest mtime over counted files only; bytes are `info.Size()`
- [x] write tests in `internal/logging/log_dir_stats_test.go` on `t.TempDir()`:
  - a fixture with `a.log`, `B.LOG`, `c.log.gz`, `request-body-1.tmp`, `sub/nested.log`, `request-log-parts-x/part.bin`, and an empty top-level directory `dir.log/`. Assert exact total and capped bytes and counts: `sub/nested.log` counts in totals only, and `dir.log/` is not counted
  - oldest and newest set with `os.Chtimes` to whole-second non-UTC times; directories set to far-off mtimes that must not appear in the result
- [x] write tests for the edge cases:
  - missing directory → zeros with a nil error;
  - empty directory → zeros and zero times;
  - blank path;
  - trailing-slash path → same result as the clean path;
  - root that is a regular file → non-nil error;
  - symlinked root → same stats as its target;
  - descendant `link.log` → regular file, and a dangling `dangling.log` → neither counted;
  - unreadable subdirectory (mode 0o000, restored in `t.Cleanup`) → scan succeeds and counts the readable files.

  The symlink tests skip when `os.Symlink` fails. The unreadable-subdirectory test skips on Windows and when running as root.
- [x] run `gofmt -w` on changed files and `go test ./internal/logging/` - must pass before task 3

### Task 3: Add the GetLogStorage management handler
- [x] add `GetLogStorage` to `internal/api/handlers/management/logs.go`. Guards copy `GetRequestLogByID`: 500 `handler unavailable` for a nil handler, 503 `configuration unavailable` for a nil config, 500 `log directory not configured` for a blank `h.logDirectory()`. Then call `logging.ScanLogDir`, and return 500 `{"error": ...}` on a scan error
- [x] build the snake_case response: `total_bytes`, `total_files`, `capped_bytes`, `capped_files`; `oldest_modified_at`/`newest_modified_at` as `t.UTC().Format(time.RFC3339)` or JSON `null` when zero; `limit_mb`, `request_log`, `logging_to_file` from `h.cfg`
- [x] write tests in `logs_test.go` using `SetLogDirectory(t.TempDir())`: files with whole-second non-UTC mtimes and the config flags set; decode the whole JSON and assert every field, including the UTC-formatted times
- [x] write tests for the error and edge cases:
  - missing directory → 200 with zeros, and both time keys present with `null` values;
  - `SetLogDirectory` pointing at a regular file → 500 with a non-empty `error`;
  - nil config → 503 `{"error":"configuration unavailable"}`.
- [x] run `gofmt -w` and `go test ./internal/api/handlers/management/` - must pass before task 4

### Task 4: Register the v8 route and document it
- [x] add `v8.GET("/observability/logs/storage", s.mgmt.GetLogStorage)` in `internal/api/server_management_v8.go` next to the other `observability/logs` routes
- [x] in `internal/api/server_management_v8_test.go`, add `"/v8/management/observability/logs/storage"` to the access-control request loop (line 44) and `"GET /v8/management/observability/logs/storage"` to the registration list (lines 90-105)
- [x] add the endpoint row to the endpoint table in `docs/management-api-v8.md` (around line 555), with the response fields, a note that bytes are logical file lengths, and a note that a scan is not atomic while logging continues
- [x] run `gofmt -w` and `go test ./internal/api/` - must pass before task 5

### Task 5: Verify acceptance criteria
- [x] verify every item in Overview and Decisions is implemented: shared logs directory, route, fields (no `directory_present`), missing-directory behavior, and capped semantics matching `isLogFileName`
- [x] run the compile check `go build -o test-output ./cmd/server && rm test-output`
- [x] run `go vet ./internal/logging/ ./internal/api/...` and `go test ./internal/logging/ ./internal/api/...`, then `go test ./...`; all must pass, or a pre-existing unrelated failure is noted with ⚠️ and evidence that it fails on `main` too

### Task 6: [Final] Update documentation
- [ ] confirm `docs/management-api-v8.md` matches the final response shape
- [ ] update project knowledge docs (AGENTS.md) only if a new pattern was introduced

## Technical Details
- Response example:
  ```json
  {
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
- `total_*` is the logical size of every regular file in the directory tree. `capped_*` is what `logs-max-total-size-mb` is enforced against: top-level `*.log`/`*.log.gz` files, which include the protected `main.log` and launchd's `stdout.log`. The panel compares `capped_bytes` with `limit_mb * 1024 * 1024`, and can derive a growth rate from `total_bytes` over `newest - oldest`.
- Scan cost: one `ReadDir` plus a walk of the few subdirectories per request, with no cache. At 20k+ files/day under request-log this stays in the tens of milliseconds, comparable to one cleaner tick.

## Post-Completion
**Deploy**: build from `main` and replace the local `local.clipproxyapi` binary following the CPA deploy notes: back up the binary, config, and plist, then kickstart. Verify `/v8/management/observability/logs/storage` with the management key against `find ~/Library/Application\ Support/CLIProxyAPI/logs -type f -exec stat -f%z {} + | awk '{s+=$1} END {print s, NR}'`. Because the launchd `WorkingDirectory` equals the config directory, the request logs stay where they are now.

**Follow-up**: the CPA-Manager-Plus plan for the read-only "CPA logs" card on `#/usage-maintenance`. It shows total size, file count, the limit, request-log status, and the retention span with approximate growth from oldest/newest. It treats a 404 from older CPA builds as unsupported, and fixes the `logs_max_size` label in en/ru/zh-CN/zh-TW to say "total".
