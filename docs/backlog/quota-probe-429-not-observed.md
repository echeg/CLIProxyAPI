# Quota probe 429 responses are neither observed nor throttled

- found: 2026-11-04, plan: 20261104-per-key-routing-profiles, phase: planning
- severity: minor
- area: internal/api/handlers/management/api_tools_quota.go

`observeAPICallQuota` (`internal/api/handlers/management/api_tools_quota.go:20-36`) returns
early for every non-2xx probe response, so a `429 Rate limited` from the Anthropic
`/api/oauth/usage` or the Codex usage endpoint leaves no trace on the credential: no
observation, no `Retry-After` memory, no cooldown of the probe itself. The panel refreshes
every enabled account in parallel on page entry and on manual refresh, including accounts
that are cooling down, so a rate-limited usage endpoint keeps being probed and the quota
cards stay red (observed by the user on the live panel on 2026-11-04). The file contains no
`Retry-After` or 429 handling at all.

Suggested direction: in `observeAPICallQuota` record a 429 probe result on the auth
(status, `Retry-After` or a short default) and have `APICallV8` short-circuit a repeated
probe for that credential with the cached status until the retry time passes, returning the
same 429 payload so the panel shows the real reason. Keep the main request cooldown separate:
a probe 429 says nothing about the generation quota. Add table tests with `nowFunc` for the
short-circuit window and for a successful probe clearing it.
