# Responses websocket drops a pin that selection accepts through an OAuth alias

- found: 2026-11-19, plan: 20261119-provider-disable-in-profiles, phase: evaluation
- severity: minor
- area: sdk/api/handlers/openai/openai_responses_websocket_session.go

The Responses websocket matches a credential to the turn's model by the literal
base model. `responsesWebsocketPinnedAuthMatchesModel`
(`sdk/api/handlers/openai/openai_responses_websocket_session.go:148`) requires
`ClientSupportsModel(auth.ID, modelKey)` (`:166`) and checks availability for
`modelKey` (`:159`). The capability check `responsesWebsocketAuthMatchesModel`
(`:203`, used by `responsesWebsocketAvailableAuthsForModel`) does the same (`:211`,
`:214`). Credential selection is more lenient. `authSupportsRouteModel`
(`sdk/cliproxy/auth/conductor_selection.go:1529`) also accepts a credential that
registers the model its OAuth alias or prefix maps the request to. It then checks
cooldown for that model (`selectionModelForAuth`, `conductor_selection.go:903`).

Failure: a websocket-capable Codex credential has a forked alias `x(high)` for `y`,
so it registers `x(high)` and `y` but not `x`. A turn for `x(high)` runs on it,
over the upstream websocket, and the session pins it. On the next turn the matcher
rejects the pin (`openai_responses_websocket.go:811`) because the credential does
not register `x`, and the remembered-pin restore rejects it the same way. A
`previous_response_id` turn then closes with "upstream requires HTTP replay"
(`openai_responses_websocket.go:864`), although the original upstream could still
serve it.

This predates the provider-disable plan. On main an explicit `x(high)` request
hits it, and so does the unfiltered `model: auto`, which counts `x(high)` on its own
registration. The plan only changed which model filtered `auto` resolves to. It
did not change how the websocket matches a credential to a model.

Suggested fix direction: export a Manager check that reproduces selection for
one auth: `authSupportsRouteModel` plus
`IsAuthAvailableForModel(auth, selectionModelForAuth(auth, model), now)`. Use it
in the non-Home branch of the pin matcher and in `responsesWebsocketAuthMatchesModel`.
Keep the Home runtime comparison of model keys. Add a websocket test with a forked
suffixed alias and a `previous_response_id` follow-up turn.
