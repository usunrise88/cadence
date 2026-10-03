package lineage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

func prefixOf(id string) string {
	p, _, _ := strings.Cut(id, "_")
	return p
}

// ---------------------------------------------------------------- registry versions

// registrySource covers registry versions by the payload convention (package doc), adoptions and aliases, and the
// pipeline runs that read a version's artifacts.
type registrySource struct{}

func (registrySource) Describe(ctx context.Context, q storage.Querier, id string) (Node, bool, error) {
	if prefixOf(id) != "ver" {
		return Node{}, false, nil
	}
	v, err := registry.GetVersion(ctx, q, "", id)
	if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, err
	}
	return Node{ID: v.ID, Kind: v.Kind, Label: v.Name + " " + v.Version, State: v.State}, true, nil
}

func (registrySource) Upstream(ctx context.Context, q storage.Querier, id string) ([]Ref, error) {
	if prefixOf(id) != "ver" {
		return nil, nil
	}
	var payload []byte
	err := q.QueryRow(ctx, `SELECT payload FROM registry_versions WHERE id = $1`, id).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lineage: read %s: %w", id, err)
	}
	return RefsIn(payload)
}

func (registrySource) Downstream(ctx context.Context, q storage.Querier, id string) ([]Ref, error) {
	// Every version whose payload names id, whatever id is.
	rows, err := q.Query(ctx, `SELECT id, payload FROM registry_versions WHERE entity_refs_in(payload) @> ARRAY[$1]::text[]
		ORDER BY created_at, id`, id)
	if err != nil {
		return nil, fmt.Errorf("lineage: versions naming %s: %w", id, err)
	}
	type row struct {
		id      string
		payload []byte
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.id, &x.payload)
	})
	if err != nil {
		return nil, fmt.Errorf("lineage: versions naming %s: %w", id, err)
	}
	var out []Ref
	for _, r := range list {
		refs, err := RefsIn(r.payload)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			if ref.ID == id {
				out = append(out, Ref{ID: r.id, Relation: ref.Relation})
			}
		}
	}
	if prefixOf(id) != "ver" {
		return out, nil
	}
	// Projects that adopted the version, with the aliases that point at it.
	used, err := registry.UsedByOf(ctx, q, []string{id})
	if err != nil {
		return nil, err
	}
	for _, u := range used[id] {
		rel := "adopted"
		if len(u.Aliases) > 0 {
			rel += " (@" + strings.Join(u.Aliases, ", @") + ")"
		}
		out = append(out, Ref{ID: u.ProjectID, Relation: rel})
	}
	// Pipeline runs whose steps read one of the version's artifacts (an eval reading a golden dataset).
	rows, err = q.Query(ctx, `SELECT DISTINCT s.pipeline_run_id FROM pipeline_steps s, registry_versions v
		WHERE v.id = $1 AND s.inputs IS NOT NULL AND artifact_hashes_in(s.inputs) && artifact_hashes_in(v.payload)
		ORDER BY 1`, id)
	if err != nil {
		return nil, fmt.Errorf("lineage: pipeline runs reading %s: %w", id, err)
	}
	runs, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("lineage: pipeline runs reading %s: %w", id, err)
	}
	for _, r := range runs {
		out = append(out, Ref{ID: r, Relation: "input"})
	}
	return out, nil
}

// ---------------------------------------------------------------- data sources

// dataSource describes registry sources (src_…); the dataset versions built from them name them in their payload.
type dataSource struct{}

func (dataSource) Describe(ctx context.Context, q storage.Querier, id string) (Node, bool, error) {
	if prefixOf(id) != "src" {
		return Node{}, false, nil
	}
	var (
		name, licence string
		cleared       bool
	)
	err := q.QueryRow(ctx, `SELECT name, licence, training_cleared FROM sources WHERE id = $1`, id).Scan(&name, &licence, &cleared)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, fmt.Errorf("lineage: read source %s: %w", id, err)
	}
	state := "eval-only"
	if cleared {
		state = "cleared"
	}
	return Node{ID: id, Kind: "source", Label: name + " (" + licence + ")", State: state}, true, nil
}

