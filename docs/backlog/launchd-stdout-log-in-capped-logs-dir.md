# launchd stdout.log counts against the logs size cap and can be unlinked

- found: 2026-11-05, plan: 20261105-logs-storage-endpoint, phase: planning
- severity: minor
- area: internal/logging/log_dir_cleaner.go

The local launchd service writes CPA stdout to `logs/stdout.log` (`StandardOutPath` in `~/Library/LaunchAgents/local.clipproxyapi.plist`), the same directory the size cleaner manages. The name matches `isLogFileName` (`log_dir_cleaner.go:159`), and only `main.log` is protected (`global_logger.go:224,240`), so:

- the unbounded, never-rotated `stdout.log` (180 MB on 2026-11-05) consumes the `logs-max-total-size-mb` budget meant for request logs;
- once it alone exceeds the cap, the cleaner unlinks it while launchd keeps writing to the deleted inode, so the space is not freed and the output is lost.

Fix direction: point the launchd `StandardOutPath` outside `logs/` (or enable `logging-to-file` and send stdout to `/dev/null`), or let the cleaner skip files that are still open / a configurable protected list.
