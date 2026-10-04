package registry

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Soft delete (docs/spec/02-domain-projects-registry.md "Registry": "a registry version referenced by anything cannot
// be deleted; otherwise admin-only soft delete"). Versions are never deleted: versions.archive moves one to state
// archived (migration 0036), which keeps its content and lineage and stops every new use of it.

// Use is one thing that uses a registry version.
type Use struct {
	Relation string // adopted, base model, mix, golden set, eval record, …
	ID       string // the user's id (a project slug, run_…, mix_…, ver_…)
}

// maxUses bounds the users InUse lists.
const maxUses = 20

// InUse lists what uses the version (at most 20): projects that adopted it or default to it, other registry versions
// whose payload names it (a golden set's dataset version, a model's base), runs and experiments built on it, mix
// revisions that name it, eval records scored on it and pipeline steps pinned to it.
func InUse(ctx context.Context, q storage.Querier, id string) ([]Use, error) {
	rows, err := q.Query(ctx, `
		SELECT 'adopted by project', p.slug FROM adoptions a JOIN projects p ON p.id = a.project_id WHERE a.version_id = $1
		UNION ALL SELECT 'default base model of project', slug FROM projects WHERE base_model_version_id = $1
		UNION ALL SELECT 'named by', v.id FROM registry_versions v
			WHERE v.id <> $1 AND entity_refs_in(v.payload) @> ARRAY[$1]::text[] AND v.state <> 'archived'
		UNION ALL SELECT 'run', id FROM runs WHERE base_version_id = $1 OR family_version_id = $1
		UNION ALL SELECT 'experiment', id FROM experiments WHERE base_version_id = $1
		UNION ALL SELECT DISTINCT 'mix', r.mix_id FROM mix_revisions r
			WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(r.content->'groups') g,
				jsonb_array_elements_text(g->'datasets') d WHERE d = $1)
		UNION ALL SELECT 'eval record', id FROM eval_records WHERE golden_set_version_id = $1 OR normalizer_version_id = $1
		UNION ALL SELECT DISTINCT 'pipeline run', pipeline_run_id FROM pipeline_steps WHERE step_kind_version_id = $1
		LIMIT `+fmt.Sprint(maxUses), id)
	if err != nil {
		return nil, fmt.Errorf("list the users of %s: %w", id, err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Use, error) {
		var u Use
		return u, r.Scan(&u.Relation, &u.ID)
	})
	if err != nil {
		return nil, fmt.Errorf("list the users of %s: %w", id, err)
	}
	return out, nil
}

// Archive moves the version to state archived (the admin's soft delete). Versions that workers publish are refused,
// and so is a version anything uses (version-in-use, each user as a field error). Archiving an archived version
// answers it unchanged with no event.
func Archive(ctx context.Context, tx pgx.Tx, id string, actor auth.Actor, now time.Time) (Version, []events.Draft, error) {
	v, err := GetVersion(ctx, tx, "", id)
	if err != nil {
		return Version{}, nil, err
	}
	if v.State == StateArchived {
		return v, nil, nil
	}
	if Published(v.Kind) {
		return Version{}, nil, problems.Conflict.New("%s %s is published by workers; a %s goes away when no worker publishes it, and pipelines warn while it is deprecated",
			v.Name, v.Version, Noun(v.Kind))
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM registry_versions WHERE id = $1 FOR UPDATE", v.ID); err != nil {
		return Version{}, nil, fmt.Errorf("lock %s: %w", v.ID, err)
	}
	uses, err := InUse(ctx, tx, v.ID)
	if err != nil {
		return Version{}, nil, err
	}
	if len(uses) > 0 {
		pe := problems.VersionInUse.New("%s %s is in use (%s %s%s); a version anything uses is never archived",
			v.Name, v.Version, uses[0].Relation, uses[0].ID, more(len(uses)))
		for _, u := range uses {
			pe.Errors = append(pe.Errors, problems.FieldError{Path: "/version", Message: u.Relation + " " + u.ID})
		}
		return Version{}, nil, pe
	}
	if _, err := tx.Exec(ctx, `UPDATE registry_versions SET state = 'archived', archived_at = $2, archived_by = $3 WHERE id = $1`,
		v.ID, now, actor); err != nil {
		return Version{}, nil, fmt.Errorf("archive %s: %w", v.ID, err)
	}
	if v, err = GetVersion(ctx, tx, "", v.ID); err != nil {
		return Version{}, nil, err
	}
	return v, []events.Draft{VersionEvent(v, v.Kind+".archived")}, nil
}

func more(n int) string {
	switch {
	case n <= 1:
		return ""
	case n >= maxUses:
		return fmt.Sprintf(" and %d or more others", n-1)
	}
	return fmt.Sprintf(" and %d more", n-1)
}
