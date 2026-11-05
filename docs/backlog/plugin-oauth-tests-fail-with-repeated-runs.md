# Plugin OAuth management tests fail under go test -count>1

- found: 2026-11-05, plan: 20261105-logs-storage-endpoint, phase: internal review
- severity: minor
- area: internal/api/handlers/management/oauth_sessions.go

Three management tests pass with the default `go test` run but fail when the package
is run more than once in one process
(`go test -count=2 -run 'TestServePluginAuthURLPassesQueryParamsAsMetadata|TestGetOAuthCallbackWritesPluginProviderCallback|TestGetOAuthCallbackDoesNotAliasPluginProvider' ./internal/api/handlers/management/`):

- `TestServePluginAuthURLPassesQueryParamsAsMetadata` (`auth_files_plugin_oauth_test.go:332`):
  status 502 `failed to generate authorization url`, logged as `oauth session already exists`.
- `TestGetOAuthCallbackWritesPluginProviderCallback` (`oauth_callback_test.go:57`) and
  `TestGetOAuthCallbackDoesNotAliasPluginProvider` (`oauth_callback_test.go:93`):
  `register plugin oauth session: oauth session already exists`.

The tests register sessions under fixed state strings (for example
`test-geminicli-state`) in the package-global OAuth session store
(`errOAuthSessionExists`, `oauth_sessions.go:32`). Some session from the first iteration
survives into the second one, so registration fails. The files are untouched by the logs
storage branch.

Fix direction: give each test a unique state (`t.Name()` plus a counter or uuid), and remove
every registered session in `t.Cleanup`, including the one `ServePluginAuthURL` creates
itself. Then confirm with `-count=3`.
