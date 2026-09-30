# internal/repos
Project repositories with the `git` binary (docs/spec/08-resolutions.md R10). No database here.

- Layout under the data directory: `repos/<slug>.git` (bare, canonical, served over smart HTTP), `work/<slug>` (the
  working clone the server commits `main` through), `worktrees/<slug>` (server-side session state; removed on
  archive). Every write to one repository holds its lock; git runs with the host's configuration ignored.
- Writes: `Create` (empty bare repository, `main`), `Link(remote)` (fetch every branch of an existing repository),
  `Commit(Change)` (files, deletes, whole directories replaced; on `main` or an existing branch; a push from outside
  in between is retried), `CreateBranch`, `DeleteBranch(expect)`, `MergeBranch(expect)` (fast-forward, or a merge
  commit from `git merge-tree --write-tree` — conflicts come back as `*ConflictError` and nothing is written),
  `PruneMerged(cutoff)` (merge markers `refs/cadence/merged/<unix>/<branch>`, 30-day retention), `PushRemote`
  (mirror `main` to GitHub or a linked URL; token as an HTTP header through git's environment, never stored),
  `RemoveWorkingState`.
- Reads: `Resolve`, `Head`, `Refs`, `ListFiles`, `ReadFile`, `History`, `Branches` (open = not merged), `Branch`,
  `DiffBranch` (files, patch against the merge base, predicted conflicts), `Changes`.
- `HTTPHandler(authorize, notify)`: `/git/<slug>.git` through `git http-backend` (CGI), smart protocol only. The
  credential is the basic-auth password (`https://x-token:<token>@host/git/<slug>.git`) or a Bearer token. A
  pre-receive hook enforces `Access.PushRefs` (agent tokens: `refs/heads/session/<id>` only), `ReadOnly` (archived
  projects) and "main is never deleted"; after a push the moved branches go to `notify`.
- `GitHub.CreateRepo`: the REST call that creates an empty repository for the token's user or an organisation.
