# recipes/ — example of a project repository after bootstrap

In production each project has its own repository (GitHub or the internal bare repo) created by the
project wizard. `projects/hebrew/` shows what the bootstrap job writes.

Data never lives here. Datasets, golden sets, models and other reusable assets are immutable versions in
the Cadence-wide registry; the project references them through `project.yaml` adoptions and aliases, and
`data.lock` records the exact versions resolved for reproducibility.
