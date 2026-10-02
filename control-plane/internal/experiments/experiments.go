// Package experiments groups the training runs that answer one question (docs/spec/04-blocks.md "Experiments and
// sweeps"): an experiment pins a mix revision and a base model version; runs.new with its id and the runs a sweep
// generates train on exactly those. A sweep turns a grid or a random draw over recipe parameters into runs that are
// started one after another (the next when the previous ended) under a GPU-hour cap. Every run goes through the runs
// service's Prepare and Create, the runs.new path, so recipes, estimates, language checks and output hooks apply
// unchanged. Like internal/runs it never names a model family.
package experiments

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity kind and event types; every event goes out on entity.experiment.{id}.
const (
	Kind = "experiment"

	EventCreated    = "experiment.created"
	EventRunChanged = "experiment.run_changed"
	EventSweepStart = "sweep.started"
	EventSweepStep  = "sweep.progress"
	EventSweepEnd   = "sweep.ended"
)

// Sweep modes and states (the contract's SweepMode and SweepState).
const (
	ModeGrid   = "grid"
	ModeRandom = "random"

	StateRunning   = "running"
	StateDone      = "done"
	StateStopped   = "stopped"
	StateCancelled = "cancelled"
	StateFailed    = "failed"
)

// ReplayShare is the sweep parameter that varies the mix's replay share (the mix revision stays the experiment's).
const ReplayShare = "replayShare"

// Topic is entity.experiment.{id}.
func Topic(id string) string { return events.EntityTopic(Kind, id) }

// Service keeps experiments and drives sweeps over the runs service.
type Service struct {
	Pool     *pgxpool.Pool
	Runs     *runs.Service
	Defaults func() *defaults.Defaults // defaults.Get when nil
	// RenderVersion renders the base model version as the contract's RegistryVersion; its summary when nil.
	RenderVersion func(registry.Version) any
	// Now is the clock of the budget check (time.Now when nil).
	Now func() time.Time
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) renderVersion(v registry.Version) any {
	if s.RenderVersion != nil {
		return s.RenderVersion(v)
	}
	return v.Summary()
}

// Install makes the runs service report run changes here: a sweep starts its next run when its current one ended,
// and an experiment's document hears about its runs.
func (s *Service) Install() { s.Runs.OnStatus = s.RunChanged }

