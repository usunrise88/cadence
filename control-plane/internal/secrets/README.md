# internal/secrets
Named credentials (R9). Postgres holds `sec_<uuidv7>`, name, kind, scope and last use; the value is sealed with NaCl
secretbox under the master key (`CADENCE_MASTER_KEY_FILE`, generated on first start with a warning) into
`<data dir>/secrets/<id>` (0600, directory 0700). No endpoint, event, log line or idempotency record carries the
value; `Store.Read(ctx, name)` is the server-side way to use one. Scope is `instance` or `project:<slug>`;
`CheckScope` (forbidden) refuses a project secret to work of another project or of none — the worker lease checks it
for every secret a step names (`ScopeAllows` is the rule).
