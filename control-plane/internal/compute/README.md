# internal/compute
Compute hosts (`cmp_<uuidv7>`) and their cards: name, card class (the estimate table's key), memory, memory cap per
card, allowed job kinds (training, eval, shadow, export, data), availability windows per job kind (R19, `Windows`:
days, start, end, IANA time zone; `Availability` answers whether a kind may run now and until when; no windows =
always open) and health (`SetHealth`, from the worker protocol's heartbeats: `compute.health` on `compute.{id}` when
the state changes). `WithTelemetry` fills each card's last worker telemetry from `card_slots` on read. Hosts are
seeded from `defaults.yaml` at first start (never overwritten) and edited with `compute.edit`; `ForJob` picks the
card a job kind would run on for the estimate. Events `compute.created|edited` on `entity.compute.{id}`, no
projectId.
