# Cadence specification — index

Export of the Claude Doc **"Cadence — spec v0.2"** (2026-09-29), split by area. The Doc stays the source of truth
for discussion and figures; these files are what coding agents and CI read. When behaviour changes, change the Doc
and re-export, or change here and port to the Doc in the same change — never let them drift.

| File | Read it when you work on |
| --- | --- |
| `00-overview.md` | Anything: decisions and their reasons, the architecture at a glance, the stack |
| `01-principles.md` | Any API, MCP tool or UI command — the agent-native rules and the `<entity>.<verb>` naming |
| `02-domain-projects-registry.md` | Entities, project scoping, the wizard, the registry, storage and mounts |
| `03-pipelines-defaults.md` | Step kinds, pipelines, defaults, playbooks, language packs and hot words, augmentation, import/export |
| `04-blocks.md` | The five process blocks, metrics, annotation, experiments, the step→window→API→tool→event matrix |
| `05-agents.md` | The agent host, ACP drivers, sessions, worktrees and merges, guardrails, security |
| `06-platform.md` | Events and SSE, authentication, operations, notifications, the testing pyramid |
| `07-audit-risks-sources.md` | What is still open, risks, spikes, sources |
| `08-resolutions.md` | Resolutions R1–R39 of open decisions and spec gaps (they win over older text until it is rewritten); proposals R40–R54 for framework extensibility, interactive testing, audio views and charts |
| `10-ui-shell.md` | The Dockview shell, uniform workflow, document anatomy, verb vocabulary, theming, accessibility |
| `11-ui-panels.md` | Panel catalogue, workspaces, commands, the backend contract the UI relies on, build order |

Figures (architecture, window states, document anatomy, Eval workspace, build order) are drawings in the Doc;
the export marks their places with a **Figure** note.

Reading order for a new contributor: `00` → `01` → `10` (Uniform workflow) → the file of your area.
