# Model listing counts alias-pool entries instead of credentials

- found: 2026-11-19, plan: 20261119-provider-disable-in-profiles, phase: evaluation
- severity: minor
- area: internal/registry/model_registry.go

A credential that repeats an alias to build an alias pool (`config.example.yaml`,
"You may repeat the same alias") registers the alias once per entry, so
`ModelRegistration.Count` and `Providers` grow by the number of entries. The quota and
suspension maps are keyed by credential ID, so an unavailable credential subtracts
only one.

`GetModelCountMatching` now subtracts all of an unavailable credential's entries
(`clientModelRegistrationsLocked`), so `model: auto` is correct. The listing paths
still subtract one per credential:

- `modelRegistrationAvailability` (`internal/registry/model_registry.go`, its
  `effectiveClients`), used by `GetAvailableModels`/`GetAvailableModelInfos`
- `GetAvailableModelsByProvider` (`internal/registry/model_registry.go`, its
  `effectiveClients`)

Effect: a pool whose only credential is suspended for a non-quota reason stays in
`/v1/models` and in the per-provider listing (for example `2 - 1 = 1`), although
that credential cannot serve it. A quota cooldown is unaffected, because cooling
models are listed on purpose.

Suggested fix: weight each unavailable credential in those loops by
`clientModelRegistrationsLocked(clientID, modelID)`, as `GetModelCountMatching` does.
Add a registry test with a two-entry pool and a non-quota suspension.
