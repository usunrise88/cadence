// Package evals is the evaluation block (docs/spec/04-blocks.md Block 3; R20–R24, R43, R54;
// docs/review/2026-10-02-phase-3-plan.md stream E): evals over a generated pipeline, the global eval-record cache,
// the paired blockwise bootstrap, the project's gate (gates.yaml) and model registration. Like internal/runs it is a
// facade over the pipeline engine and never names a model family: the step kinds of each role come from the family
// descriptor the model's runtime published (R41).
package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity kinds, statuses and event types.
const (
	Kind = "eval"

	StatusQueued  = "queued"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"

	RoleSubject  = "subject"
	RoleBaseline = "baseline"

	CellCached  = "cached"
	CellQueued  = "queued"
	CellRunning = "running"
	CellDone    = "done"
	CellFailed  = "failed"

	EventCreated  = "eval.created"
	EventStatus   = "eval.status_changed"
	EventProgress = "eval.progress"
	EventGated    = "eval.gated"
)

// Artifact types an eval reads and writes (R42 and the phase-3 plan).
const (
	TypeScores     = "scores"
	TypeHypotheses = "hypotheses"
	TypeNormalizer = "normalizer"
	TypeBoostList  = "boost_list"
	TypeDataset    = "dataset"
	TypeCheckpoint = runs.TypeCheckpoint
	TypeBaseModel  = runs.TypeBaseModel
)

// Roles of a family descriptor an eval uses, and the scorer: a neutral core kind every runtime publishes.
const (
	RoleMaterialize = "materialize"
	RoleTranscribe  = "transcribe"
	ScorerKind      = "wer_score"
)

// ProgressTopic is eval.{id}.progress: cells done and total and the eval's status.
func ProgressTopic(id string) string { return "eval." + id + ".progress" }

