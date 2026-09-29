# Bootstrap templates

The project wizard renders these into a new project repository (bootstrap job):

- `instructions/<name>/AGENTS.md.tmpl` — instruction templates; `CLAUDE.md` is always `@AGENTS.md`
- `agent-config/opencode.json.tmpl`, `agent-config/claude-settings.json.tmpl` — rendered from the agent profile
- `pipelines/*.yaml` — starter pipelines copied verbatim
- `skills/<name>/SKILL.md` — Cadence's product skills, copied into the project's `.claude/skills/` (both agents read that path).
  They live here, not in this repo's `.claude/skills/`, so agents building Cadence don't load project-operating skills.

- `presets/<name>.yaml` — permission presets (R7), embedded in the binary (`templates.go`) and read by the policy
  engine (`internal/policy`) on every command: `guardrails-default` (the Guardrails table) and `read-only` ("Explain
  this" sessions). The bootstrap renders the project's preset into the agent config files below.

Template variables: `{{ .Name }} {{ .Slug }} {{ .Locales }} {{ .Domain }} {{ .BaseModel.Repo }} {{ .BaseModel.Revision }}
{{ .Agent.Driver }} {{ .Agent.Model }} {{ .Agent.PermissionPreset }} {{ .Repo.URL }} {{ .Budgets.GPUHoursPerDay }}`

Rendered from the preset (`internal/policy`, JSON via `policy.JSON`):
- `{{ .Agent.PermissionPresetClaudeJSON }}` — `policy.RenderClaude(preset, tools).Permissions` (allow / ask / deny rules)
- `{{ .Agent.PermissionPresetClaudeSandboxJSON }}` — `policy.RenderClaude(preset, tools).Sandbox` (shell sandbox, network allowlist)
- `{{ .Agent.PermissionPresetOpencodeJSON }}` — `policy.RenderOpencode(preset, tools)` (opencode's `permission` block)

`tools` is the MCP manifest (`internal/mcp/tools.json`: name, and `readOnlyHint` for the verb class).
