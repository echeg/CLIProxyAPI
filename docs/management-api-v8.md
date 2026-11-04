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
file or adding omitted runtime defaults. A successful v8 configuration write
migrates legacy settings; reads and rejected writes leave the file unchanged.

JSON writes accept the value directly, without a `{ "value": ... }` envelope.
PUT replaces its target. PATCH merges objects and replaces lists and scalars.
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

`routing.claude-fallback` sends selected Claude models to a Codex subscription:

```yaml
routing:
  claude-fallback:
    enabled: true
    mode: auto          # auto (default) or always
    mappings:
      - from: "claude-opus-*"
        to: "gpt-6-astra"
```

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
with `Cache-Control: no-store`. `models` lists the model IDs of enabled Claude
credentials and Codex OAuth credentials. `supported` is `false` in Home mode and
with a scheduler plugin. Write the settings through
`PATCH /v8/management/config`. A key profile can carry its own
`claude-fallback` block (see below).

### Per-key routing profiles and selection pipeline

Every client key in `access.api-keys` can have its own routing profile. A profile
has four stages, applied in this order to each request:

1. **Sticky session** (`session-affinity`): a session already bound to a
   credential keeps it while it stays usable.
2. **Preferred subscription** (`preferred-accounts`): for a new selection, the
   preferred `claude` or `codex` credential is taken when it is usable.
3. **Pool strategy** (`selection`): `primary`, then `tiebreak`, choose among the
   remaining credentials.
4. **Claude -> Codex fallback** (`claude-fallback`): the Codex leg of a failed or
   redirected Claude request runs stages 1-3 again with the same key's Codex
   preference and selection.

A key without a profile uses the shared profile: the global
`routing.session-affinity`, `routing.preferred-accounts`, `routing.selection`, and
`routing.claude-fallback` fields. A key with a profile uses it completely. Nothing
is inherited from the shared fields: an omitted block means off or empty, and an
omitted `selection` means `{primary: reserves, tiebreak: round-robin}`.

