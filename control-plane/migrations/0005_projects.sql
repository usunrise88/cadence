-- 0005 · Phase 1 projects (wave 2): the wizard's facts on the project, the repository it lives in, its bootstrap
-- state and its agent profile. Projects created before this migration stay active with no repository and no agent
-- profile; the API shows them as they are.

ALTER TABLE projects
    ADD COLUMN state                 text NOT NULL DEFAULT 'active'
        CHECK (state IN ('bootstrapping', 'active', 'failed')),
    ADD COLUMN locales               text[] NOT NULL DEFAULT '{}',
    ADD COLUMN domain                text NOT NULL DEFAULT '',
    ADD COLUMN base_model_version_id text REFERENCES registry_versions (id),
    -- {kind: internal | github | url, remote, secret, pushError}; {} for projects without a repository
    ADD COLUMN repository            jsonb NOT NULL DEFAULT '{}',
    -- {gpuHoursPerDay, agentTokensPerDay}; {} means the defaults of defaults.yaml
    ADD COLUMN budgets               jsonb NOT NULL DEFAULT '{}',
    ADD COLUMN bootstrap_job_id      text,
    ADD COLUMN bootstrap_error       text;

-- One agent profile per project (docs/spec/02-domain-projects-registry.md "Project wizard", step 3–4; Agent
-- settings panel). The files rendered from it live in the project repository; commit is the last commit that
-- wrote them.
CREATE TABLE agent_profiles (
    project_id            text PRIMARY KEY REFERENCES projects (id),
    driver                text NOT NULL CHECK (driver IN ('claude-code', 'opencode')),
    model                 text NOT NULL,
    permission_preset     text NOT NULL,
    instructions_template text NOT NULL,
    auto_merge            text NOT NULL CHECK (auto_merge IN ('when-clean', 'never')),
    draft_policy          jsonb NOT NULL,
    last_commit           text NOT NULL DEFAULT '',
    rev                   integer NOT NULL DEFAULT 1 CHECK (rev >= 1),
    updated_at            timestamptz NOT NULL DEFAULT now()
);
