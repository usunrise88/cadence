package data

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Sources in full (phase 4 · stream D): registration by a person or an agent (sources.new), the licence and
// clearance history (source_clearances) and the ingest history (source_ingests), and the "no licence, no ingest"
// rule the pipeline engine and the dataset hook apply.

// Kinds of a clearance history entry.
const (
	ChangeCreated   = "created"
	ChangeLicence   = "licence"
	ChangeCleared   = "cleared"
	ChangeUncleared = "uncleared"
)

// Clearance is one change of a source's licence or training clearance.
type Clearance struct {
	Change          string
	Licence         string
	TrainingCleared bool
	Actor           auth.Actor
	At              time.Time
}

// Ingest is a dataset version registered from a source by an import or an ingest.
type Ingest struct {
	VersionID     string
	PipelineRunID string
	ProjectID     string
	StepKind      string
	Frozen        bool
	Utterances    int
	Hours         float64
	At            time.Time
}

// maxIngests bounds the ingest history sources.get returns.
const maxIngests = 100

// unlicensed are licence values that name no licence (case-insensitive): a source with one of them is never ingested.
var unlicensed = []string{"", "unknown", "none", "noassertion", "unlicensed", "n/a", "na", "tbd", "todo", "?"}

// Unlicensed reports whether licence names no usable licence (empty, unknown, none, NOASSERTION, …).
func Unlicensed(licence string) bool {
	l := strings.ToLower(strings.TrimSpace(licence))
	for _, u := range unlicensed {
		if l == u {
			return true
		}
	}
	return false
}

// IngestAllowed is "no licence, no ingest": the source an ingest names must be registered (sources.new), not
// archived, and carry a usable licence. It fails with source-unlicensed otherwise.
func IngestAllowed(ctx context.Context, q storage.Querier, name string) (Source, error) {
	if strings.TrimSpace(name) == "" {
		return Source{}, problems.SourceUnlicensed.New("the ingest names no source; register the corpus with sources.new (name, licence, kind) and set the step's source parameter")
	}
	s, err := getSource(ctx, q, name, "")
	if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
		return Source{}, problems.SourceUnlicensed.New("no source %q in the registry; register it with sources.new (name, licence, kind) before ingesting — no licence, no ingest", name)
	}
	if err != nil {
		return Source{}, err
	}
	switch {
	case s.Archived:
		return Source{}, problems.SourceUnlicensed.New("source %s is archived and takes no new ingests", s.Name)
	case Unlicensed(s.Licence):
		return Source{}, problems.SourceUnlicensed.New("source %s has no usable licence (%q); a person sets the corpus's licence with sources.edit before it is ingested — no licence, no ingest",
			s.Name, s.Licence)
	}
	return s, nil
}

// NewInput is the body of sources.new.
type NewInput struct {
	Name        string
	Licence     string
	Kind        string
	Languages   []string
	URL         string
	Description string
}