```yaml
routing:
  session-affinity: true
  session-affinity-ttl: 24h
  preferred-accounts: {claude: "claude-account-index", codex: "codex-account-index"}
  selection: {primary: reserves, tiebreak: earliest-reset}
  claude-fallback: {enabled: true, mode: auto, mappings: [{from: "claude-opus-*", to: "gpt-6-astra"}]}
  key-profiles:
    "client-key-a":
      session-affinity: false
      preferred-accounts: {claude: "another-claude-index"}
      selection: {primary: priority, tiebreak: round-robin}
      claude-fallback: {enabled: true, mode: auto, mappings: [{from: "claude-*", to: "gpt-6-sol"}]}
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
available = every priority tier; not disabled, cooling down, model-ineligible,
            or under an active hard quota reserve
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
their credential and ignore preferences, reserves, and selection.

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
key's preferred account.

#### Reading profiles

`GET /v8/management/routing/profiles` returns the shared profile, every key
profile in `access.api-keys` order, and the accepted slot values. The response
contains raw client keys and is sent with `Cache-Control: no-store`.

```json
{
  "supported": true,
  "shared": {
    "session-affinity": true,
    "preferred-accounts": {"claude": "claude-account-index"},
    "selection": {"primary": "reserves", "tiebreak": "earliest-reset"},
    "claude-fallback": {"enabled": true, "mode": "auto", "mappings": [{"from": "claude-opus-*", "to": "gpt-6-astra"}]}
  },
  "keys": [
    {
      "key": "client-key-a",
      "profile": {
        "session-affinity": false,
        "preferred-accounts": {"claude": "another-claude-index"},
        "selection": {"primary": "priority", "tiebreak": "round-robin"},
        "claude-fallback": {"enabled": true, "mode": "auto", "mappings": [{"from": "claude-*", "to": "gpt-6-sol"}]}
      }
    }
  ],
  "selection_options": {
    "narrowing": ["priority", "reserves", "earliest-reset", "most-remaining"],
    "pickers": ["round-robin", "weighted-round-robin", "fill-first"]
  }
}
```

`shared.selection` and each key's `selection` are effective values: a derived
legacy selection or the key default is shown instead of an empty object.
`preferred-accounts` is omitted when empty. `supported` is `false` in Home mode
and with a scheduler plugin. Take `auth_index` values from
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
  than `claude` or `codex` in `preferred-accounts`, or an invalid
  `claude-fallback` block inside a profile returns `422`
  `{"error": "invalid_config", "message": "..."}`. Messages identify a key profile
  by the first 12 hex characters of the key's SHA-256, for example
  `routing.key-profiles.3f2a9c1b7d4e.selection.primary`.
- On load and hot reload, profiles for keys missing from `access.api-keys` and
  `null` entries are dropped; a missing key is reported as a warning with its hash
  prefix.

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
configuration diff in the log reports `routing.selection` changes and the
`routing.key-profiles` count, or `updated (redacted)`, never keys or profile
contents.

#### Home mode, plugins, and logging

Key profiles apply to native standalone routing only. Home mode resolves no
profile and keeps its control-plane selection; it also disables these management
endpoints. With a scheduler plugin, every request uses the shared profile, so
global preferences keep applying there, while key profiles and the Claude ->
Codex fallback do not apply.

Each selection for a key with its own profile writes one info line:

```text
level=info msg="Routing profile selection" auth_index=claude-account-index provider=claude routing_key=3f2a9c1b7d4e routing_stage=preferred
```

`routing_key` is the first 12 hex characters of the key's SHA-256; the raw key is
never logged. `routing_stage` is `sticky` (a bound session reused its credential),
`preferred`, or `pipeline`. Inside the Codex fallback leg it is `fallback`, and
`fallback_stage` records which of those stages chose the Codex credential.
Requests that use the shared profile write no such line.

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
comments. New v8 writes must use the client path. Home configuration publishers
need matching client schema support before emitting the new path; older YAML
payloads remain readable.

## Operational endpoints

All paths below are relative to `/v8/management`. Request and response bodies
retain the corresponding business operation's fields.

| Path | Methods | Description |
| --- | --- | --- |
| `/server/latest-version` | GET | Get latest release information. |
| `/requests/api-call` | POST | Make an authenticated upstream call. |
| `/routing/cooldown/reset` | POST | Clear credential cooldown. |
| `/routing/model-definitions/<channel>` | GET | Get model definitions. |
| `/routing/activity` | GET | Get each credential's latest actual selection time. |
| `/routing/profiles` | GET | Get the shared and per-key routing profiles and the selection slot values. |
| `/routing/claude-fallback` | GET | Get the shared Claude -> Codex fallback settings, support status, and available Claude/Codex model IDs. |
| `/observability/logs` | GET, DELETE | Read or clear application logs. |
| `/observability/logs/errors` | GET | List error-log files. |
| `/observability/logs/errors/<name>` | GET | Download an error-log file. |
| `/observability/logs/requests/<id>` | GET | Get a request log. |
| `/observability/usage/api-keys` | GET | Get API-key usage. |
| `/observability/usage/queue` | GET | Get queued usage events. |
| `/credentials` | GET, POST, DELETE | List, upload, or delete credential files. |
| `/credentials/models` | GET | Get credential models. |
| `/credentials/download` | GET | Download a credential file. |
| `/credentials/status` | PATCH | Change credential status. |
| `/credentials/fields` | PATCH | Change credential fields. |
| `/credentials/refresh` | POST | Refresh credentials. |
| `/oauth/import?provider=vertex` | POST | Import a Vertex service account using a multipart `file` upload. |
| `/oauth/auth-url?provider=<provider>` | GET | Start built-in or plugin OAuth. |
| `/oauth/status?state=<state>` | GET | Get login status. |
| `/oauth/session?state=<state>` | DELETE | Cancel a login session. |
| `/oauth/callback` | GET, POST | Submit an OAuth callback. |
| `/plugins` | GET | List installed plugins. |
| `/plugins/<id>` | DELETE | Delete a plugin. |
| `/plugins/store` | GET | List the plugin store. |
| `/plugins/store/<id>/install` | POST | Install or update a plugin. |
| `/plugins/<id>/quota` | GET, POST, DELETE | Read, fetch, or reset plugin quota. |

## OAuth

The login URL is shared by all providers. Set the required `provider` query
parameter to `claude`, `codex`, `antigravity`, `kimi`, `kimi-ai`, `xai`, `devin`,
`meta`, or a registered plugin provider ID. For example:

```http
GET /v8/management/oauth/auth-url?provider=codex
Authorization: Bearer <management-key>
```

Keep any provider-specific login parameters in the query string. The login
response includes the authorization URL and a session `state`. Use that state
for status queries and cancellation.

Callbacks accept `provider`, `state`, `code`, and `error` as GET query parameters
or POST JSON fields. POST also accepts `redirect_url` containing the complete
callback URL. If `provider` is omitted, it is inferred from the pending state;
an explicit provider must match that state. Poll until the status is `ok` or
`error`; `wait` means the login is still pending. Callback acceptance alone does
not mean credential exchange and persistence have completed.

Import uses `POST /v8/management/oauth/import?provider=vertex`. The `provider`
query parameter is required; Vertex is currently the supported import provider.

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
precedence and its legacy equivalent is removed. Individual v0 setters can
update migrated fields. `PUT /v0/management/config.yaml` still replaces the
complete file and accepts legacy, new, or mixed layouts.

Plugin OAuth uses the shared v8 login endpoint. Other plugin-defined HTTP
extensions retain their declared `/v0/management` routes.