// row is an experiments row.
type row struct {
	ID            string
	ProjectID     string
	Name          string
	Question      string
	Tag           string
	MixID         string
	MixRev        int
	BaseVersionID string
	Actor         auth.Actor
	Rev           int
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

const expCols = `id, project_id, name, question, tag, mix_id, mix_rev, base_version_id, actor, rev, created_at, updated_at`

func scanExp(r pgx.CollectableRow) (row, error) {
	var x row
	err := r.Scan(&x.ID, &x.ProjectID, &x.Name, &x.Question, &x.Tag, &x.MixID, &x.MixRev, &x.BaseVersionID, &x.Actor, &x.Rev,
		&x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func getExp(ctx context.Context, q storage.Querier, id, lock string) (row, error) {
	rows, err := q.Query(ctx, "SELECT "+expCols+" FROM experiments WHERE id = $1 "+lock, id)
	if err != nil {
		return row{}, fmt.Errorf("read experiment %s: %w", id, err)
	}
	x, err := pgx.CollectExactlyOneRow(rows, scanExp)
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, problems.NotFound.New("no experiment %q", id)
	}
	if err != nil {
		return row{}, fmt.Errorf("read experiment %s: %w", id, err)
	}
	return x, nil
}

// ProjectOf returns the project experiment id belongs to (for scope checks).
func ProjectOf(ctx context.Context, q storage.Querier, id string) (string, error) {
	x, err := getExp(ctx, q, id, "")
	return x.ProjectID, err
}

// ---------------------------------------------------------------- experiments.new

// NewInput is experiments.new.
type NewInput struct {
	ProjectID   string
	Name        string
	Question    string
	Mix         string
	MixRevision int
	BaseModel   string
	Tag         string
	Actor       auth.Actor
}

// Prepared is an experiment checked and pinned, not yet written.
type Prepared struct {
	row
	MixName     string
	ReplayShare float64
	Base        registry.Version
}

var nonTag = regexp.MustCompile(`[^a-z0-9._-]+`)

// tagOf derives a tag from a name: lower case, runs of other characters as one dash, at most 63 characters.
func tagOf(name string) string {
	t := strings.Trim(nonTag.ReplaceAllString(strings.ToLower(name), "-"), "-._")
	if len(t) > 63 {
		t = strings.Trim(t[:63], "-._")
	}
	if t == "" {
		t = "experiment"
	}
	return t
}

// Prepare checks an experiments.new request: a unique name, a mix revision of the project, a base model version
// whose model family is published. Nothing is written.
func (s *Service) Prepare(ctx context.Context, q storage.Querier, in NewInput) (Prepared, error) {
	d := s.defaults()
	name, question := strings.TrimSpace(in.Name), strings.TrimSpace(in.Question)
	var fields []problems.FieldError
	if name == "" {
		fields = append(fields, problems.FieldError{Path: "/name", Message: "name the experiment"})
	}
	if question == "" {
		fields = append(fields, problems.FieldError{Path: "/question", Message: "say what the experiment answers"})
	}
	if len(fields) > 0 {
		return Prepared{}, problems.Validation(fields)
	}
	if _, err := projects.GetByID(ctx, q, in.ProjectID); err != nil {
		return Prepared{}, err
	}
	var taken bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM experiments WHERE project_id = $1 AND name = $2)", in.ProjectID, name).
		Scan(&taken); err != nil {
		return Prepared{}, fmt.Errorf("check experiment name: %w", err)
	}
	if taken {
		return Prepared{}, problems.Conflict.New("the project has an experiment named %q; pick another name or open it with experiments.list", name)
	}
	mixID, content, rev, err := runs.ResolveMix(ctx, q, in.ProjectID, strings.TrimSpace(in.Mix), in.MixRevision)
	if err != nil {
		return Prepared{}, err
	}
	base, err := registry.Resolve(ctx, q, in.ProjectID, registry.KindBaseModel, or(strings.TrimSpace(in.BaseModel), d.Wizard.BaseModel.Value))
	if err != nil {
		return Prepared{}, err
	}
	if _, err := runs.FamilyOf(ctx, q, base); err != nil {
		return Prepared{}, err
	}
	tag := strings.TrimSpace(in.Tag)
	if tag == "" {
		tag = tagOf(name)
	}
	now := s.now().UTC()
	return Prepared{
		row: row{ID: "exp_" + uuid.Must(uuid.NewV7()).String(), ProjectID: in.ProjectID, Name: name, Question: question, Tag: tag,
			MixID: mixID, MixRev: rev, BaseVersionID: base.ID, Actor: in.Actor, Rev: 1, CreatedAt: now, UpdatedAt: now},
		MixName: content.Name, ReplayShare: content.ReplayShare, Base: base,
	}, nil
}

// Create writes a prepared experiment and its experiment.created event.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, p Prepared) (View, []events.Draft, error) {
	rows, err := tx.Query(ctx, `INSERT INTO experiments (id, project_id, name, question, tag, mix_id, mix_rev, base_version_id, actor)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING `+expCols,
		p.ID, p.ProjectID, p.Name, p.Question, p.Tag, p.MixID, p.MixRev, p.BaseVersionID, p.Actor)
	if err != nil {
		return View{}, nil, fmt.Errorf("insert experiment: %w", err)
	}
	x, err := pgx.CollectExactlyOneRow(rows, scanExp)
	if err != nil {
		return View{}, nil, fmt.Errorf("insert experiment: %w", err)
	}
	v, err := s.view(ctx, tx, x, true)
	if err != nil {
		return View{}, nil, err
	}
	return v, []events.Draft{expDraft(x, EventCreated, map[string]any{"name": x.Name})}, nil
}

// Preview is the experiment a dry run answers: what Create would write.
func (s *Service) Preview(ctx context.Context, q storage.Querier, p Prepared) (View, error) {
	return s.shell(ctx, q, p.row)
}

