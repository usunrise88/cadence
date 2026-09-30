# internal/telemetry
Metric points in Postgres (`metric_points`, migration 0012; R15). `Insert(ctx, tx, Source{JobID, RunID, StepID,
ProjectID}, points)` copies a worker's batch in; `Get(ctx, q, Query{RunID | JobID, Names, AfterStep, MaxPoints})`
returns one `Series` per metric in step order, thinned evenly to `MaxPoints` (first and last kept) with the
original `Total`. Stream R exposes `Get` as `metrics.get`; live points go out on `run.{id}.metrics` from
`internal/workers`.
