# Flaky websocket replay close after pinned auth failure

- found: 2026-10-08, plan: 20261008-quota-reserve, phase: task
- severity: minor
- area: sdk/api/handlers/openai/openai_responses_websocket.go

`TestResponsesWebsocketReplaysImmediatelyAfterPinnedAuthFailure/unauthorized_to_http`
(`sdk/api/handlers/openai/openai_responses_websocket_test.go:6547`) failed once during a
full `go test ./...` run with `websocket: close 1006 (abnormal closure): unexpected EOF`
instead of the expected close `1012 "upstream requires HTTP replay"`. It passed 10/10 runs
in isolation, 4/4 package runs, and a second full-suite run, so it only shows under
parallel load. The test uses its own `orderedWebsocketSelector` and no quota reserve, so
it is unrelated to the selector changes in this plan.

The client saw the TCP connection end without the close frame. Likely the handler closes
the connection before the close control frame built at
`openai_responses_websocket.go:75-79` is flushed, or the pinned-auth failure is sometimes
classified as a non-replay error under load. Suggested direction: reproduce with
`go test ./sdk/api/handlers/openai/ -run TestResponsesWebsocketReplaysImmediatelyAfterPinnedAuthFailure -count=200 -race -cpu 1,4`
while the machine is loaded, then check that the close frame write completes before the
connection is closed and that the replay-required classification does not depend on timing.
