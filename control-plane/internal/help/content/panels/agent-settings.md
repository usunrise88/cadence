---
title: Agent settings
summary: The project's agent profile — driver, model, permission preset, merge and draft policy, AGENTS.md — and the config files rendered from it.
contexts: [panel:agent-settings, command:agentProfile.edit]
---

## What this is

A tool panel for the current project's agent profile: which agent runs the project's sessions (Claude Code or
opencode) with which model, under which permission preset, what happens to a session branch when the session ends,
and whether agent changes to mixes, gates, notes and language packs land directly or as drafts. It previews the files
rendered from the profile — `.claude/settings.json` and `opencode.json` (permissions only) and `CLAUDE.md` — and edits
`AGENTS.md`, the project instructions every session reads.

## Place in the loop

The profile is set by the project wizard and changed here. **Save** commits the rendered files to `main` of the
project repository; running sessions pick them up at their next turn. The files only reduce the agent's own
prompts (Claude Code applies their ask and deny rules; the Cadence tools the preset allows are pre-allowed by the
agent host when a session starts, because Claude Code does not take allow rules from a repository's own file): the server's policy engine decides what a session may do, from the preset in its token, whatever the files
say. The Cadence MCP server and the session token reach the agent through the session, never through a file in the
repository.

## Fields and defaults

| Field | Meaning | Default |
| --- | --- | --- |
| Driver | `claude-code` (the owner's Claude subscription) or `opencode` | `wizard.driver` |
| Model | A Claude Code model alias, or opencode's `provider/model` (any configured provider) | `wizard.claude_code_model`, `wizard.opencode_model` |
| Permission preset | The Guardrails rules rendered into both config files and applied by the policy engine | `wizard.permission_preset` |
| Session branches | `when-clean`: merge into `main` at session end when it applies cleanly (a branch that changes `gates.yaml`, `lang/`, `project.yaml`, `.claude/` or `opencode.json` always waits for a person); `never`: always wait as Session changes | `when-clean` |
| Agent changes land as | Per kind (mix, gate, note, language pack): direct changes, or drafts to accept or revert | Direct |
| Instructions template | The template `AGENTS.md` is rendered from; `custom` once you edited `AGENTS.md` here | `wizard.instructions_template` |

**Reset to template** re-renders `AGENTS.md` from the template with the project facts and drops your edits.

## Commands

- `agentProfile.edit` — Save (commits the rendered files); an agent changing its own profile waits for an approval
- `agentModels.list`, `templates.list` — the catalogues the selects read
- Open in Recipe — shows the committed file with its history

## Playbooks

- Tighten what agents may do: switch the preset to `read-only` for an exploration project.
- Keep a person in the loop on merges: set Session branches to `never`.

## Sources

docs/spec/02-domain-projects-registry.md "Project wizard" (Agent settings panel); docs/spec/05-agents.md "Security";
docs/spec/08-resolutions.md R2 (no MCP section in rendered files), R6 (agent authentication), R7 (presets and the
policy engine).
