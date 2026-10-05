"""Cadence omni pack (runtime ``omni``, GPU): reference alignment with omniASR CTC emissions (docs/review/
2026-10-03-phase-4-plan.md decision 8; R26, R51, R54) and language identification with SpeechBrain's VoxLingua107
classifier (``lid_classify@2``, decision 7). Each model is an ``auxiliary`` version (roles ``align``, ``lid``), loaded
per job (R45's one-off allowance); fairseq2, speechbrain and torch are imported lazily, inside the runtime image."""

RUNTIME = "omni"