func (dataSource) Upstream(context.Context, storage.Querier, string) ([]Ref, error) { return nil, nil }
func (dataSource) Downstream(context.Context, storage.Querier, string) ([]Ref, error) {
	return nil, nil
}

// ---------------------------------------------------------------- projects

// projectSource describes projects: terminal nodes (what a project adopted is not lineage of each other); a
// project as the root lists what it adopted upstream.
type projectSource struct{}

func (projectSource) Describe(ctx context.Context, q storage.Querier, id string) (Node, bool, error) {
	if prefixOf(id) != "prj" {
		return Node{}, false, nil
	}
	var slug string
	err := q.QueryRow(ctx, `SELECT slug FROM projects WHERE id = $1`, id).Scan(&slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, fmt.Errorf("lineage: read project %s: %w", id, err)
	}
	return Node{ID: id, Kind: "project", Label: slug, ProjectID: id, Terminal: true}, true, nil
}

func (projectSource) Upstream(ctx context.Context, q storage.Querier, id string) ([]Ref, error) {
	if prefixOf(id) != "prj" {
		return nil, nil
	}
	list, err := registry.ListAdoptions(ctx, q, id, "")
	if err != nil {
		return nil, err
	}
	out := make([]Ref, 0, len(list))
	for _, a := range list {
		out = append(out, Ref{ID: a.Version.ID, Relation: "adopted"})
	}
	return out, nil
}

func (projectSource) Downstream(context.Context, storage.Querier, string) ([]Ref, error) {
	return nil, nil
}

// ---------------------------------------------------------------- project work

// workSource covers training work: runs (a mix revision and a base model or checkpoint in, checkpoints out),
// checkpoints, mixes (dataset versions in) and pipeline runs.
type workSource struct{}

func (workSource) Describe(ctx context.Context, q storage.Querier, id string) (Node, bool, error) {
	var (
		n   = Node{ID: id}
		err error
	)
	switch prefixOf(id) {
	case "run":
		var (
			mix   string
			rev   int
			steps int
		)
		n.Kind = "run"
		err = q.QueryRow(ctx, `SELECT project_id, mix_name, mix_rev, steps, status FROM runs WHERE id = $1`, id).
			Scan(&n.ProjectID, &mix, &rev, &steps, &n.State)
		n.Label = fmt.Sprintf("run on %s r%d, %d steps", mix, rev, steps)
	case "ckp":
		var (
			kind string
			step *int64
			wer  *float64
		)
		n.Kind = "checkpoint"
		err = q.QueryRow(ctx, `SELECT project_id, kind, step, val_wer FROM checkpoints WHERE id = $1`, id).
			Scan(&n.ProjectID, &kind, &step, &wer)
		n.Label, n.State = kind+" checkpoint", kind
		if step != nil {
			n.Label += " at step " + strconv.FormatInt(*step, 10)
		}
		if wer != nil {
			n.Label += fmt.Sprintf(" (val WER %.3f)", *wer)
		}
	case "mix":
		var (
			name string
			rev  int
		)
		n.Kind = "mix"
		err = q.QueryRow(ctx, `SELECT project_id, name, rev FROM mixes WHERE id = $1`, id).Scan(&n.ProjectID, &name, &rev)
		n.Label = fmt.Sprintf("mix %s r%d", name, rev)
	case "plr":
		var pipeline string
		n.Kind = "pipeline_run"
		err = q.QueryRow(ctx, `SELECT project_id, pipeline, state FROM pipeline_runs WHERE id = $1`, id).
			Scan(&n.ProjectID, &pipeline, &n.State)
		n.Label = "pipeline " + pipeline
	case "pls":
		var step, kind, version string
		n.Kind = "pipeline_step"
		err = q.QueryRow(ctx, `SELECT project_id, step, kind, kind_version, state FROM pipeline_steps WHERE id = $1`, id).
			Scan(&n.ProjectID, &step, &kind, &version, &n.State)
		n.Label = fmt.Sprintf("step %s (%s@%s)", step, kind, version)
	default:
		return Node{}, false, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return Node{}, false, nil
	}
	if err != nil {
		return Node{}, false, fmt.Errorf("lineage: read %s: %w", id, err)
	}
	return n, true, nil
}

