# Responses websocket stays on the API key after an API fallback turn

- found: 2026-11-10, plan: 20261110-provider-api-mode-and-fallback, phase: internal review
- severity: minor
- area: sdk/api/handlers/openai/openai_responses_websocket.go

`routing.providers.codex.fallback.target: api` runs the API fallback leg without
session affinity, so HTTP requests return to the subscriptions on the next request.
A Responses websocket connection behaves differently: when a turn is served by a
Codex API key with `websockets: true` over an upstream websocket, the handler pins
that credential for the rest of the connection (`rememberPinnedAuth` at
`openai_responses_websocket.go:1084`, `:1113`, `:1122`). Pinned executions skip the
provider policy and the fallback (`conductor_selection.go` `authSelectionEligibilityForRequest`,
`provider_fallback.go` `providerFallbackFor`), so every later turn on that connection
bills the API key even after the subscriptions recover. The reverse also holds: a turn
pinned to a subscription cannot fall back to the API key.

The pin exists because the upstream websocket holds the conversation state
(`previous_response_id` incremental input), so moving the next turn to another
credential needs a full transcript replay. The behavior is documented in
`docs/management-api-v8.md` (Runtime rules) and `config.example.yaml`.

Suggested direction: mark selections made in an API fallback leg (for example a
metadata flag published next to the selected auth) and have the websocket handler
skip `rememberPinnedAuth` for them, then force an HTTP replay
(`responsesWebsocketHTTPReplayRequiredError`) or keep a transcript so the next turn can
start from the subscriptions. Add a two-turn websocket regression test in
`sdk/api/handlers/openai` (turn 1 falls back, turn 2 runs with healthy subscriptions).
