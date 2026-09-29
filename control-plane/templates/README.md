# Bootstrap templates

The project wizard renders these into a new project repository (bootstrap job):

- `instructions/<name>/AGENTS.md.tmpl` — instruction templates; `CLAUDE.md` is always `@AGENTS.md`
- `agent-config/opencode.json.tmpl`, `agent-config/claude-settings.json.tmpl` — rendered from the agent profile
- `pipelines/*.yaml` — starter pipelines copied verbatim
- `skills/<name>/SKILL.md` — Cadence's product skills, copied into the project's `.claude/skills/` (both agents read that path).
  They live here, not in this repo's `.claude/skills/`, so agents building Cadence don't load project-operating skills.

Template variables: `{{ .Name }} {{ .Slug }} {{ .Locales }} {{ .Domain }} {{ .BaseModel.Repo }} {{ .BaseModel.Revision }}
{{ .Agent.Driver }} {{ .Agent.Model }} {{ .Agent.PermissionPreset }} {{ .Repo.URL }} {{ .Budgets.GPUHoursPerDay }}`

`embed.go` embeds the whole tree. At start the control plane registers every entry under `instructions/`, `presets/`,
`skills/`, `pipelines/` and `agent-config/` as a registry template version (`template/<kind>-<name>`, e.g.
`template/skill-cadence-train`); a changed file registers a new version, so `templates.list` shows what projects can
sync to.
