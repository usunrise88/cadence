# internal/projects
Projects: recipes branch per project, worktree per agent session, budgets, gates, workspaces per user per project.

Phase 0 holds the records only: projects (`prj_<uuidv7>`, unique slug, `rev`, soft archive) and per-user workspaces (`wsp_<uuidv7>`, Dockview layout + panel state, `rev`). Mutations take the command pipeline's transaction, check revisions and return the events they emit (`project.created|edited|archived`, `workspace.set` on `entity.{kind}.{id}`, payload `{project: …}` / `{workspace: {id, name, rev, updatedAt}}`). Repository bootstrap arrives in phase 1.
