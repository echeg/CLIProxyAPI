# Log management handlers read h.cfg without h.mu

- found: 2026-11-05, plan: 20261105-logs-storage-endpoint, phase: evaluation
- severity: minor
- area: internal/api/handlers/management/logs.go

`SetConfig` (`handler.go:134-141`, called on hot reload from `server_reload.go:197`),
`PutConfigYAML` (`config_basic.go:168`) and `ConfigV8` (`config_v8.go:203`) replace `h.cfg`
while holding `h.mu`. The older log handlers read `h.cfg` and its fields with no lock:
`GetLogs` (`logs.go:45,49`), `DeleteLogs` (`logs.go:140,144`), `GetRequestErrorLogs`
(`logs.go:202,206`), `GetRequestLogByID` (`logs.go:266`) and `DownloadRequestErrorLog`
(`logs.go:435`). `logDirectory()` (`logs.go:568`) also reads `h.cfg` when no log directory was
set. A request that overlaps a config reload is a data race. One request can also check
`h.cfg == nil` on one snapshot and then read fields from another.

`GetLogStorage` (`logs.go:501`) was fixed on this branch. It copies the fields it needs under
`h.mu` and scans the directory after unlocking, and
`TestGetLogStorageConcurrentSetConfigIsRaceFree` covers it under `-race`. The handlers above
predate this branch.

Suggested fix: snapshot `h.cfg` (or just the fields used) and `h.logDirectory()` under `h.mu` at
the top of each handler, the way `GetClaudeFallback` (`claude_fallback.go:16-26`) and
`GetLogStorage` do. Then add a `-race` test per handler that runs `SetConfig` concurrently.
