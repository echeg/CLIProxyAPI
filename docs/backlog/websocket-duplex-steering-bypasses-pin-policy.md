# Duplex response steering forwards successor turns without the pin policy check

- found: 2026-11-16, plan: 20261116-websocket-pin-routing-policy, phase: planning
- severity: minor
- area: internal/runtime/executor/codex_websockets_duplex.go

With `codex.response-steering` enabled (experimental, default off), the Responses
websocket handler hands its input channel to the Codex duplex executor
(`sdk/api/handlers/openai/openai_responses_websocket.go:471`), which forwards later
`response.create` / `response.append` messages on the same upstream socket itself
(`internal/runtime/executor/codex_websockets_duplex.go:780`). Those turns never reach
the handler's turn-start check that releases a pin rejected by the routing policy
(plan `20261116-websocket-pin-routing-policy`), so a duplex stream that started on the
API fallback key keeps billing that key until the stream ends, even after the
subscriptions recover. The turn authorizer (`openai_responses_websocket.go:977`) checks
only the client credential.

Suggested direction: give the duplex executor a policy callback (like
`WithWebsocketAuthCheck`) that rejects a successor create when the bound credential is
rejected by `Manager.RoutingPolicyAllowsAuth` and an alternative exists, ending the
duplex turn with the replay-required error. Cover it with an integration test that uses
the real duplex executor with steering enabled.