// refs runs a query of (id, relation) rows.
func refs(ctx context.Context, q storage.Querier, sql string, args ...any) ([]Ref, error) {
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("lineage: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Ref, error) {
		var x Ref
		return x, r.Scan(&x.ID, &x.Relation)
	})
	if err != nil {
		return nil, fmt.Errorf("lineage: %w", err)
	}
	return out, nil
}

func (workSource) Upstream(ctx context.Context, q storage.Querier, id string) ([]Ref, error) {
	switch prefixOf(id) {
	case "run":
		return refs(ctx, q, `SELECT x.id, x.rel FROM runs r, LATERAL (VALUES
				(r.base_version_id, 'base'), (r.family_version_id, 'family'), (r.mix_id, 'mix r' || r.mix_rev),
				(r.checkpoint_id, 'init')) AS x(id, rel)
			WHERE r.id = $1 AND x.id IS NOT NULL`, id)
	case "ckp":
		return refs(ctx, q, `SELECT run_id, 'run' FROM checkpoints WHERE id = $1
			UNION ALL SELECT unnest(averaged_from), 'averaged' FROM checkpoints WHERE id = $1`, id)
	case "mix":
		var content []byte
		err := q.QueryRow(ctx, `SELECT content FROM mixes WHERE id = $1`, id).Scan(&content)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("lineage: read mix %s: %w", id, err)
		}
		var c struct {
			Groups []struct {
				Name     string   `json:"name"`
				Datasets []string `json:"datasets"`
			} `json:"groups"`
		}
		if err := json.Unmarshal(content, &c); err != nil {
			return nil, fmt.Errorf("lineage: read mix %s: %w", id, err)
		}
		var out []Ref
		for _, g := range c.Groups {
			for _, d := range g.Datasets {
				out = append(out, Ref{ID: d, Relation: "group " + g.Name})
			}
		}
		return out, nil
	case "plr":
		return refs(ctx, q, `SELECT run_id, 'run' FROM pipeline_runs WHERE id = $1 AND run_id IS NOT NULL`, id)
	case "pls":
		return refs(ctx, q, `SELECT pipeline_run_id, 'pipeline run' FROM pipeline_steps WHERE id = $1`, id)
	}
	return nil, nil
}

func (workSource) Downstream(ctx context.Context, q storage.Querier, id string) ([]Ref, error) {
	switch prefixOf(id) {
	case "ver":
		return refs(ctx, q, `SELECT id, 'base' FROM runs WHERE base_version_id = $1
			UNION ALL SELECT id, 'family' FROM runs WHERE family_version_id = $1
			UNION ALL SELECT DISTINCT r.mix_id, 'dataset' FROM mix_revisions r
				WHERE EXISTS (SELECT 1 FROM jsonb_array_elements(r.content->'groups') g,
					jsonb_array_elements_text(g->'datasets') d WHERE d = $1)`, id)
	case "mix":
		return refs(ctx, q, `SELECT id, 'mix r' || mix_rev FROM runs WHERE mix_id = $1 ORDER BY created_at`, id)
	case "run":
		return refs(ctx, q, `SELECT id, 'run' FROM checkpoints WHERE run_id = $1
			UNION ALL SELECT pipeline_run_id, 'pipeline run' FROM runs WHERE id = $1`, id)
	case "ckp":
		return refs(ctx, q, `SELECT id, 'init' FROM runs WHERE checkpoint_id = $1
			UNION ALL SELECT id, 'averaged' FROM checkpoints WHERE $1 = ANY(averaged_from)`, id)
	}
	return nil, nil
}
