# rebind-on never keeps sessions bound to a credential that became ineligible

- found: 2026-11-10, plan: 20261110-provider-api-mode-and-fallback, phase: internal review
- severity: minor
- area: sdk/cliproxy/auth/selector.go

With `routing.session-affinity-rebind-on: never`, a session stays bound to its
credential when that credential is still registered but no longer passes eligibility.
This happens when a hot reload switches a key between `subscription` and `api` mode
or changes its `api-credential`, and also when a credential is disabled rather than
removed. Each request then fails with `session_auth_unavailable` (429, not retryable):

- regular cache-hit path: `selector.go:1384-1399`
- fallback-ID cache-hit path: `selector.go:1461-1466`
- LCP cache-hit path: `selector.go:1649`

`SessionCache.GetAndRefresh` (`session_cache.go:124`) renews the binding's TTL on every
hit, so a client that keeps retrying never gets past the stale binding. Bindings are
cleared only when a credential is removed (`invalidateSessionAffinity` at
`conductor_lifecycle.go:714`). The provider policy adds a new way to hit this: under
`mode: api` the fallback must be `off`, so nothing rescues those requests.
`TestProviderPolicyHotReloadMovesBoundSessions` (`provider_policy_test.go:376`) covers
only the default `rebind-on`.

Suggested direction: treat a bound credential that is registered but excluded by
eligibility (provider policy, disabled) like a removed one, and drop the binding even
under `never`. Rejecting it the way a cooldown is rejected keeps the binding alive.
Another option is to invalidate a profile's bindings when a config update changes its
effective `providers`. Add a `RebindOn: never` case to the hot-reload test.
