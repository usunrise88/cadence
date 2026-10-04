"""Cadence omni pack (runtime ``omni``, GPU): reference alignment with omniASR CTC emissions (docs/review/
2026-10-03-phase-4-plan.md decision 8; R26, R51, R54). The model is an ``auxiliary`` version with the ``align`` role,
loaded per job (R45's one-off allowance); fairseq2 and torch are imported lazily, inside the runtime image."""

RUNTIME = "omni"
