# `model: auto` ignores the key's provider mode

- found: 2026-11-19, plan: 20261119-provider-disable-in-profiles, phase: evaluation
- severity: minor
- area: sdk/api/handlers/handlers_routing.go

`model: auto` resolves through `BaseAPIHandler.AutoModelProviderFilter`
(`sdk/api/handlers/handlers_routing.go`), built on `Manager.ProviderEnabledForRequest`
(`sdk/cliproxy/auth/provider_policy.go`). `util.ResolveAutoModelMatching` passes it to
`ModelRegistry.GetFirstAvailableModelMatching`
(`internal/registry/model_registry.go`), which counts only the available clients of
providers the profile leaves enabled. The execute path and the Responses websocket
pin matcher (`responsesWebsocketResolvedModel` in
`sdk/api/handlers/openai/openai_responses_websocket_session.go`) both use this filter.

The filter checks only `providers.<p>.disabled`, not the provider's `mode`. `auto`
can still pick a Claude or Codex model whose only available credentials the
profile's mode excludes, which then fails at selection. Examples: `mode: api` with an
`api-credential` that serves a different model, or `mode: subscription` when the
model's only available credentials are API keys. This gap predates the `disabled`
switch, because `auto` never read the provider mode.

Suggested fix: make the registry predicate per client instead of per provider (for
example `GetFirstAvailableModelMatching(handlerType, acceptClient func(clientID,
provider string) bool)`), and have the auth manager answer "would the profile's
policy select this auth" (`providerPolicyAllows`) for the client ID. Still log no
refusal.