// Service runs evals on the pipeline engine.
type Service struct {
	Pool     *pgxpool.Pool
	Engine   *pipelines.Engine
	CAS      *cas.Store
	Runs     *runs.Service
	Repo     func() pipelines.Repo     // the project repositories; nil: no repository (defaults only)
	Defaults func() *defaults.Defaults // defaults.Get when nil
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func (s *Service) repo() pipelines.Repo {
	if s.Repo == nil {
		return nil
	}
	return s.Repo()
}

// Install registers the scores hook (eval records) and the engine observer that mirrors an eval's pipeline run onto
// the eval. Both are idempotent, so two servers on one engine (tests) only repeat no-ops.
func (s *Service) Install(hooks *steps.Hooks) {
	hooks.On(TypeScores, s.scoresHook)
	hooks.On(TypeMetricScores, s.metricsHook)
	s.Engine.SetNamedObserver("evals", s.Observe)
}

// Model is one model an eval compares: the subject or the baseline (the contract's EvalModel).
type Model struct {
	Kind     string `json:"kind"` // checkpoint | model | base_model
	ID       string `json:"id"`
	Label    string `json:"label"`
	ModelKey string `json:"modelKey"`
	Family   string `json:"family"`
	RunID    string `json:"runId,omitempty"`
	Source   string `json:"source,omitempty"` // baseline: request | alias | project-default

	family   family
	artifact steps.ArtifactRef // a checkpoint artifact, or the rendered base_model artifact of a base model
	base     registry.Version  // the base model version (itself for a base model): its locale tags
}

// Profile is a latency profile of a family descriptor (R43).
type Profile struct {
	Name      string  `json:"name"`
	LatencyMs float64 `json:"latencyMs"`
	Label     string  `json:"label,omitempty"`
}

// GoldenSet is a golden set version an eval scores on (the contract's EvalGoldenSet).
type GoldenSet struct {
	VersionID           string  `json:"versionId"`
	Name                string  `json:"name"`
	Version             string  `json:"version"`
	NormalizerVersionID string  `json:"normalizerVersionId"`
	Locale              string  `json:"locale"`
	Domain              string  `json:"domain,omitempty"`
	Utterances          int     `json:"utterances"`
	Hours               float64 `json:"hours"`
	Groups              string  `json:"groups"`
	Replay              bool    `json:"replay,omitempty"` // a replay golden set: scored at the primary profile only
	// DecodeAs is the language the models decode the set in when evals.new mapped its locale (Languages); empty
	// means the set's own locale.
	DecodeAs string `json:"decodeAs,omitempty"`

	datasetHash string
}

// decodeLanguage is the language a transcribe step decodes the set in.
func (g GoldenSet) decodeLanguage() string {
	if g.DecodeAs != "" {
		return g.DecodeAs
	}
	return g.Locale
}

// Decoding is one decoding variant of an eval (R24; the contract's EvalDecoding).
type Decoding struct {
	Index    int      `json:"index"`
	Boost    string   `json:"boost"` // none | lang/<locale>/boost/<file>@<commit>
	Weight   *float64 `json:"weight,omitempty"`
	Terms    int      `json:"terms,omitempty"`
	Artifact string   `json:"artifact,omitempty"`

	ref *steps.ArtifactRef
}

// Estimate is what the cells to compute cost (the contract's EvalEstimate).
type Estimate struct {
	GPUHours             float64 `json:"gpuHours"`
	AudioHours           float64 `json:"audioHours"`
	CellsToCompute       int     `json:"cellsToCompute"`
	GPUHoursPerAudioHour float64 `json:"gpuHoursPerAudioHour"`
	Basis                string  `json:"basis"`
}

// Eval is an evals row.
type Eval struct {
	ID             string
	ProjectID      string
	Status         string
	Error          string
	Subject        Model
	Baseline       Model
	GoldenSets     []GoldenSet
	Profiles       []Profile
	PrimaryProfile string
	Decoding       []Decoding
	Augmentations  []Augmentation
	Significance   Significance
	Estimate       Estimate
	PipelineRunID  string
	Gate           json.RawMessage
	GatedAt        *time.Time
	Actor          auth.Actor
	Rev            int
	CreatedAt      time.Time
	UpdatedAt      time.Time
	FinishedAt     *time.Time
}

// Cell is an eval_cells row.
type Cell struct {
	ID                  string
	EvalID              string
	Position            int
	Role                string
	GoldenSetVersionID  string
	NormalizerVersionID string
	Profile             string
	DecodingIndex       int
	AugmentationIndex   int
	DecodingHash        string
	ModelKey            string
	Scorer              string
	State               string
	RecordID            string
	ScoreStep           string
	Delta               json.RawMessage
	Metrics             map[string]MetricPlan
}

// sameCell reports whether two cells sit at the same golden set, profile, decoding and augmentation.
func (c Cell) sameCell(o Cell) bool {
	return c.GoldenSetVersionID == o.GoldenSetVersionID && c.Profile == o.Profile && c.DecodingIndex == o.DecodingIndex &&
		c.AugmentationIndex == o.AugmentationIndex
}

const evalCols = `id, project_id, status, coalesce(error, ''), subject, baseline, golden_sets, profiles, primary_profile, decoding,
	significance, estimate, coalesce(pipeline_run_id, ''), gate, gated_at, actor, rev, created_at, updated_at, finished_at, augmentations`

func scanEval(row pgx.CollectableRow) (Eval, error) {
	var e Eval
	var subject, baseline, gs, profiles, decoding, sig, est, augs []byte
	if err := row.Scan(&e.ID, &e.ProjectID, &e.Status, &e.Error, &subject, &baseline, &gs, &profiles, &e.PrimaryProfile, &decoding,
		&sig, &est, &e.PipelineRunID, &e.Gate, &e.GatedAt, &e.Actor, &e.Rev, &e.CreatedAt, &e.UpdatedAt, &e.FinishedAt, &augs); err != nil {
		return Eval{}, err
	}
	for _, f := range []struct {
		b []byte
		v any
	}{{subject, &e.Subject}, {baseline, &e.Baseline}, {gs, &e.GoldenSets}, {profiles, &e.Profiles}, {decoding, &e.Decoding},
		{sig, &e.Significance}, {est, &e.Estimate}, {augs, &e.Augmentations}} {
		if err := json.Unmarshal(f.b, f.v); err != nil {
			return Eval{}, fmt.Errorf("decode eval %s: %w", e.ID, err)
		}
	}
	return e, nil
}

func getEval(ctx context.Context, q storage.Querier, id, lock string) (Eval, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+evalCols+" FROM evals WHERE id = $1 "+lock, id)
	if err != nil {
		return Eval{}, false, fmt.Errorf("read eval %s: %w", id, err)
	}
	e, err := pgx.CollectExactlyOneRow(rows, scanEval)
	if errors.Is(err, pgx.ErrNoRows) {
		return Eval{}, false, nil
	}
	if err != nil {
		return Eval{}, false, fmt.Errorf("read eval %s: %w", id, err)
	}
	return e, true, nil
}

// Get reads eval id or answers not-found.
func Get(ctx context.Context, q storage.Querier, id string) (Eval, error) {
	e, found, err := getEval(ctx, q, id, "")
	if err == nil && !found {
		err = problems.NotFound.New("no eval %q", id)
	}
	return e, err
}

