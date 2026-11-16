# HTTP-mode websocket pin failure does not reach the API fallback on that turn

- found: 2026-11-16, plan: 20261116-websocket-pin-routing-policy, phase: planning
- severity: minor
- area: sdk/api/handlers/openai/openai_responses_websocket.go

A Responses websocket turn can pin a credential in HTTP upstream mode after a local
compaction turn (`rememberPinnedAuth` after `LocalWebsocketTurnExecuted` in
`openai_responses_websocket.go`). When that
pinned subscription fails with 401/429, `replayPinnedAuthFailure` returns false because it
handles only native websocket passthrough pins, and pinned executions never
run the provider fallback (`sdk/cliproxy/auth/provider_fallback.go:34`). The client gets the
error for that turn; the pin is then released (`shouldReleaseResponsesWebsocketPinnedAuth`)
and the next turn can reach `fallback.target: api`.

Suggested direction: for HTTP-mode pins the handler holds the transcript, so on a
releasable pinned failure it can retry the turn in place unpinned (like the
`failoverInPlace` path) when `HasAlternativeTargetForModel` reports a
target. Add a websocket test with a local-compaction pin and `target: api`.
