# internal/queue
Where a step job may run — pure functions, no database. `Fit(card, need, now)` answers the memory (MB) the job gets
on a card or why it does not fit: the card must accept the job kind, hold no other training job when this is one,
have the reservation left under its memory cap (the declared `memoryGb`, or the whole remaining cap), show that much
free memory by the worker's telemetry when idle (1 GB slack), and the kind's availability window must be open with
the estimate ending before it closes (`WindowFits`; unknown estimates and resumed steps start whenever it is open).
`WindowClosed` tells a heartbeat to stop a running training step at a close. `internal/workers` loads the cards and
their leases under the card slot locks and records the lease.
