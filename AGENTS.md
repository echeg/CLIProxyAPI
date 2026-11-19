# AGENTS.md

Go 1.26+ proxy server providing OpenAI/Gemini/Claude/Codex compatible APIs with OAuth and round-robin load balancing.

## Repository
- GitHub: https://github.com/router-for-me/CLIProxyAPI

## Commands
```bash
gofmt -w . # Format (required after Go changes)
go build -o cli-proxy-api ./cmd/server # Build
go run ./cmd/server # Run dev server
go test ./... # Run all tests
go test -v -run TestName ./path/to/pkg # Run single test
go build -o test-output ./cmd/server && rm test-output # Verify compile (REQUIRED after changes)
```
- Common flags: `--config <path>`, `--tui`, `--standalone`, `--local-model`, `--no-browser`, `--oauth-callback-port <port>`

## Config
- Default config: `config.yaml` (template: `config.example.yaml`)
- `.env` is auto-loaded from the working directory
- Auth material defaults under `auths/`
- Storage backends: file-based default; optional Postgres/git/object store (`PGSTORE_*`, `GITSTORE_*`, `OBJECTSTORE_*`)

## Architecture
- `cmd/server/` — Server entrypoint
- `internal/api/` — Gin HTTP API (routes, middleware, modules)
- `internal/api/modules/amp/` — Amp integration (Amp-style routes + reverse proxy)
- `internal/thinking/` — Main thinking/reasoning pipeline. `ApplyThinking()` (apply.go) parses suffixes (`suffix.go`, suffix overrides body), normalizes config to canonical `ThinkingConfig` (`types.go`), normalizes and validates centrally (`validate.go`/`convert.go`), then applies provider-specific output via `ProviderApplier`. Do not break this "canonical representation → per-provider translation" architecture.
- `internal/runtime/executor/` — Per-provider runtime executors (incl. Codex WebSocket)
- `internal/translator/` — Provider protocol translators (and shared `common`)
- `internal/registry/` — Model registry + remote updater (`StartModelsUpdater`); `--local-model` disables remote updates
- `internal/store/` — Storage implementations and secret resolution
- `internal/managementasset/` — Config snapshots and management assets
- `internal/cache/` — Request signature caching
- `internal/watcher/` — Config hot-reload and watchers
- `internal/wsrelay/` — WebSocket relay sessions
- `internal/usage/` — Usage and token accounting
- `internal/home/` — CLIProxyAPIHome control plane integration (bootstrap, RESP communication, dispatch coordination)
- `internal/tui/` — Bubbletea terminal UI (`--tui`, `--standalone`)
- `sdk/cliproxy/` — Embeddable SDK entry (service/builder/watchers/pipeline)
- `sdk/cliproxy/auth/` — Credential selection. New selections run through `PipelineSelector` (`selection_pipeline.go`): hard-only availability -> the profile's preferred account -> `selection {primary, tiebreak}` narrowers/pickers -> round-robin for leftover ties. The per-key `routingProfile` (`routing_profile.go`) is resolved once in the public `Execute*` and carried in the context through retries and fallback legs. The profile's per-provider policy (`routing.providers`: `subscription` excludes Claude/Codex API keys, `api` allows only the profile's `api-credential`) is enforced in `authSelectionEligibility.allows` (`provider_policy.go`), skipped for pinned requests (the Responses websocket handler releases a pin the policy rejects at turn start via `Manager.RoutingPolicyAllowsAuth` once an alternative exists, and `HasAlternativeTargetForModel` counts an available `api` fallback leg), requests with their own `requiredKind`/`credentialPolicy`, Home, and scheduler plugins. `executeWithProviderFallback` (`provider_fallback.go`) runs at most one fallback leg per request, `claude` before `codex`: target `codex` (Claude -> Codex OAuth) or `api` (same model on that provider only, derived profile in `mode: api` without session affinity). `providers.<p>.disabled` is enforced outside the pipeline and is not skipped for pinned or `requiredKind`/`credentialPolicy` requests: `filterDisabledProviders` runs first in `executeWithProviderFallback` (it must stay ahead of `providerFallbackFor`, since its 403 would count as a fallback trigger) and drops disabled providers, refusing when none remain, when the pin is an auth of a disabled provider, or when a disabled Claude owns the request's Claude Thread and was dropped or `auto` resolved away from it (`requestedAutoModel`; `withClaudeThreadAuth` only runs while `claude` is listed); the direct `SelectAuthExcluding`/`SelectAuthByKind`/`SelectAuthWithCredentialPolicy` call `refuseDisabledSelection`, which only an open-session pin (`WithOpenSessionPin` plus a pinned auth, used by the realtime hangup) skips; `providerFallbackFor` gives a disabled provider no fallback and builds no Claude -> Codex leg while Codex is disabled; `providerPolicyAllows` also rejects its auths so websocket pins are released. The refusal is `providerDisabledError` (request-scoped 403 whose `Error()` is the bare client message, detected by `IsProviderDisabledError`), not `NewRequestScopedError`; a new execute or direct-selection entry point must pass one of these gates. `model: auto` resolves through `BaseAPIHandler.AutoModelProviderFilter` (`Manager.ProviderEnabledForRequest`, no refusal log), which counts only available clients of enabled providers (`GetModelCountMatching`); it counts each unavailable client once per alias-pool entry (`clientModelCounts`); when the profile disables a provider that registers a model and no enabled provider has an available client, `auto` resolves to the newest model an enabled provider registers (`GetFirstRegisteredModelMatching`, which reads registrations, not the listing), so the request gets that provider's own error, and to the newest model of a disabled provider only when no enabled provider registers one, so the request still gets the 403; every resolution of `auto` for a request, including the Responses websocket pin matcher (`responsesWebsocketResolvedModel`), must use it, and the websocket capability checks (`responsesWebsocketAvailableAuthsForModel`) leave out auths of disabled providers; in-band stream error writers pass the error cause (`BuildErrorResponseBodyWithError`, `ResponsesStreamErrorTextWithError`) so a refusal after a keep-alive keeps `provider_disabled`. Read the policy through `config.RoutingProfile.EffectiveProviders()`: the stored `Providers` keeps only what the file spells out, and the legacy `claude-fallback` is converted there. The Codex executor reads the speed policy (`providers.codex.speed`, `ignore-client-tier`) from the context profile through `CodexSpeedFromContext` (`routing_profile.go`) and falls back to the shared profile of its own config only when the context has none (Home, direct executor calls); do not reintroduce a global Codex tier flag. Every Codex `response.create` and `/responses/compact` build path calls `applyCodexServiceTierDoc` before the payload finalizer, `response.steer` frames call only `removeCodexClientServiceTierDoc`, and tier tests install the `useCodexServiceTierFixture` catalog instead of relying on the embedded catalog's tiers. In tests, a Claude/Codex credential with an `api_key` attribute is excluded by default; use the `withAPIModeRouting` test helpers to select one. Do not add implicit priority or reserve layers outside the pipeline; legacy selectors run inside it only as pickers.
- `test/` — Cross-module integration tests

## Code Conventions
- Keep changes small and simple (KISS)
- Comments in English only
- If editing code that already contains non-English comments, translate them to English (don’t add new non-English comments)
- For user-visible strings, keep the existing language used in that file/area
- New Markdown docs should be in English unless the file is explicitly language-specific (e.g. `README_CN.md`)
- As a rule, do not make standalone changes to `internal/translator/`. You may modify it only as part of broader changes elsewhere.
- If a task requires changing only `internal/translator/`, run `gh repo view --json viewerPermission -q .viewerPermission` to confirm you have `WRITE`, `MAINTAIN`, or `ADMIN`. If you do, you may proceed; otherwise, file a GitHub issue including the goal, rationale, and the intended implementation code, then stop further work.
- `internal/runtime/executor/` should contain executors and their unit tests only. Place any helper/supporting files under `internal/runtime/executor/helps/`.
- Do not use `github.com/tidwall/gjson`, `github.com/tidwall/sjson`, Go's standard `encoding/json`, Go's standard `encoding/base64`, or Go's standard `regexp`. Always use `github.com/MoeForge/moego/json`, `github.com/MoeForge/moego/ojson`, `github.com/MoeForge/moego/base64`, and `github.com/MoeForge/moego/regexp` for all JSON, Base64, and regular expression operations. All JSON write operations (`Set`, `SetRaw`, `Delete`, etc.) MUST be performed on `*ojson.Doc` (`ojson.NewDoc` / `ojson.NewDocString`), passing `*ojson.Doc` across mutation stages and serializing only once at the final exit (`doc.Bytes()` / `doc.String()`), rather than mutating raw `[]byte` or `string` payloads directly.
- Payload configuration MUST be the final semantic barrier before sending requests in every executor, including streaming, WebSocket, continuation, retry/fallback, image, and token-count paths where applicable. Complete all built-in payload translation, normalization, injection, and cleanup first, then evaluate and apply user payload rules exactly once to the final business payload. No subsequent logic may overwrite configured values or restore filtered fields. Only necessary transport framing, serialization, signature calculation, and read-only validation may follow without changing business payload semantics. Add regression tests when introducing or changing request-building paths to enforce this ordering.
- Follow `gofmt`; keep imports goimports-style; wrap errors with context where helpful
- Do not use `log.Fatal`/`log.Fatalf` (terminates the process); prefer returning errors and logging via logrus
- Shadowed variables: use method suffix (`errStart := server.Start()`)
- Wrap defer errors: `defer func() { if err := f.Close(); err != nil { log.Errorf(...) } }()`
- Use logrus structured logging; avoid leaking secrets/tokens in logs
- Avoid panics in HTTP handlers; prefer logged errors and meaningful HTTP status codes
- Timeouts are allowed only during credential acquisition; after an upstream connection is established, do not set timeouts for any subsequent network behavior. Intentional exceptions that must remain allowed are the Codex websocket liveness deadlines in `internal/runtime/executor/codex_websockets_executor.go`, the wsrelay session deadlines in `internal/wsrelay/session.go`, the management APICall timeout in `internal/api/handlers/management/api_tools.go`, and the `cmd/fetch_antigravity_models` utility timeouts
- Avoid wall-clock `time.Sleep` in TTL, expiration, ordering, or cache-eviction unit tests due to platform timer granularity (e.g. Windows default timer resolution of ~15.6ms) and CI jitter under load; prefer controllable clocks (`nowFunc` / mock clock), explicit timestamp manipulation, or deterministic synchronization primitives.
- Note: if modifying features that involve CLIProxyAPIHome, check if corresponding updates are needed in the CLIProxyAPIHome repository.
- Endpoints under the `/v0/management` base URL are deprecated and no longer maintained. For any feature changes, do not modify endpoints under `/v0/management` unless necessary to fix compilation errors.
