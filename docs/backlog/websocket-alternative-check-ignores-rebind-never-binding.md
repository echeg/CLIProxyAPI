# Websocket alternative-target check ignores rebind-on never session bindings

- found: 2026-11-16, plan: 20261116-websocket-pin-routing-policy, phase: internal review
- severity: minor
- area: sdk/cliproxy/auth/conductor_selection.go

`Manager.HasAlternativeTargetForModel` (primary check in `hasAvailableTargetForModel`)
counts every eligible, available credential, but a fresh request under
`routing.session-affinity-rebind-on: never` stays on the session's bound credential
and refuses to move (`SessionAffinitySelector.Pick`, `session_auth_unavailable` in
`selector.go`). The check therefore promises a target the replayed request cannot reach.

Effect with `fallback.target: api` on the Responses websocket: while the bound
subscription cools down and another subscription is available, the turn-start policy
check releases the API-key pin, the incremental turn gets the `1012` replay close, the
replay hits the bound subscription, falls back to the API key, and pins it again. Every
incremental turn repeats this until the bound subscription recovers. The pre-existing
pinned-failure replay guard (`replayPinnedAuthFailure`) has the same blind spot: it can
send a replay close that ends in `session_auth_unavailable`.

Suggested direction: under `rebind-on: never` (and session affinity on for the
profile), restrict the primary candidates of `hasAvailableTargetForModel` to the
request's bound credential (`SessionAffinitySelector.boundAuthForRequest`, already used
in `conductor_execution.go` for the same mode), so only the API leg, which ignores
bindings, can count as an alternative to it. Add `HasAlternativeTargetForModel` and
websocket tests with a `RebindOn: never` selector.