// ProjectOf returns the project of eval id (not-found when there is none).
func ProjectOf(ctx context.Context, q storage.Querier, id string) (string, error) {
	e, err := Get(ctx, q, id)
	return e.ProjectID, err
}

// ListFilter narrows evals.list.
type ListFilter struct {
	ProjectID string
	Status    string
	Subject   string
	Limit     int
}

// List returns the project's evals, newest first.
func List(ctx context.Context, q storage.Querier, f ListFilter) ([]Eval, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := q.Query(ctx, "SELECT "+evalCols+` FROM evals WHERE project_id = $1 AND ($2 = '' OR status = $2)
		AND ($3 = '' OR subject_id = $3) ORDER BY created_at DESC, id DESC LIMIT $4`, f.ProjectID, f.Status, f.Subject, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list evals: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanEval)
	if err != nil {
		return nil, fmt.Errorf("read evals: %w", err)
	}
	return out, nil
}

func insertEval(ctx context.Context, tx pgx.Tx, e Eval) error {
	augs := e.Augmentations
	if len(augs) == 0 {
		augs = []Augmentation{{Index: 0, Profile: AugmentNone}}
	}
	_, err := tx.Exec(ctx, `INSERT INTO evals (id, project_id, status, error, subject, subject_id, baseline, golden_sets, profiles,
			primary_profile, decoding, significance, estimate, actor, augmentations)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		e.ID, e.ProjectID, e.Status, e.Error, mustJSON(e.Subject), e.Subject.ID, mustJSON(e.Baseline), mustJSON(e.GoldenSets),
		mustJSON(e.Profiles), e.PrimaryProfile, mustJSON(e.Decoding), mustJSON(e.Significance), mustJSON(e.Estimate), e.Actor, mustJSON(augs))
	if err != nil {
		return fmt.Errorf("insert eval: %w", err)
	}
	return nil
}

// saveStatus writes status, error and finishedAt, bumping the revision.
func saveStatus(ctx context.Context, tx pgx.Tx, e *Eval) error {
	err := tx.QueryRow(ctx, `UPDATE evals SET status = $2, error = NULLIF($3, ''), finished_at = $4, rev = rev + 1, updated_at = now()
		WHERE id = $1 RETURNING rev, updated_at`, e.ID, e.Status, e.Error, e.FinishedAt).Scan(&e.Rev, &e.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save eval %s: %w", e.ID, err)
	}
	return nil
}

const cellCols = `id, eval_id, position, role, golden_set_version_id, normalizer_version_id, profile, decoding_index, decoding_hash,
	model_key, scorer, state, coalesce(record_id, ''), coalesce(score_step, ''), delta, augmentation_index, metrics`

func scanCell(row pgx.CollectableRow) (Cell, error) {
	var c Cell
	var metrics []byte
	err := row.Scan(&c.ID, &c.EvalID, &c.Position, &c.Role, &c.GoldenSetVersionID, &c.NormalizerVersionID, &c.Profile, &c.DecodingIndex,
		&c.DecodingHash, &c.ModelKey, &c.Scorer, &c.State, &c.RecordID, &c.ScoreStep, &c.Delta, &c.AugmentationIndex, &metrics)
	if err == nil && len(metrics) > 0 {
		err = json.Unmarshal(metrics, &c.Metrics)
	}
	return c, err
}

func cellsOf(ctx context.Context, q storage.Querier, evalID string) ([]Cell, error) {
	rows, err := q.Query(ctx, "SELECT "+cellCols+" FROM eval_cells WHERE eval_id = $1 ORDER BY position", evalID)
	if err != nil {
		return nil, fmt.Errorf("read cells of %s: %w", evalID, err)
	}
	out, err := pgx.CollectRows(rows, scanCell)
	if err != nil {
		return nil, fmt.Errorf("read cells of %s: %w", evalID, err)
	}
	return out, nil
}

func insertCell(ctx context.Context, tx pgx.Tx, c Cell) error {
	var metrics []byte
	if len(c.Metrics) > 0 {
		metrics = mustJSON(c.Metrics)
	}
	_, err := tx.Exec(ctx, `INSERT INTO eval_cells (id, eval_id, position, role, golden_set_version_id, normalizer_version_id, profile,
			decoding_index, decoding_hash, model_key, scorer, state, record_id, score_step, augmentation_index, metrics)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''), NULLIF($14, ''), $15, $16)`,
		c.ID, c.EvalID, c.Position, c.Role, c.GoldenSetVersionID, c.NormalizerVersionID, c.Profile, c.DecodingIndex, c.DecodingHash,
		c.ModelKey, c.Scorer, c.State, c.RecordID, c.ScoreStep, c.AugmentationIndex, metrics)
	if err != nil {
		return fmt.Errorf("insert eval cell: %w", err)
	}
	return nil
}

// Record is an eval_records row: one scored cell shared by every project.
type Record struct {
	ID                  string
	ModelKey            string
	GoldenSetVersionID  string
	NormalizerVersionID string
	DecodingHash        string
	Scorer              string
	Profile             string
	Decoding            json.RawMessage
	Scores              string
	Hypotheses          string
	Summary             json.RawMessage
	Family              string
}

const recordCols = `id, model_key, golden_set_version_id, normalizer_version_id, decoding_hash, scorer, profile, decoding, scores_hash,
	coalesce(hypotheses_hash, ''), summary, family`

func scanRecord(row pgx.CollectableRow) (Record, error) {
	var r Record
	err := row.Scan(&r.ID, &r.ModelKey, &r.GoldenSetVersionID, &r.NormalizerVersionID, &r.DecodingHash, &r.Scorer, &r.Profile, &r.Decoding,
		&r.Scores, &r.Hypotheses, &r.Summary, &r.Family)
	return r, err
}

// Key is the identity of an eval record (decision 4 of the phase-3 plan).
type Key struct {
	ModelKey            string
	GoldenSetVersionID  string
	NormalizerVersionID string
	DecodingHash        string
	Scorer              string
}

func (k Key) String() string {
	return strings.Join([]string{k.ModelKey, k.GoldenSetVersionID, k.NormalizerVersionID, k.DecodingHash, k.Scorer}, "|")
}

func findRecord(ctx context.Context, q storage.Querier, k Key) (Record, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+recordCols+` FROM eval_records WHERE model_key = $1 AND golden_set_version_id = $2
		AND normalizer_version_id = $3 AND decoding_hash = $4 AND scorer = $5`, k.ModelKey, k.GoldenSetVersionID, k.NormalizerVersionID,
		k.DecodingHash, k.Scorer)
	if err != nil {
		return Record{}, false, fmt.Errorf("find eval record: %w", err)
	}
	r, err := pgx.CollectExactlyOneRow(rows, scanRecord)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("find eval record: %w", err)
	}
	return r, true, nil
}

func recordsByID(ctx context.Context, q storage.Querier, ids []string) (map[string]Record, error) {
	out := map[string]Record{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, "SELECT "+recordCols+" FROM eval_records WHERE id = ANY($1)", ids)
	if err != nil {
		return nil, fmt.Errorf("read eval records: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanRecord)
	if err != nil {
		return nil, fmt.Errorf("read eval records: %w", err)
	}
	for _, r := range list {
		out[r.ID] = r
	}
	return out, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("null")
	}
	return b
}

// progressDraft is eval.progress on eval.{id}.progress.
func progressDraft(e Eval, done, total int) events.Draft {
	return events.Draft{
		Topic: ProgressTopic(e.ID), Type: EventProgress, ProjectID: e.ProjectID,
		Entity: &events.EntityRef{Kind: Kind, ID: e.ID, Rev: e.Rev},
		Payload: map[string]any{"eval": map[string]any{"id": e.ID, "projectId": e.ProjectID, "status": e.Status, "error": e.Error,
			"rev": e.Rev, "cellsDone": done, "cellsTotal": total}},
	}
}

// entityDraft is an eval.* event on entity.eval.{id}; panels re-read evals.get.
func entityDraft(e Eval, typ string) events.Draft {
	return events.Draft{
		Topic: events.EntityTopic(Kind, e.ID), Type: typ, ProjectID: e.ProjectID,
		Entity: &events.EntityRef{Kind: Kind, ID: e.ID, Rev: e.Rev},
		Payload: map[string]any{"eval": map[string]any{"id": e.ID, "projectId": e.ProjectID, "status": e.Status, "error": e.Error,
			"rev": e.Rev, "subject": e.Subject, "pipelineRunId": e.PipelineRunID}},
	}
}

// counts reads how many of an eval's cells have scores and how many there are.
func counts(ctx context.Context, q storage.Querier, evalID string) (done, total int, err error) {
	err = q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE record_id IS NOT NULL), count(*) FROM eval_cells WHERE eval_id = $1`, evalID).
		Scan(&done, &total)
	if err != nil {
		err = fmt.Errorf("count cells of %s: %w", evalID, err)
	}
	return done, total, err
}