// New registers a source, eval-only until a person clears it. A source with that name already registered is a
// conflict. The licence may be one that names no licence (it is recorded), but such a source is never ingested.
func New(ctx context.Context, tx pgx.Tx, in NewInput, actor auth.Actor, now time.Time) (Source, []events.Draft, error) {
	in.Licence = strings.TrimSpace(in.Licence)
	var bad []problems.FieldError
	if !sourceName.MatchString(in.Name) {
		bad = append(bad, problems.FieldError{Path: "/name", Message: "must be 2–100 lowercase letters, digits, dots, dashes or underscores"})
	}
	if in.Licence == "" {
		bad = append(bad, problems.FieldError{Path: "/licence", Message: "must not be empty"})
	}
	known := false
	for _, k := range SourceKinds {
		known = known || k == in.Kind
	}
	if !known {
		bad = append(bad, problems.FieldError{Path: "/kind", Message: "must be one of " + strings.Join(SourceKinds, ", ")})
	}
	if len(bad) > 0 {
		return Source{}, nil, problems.Validation(bad)
	}
	langs := []string{}
	for _, l := range in.Languages {
		if l = strings.TrimSpace(l); l != "" && !containsFold(langs, l) {
			langs = append(langs, l)
		}
	}
	tag, err := tx.Exec(ctx, `INSERT INTO sources (id, name, description, licence, kind, languages, url, created_by, created_at,
		updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9) ON CONFLICT (name) DO NOTHING`,
		"src_"+uuid.Must(uuid.NewV7()).String(), in.Name, in.Description, in.Licence, in.Kind, langs, strings.TrimSpace(in.URL), actor, now)
	if err != nil {
		return Source{}, nil, fmt.Errorf("insert source: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Source{}, nil, problems.Conflict.New("a source named %s is already registered (sources.get %s)", in.Name, in.Name)
	}
	s, err := getSource(ctx, tx, in.Name, "")
	if err != nil {
		return Source{}, nil, err
	}
	if err := recordClearance(ctx, tx, s.ID, ChangeCreated, s.Licence, false, actor, now); err != nil {
		return Source{}, nil, err
	}
	if s, err = GetSource(ctx, tx, s.ID); err != nil {
		return Source{}, nil, err
	}
	return s, []events.Draft{SourceEvent(s, "source.created")}, nil
}

func recordClearance(ctx context.Context, tx pgx.Tx, sourceID, change, licence string, cleared bool, actor auth.Actor, now time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO source_clearances (source_id, change, licence, training_cleared, actor, at)
		VALUES ($1, $2, $3, $4, $5, $6)`, sourceID, change, licence, cleared, actor, now); err != nil {
		return fmt.Errorf("record source clearance: %w", err)
	}
	return nil
}

// recordIngest writes the ingest history of the sources of a newly registered dataset version (one row per source,
// counted from the version's memberships).
func recordIngest(ctx context.Context, tx pgx.Tx, versionID string, lin lineage, now time.Time) error {
	if _, err := tx.Exec(ctx, `INSERT INTO source_ingests (version_id, source_id, pipeline_run_id, project_id, step_kind,
			utterances, hours, at)
		SELECT d.version_id, u.source_id, $2, $3, $4, count(*)::int, sum(u.duration_s) / 3600.0, $5
		FROM dataset_utterances d JOIN utterances u ON u.id = d.utterance_id WHERE d.version_id = $1
		GROUP BY d.version_id, u.source_id ON CONFLICT DO NOTHING`,
		versionID, lin.PipelineRunID, lin.ProjectID, lin.StepKind, now); err != nil {
		return fmt.Errorf("record source ingest: %w", err)
	}
	return nil
}

// withHistory loads a source's clearance and ingest history.
func withHistory(ctx context.Context, q storage.Querier, s *Source) error {
	rows, err := q.Query(ctx, `SELECT change, licence, training_cleared, actor, at FROM source_clearances WHERE source_id = $1
		ORDER BY id`, s.ID)
	if err != nil {
		return fmt.Errorf("query source clearances: %w", err)
	}
	s.Clearances, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Clearance, error) {
		var c Clearance
		return c, row.Scan(&c.Change, &c.Licence, &c.TrainingCleared, &c.Actor, &c.At)
	})
	if err != nil {
		return fmt.Errorf("read source clearances: %w", err)
	}
	rows, err = q.Query(ctx, `SELECT i.version_id, i.pipeline_run_id, i.project_id, i.step_kind, coalesce(v.state = 'frozen', false),
		i.utterances, i.hours, i.at FROM source_ingests i LEFT JOIN registry_versions v ON v.id = i.version_id
		WHERE i.source_id = $1 ORDER BY i.at DESC, i.version_id LIMIT $2`, s.ID, maxIngests)
	if err != nil {
		return fmt.Errorf("query source ingests: %w", err)
	}
	s.Ingests, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (Ingest, error) {
		var i Ingest
		return i, row.Scan(&i.VersionID, &i.PipelineRunID, &i.ProjectID, &i.StepKind, &i.Frozen, &i.Utterances, &i.Hours, &i.At)
	})
	if err != nil {
		return fmt.Errorf("read source ingests: %w", err)
	}
	return nil
}
