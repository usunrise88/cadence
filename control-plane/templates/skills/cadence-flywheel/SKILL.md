---
name: cadence-flywheel
description: Cadence block "flywheel" workflow: which MCP tools to call in what order, the gates to respect, and what to report. Load when working on the flywheel block of a Cadence project.
---

# cadence-flywheel

Stub — filled in during the phase that builds this block (see docs/spikes/README.md for the order).

## Contract
- Act only through Cadence MCP tools; never edit the database or production.
- Every GPU-consuming command: call with dryRun first, compare with the project budget, then run.
- Report results as entity references (`@run:123`, `@eval:45`) so the UI can link them.