// ---------------------------------------------------------------- runs of an experiment

// ForRun makes a runs.new input a run of experiment ref (exp_…) in project projectID: the run trains on the
// experiment's mix revision from its base model. A mix or base model the request names must be the experiment's.
func (s *Service) ForRun(ctx context.Context, q storage.Querier, projectID, ref string, in *runs.NewInput) error {
	x, err := getExp(ctx, q, strings.TrimSpace(ref), "")
	if err != nil {
		return err
	}
	if x.ProjectID != projectID {
		return problems.NotFound.New("the project has no experiment %q", ref)
	}
	var fields []problems.FieldError
	if in.Init == runs.InitCheckpoint || in.Checkpoint != "" {
		fields = append(fields, problems.FieldError{Path: "/init", Message: fmt.Sprintf("the runs of experiment %s start from its base model (init base)", x.ID)})
	}
	if in.Mix != "" {
		mixID, _, rev, err := runs.ResolveMix(ctx, q, projectID, in.Mix, in.MixRevision)
		if err != nil {
			return err
		}
		if mixID != x.MixID || rev != x.MixRev {
			fields = append(fields, problems.FieldError{Path: "/mix", Message: fmt.Sprintf("experiment %s trains on mix %s revision %d; leave mix out or name that", x.ID, x.MixID, x.MixRev)})
		}
	}
	if in.BaseModel != "" {
		base, err := registry.Resolve(ctx, q, projectID, registry.KindBaseModel, in.BaseModel)
		if err != nil {
			return err
		}
		if base.ID != x.BaseVersionID {
			fields = append(fields, problems.FieldError{Path: "/baseModel", Message: fmt.Sprintf("experiment %s trains from base model version %s; leave baseModel out or name that", x.ID, x.BaseVersionID)})
		}
	}
	if len(fields) > 0 {
		return problems.Validation(fields)
	}
	in.Init, in.Mix, in.MixRevision, in.BaseModel, in.ExperimentID = runs.InitBase, x.MixID, x.MixRev, x.BaseVersionID, x.ID
	return nil
}

// RunChanged is the runs service's OnStatus: a run of an experiment was created or changed status. The experiment's
// document hears of it; when the run is the current run of a running sweep and it ended, the sweep goes on (or is
// cancelled with it).
func (s *Service) RunChanged(ctx context.Context, tx pgx.Tx, c runs.StatusChange) ([]events.Draft, error) {
	if c.ExperimentID == "" {
		return nil, nil
	}
	x, err := getExp(ctx, tx, c.ExperimentID, "")
	if err != nil {
		return nil, err
	}
	drafts := []events.Draft{expDraft(x, EventRunChanged, map[string]any{"run": map[string]any{
		"id": c.RunID, "status": c.Status, "error": c.Error, "sweepId": c.SweepID, "created": c.Created}})}
	if c.SweepID == "" || c.Created || !runs.Ended(c.Status) {
		return drafts, nil
	}
	sw, err := getSweep(ctx, tx, c.SweepID, "FOR UPDATE")
	if err != nil {
		return nil, err
	}
	if sw.State != StateRunning || sw.CurrentRunID != c.RunID {
		return drafts, nil
	}
	var more []events.Draft
	if c.Status == runs.StatusCancelled {
		more, err = s.finish(ctx, tx, x, &sw, StateCancelled, fmt.Sprintf("run %s was cancelled", c.RunID))
	} else {
		more, err = s.advance(ctx, tx, x, &sw, false)
	}
	if err != nil {
		return nil, err
	}
	return append(drafts, more...), nil
}

// expDraft is an event on entity.experiment.{id}; documents re-read experiments.get.
func expDraft(x row, typ string, payload map[string]any) events.Draft {
	body := map[string]any{"id": x.ID, "projectId": x.ProjectID, "rev": x.Rev}
	for k, v := range payload {
		body[k] = v
	}
	return events.Draft{Topic: Topic(x.ID), Type: typ, ProjectID: x.ProjectID, Entity: &events.EntityRef{Kind: Kind, ID: x.ID, Rev: x.Rev},
		Payload: map[string]any{"experiment": body}}
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
