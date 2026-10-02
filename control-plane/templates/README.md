# Bootstrap templates

The project wizard renders these into a new project repository (the `projects.bootstrap` job,
`internal/projects/bootstrap`; rendering in `internal/projects/layout`):

- `instructions/<name>/AGENTS.md.tmpl` — instruction templates (`default`, `minimal`); `CLAUDE.md` is always
  `@AGENTS.md`. A project whose AGENTS.md was edited in Agent settings has the instructions `custom` and is never
  re-rendered.
- `agent-config/claude-settings.json.tmpl` → `.claude/settings.json`, `agent-config/opencode.json.tmpl` →
  `opencode.json` — rendered from the agent profile's permission preset. Permissions only: no MCP section and no
  token; the MCP server reaches the agent through ACP `session/new` (docs/spec/08-resolutions.md R2).
- `pipelines/*.yaml` — starter pipelines, copied verbatim to `pipelines/` (format: `internal/pipelines/README.md`;
  steps pin `kind@version`, parameters equal to their defaults are omitted). A project without its own file of a
  name runs the bundled one, so these are also what `pipelines.list` offers a project without a repository.
  `internal/pipelines` tests that every file parses.
- `skills/<name>/SKILL.md` — Cadence's product skills, copied into the project's `.claude/skills/` (both agents read
  that path). They live here, not in this repo's `.claude/skills/`, so agents building Cadence don't load
  project-operating skills.
- `playbooks/<name>.yaml` — playbooks (R16; format and rules in `internal/playbooks/README.md`), copied to the
  project's `playbooks/` and served by `playbooks.list|get|run` from the bundled files. Their `prompt` is a Go
  `text/template` over `.Inputs.<name>` (each input as text: `dataset/fleurs-he 2026-09-30.ab12 (ver_…)`, a number, or
  `none (…)` for an optional input without a value) and `.Project.Name`, `.Project.Slug`, `.Project.Locales`.
  `internal/playbooks` tests that every file parses and validates.
- `lang/<locale>/` — starter language packs (he-IL, sr; format and checks in `internal/langpacks`): the bootstrap
  copies the pack serving each project locale (the same locale, a less specific one — `sr` for `sr-Latn` — or one
  of the same language) to the project's `lang/<locale>/`, verbatim. `projects.sync` offers pack files three-way
  (an unedited file is updated, an edited or deleted one is left alone, a missing pack is offered whole). Each pack
  registers as `template/langpack-<locale>` (lower case). `internal/langpacks` tests that every pack checks out and
  that `sr`'s scheme equals the worker's `sr-Cyrl-Latn`.
- `presets/<name>.yaml` — permission presets (R7), embedded in the binary and read by the policy engine
  (`internal/policy`) on every command: `guardrails-default` (the Guardrails table) and `read-only` ("Explain
  this" sessions).

Cadence also writes `project.yaml` (the wizard's facts), `data.lock` (every registry version the project depends on,
including the template versions its files came from) and the first `NOTES.md`; those are generated in Go, not from
templates here.

Template variables:

| Variable | Value |
| --- | --- |
| `{{ .Name }}`, `{{ .Slug }}`, `{{ .Description }}` | The project |
| `{{ .Locales }}` | Locales joined with ", " (`he-IL`) |
| `{{ .Domain }}` | `telephony`, … |
| `{{ .BaseModel.Repo }}`, `{{ .BaseModel.Revision }}`, `{{ .BaseModel.Version }}`, `{{ .BaseModel.Collection }}`, `{{ .BaseModel.Licence }}` | The default base model: Hugging Face repository and pinned revision, registry version and collection |
| `{{ .Agent.Driver }}`, `{{ .Agent.Model }}` | `claude-code` or `opencode`, and its model |
| `{{ .Agent.OpencodeModel }}` | opencode's model: the profile's when the driver is opencode, else `wizard.opencode_model` of defaults.yaml |
| `{{ .Agent.PermissionPreset }}`, `{{ .Agent.InstructionsTemplate }}`, `{{ .Agent.AutoMerge }}` | The agent profile |
| `{{ .Repo.Kind }}`, `{{ .Repo.URL }}` | `internal`, `github` or `url`; the remote, or `/git/<slug>.git` |
| `{{ .Budgets.GPUHoursPerDay }}`, `{{ .Budgets.AgentTokensPerDay }}` | Daily budgets |

Rendered from the preset (`internal/policy`, JSON via `policy.JSON`):
- `{{ .Agent.PermissionPresetClaudeJSON }}` — `policy.RenderClaude(preset, tools).Permissions` (allow / ask / deny rules)
- `{{ .Agent.PermissionPresetClaudeSandboxJSON }}` — `policy.RenderClaude(preset, tools).Sandbox` (shell sandbox, network allowlist)
- `{{ .Agent.PermissionPresetOpencodeJSON }}` — `policy.RenderOpencode(preset, tools)` (opencode's `permission` block)

`tools` is the MCP manifest (`internal/mcp/tools.json`: name, and `readOnlyHint` for the verb class).
`embed.go` embeds the whole tree. At start the control plane registers every entry under `instructions/`, `presets/`,
`skills/`, `pipelines/`, `playbooks/`, `agent-config/` and `lang/` as a registry template version (`template/<kind>-<name>`, e.g.
`template/skill-cadence-train`); a changed file registers a new version, so `templates.list` shows what projects can
sync to, and `projects.sync` offers the difference to a project as a draft branch.
