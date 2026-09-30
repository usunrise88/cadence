# internal/projects
Projects: the wizard's facts, the repository they live in, the agent profile, workspaces per user per project.

- Records (`prj_<uuidv7>`, unique slug, `rev`, soft archive): state `bootstrapping | active | failed`, locales,
  domain, default base model (joined from the registry), repository (`internal | github | url`, remote, secret name,
  last push error), budgets. The JSON form of `Project` is the contract's, and project events carry it as is.
- Agent profiles (one per project, own `rev`): driver, model, permission preset, instructions template (`custom` once
  AGENTS.md was edited), auto-merge, draft policy per draftable kind, the commit holding the rendered files.
  `GetAgentProfile` is what the agent-session stream reads.
- Workspaces (`wsp_<uuidv7>`, Dockview layout + panel state, `rev`); `CreateDefaultWorkspaces` makes the five
  default ones as placeholders (empty layout) that the web shell fills.
- Mutations take the command pipeline's transaction, check revisions and return their events
  (`project.created|edited|archived|bootstrapped|bootstrap_failed|push_status|noted|synced|branch_accepted|branch_reverted`,
  `agent_profile.edited` on `entity.project.{id}`; `workspace.set`).

Subpackages: `layout` renders the repository files from the facts and the bundled templates (pure);
`bootstrap` is the `projects.bootstrap` job and the commands that commit to the repository (agent profile, facts,
notes, template sync, merges, pushes from outside, archive, branch retention).
