# A model registered only under its suffixed name cannot be served

- found: 2026-11-19, plan: 20261119-provider-disable-in-profiles, phase: evaluation
- severity: minor
- area: sdk/cliproxy/auth/conductor_selection.go

Handler routing keeps the Story 11.8 fallback: when no provider registers the base
model of a request (`x` for `x(high)`), `routedModel`
(`sdk/api/handlers/handlers_routing.go:238`) routes the request by the full
suffixed name, so a provider that registers only `x(high)` receives it. Credential
selection does not follow it. `authSupportsRouteModel`
(`sdk/cliproxy/auth/conductor_selection.go:1529`) strips the suffix
(`canonicalModelKey`) and requires the client to register `x`, so every credential is
rejected and the request fails with `auth_not_found: no auth available`. The
Responses websocket pin matcher checks `x` too
(`responsesWebsocketProviderSetForModel`,
`sdk/api/handlers/openai/openai_responses_websocket_session.go:196`).

Reproduced: register a Codex client with only `x(high)` and call `Manager.Execute`
with model `x(high)`; it returns `auth_not_found` and no executor runs. A suffixed
OAuth alias without `fork` (alias `x(high)` for upstream `y`) fails the same way,
since selection then checks `y`, which the client does not register.

This affects explicit requests for such a model and the unfiltered `model: auto`
(`ModelRegistry.GetFirstAvailableModel`, `internal/registry/model_registry.go:2202`),
which counts the listed `x(high)` on its own registration and can pick it over an
older servable model. It predates the provider-disable plan. That plan only fixes
the filtered `auto`, used when a routing profile disables a provider:
`ResolveAutoModelForRequest` counts only the clients that selection accepts for the
routed request (`registry.ModelRoute`, with the OAuth alias and prefix keys from
`Manager.RouteModelSelectionKeys`), so such a model is skipped there.

Suggested direction: choose one behavior and apply it in all three places.

- Make selection and the websocket matcher accept the registry key that routing
  matched (`util.GetRegisteredModelProviders` returns it). The request keeps its
  model for the upstream payload, and cooldown/model-state keys must stay
  consistent.
- Or drop the full-name fallback from `routedModel` and return `model_not_found`.

Either way, the unfiltered `auto` should then count a listed model the same way
selection matches it.
