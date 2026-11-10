# v8 Management API

The base path is `/v8/management`. Configuration endpoints follow the structure in
[config.example.yaml](../config.example.yaml); operational endpoints are grouped
by function. The existing `/v0/management` API remains supported. Its reference is
available in the [management API guide](https://help.router-for.me/management/api).

## Access

The v8 endpoints use the same management key and remote-access policy as v0.
Send `Authorization: Bearer <management-key>` or `X-Management-Key: <management-key>`.
OAuth callbacks validate a pending login state and do not require a management-key
header. Home mode disables local management endpoints for both versions.

## Configuration endpoints

All paths below are relative to `/v8/management`.

| Path | Methods | Description |
| --- | --- | --- |
| `/config` | GET, PUT, PATCH | Read, replace, or merge the complete configuration as JSON. |
| `/config.yaml` | GET, PUT | Read or replace the complete configuration as YAML. |
| `/config/<section>/<field>` | GET, PUT, PATCH, DELETE | Read or modify a section or field using its path in the v8 tree. |

GET renders the persisted configuration in the v8 layout without migrating the
file or adding omitted runtime defaults. Successful v8 configuration writes save
the latest layout; reads and rejected writes leave the file unchanged.
Historical v8 paths and request bodies remain accepted as aliases.

JSON writes accept the value directly, without a `{ "value": ... }` envelope.
PUT replaces its target. PATCH merges objects and replaces lists and scalars.
Scalar field updates retain existing comments; complete replacements use the submitted document.
DELETE removes a field. Paths identify mapping keys, not array indexes; replace
an entire list to change its entries. Optional per-key `null` overrides inherit
the corresponding group value. Legacy field names are rejected by v8 writes.

| Example path | Value or purpose |
| --- | --- |
| `/config/access/api-keys` | Client authentication keys, for example `["client-key"]`. |
| `/config/api-keys` | All upstream provider groups. |
| `/config/api-keys/codex` | Codex upstream groups. |
| `/config/client/codex/optimize-multi-agent-v2` | Boolean, default `false`; applies to Codex clients across OAuth and API-key routes. |
| `/config/oauth/providers/codex/fast-mode` | Boolean, default `false`; requests the priority service tier (Codex Fast) for subscription requests without a tier on models whose Codex catalog advertises it. API-key routes and payload rules are unaffected. |
| `/config/observability/logs/debug` | A boolean, for example `true`. |
| `/config/routing/retry/request-retry` | A number, for example `0`. |
| `/config/plugins/configs/<id>` | A plugin configuration object. |

For example, `PATCH /v8/management/config` with this body disables additional
request retries while preserving other settings:

```json
{
  "routing": {
    "retry": {
      "request-retry": 0
    }
  }
}
```

JSON reads omit TURN usernames and credentials under
`oauth.providers.codex.live-media-relay.ice-servers`; YAML reads include them.
The Home-owned revision fields
`credentials.concurrency.lifecycle-config-revision`,
`credentials.concurrency.observation-barrier-revision`, and `plugins.auth-revision`
cannot be changed through these endpoints.

### Subscription priority and session affinity

To prefer Codex and Claude accounts whose subscription quotas reset soonest,
send `PATCH /v8/management/config/routing` with:

```json
{
  "strategy": "earliest-reset",
  "session-affinity": true,
  "session-affinity-ttl": "24h"
}
```

Without an explicit `routing.selection`, `strategy` only feeds the derived
selection `{primary: reserves, tiebreak: <strategy>}` (see
[Per-key routing profiles and selection pipeline](#per-key-routing-profiles-and-selection-pipeline)).
For `earliest-reset` the selector first applies availability, model eligibility,
and quota reserves (see below) across every credential priority tier. It then
chooses the earliest future reset of a shared subscription window with remaining
quota: Codex primary and secondary windows, or Claude five-hour and seven-day
windows. Manual credential priority applies only when `priority` is one of the
selection slots, for example `{primary: priority, tiebreak: earliest-reset}`.
Equal reset times rotate round-robin. Unknown, malformed, exhausted, and expired
observations do not provide a ranking; credentials without a usable reset are
skipped only while another candidate has one, and when no candidate has one the
selector uses round-robin. Other providers use the same round-robin fallback.
Model-specific additional limits, code-review limits, and paid overage do not
influence this strategy. Existing quota cooldowns still govern availability.

Established sessions keep their credential even when another account gains an
earlier reset or higher manual priority. An unavailable, disabled, or removed
credential triggers failover. The affinity TTL measures inactivity and refreshes
on access; bindings are held in memory and are lost on restart, when session
affinity is switched on or off for every profile at once, or when
`session-affinity-ttl` or `session-affinity-subagents` changes. Strategy,
selection, and preference changes keep them. Stable client session IDs give the most reliable affinity;
content-based inference remains available for clients without them. Affinity
preserves the account choice but cannot guarantee an upstream cache hit.

To prefer a particular subscription for new threads, use its `auth_index` from
`GET /v8/management/credentials`:

```json
{
  "preferred-accounts": {
    "codex": "codex-account-index",
    "claude": "claude-account-index"
  }
}
```

Send this body to `PATCH /v8/management/config/routing`. A usable preferred
account takes precedence over automatic ordering, numeric credential priority,
and soft quota reserve levels: it is taken even at soft levels 1-3. Existing
session bindings remain intact when preferences change or are cleared.
Unavailable, disabled, removed, model-ineligible, or hard-reserved preferred
accounts fall back to the configured selection. With affinity disabled,
preferences apply to each request. To restore automatic selection for one
provider, patch its value to `null`; to clear both, patch `preferred-accounts` to
`null`. Other providers are not supported by this setting. These fields form the
shared profile; a client key can replace them with its own profile. Home and
plugin schedulers remain authoritative.

`GET /v8/management/routing/activity` returns
`{"accounts":[{"auth_index":"...","provider":"codex","last_selected_at":"2026-10-02T12:00:00Z"}]}`.
It records actual credential selections in memory, resets on restart, and excludes
removed credentials. An empty `accounts` array means no selections have been
recorded. These timestamps describe the most recent selections, not in-flight
requests or a single globally active subscription: concurrent threads can use
different accounts. Quota refresh probes do not update this activity.

Quota observations come from normal upstream responses and successful Codex or
Claude usage probes through `POST /v8/management/requests/api-call`. Refresh quotas
in CPAMC after adding accounts or restarting to initialize accounts that have
not yet served requests. Probes update observations without clearing cooldowns or
counting as model requests. No background token consumption is generated: the
strategy routes incoming work. It does not initiate traffic to spend a balance.

For a local CPAMC build, copy its generated `dist/index.html` to the server's
`static/management.html` and set `management.disable-auto-update-panel: true`
to retain the custom panel. The source checkout can remain alongside CLIProxyAPI;
it has its own build and version control. This strategy is implemented for
standalone CLIProxyAPI. Home mode uses a separate control-plane selector and
disables these local management endpoints. CLIProxyAPIHome needs corresponding
selector, quota observation, and configuration support before using this strategy
in a cluster.

### Claude -> Codex subscription fallback

`routing.providers.claude.fallback` with `target: codex` sends selected Claude
models to a Codex subscription:

```yaml
routing:
  providers:
    claude:
      fallback:
        target: codex       # off (default), codex, or api
        mode: auto          # auto (default) or always
        mappings:
          - from: "claude-opus-*"
            to: "gpt-6-astra"
```

The legacy spelling `routing.claude-fallback: {enabled, mode, mappings}` is
still accepted in the shared profile and in key profiles. `enabled: true`
means `target: codex` and `enabled: false` means `target: off`; `mode` and
`mappings` are kept. When a profile carries both blocks,
`providers.claude.fallback` wins and the legacy block is ignored with a
warning. A save writes `providers` and drops the legacy block of that profile.
See [Per-provider mode and API fallback](#per-provider-mode-and-api-fallback)
for the `api` target.

- A `from` that matches the model exactly wins; otherwise the longest matching
  trailing-`*` prefix wins. Unmapped models stay on Claude. A thinking suffix
  such as `(high)` is kept on the target model.
- `auto` tries Claude first and switches to Codex after a failed Claude round
  instead of waiting out Claude cooldowns. It reacts to 401, 403, 408, 429, and
  5xx responses, to no available Claude credential, and to transient connection
  failures. It never reacts to invalid requests or a canceled client request.
- `always` sends mapped models straight to Codex.
- The Codex leg uses Codex OAuth credentials only. Pinned requests and requests
  that require a non-OAuth credential are never rerouted.

`GET /v8/management/routing/claude-fallback` returns
`{"supported": bool, "settings": {...}, "models": {"claude": [...], "codex": [...]}}`
with `Cache-Control: no-store`. `settings` is the legacy
`{enabled, mode, mappings}` view derived from the shared
`providers.claude.fallback`; `enabled` is `true` only for `target: codex`.
`models` lists the model IDs of enabled Claude credentials and Codex OAuth
credentials. `supported` is `false` in Home mode and with a scheduler plugin.
Write the settings through `PATCH /v8/management/config`. A key profile can
carry its own `providers` block (see below).

### Per-provider mode and API fallback

> **Default change.** Upstream API keys of Claude and Codex
> (`api-keys.claude`, `api-keys.codex`) no longer join the primary selection
> pool. Without a `providers` block every profile runs both providers in
> `subscription` mode, which selects OAuth subscriptions only. To use an API
> key, name it in `providers.<provider>.api-credential` and set
> `mode: api`, or set `fallback.target: api` to use it only after the
> subscriptions fail. With no subscription credential at all, a request fails
> with `auth_not_found` and the message
> `no subscription credentials for claude; set routing providers.claude.mode: api`.

Every routing profile (the shared `routing.*` profile and each
`routing.key-profiles.<key>`) can carry a `providers` block with a `claude` and a
`codex` sub-block:

```yaml
routing:
  providers:
    claude:
      mode: subscription               # subscription (default) or api
      api-credential: "<auth_index>"   # required for mode: api or fallback.target: api
      fallback:
        target: codex                  # off (default), codex, or api
        mode: auto                     # auto or always; target codex only
        mappings: [{from: "claude-opus-*", to: "gpt-6-astra"}]  # target codex only
    codex:
      mode: subscription
      api-credential: "<auth_index>"
      fallback:
        target: api                    # off or api
  key-profiles:
    "client-key-a":
      providers:
        claude: {mode: api, api-credential: "<auth_index>"}
        codex: {api-credential: "<auth_index>", fallback: {target: api}}
```

`api-credential` is the `auth_index` of an `api-keys.claude` or `api-keys.codex`
entry of the same provider; take it from `api_credentials` in
`GET /v8/management/routing/profiles`. A key profile replaces the shared profile
completely: a key profile without `providers` runs both providers in
`subscription` mode with fallback `off`.

#### Validation

Invalid settings fail the load and return `422 invalid_config` on a write. Key
profile paths use the 12-hex key hash, for example
`routing.key-profiles.3f2a9c1b7d4e.providers.claude.api-credential`.

- `mode` must be `subscription` or `api`; `fallback.target` must be `off`,
  `codex`, or `api`. Values are trimmed and lower-cased.
- `mode: api` or `fallback.target: api` without `api-credential` is an error.
- `codex.fallback.target: codex` is an error.
- `mode: api` with a `fallback.target` other than `off` is an error.
- `target: codex` keeps the Claude -> Codex rules: at least one mapping, a `*`
  only at the end of `from`, no duplicate `from`.
- `fallback.mode` and `mappings` under `target: api` are kept but ignored; the
  load logs a warning.
- An `api-credential` that matches no enabled API-key credential of that provider
  is not an error, because the key may be added later. A successful
  configuration write logs one warning per such profile with the fields
  `profile` (`shared` or the key hash) and `claude_api_credential` /
  `codex_api_credential` (the `auth_index`).

#### Runtime rules

- **Primary leg.** Each candidate credential is filtered by the mode of its own
  provider, so a request that selects from a mixed Claude and Codex pool applies
  both modes. `subscription` excludes API-key credentials; `api` allows only the
  API-key credential whose `auth_index` equals `api-credential`. Other providers
  are not affected.
- **`mode: api`.** Subscriptions are skipped. Session affinity works as usual
  within the one credential. A cooling-down key returns its cooldown error with
  `Retry-After`; no other API key is ever substituted. A missing or disabled
  credential returns `auth_not_found` with
  `no enabled claude API key matches routing providers.claude.api-credential "<auth_index>"`.
- **`fallback.target: api`.** After a failed primary round the same model runs
  again on the same provider with only the profile's API key. The triggers match
  the Codex fallback in `auto` mode: 401, 403, 408, 429, and 5xx responses, no
  available credential, and transient connection failures; never invalid
  requests, a canceled client request, or a request stop. `fallback.mode` does
  not apply: the API target is always `auto`.
- The API leg neither reads nor writes session bindings, so the next request of
  the session starts from the subscriptions again.
- When the API key cannot be selected (cooldown, disabled, removed, unknown
  index), the client gets the primary error with its status and `Retry-After`.
- Fallbacks do not chain: a fallback leg never falls back again. Streaming
  requests fall back only before the response starts.
- A request that can use both providers checks the `claude` fallback first.
- A completed primary round goes to the fallback instead of waiting for a
  cooldown, for both targets.
- The policy is skipped for executions pinned to a credential (Codex websocket
  continuations) and for requests that require their own credential kind or
  policy (the Codex leg of the Claude -> Codex fallback, Codex Alpha Search with
  an API key).
- Home mode and scheduler plugins keep their own selection: the provider policy
  and both fallback targets do not apply there. CLIProxyAPIHome does not
  implement the provider policy, so a cluster deployment needs matching selector
  support in Home before using these settings.

#### Logging

`Routing profile selection` lines (see
[Home mode, plugins, and logging](#home-mode-plugins-and-logging)) report
`routing_stage=api` for a credential selected under `mode: api`. Inside a
fallback leg `routing_stage` is `fallback`, `fallback_stage` records the stage
that chose the credential, and `fallback_target` is `codex` or `api`. The
fallback itself logs `Routing claude request to API key fallback` (fields
`provider`, `source_model`, `fallback_target`, `primary_status`) or
`Routing Claude request to Codex fallback` (fields `source_model`,
`fallback_model`, `fallback_mode`, `fallback_target`, `primary_status`). The
configuration diff reports `routing.providers: updated` for the shared block and
`updated (redacted)` for key profiles; raw keys and API keys are never logged.

### Per-key routing profiles and selection pipeline

Every client key in `access.api-keys` can have its own routing profile. A profile
has five stages, applied in this order to each request:

1. **Provider mode** (`providers.<provider>.mode`): `subscription` keeps only
   Claude/Codex subscriptions in the candidate pool, `api` keeps only the
   profile's `api-credential` (see
   [Per-provider mode and API fallback](#per-provider-mode-and-api-fallback)).
2. **Sticky session** (`session-affinity`): a session already bound to a
   credential keeps it while it stays usable.
3. **Preferred subscription** (`preferred-accounts`): for a new selection, the
   preferred `claude` or `codex` credential is taken when it is usable.
4. **Pool strategy** (`selection`): `primary`, then `tiebreak`, choose among the
   remaining credentials.
5. **Fallback** (`providers.<provider>.fallback`): after a failed primary round,
   the `codex` target runs stages 1-4 again on Codex with the same key's Codex
   preference and selection; the `api` target runs the same model again with only
   the profile's API key and without session affinity.

A key without a profile uses the shared profile: the global
`routing.session-affinity`, `routing.preferred-accounts`, `routing.selection`, and
`routing.providers` fields (or the legacy `routing.claude-fallback`). A key with a
profile uses it completely. Nothing is inherited from the shared fields: an
omitted block means off or empty, an omitted `providers` means `subscription`
mode with fallback `off`, and an omitted `selection` means
`{primary: reserves, tiebreak: round-robin}`.

```yaml
routing:
  session-affinity: true
  session-affinity-ttl: 24h
  preferred-accounts: {claude: "claude-account-index", codex: "codex-account-index"}
  selection: {primary: reserves, tiebreak: earliest-reset}
  providers:
    claude: {fallback: {target: codex, mode: auto, mappings: [{from: "claude-opus-*", to: "gpt-6-astra"}]}}
  key-profiles:
    "client-key-a":
      session-affinity: false
      preferred-accounts: {claude: "another-claude-index"}
      selection: {primary: priority, tiebreak: round-robin}
      providers:
        claude: {api-credential: "claude-api-key-index", fallback: {target: api}}
        codex: {mode: api, api-credential: "codex-api-key-index"}
```

`key-profiles` is keyed by the exact client key. `session-affinity-ttl` and
`session-affinity-subagents` stay global and apply to every key.

#### Selection slots

`primary` and `tiebreak` accept the same values. Narrowing criteria keep the best
group of candidates; pickers choose one credential.

| Value | Kind | Keeps or chooses |
| --- | --- | --- |
| `priority` | narrowing | The highest manual credential `priority`. |
| `reserves` | narrowing | The lowest soft quota reserve level (see [Quota reserve](#quota-reserve-for-shared-subscriptions)). |
| `earliest-reset` | narrowing | The earliest future reset of a subscription window with remaining quota. |
| `most-remaining` | narrowing | The highest remaining percentage, taking each credential's most constrained window. |
| `round-robin` | picker | Rotates through the group. |
| `weighted-round-robin` | picker | Rotates by credential `weight`; credentials with weight 0 are never chosen. |
| `fill-first` | picker | Uses the first credential until it becomes unavailable. |

`earliest-reset` and `most-remaining` skip credentials without usable quota data
only while another candidate has data; otherwise every candidate stays. A picker
as `primary` makes `tiebreak` inactive. When both slots narrow, or `tiebreak` is
empty, round-robin chooses among the remaining ties. A `tiebreak` without a
`primary` is invalid.

A new selection runs this pipeline per provider:

```text
available = every priority tier; allowed by the provider mode; not disabled,
            cooling down, model-ineligible, or under an active hard quota reserve
if the profile's preferred credential for the provider is available: use it
group = available
for slot in [primary, tiebreak]:
    narrowing slot: keep the best group
    picker slot:    choose one credential from the group and stop
otherwise round-robin chooses from the group
```

The preferred credential is checked before any soft reserve level, so a key keeps
consuming its assigned subscription at soft levels 1-3. Only a hard reserve,
cooldown, disabled status, model ineligibility, or removal moves it to the pool.
Soft levels rank only the remaining pool and only when `reserves` is one of the
slots. Manual credential priority applies only when `priority` is one of the slots.
Executions pinned to a credential, such as Codex websocket continuations, keep
their credential and ignore the provider mode, preferences, reserves, and
selection.

#### Legacy configuration

Without `routing.selection`, the shared selection is derived from `strategy`:
`{primary: reserves, tiebreak: <strategy>}`, where `strategy` is `round-robin`
(the default), `weighted-round-robin`, `fill-first`, or `earliest-reset`. A
configuration without `selection` and `key-profiles` keeps its previous behavior
with three differences:

- The manual Priority tier no longer applies implicitly; choose `priority` as a
  slot to keep it, for example `{primary: reserves, tiebreak: priority}` keeps
  the old reserves-then-priority narrowing before round-robin.
- Under `earliest-reset`, equal reset times rotate round-robin instead of always
  choosing the lowest credential ID.
- A shared preferred account is taken at soft reserve levels 1-3 instead of
  counting as unavailable.

Changing `strategy`, `selection`, or preferences takes effect on the next request
and keeps the round-robin position and session bindings.

#### Session affinity per key

The session affinity layer exists while the shared `session-affinity` or any key
profile's `session-affinity` is true. Each request then follows its own profile.
A key with `session-affinity: false` neither reads nor writes bindings, even when
another key binds sessions. Turning affinity on or off for every profile at once,
for example when the last sticky key profile switches off, rebuilds the selector
and drops every existing binding. Edits that keep some profile sticky keep the
bindings. A cold selection for a sticky key runs the full pipeline, including the
key's preferred account. Bindings of a key with its own profile are kept apart
from every other key, so a session identity that two clients happen to share,
such as a fixed `prompt_cache_key`, binds separately for each of them. Keys on
the shared profile share bindings as before.

#### Reading profiles

`GET /v8/management/routing/profiles` returns the shared profile, every key
profile in `access.api-keys` order, the Claude and Codex API-key credentials a
profile can name, and the accepted slot values. The response contains raw client
keys and is sent with `Cache-Control: no-store`.

```json
{
  "supported": true,
  "shared": {
    "session-affinity": true,
    "preferred-accounts": {"claude": "claude-account-index"},
    "selection": {"primary": "reserves", "tiebreak": "earliest-reset"},
    "claude-fallback": {"enabled": true, "mode": "auto", "mappings": [{"from": "claude-opus-*", "to": "gpt-6-astra"}]},
    "providers": {
      "claude": {"mode": "subscription", "api-credential": "", "fallback": {"target": "codex", "mode": "auto", "mappings": [{"from": "claude-opus-*", "to": "gpt-6-astra"}]}},
      "codex": {"mode": "subscription", "api-credential": "", "fallback": {"target": "off", "mode": "", "mappings": []}}
    }
  },
  "keys": [
    {
      "key": "client-key-a",
      "profile": {
        "session-affinity": false,
        "preferred-accounts": {"claude": "another-claude-index"},
        "selection": {"primary": "priority", "tiebreak": "round-robin"},
        "claude-fallback": {"enabled": false, "mode": "", "mappings": []},
        "providers": {
          "claude": {"mode": "subscription", "api-credential": "claude-api-key-index", "fallback": {"target": "api", "mode": "", "mappings": []}},
          "codex": {"mode": "api", "api-credential": "codex-api-key-index", "fallback": {"target": "off", "mode": "", "mappings": []}}
        }
      }
    }
  ],
  "api_credentials": {
    "claude": [{"auth_index": "claude-api-key-index", "label": "Anthropic", "base_url": "", "disabled": false}],
    "codex": [{"auth_index": "codex-api-key-index", "label": "OpenAI", "base_url": "https://api.openai.com/v1", "disabled": false}]
  },
  "selection_options": {
    "narrowing": ["priority", "reserves", "earliest-reset", "most-remaining"],
    "pickers": ["round-robin", "weighted-round-robin", "fill-first"]
  }
}
```

`shared.selection` and each key's `selection` are effective values: a derived
legacy selection or the key default is shown instead of an empty object.
`providers` is effective too: both sub-blocks are always present, defaults are
filled in, a legacy `claude-fallback` block is already converted, and `mappings`
is `[]` rather than `null`. `claude-fallback` is the legacy view derived from
`providers.claude.fallback` for older clients; its `enabled` is `true` only for
`target: codex`. `api_credentials` always has a `claude` and a `codex` list of
API-key credentials sorted by `auth_index`; `disabled` covers disabled
credentials, and the API key itself is never included.
`preferred-accounts` is omitted when empty. `supported` is `false` in Home mode
and with a scheduler plugin. Take subscription `auth_index` values from
`GET /v8/management/credentials` and fallback target models from
`GET /v8/management/routing/claude-fallback`.

#### Writing profiles

Profiles are written through the configuration endpoints. To set the shared
selection and a key profile, send `PATCH /v8/management/config`:

```json
{
  "routing": {
    "selection": {"primary": "most-remaining", "tiebreak": "weighted-round-robin"},
    "key-profiles": {
      "client-key-a": {
        "session-affinity": true,
        "preferred-accounts": {"claude": "claude-account-index"},
        "selection": {"primary": "earliest-reset"}
      }
    }
  }
}
```

PATCH merges objects, so other key profiles and the fields a profile body omits
stay unchanged. To replace one profile completely, send `null` for it and then
the new profile in two PATCH requests. Keep client keys in the request body:
a key used as a path segment, as in
`PUT /v8/management/config/routing/key-profiles/<key>`, is written to the request
log. To delete a profile, patch it to
`null`; the key then uses the shared profile and the entry is removed from the
file:

```json
{"routing": {"key-profiles": {"client-key-a": null}}}
```

To return to the derived legacy selection, patch `routing.selection` to `null`.

- A profile for a key missing from `access.api-keys` returns `400`
  `{"error": "unknown_api_key", "count": N}`. The body never contains the key, and
  the file is not changed. A `null` entry for an unknown key is accepted because
  it only deletes. An unchanged stale entry already on disk does not block other
  writes; it is dropped on save.
- An unsupported slot value, a `tiebreak` without a `primary`, a provider other
  than `claude` or `codex` in `preferred-accounts`, or an invalid `providers` or
  `claude-fallback` block inside a profile returns `422`
  `{"error": "invalid_config", "message": "..."}`. Messages identify a key profile
  by the first 12 hex characters of the key's SHA-256, for example
  `routing.key-profiles.3f2a9c1b7d4e.selection.primary`.
- On load and hot reload, profiles for keys missing from `access.api-keys` and
  `null` entries are dropped; a missing key is reported as a warning with its hash
  prefix.

Writes that still use the legacy `claude-fallback` block (older panels send it
with each profile and no `providers`) are merged into the stored `providers`
instead of replacing it, so an older client cannot wipe API settings. For the
shared profile and each key profile in the write that carries `claude-fallback`
and no `providers.claude.fallback`:

- The stored `providers` of that profile is kept; only
  `providers.claude.fallback` is updated from the legacy block.
- `enabled: true` sets `target: codex`. `enabled: false` sets `target: off`,
  unless the stored target is `api`, which is kept because the legacy view cannot
  express it. `mode` and `mappings` are copied in both cases.
- The legacy node is removed from the file once `providers.claude.fallback` is
  set, so the file carries one spelling per profile.
- A `PUT` of a whole profile that omits `providers` keeps the stored `providers`
  of that profile as well. YAML document writes (`/config.yaml`) are stored as
  sent.

```json
{"routing": {"key-profiles": {"client-key-a": {"providers": {"claude": {"api-credential": "claude-api-key-index", "fallback": {"target": "api"}}}}}}}
```

Profiles follow their key. Removing a key from `access.api-keys` through a
configuration write drops its profile from the runtime configuration and from
the file. To rotate a key through the configuration endpoints, send the new key
in `access.api-keys` and its profile under the new key in the same PATCH; the old
key's profile is dropped. The legacy `/v0/management/api-keys` list endpoints
reconcile profiles automatically: `PATCH` with `{"old": "...", "new": "..."}`,
or an index patch that replaces one key with a new value, moves the profile to
the new key; `DELETE`, a `PUT` that removes keys, and a patch that overwrites a
key with one already listed drop the profiles of keys no longer listed. A `PATCH`
that appends a new key leaves profiles unchanged. Saving always prunes
`routing.key-profiles` to the current profiles and removes the node when it is
empty, so a deleted, rotated, or nulled key does not remain in the file. The
configuration diff in the log reports `routing.selection` changes,
`routing.providers: updated`, and the `routing.key-profiles` count, or
`updated (redacted)`, never keys, credential indexes, or profile contents.

#### Home mode, plugins, and logging

Key profiles apply to native standalone routing only. Home mode resolves no
profile and keeps its control-plane selection; it also disables these management
endpoints. With a scheduler plugin, every request uses the shared profile, so
global preferences keep applying there, while key profiles, the provider mode,
and both fallback targets do not apply. CLIProxyAPIHome does not implement the
provider policy.

Each selection for a key with its own profile writes one info line:

```text
level=info msg="Routing profile selection" auth_index=claude-account-index provider=claude routing_key=3f2a9c1b7d4e routing_stage=preferred
```

`routing_key` is the first 12 hex characters of the key's SHA-256; the raw key is
never logged. `routing_stage` is `sticky` (a bound session reused its credential),
`preferred`, `pipeline`, or `api` (an API-key credential selected for a provider
in `mode: api`). Inside a fallback leg it is `fallback`, `fallback_stage` records
which of those stages chose the credential, and `fallback_target` is `codex` or
`api`. Requests that use the shared profile write no such line.

### Quota reserve for shared subscriptions

A Codex or Claude subscription can also serve tools that do not go through this
proxy. A quota reserve controls when new proxy sessions may use the last part of such a
subscription's quota. Store it in the credential JSON next to `priority`:

```json
{
  "quota_reserve": {"percent": 25, "mode": "soft"}
}
```

`percent` is an integer from 1 to 99. `mode` is `soft` (the default) or `hard`.
Each threshold applies to every subscription window: Codex primary and secondary,
or Claude five-hour and seven-day. The reserve is active when the remaining quota
(`100 - used`) in any window is below `percent`. It is not active when the
remaining quota equals `percent`. The reserve uses the latest quota observation,
a single upstream response or usage probe. A window whose reset time has passed
is ignored. A credential with no usable observation counts as available. Other
providers do not support a reserve. An invalid value in a credential file is
ignored with a warning, and the credential then has no reserve.

The reserve affects only new selections:

- `hard` excludes the credential.
- `soft` assigns a reserve level; level 0 means no active reserve. A usable
  preferred account is taken first at any soft level. Otherwise the `reserves`
  selection slot keeps the lowest available level across all credential priority
  tiers, and the other slot chooses within it; the derived legacy selection
  `{reserves, <strategy>}` applies the levels first. A selection without
  `reserves`, such as `{priority, round-robin}`, ignores soft levels. The deepest
  soft level remains eligible as the last resort.
- When every candidate is excluded by a hard reserve or is cooling down, the
  request gets the usual model-cooldown `429`. Its `Retry-After` points to the
  earliest window reset that would make a candidate selectable again.

A session already bound by session affinity keeps its credential after the
reserve trips, so the session continues. Executions pinned to a credential, such
as Codex websocket continuations, also ignore the reserve. New sessions are bound
elsewhere unless the credential is the profile's preferred account under a soft
reserve. A preferred account under a `hard` reserve counts as unavailable, while a
`soft` reserve does not block it. The reserve is only a selection filter. It
never writes cooldown state.

The proxy does not send background probes. Subscription limits are account-wide,
so upstream rate-limit headers already include usage by other tools, and the next
proxied request refreshes a stale observation. Requests already in flight when
the reserve trips can still overshoot it. Refresh quotas in the CPAMP workbench to observe
idle accounts.

A soft policy can have two additional descending thresholds:

```json
{"quota_reserve": {"percent": 30, "mode": "soft", "steps": [15, 5]}}
```

Here `percent` is soft1, and `steps` supplies soft2 and soft3. Remaining quota at
least 30% is level 0; `[15, 30)` is soft1; `[5, 15)` is soft2; below 5% is soft3.
The highest level across usable windows is the account's level. Each additional
threshold is an integer from 1 to 99 and strictly lower than its predecessor.
A legacy single threshold remains a single soft1 level, even at very low remaining
quota. Hard mode cannot contain nonempty steps.

The credential-list response advertises `quota_reserve_max_levels: 3`. Clients
must check this before sending extra thresholds to an older CPA, which would
discard them. A staged policy must be replaced explicitly: `steps: []` collapses
it to one level, and `quota_reserve: null` removes the reserve. A replacement that
omits steps while a staged policy exists is rejected, except an unchanged
legacy policy can be treated as a no-op.

To set the reserve, send `PATCH /v8/management/credentials/fields`:

```json
{
  "name": "claude-account.json",
  "quota_reserve": {"percent": 25, "mode": "hard"}
}
```

`name` is the credential file name or ID. The stored value is normalized to
`{"percent": N, "mode": "soft"|"hard"}` with optional nonempty `steps`. Set `quota_reserve` to `null` to remove
it; this works for any credential. Invalid values, nested paths such as
`quota_reserve.percent`, and a non-null reserve on a credential other than Codex
or Claude return `400`.

Each entry in `GET /v8/management/credentials` includes:

| Field | Description |
| --- | --- |
| `quota_reserve` | The configured `percent`, `mode`, and optional `steps`. Omitted when no reserve is set. |
| `quota_reserve_level` | The current server-evaluated level from 0 to 3. An active hard reserve reports 1; inspect `mode` to distinguish it. |
| `quota_reserve_active` | `true` when the latest observation is below the reserve. Always present. |
| `quota_reserve_until` | RFC3339 time when the reserve stops applying: the latest reset among the windows below the reserve. Present only while active. A level may improve before this time when its most constrained window resets first. |

Scheduler plugins receive candidates with the reserve already applied: hard-reserved
credentials are removed, and only the lowest available soft level is supplied. The reserve is implemented for standalone CLIProxyAPI. Home mode uses its own
selector and disables these local management endpoints. CLIProxyAPIHome needs
matching reserve support before this works in a cluster.

### Weighted routing with session affinity

Set `routing.strategy` to `weighted-round-robin` to use per-credential integer
`weight` values (default `1`, maximum `1000000`; non-positive values are excluded).
With `routing.session-affinity` enabled, sticky requests, parent inheritance,
Guardian selections, and LCP matches also consume weighted credits without moving
existing bindings. New or reselected sessions favor under-served credentials
within the same eligible credential set. For example, with weights `3:1`, four
sticky requests to the second credential can be compensated by twelve new
requests to the first when both remain eligible.

Different priority tiers, transport preferences, and retry/cooldown candidate
sets keep separate credits; returning to a set resumes its retained credits.
Pinned requests account against the eligible pool before pinning, within the
pinned credential's priority tier and transport class, even when session affinity
is disabled. Pinned and ordinary requests share the same weighted credits. A
lower-priority binding never shifts the request ratio among higher-priority credentials.

Settled credits are capped at ±32 times the eligible pool total, so request
ratios are approximate rather than guaranteed. While reservations are in flight,
committed balances can temporarily exceed the cap; their pending offsets remain
intact until the pool settles, including when the last reservation rolls back. Sticky requests still use their binding at
the cap, but stop adding compensation when their full credit delta cannot fit.
New sessions continue repaying debt and earning credit, so excessive sticky
traffic does not starve smaller-weight peers.

Credits are local to the CPA process and reset on restart, selector replacement,
or bounded pool-cache eviction. Concurrent selections see pending credit; local
selection/binding success confirms it, while failure releases it. In Home mode,
scheduling and credit accounting remain owned by Home. With matching CPA and Home
versions, retained WebSocket responses (including Codex duplex successors) report
`home_selection.reused` through usage so Home can charge later turns without
redispatching or acquiring another concurrency slot. The scheduling model is
carried separately from the upstream model. First-turn primary usage does not
charge again. Split additional-model billing records (such as image usage) carry
`home_selection.additional` and never add scheduling charges, even when they have
independent execution IDs. Each reused execution is charged once through primary
usage; redelivery of that execution ID is deduplicated.
Compensation arrives after completion and uses Home's current eligible
pool and API-key permissions, so it remains approximate. Old peers ignore the
optional metadata; missing usage cannot compensate a reused turn.

### Codex multi-agent configuration migration

`client.codex.optimize-multi-agent-v2` is the sole runtime setting. Loading older
YAML accepts `providers.codex.optimize-multi-agent-v2`,
`oauth.providers.codex.optimize-multi-agent-v2`, and the flat
`codex.optimize-multi-agent-v2` path. The client path always wins conflicts,
including explicit `false` or `null`. If it is absent, the historical OAuth path
wins over `providers`, which wins over the flat path.

These aliases now have client-wide semantics, not OAuth-only scope. Loading does
not rewrite legacy-only settings; normalization removes aliases conflicting with
the client path. A successful v8 write migrates the aliases while preserving their
comments. Historical API paths remain usable. Home configuration publishers
need matching client schema support before emitting the new path; older YAML
payloads remain readable.

## Operational endpoints

All paths below are relative to `/v8/management`. Request and response bodies
retain the corresponding business operation's fields.

| Path | Methods | Description |
| --- | --- | --- |
| `/config/upstream/<provider>` | GET, PUT, PATCH, DELETE | Manage shared provider settings. |
| `/server/latest-version` | GET | Get latest release information. |
| `/requests/api-call` | POST | Make an authenticated upstream call. |
| `/routing/cooldown/reset` | POST | Clear credential cooldown. |
| `/routing/model-definitions/<channel>` | GET | Get model definitions. |
| `/routing/activity` | GET | Get each credential's latest actual selection time. |
| `/routing/profiles` | GET | Get the shared and per-key routing profiles (including effective `providers`), the Claude/Codex API-key credentials, and the selection slot values. |
| `/routing/claude-fallback` | GET | Get the legacy view of the shared Claude -> Codex fallback settings, support status, and available Claude/Codex model IDs. |
| `/observability/logs` | GET, DELETE | Read or clear application logs. |
| `/observability/logs/errors` | GET | List error-log files. |
| `/observability/logs/errors/<name>` | GET | Download an error-log file. |
| `/observability/logs/requests/<id>` | GET | Get a request log. |
| `/observability/logs/storage` | GET | Get logs directory disk usage and the related logging settings. |
| `/observability/usage/api-keys` | GET | Get API-key usage. |
| `/observability/usage/queue` | GET | Get queued usage events. |
| `/credentials` | GET, POST, DELETE | List, upload, or delete credential files. |
| `/credentials/models` | GET | Get credential models. |
| `/credentials/download` | GET | Download a credential file. |
| `/credentials/status` | PATCH | Change credential status. |
| `/credentials/fields` | PATCH | Change credential fields. |
| `/credentials/refresh` | POST | Refresh credentials. |
| `/credentials/verify` | POST | Run an operator-triggered, account-pinned inference verification using an isolated session and minimal 1-token prompt. |
| `/oauth/import?provider=vertex` | POST | Import a Vertex service account using a multipart `file` upload. |
| `/oauth/auth-url?provider=<provider>` | GET, POST | Start built-in or plugin OAuth (supports `name`/`auth_index` for targeted credential recovery and optional `verify` opt-in). |
| `/oauth/siwc/bind` | POST | Start SIWC binding to an existing file-backed Codex OAuth credential. |
| `/oauth/siwc/status` | GET | Read local SIWC credential status (`off`, `valid`, or `invalid`). |
| `/oauth/status?state=<state>` | GET | Get login status. |
| `/oauth/session?state=<state>` | DELETE | Cancel a login session. |
| `/oauth/callback` | GET, POST | Submit an OAuth callback. |
| `/plugins` | GET | List installed plugins. |
| `/plugins/<id>` | DELETE | Delete a plugin. |
| `/plugins/store` | GET | List the plugin store (supports optional `?refresh=true` to bypass cached release checks). |
| `/plugins/store/<id>/install` | POST | Install or update a plugin. |
| `/plugins/<id>/quota` | GET, POST, DELETE | Read, fetch, or reset plugin quota. |

### Logs directory storage

`GET /observability/logs/storage` scans the logs directory that the request
logger, `main.log`, and the `logs-max-total-size-mb` cleaner share. It works
whether or not `logging-to-file` is enabled. The directory is `$WRITABLE_PATH/logs`
when `WRITABLE_PATH` is set, otherwise `logs` under the process working directory
when that directory exists and is writable, otherwise `<auth-dir>/logs`. It is not
resolved against the config file's directory, and the response does not include
the path. Example response:

```json
{
  "total_bytes": 3456789012,
  "total_files": 18234,
  "capped_bytes": 3401234567,
  "capped_files": 18220,
  "oldest_modified_at": "2026-11-05T08:00:00Z",
  "newest_modified_at": "2026-11-05T12:56:16Z",
  "limit_mb": 10000,
  "request_log": true,
  "logging_to_file": true
}
```

| Field | Description |
| --- | --- |
| `total_bytes`, `total_files` | Every regular file in the directory tree, including subdirectories. Symlinks are not counted. |
| `capped_bytes`, `capped_files` | Top-level `*.log` and `*.log.gz` files: the set `logs-max-total-size-mb` is enforced against. It includes the protected `main.log`. |
| `oldest_modified_at`, `newest_modified_at` | Oldest and newest modification time of the counted files, in UTC RFC 3339. `null` when no file was counted. |
| `limit_mb` | Runtime `logs-max-total-size-mb`, with defaults applied; `0` means no cap. |
| `request_log` | Whether request logging is on: `request-log` with defaults applied, and `false` when `commercial-mode` is enabled. |
| `logging_to_file` | Runtime `logging-to-file`, with defaults applied. |

- Bytes are logical file lengths, the same values the cleaner sums. They are
  not allocated disk blocks, so they can differ from `du`.
- A missing logs directory returns 200 with zero counts and `null` times.
- The scan is not atomic. While logging continues, files can be written or
  removed during the scan, so two calls can return different values.
- Entries below the logs directory that cannot be read are skipped. A logs
  directory path that cannot be read, or is not a directory, returns 500 with
  an `error` field.

## OAuth

The login URL is shared by all providers. Set the required `provider` query
parameter to `claude`, `codex`, `antigravity`, `kimi`, `kimi-ai`, `xai`, `devin`,
`meta`, or a registered plugin provider ID. For example:

```http
GET /v8/management/oauth/auth-url?provider=codex
Authorization: Bearer <management-key>
```

Keep any provider-specific login parameters in the query string. To target recovery
at a specific existing credential, pass `name` (or `auth_index`) via query parameter
or POST JSON body; authenticating a different account will be rejected rather than
overwriting or clearing the target credential's recovery state. By default, post-login
inference verification is not executed automatically to avoid unexpected token
consumption; operators may explicitly opt in by passing `verify: true` (and optional
`verify_model`) when starting the targeted login, or trigger verification explicitly
after adoption via `POST /v8/management/credentials/verify` (`{"name":"<file>","model":"<optional-model>"}`).
The login response includes the authorization URL and a session `state` (plus `target_auth_id`
when targeted). Use that state for status queries and cancellation.

Callbacks accept `provider`, `state`, `code`, and `error` as GET query parameters
or POST JSON fields. POST also accepts `redirect_url` containing the complete
callback URL. If `provider` is omitted, it is inferred from the pending state;
an explicit provider must match that state. Poll until the status is `ok` or
`error`; `wait` means the login is still pending. Callback acceptance alone does
not mean credential exchange and persistence have completed.

Import uses `POST /v8/management/oauth/import?provider=vertex`. The `provider`
query parameter is required; Vertex is currently the supported import provider.

### Sign in with ChatGPT (SIWC) binding

SIWC is a second credential partition of an existing Codex credential, not a new
provider. This flow is available only in standalone CPA with local file-backed
Codex OAuth credentials. Home mode and non-file storage backends are not supported.

Start a binding with management authentication:

```http
POST /v8/management/oauth/siwc/bind
Authorization: Bearer <management-key>
Content-Type: application/json

{"name":"codex-example.json"}
```

`name` identifies an existing managed credential by its ID or file name; it is not
an arbitrary filesystem path. The credential must contain nonempty outer Codex
access and refresh tokens. A successful start returns:

```json
{
  "status": "ok",
  "url": "https://auth.openai.com/api/accounts/authorize?...",
  "state": "<random-state>",
  "flow": "siwc_bind",
  "expires_in": 300
}
```

Opening the returned URL starts authorization. Its redirect URI is fixed to
`http://127.0.0.1:1455/auth/callback`. This endpoint does not start a listener on
port 1455: for remote deployments, paste the complete URL from the browser address
bar, even if the loopback page cannot be reached, and submit it to the server:

```http
POST /v8/management/oauth/callback
Content-Type: application/json

{"provider":"codex","redirect_url":"http://127.0.0.1:1455/auth/callback?state=<state>&code=<code>&client_id=<issued-client-id>"}
```

The v8 callback also accepts `state`, `code`, and `client_id` directly as POST JSON
fields or GET query parameters. SIWC requires the **issued** `client_id` from the
callback; the authorization URL's `dynamic_agent_client` is not a substitute.
Error callbacks can supply `error` instead of `code` and `client_id`. Conflicting
parameters are rejected. The target, PKCE verifier, device ID, and redirect URI
are retained on the server and cannot be changed by a callback. Callback values
are handed directly to the SIWC session rather than written to legacy callback
files. Neither callbacks nor status responses return tokens.

Poll `GET /oauth/status?state=<state>` until `ok` or `error`. `wait` means binding
has not completed, including while tokens are being exchanged. Success means the
new `siwc` partition and `use_siwc=true` have been saved and published to the auth
manager. Other credential fields are retained. Rebinding explicitly replaces the
SIWC partition and reuses a valid existing device ID.

`DELETE /oauth/session?state=<state>` cancels a pending binding. Cancellation that
wins the commit gate prevents saving a late token response; cancellation after
commit returns `cancelled:false`. Duplicate callbacks and a second pending binding
for the same credential return `409`. Missing targets return `404`; ineligible
targets and incomplete callbacks return `400`. Deleting or replacing the target
through the v8 credential API invalidates its pending binding. An old server
returns `404` for the start endpoint; clients must not fall back to ordinary Codex
login. Sessions are process-local, expire after five minutes, and do not survive
restart; deployments with multiple independent instances require session affinity.

Query local SIWC status for a Codex credential with `GET /v8/management/oauth/siwc/status?name=<name>` (or `?auth_index=<auth_index>`). The endpoint reads local credentials and runtime rejection state without triggering refresh or upstream verification requests, and returns `{"status": "off" | "valid" | "invalid"}`.

For Codex entries, `GET /credentials` includes these non-secret summary fields:

| Field | Type | Meaning |
| --- | --- | --- |
| `siwc_bind_supported` | boolean | This credential is eligible for the binding flow. Missing means unsupported. |
| `use_siwc` | boolean | Requests are configured to use the nested SIWC credential. |
| `siwc_status` | string | Local SIWC status: `off`, `valid`, or `invalid`. |
| `siwc_bound` | boolean | SIWC access token, refresh token, and issued client ID are present; not a live validity check. |
| `siwc_expires_at` | optional RFC3339 string | Stored SIWC expiration, when available. |
| `access_token_expires_at` | optional RFC3339 string | Active access token expiration deadline, when available. |
| `next_refresh_after` | optional RFC3339 string | Next scheduled automatic refresh check, when in the future. |
| `requires_sign_in` | boolean | Automatic renewal failed with a terminal grant rejection and independent sign-in is required. |
| `renewal_state` | string | Renewal lifecycle state: `sign_in_required`, `expired_sign_in_required`, `retry_scheduled`, `awaiting_first_renewal`, or `renewal_verified`. |
| `recovery_stage` | string | Post-login runtime adoption and verification stage: `sign_in_required`, `quota_blocked`, `loaded`, `inference_verified`, or `renewal_verified`. |
| `adopted` | boolean | Whether the current credential version is loaded in the running auth manager. |
| `inference_verified` | boolean | Whether the current credential version has completed an inference response. |
| `renewal_verified` | boolean | Whether the current credential version has completed an automatic refresh cycle. |
| `refresh_error` | optional object | Sanitized last refresh failure (`code`, `message`, `http_status`) with no tokens, codes, or URLs. |
| `next_action` | optional string | Required operator recovery action (`sign_in` when `requires_sign_in` is true). |
| `recovery_hint` | optional string | Operator guidance explaining instance-scoped OAuth recovery when `requires_sign_in` is true. |
| `renewal_event` | optional object | Deduplicated structured event (`renewal_sign_in_required`) for terminal renewal failures. |

The v0 callback and credential-list contracts are unchanged. SIWC activation,
unbinding, and standalone SIWC credential creation are not separate management
operations in this release.

## v0 compatibility

The `/v0/management` endpoints keep their original paths and payloads, including
provider-specific OAuth login URLs, callbacks, status queries, and cancellation.
For example, `GET /v0/management/codex-auth-url` and
`GET /v8/management/oauth/auth-url?provider=codex` start the same login flow.

Legacy flat configuration endpoints such as `/debug`, `/request-retry`, and
`/codex-api-key` exist under `/v0/management` only.
`/v0/management/api-keys` continues to manage client authentication keys; the v8
equivalent is `/v8/management/config/access/api-keys`.

Legacy-only configuration files keep their layout until a successful v8
configuration write. When both layouts specify a field, the new field takes
precedence and its legacy equivalent is removed. V0 setters keep legacy-only
files in their original layout; existing v8 files are saved in the latest v8
layout. `PUT /v0/management/config.yaml` still replaces the complete file and
accepts legacy, new, or mixed layouts, normalizing v8 documents on save.

Plugin OAuth uses the shared v8 login endpoint. Other plugin-defined HTTP
extensions retain their declared `/v0/management` routes.
