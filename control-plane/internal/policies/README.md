# internal/policies
Instance-wide policies, one row (`id = 'instance'`, `rev`). Only values set by `policies.edit` are stored; the rest
reads through from `defaults.yaml`, and `departures` lists what differs. Budgets now (GPU-hours per project per day,
agent turns per session); retention, PII and cache quotas join in phases 4–5.
