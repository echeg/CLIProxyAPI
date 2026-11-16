# Pinned API-key websocket turns are refused by rebind-on never session bindings

- found: 2026-11-16, plan: 20261116-websocket-pin-routing-policy, phase: internal review
- severity: major
- area: sdk/cliproxy/auth/selector.go

Under `routing.session-affinity-rebind-on: never` with `fallback.target: api`, a
Responses websocket turn that falls back to the API key pins that key, while the
session stays bound to the subscription that failed. The next incremental turn runs
pinned to the key with the regular (sticky) routing profile, so
`SessionAffinitySelector.Pick` hits the binding, finds the bound subscription
unavailable, and refuses with `session_auth_unavailable` (429). The pinned key is
never attempted (`pinnedAuthAttempted` stays false), so `replayPinnedAuthFailure`
does not rescue the turn. The API fallback leg avoids the binding only because its
derived profile turns session affinity off (`apiFallbackProfile`, `sticky = false`);
a pinned follow-up turn does not.

Effect, checked with a two-turn websocket test (rebind-never selector, two Codex
subscriptions, one API key, `X-Session-Id` on the dial):

- All subscriptions cooling down: the turn-start policy check finds no alternative,
  the pin stays, and every incremental turn returns 429 instead of staying
  incremental on the API key, while HTTP requests of the same session are served by
  the API leg. `main` behaves the same.
- The bound subscription cooling down, another one available: the turn-start check
  releases the pin, the turn gets the `1012` replay close, and the replayed request
  reaches the API key through the fallback leg. This replay on every incremental
  turn is what gets the turn served today.

Do not restrict `hasAvailableTargetForModel` (`conductor_selection.go`) to the
session's bound credential on its own: with the refusal above, that turns the second
case into the 429 of the first.

Suggested direction: let a pinned execution of a credential the routing policy
rejects (the API fallback key, `Manager.RoutingPolicyAllowsAuth` false) select
without reading or writing session bindings, as the API leg does. After that, the
turn-start alternative check can respect rebind-on never bindings
(`SessionAffinitySelector.boundAuthForRequest`), so the second case also stays
incremental on the key. Cover both cases with `RebindOn: never` websocket tests built
on the `newPolicyPinFixture` fixture in
`sdk/api/handlers/openai/openai_responses_websocket_policy_pin_test.go`.
