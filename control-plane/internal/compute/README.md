# internal/compute
Compute hosts (`cmp_<uuidv7>`) and their cards: memory cap per card, allowed job kinds (training, eval, shadow,
export), health (`unknown` until a worker reports in phase 2). Hosts are seeded from `defaults.yaml` at first start
and edited with `compute.edit`; `ForJob` picks the card a job kind would run on (the estimate uses it now, the queue
in phase 2). Events `compute.created|edited` on `entity.compute.{id}`, no projectId.
