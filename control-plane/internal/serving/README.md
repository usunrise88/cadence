# internal/serving
Staging serving (phase 5 · stream D2; docs/spec/06-platform.md "Staging serving", R30). Migration 0049:
`serving_health`, `serving_models`, `serving_leases` (target ids without a foreign key: the targets table is 0050).

- **`Granted`** (a worker grant hook): a step that consumes a `deployable` (`steps.ServedModels`) gets
  `CADENCE_SERVING_TARGET|ENDPOINT|SERVER|SERVER_VERSION|MODEL|MODELS` — or `CADENCE_SERVING_REFUSED` for an unknown,
  archived or delivery target, or one that does not serve the deployable's family, format or profile — and its lease
  is counted per model. The target is the step's `target` param, else `serving.default_target`.
- **`Check`**: the request-time check (`target-does-not-serve`, `serving-unavailable` when the last check found the
  server down); `transcriptions.new` uses it for deployment lanes, stream D1's parity and benchmark should too.
- **`Tick`** (periodic `serving.check`, every `serving.health_check_seconds`): health of every active staging target,
  reconciliation with the server's index, unload of models idle for `serving.unload_idle_minutes`. The paths are data
  (`serving.servers`); nothing here names a server's protocol.
- **`HealthOf`, `Models`**: what `deploymentTargets.list|get` add as `health` and `servedModels`.

The queue's side (the serving reserve, a served model reserved once per card) is `internal/queue`. Tests:
`serving_test.go`; `internal/server/serving_integration_test.go` (a fake server: leases, reserve, health, unload,
transcriptions through a deployment).
