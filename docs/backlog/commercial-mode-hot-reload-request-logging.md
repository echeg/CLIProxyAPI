# Hot-reloading commercial-mode does not add or remove request logging middleware

- found: 2026-11-05, plan: 20261105-logs-storage-endpoint, phase: internal review
- severity: minor
- area: internal/api/server_reload.go

`commercial-mode` hot-reloads without a restart, but the request logging middleware is
added only once, in `NewServer`, and only when `!cfg.CommercialMode`
(`internal/api/server.go:158`). On reload, `UpdateClientsContext` toggles the logger only when
`request-log` changes, and it never checks `commercial-mode`
(`internal/api/server_reload.go:61-72`). The middleware checks only `logger.IsEnabled()`
(`internal/api/middleware/request_logging.go:49`). Meanwhile, executor capture reads the
live config on every request (`internal/runtime/executor/helps/logging_helpers.go:61,66`).
The result:

- turning `commercial-mode` on at runtime stops executor capture, but the middleware
  keeps writing one request log file per request (without upstream details);
- turning it off at runtime does nothing until restart, because no request logger exists.

`request_log` on `GET /observability/logs/storage` (`internal/api/handlers/management/logs.go:532`)
reports `request-log && !commercial-mode` from the live config. After such a reload it no
longer matches what is actually written to disk.

Fix direction: either have `UpdateClientsContext` disable or enable the request logger when
`commercial-mode` changes (create the logger in `NewServer` regardless of commercial mode,
and gate it with `SetEnabled(cfg.RequestLog && !cfg.CommercialMode)`), or document that
changing `commercial-mode` needs a restart.
