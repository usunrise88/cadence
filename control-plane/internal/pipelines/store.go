package pipelines

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity kinds in references.
const (
	RunKind  = "pipeline_run"
	StepKind = "pipeline_step"
)

// Pipeline run states.
const (
	RunRunning   = "running"
	RunDone      = "done"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Step states.
const (
	StepWaiting   = "waiting"   // an input is not produced yet
	StepQueued    = "queued"    // its step job waits for a worker
	StepRunning   = "running"   // a worker leased it
	StepDone      = "done"      // finished; outputs recorded
	StepReused    = "reused"    // a finished step with the same input hash supplied the outputs
	StepFailed    = "failed"    // the last attempt failed and no automatic retry is left
	StepSkipped   = "skipped"   // the run failed before the step could start
	StepCancelled = "cancelled" // the run (or the step job) was cancelled
)

// Attempt reasons.
const (
	ReasonInitial = "initial"
	ReasonOOM     = "oom"
	ReasonLost    = "lost"
	ReasonRetry   = "retry"
	ReasonResume  = "resume" // a retry that continues from a training-state artifact (runs.resume)
)

// Event types on pipeline_run.{id}.
const (
	EventStarted      = "pipeline_run.started"
	EventStateChanged = "pipeline_run.state_changed"
	EventStepChanged  = "pipeline_run.step_changed"
)

// Topic is the topic of one pipeline run: pipeline_run.{id}.
func Topic(id string) string { return "pipeline_run." + id }

// Terminal reports whether a run state is an end state.
func Terminal(state string) bool {
	return state == RunDone || state == RunFailed || state == RunCancelled
}

func active(state string) bool { return state == StepQueued || state == StepRunning }

func finished(state string) bool { return state == StepDone || state == StepReused }

// optional reports whether step is marked optional in the run's pipeline.
func (r Run) optional(step string) bool {
	for _, s := range r.Definition.Steps {
		if s.ID == step {
			return s.Optional
		}
	}
	return false
}

// needsAny reports whether s reads a step in set through an input it cannot run without: an indexed input
// (hypotheses.2, one of several artifacts) from an optional step is dropped instead (Step.Optional).
func (r Run) needsAny(s StepRow, set map[string]bool) bool {
	for name, v := range s.Wiring {
		if w, ok := ParseWire(v); ok && w.Step != "" && set[w.Step] && !(Indexed(name) && r.optional(w.Step)) {
			return true
		}
	}
	return false
}

// Run is a pipeline run. Its JSON form is the contract's PipelineRun (PipelineRunSummary without steps).
type Run struct {
	ID         string                       `json:"id"`
	ProjectID  string                       `json:"projectId"`
	Pipeline   string                       `json:"pipeline"`
	Source     string                       `json:"source"`
	Ref        string                       `json:"ref,omitempty"`
	Commit     string                       `json:"commit,omitempty"`
	Version    string                       `json:"version"`
	Definition Pipeline                     `json:"-"`
	Inputs     map[string]steps.ArtifactRef `json:"inputs"`
	State      string                       `json:"state"`
	RunID      string                       `json:"runId,omitempty"`
	Fresh      bool                         `json:"fresh"`
	Priority   int                          `json:"priority"`
	Error      string                       `json:"error,omitempty"`
	Actor      auth.Actor                   `json:"actor"`
	Rev        int                          `json:"rev"`
	CreatedAt  time.Time                    `json:"createdAt"`
	UpdatedAt  time.Time                    `json:"updatedAt"`
	FinishedAt *time.Time                   `json:"finishedAt,omitempty"`
	Steps      []StepRow                    `json:"steps,omitempty"`
}

// Attempt is one execution of a step: its own step job.
type Attempt struct {
	Attempt    int              `json:"attempt"`
	JobID      string           `json:"jobId"`
	Reason     string           `json:"reason"`
	BatchScale float64          `json:"batchScale,omitempty"`
	ResumeFrom string           `json:"resumeFrom,omitempty"` // training-state artifact the attempt resumes from
	State      string           `json:"state"`                // queued | running | done | failed | cancelled
	Error      *steps.StepError `json:"error,omitempty"`
	StartedAt  *time.Time       `json:"startedAt,omitempty"`
	FinishedAt *time.Time       `json:"finishedAt,omitempty"`
}

// StepRow is a step of a pipeline run. Its JSON form is the contract's PipelineStep.
type StepRow struct {
	ID                string                       `json:"id"`
	PipelineRunID     string                       `json:"-"`
	ProjectID         string                       `json:"-"`
	Step              string                       `json:"step"`
	Position          int                          `json:"position"`
	Kind              string                       `json:"kind"`
	KindVersion       string                       `json:"kindVersion"`
	StepKindVersionID string                       `json:"stepKindVersionId,omitempty"`
	State             string                       `json:"state"`
	Params            map[string]any               `json:"params"`
	Departures        []Departure                  `json:"departures"`
	Wiring            map[string]string            `json:"in"`
	Inputs            map[string]steps.ArtifactRef `json:"inputs,omitempty"`
	Produces          map[string]string            `json:"produces"`
	Outputs           map[string]steps.ArtifactRef `json:"outputs,omitempty"`
	Resources         steps.Resources              `json:"resources"`
	SecretNames       []string                     `json:"-"`
	// Auxiliaries are the registry versions the step's parameters name (x-cadence.registryRef), resolved when the run
	// was planned; the step spec carries them to the worker.
	Auxiliaries     map[string]steps.RegistryRef `json:"-"`
	EstimateSeconds *float64                     `json:"estimateSeconds,omitempty"`
	InputHash       string                       `json:"inputHash,omitempty"`
	ReusedFrom      string                       `json:"reusedFrom,omitempty"`
	Attempts        int                          `json:"attempts"`
	AttemptLog      []Attempt                    `json:"attemptLog"`
	JobID           string                       `json:"jobId,omitempty"`
	Error           *steps.StepError             `json:"error,omitempty"`
	Metrics         map[string]float64           `json:"metrics,omitempty"`
	Rev             int                          `json:"-"`
	CreatedAt       time.Time                    `json:"-"`
	UpdatedAt       time.Time                    `json:"-"`
	StartedAt       *time.Time                   `json:"startedAt,omitempty"`
	FinishedAt      *time.Time                   `json:"finishedAt,omitempty"`
}

// lastAttempt returns the current attempt's log entry (nil before the first).
func (s *StepRow) lastAttempt() *Attempt {
	if len(s.AttemptLog) == 0 {
		return nil
	}
	return &s.AttemptLog[len(s.AttemptLog)-1]
}

func (s *StepRow) hadAttempt(reason string) bool {
	for _, a := range s.AttemptLog {
		if a.Reason == reason {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- runs

const runCols = `id, project_id, pipeline, source, ref, commit_sha, version, definition, inputs, state, coalesce(run_id, ''),
	fresh, priority, coalesce(error, ''), actor, rev, created_at, updated_at, finished_at`

func scanRun(row pgx.CollectableRow) (Run, error) {
	var r Run
	err := row.Scan(&r.ID, &r.ProjectID, &r.Pipeline, &r.Source, &r.Ref, &r.Commit, &r.Version, &r.Definition, &r.Inputs,
		&r.State, &r.RunID, &r.Fresh, &r.Priority, &r.Error, &r.Actor, &r.Rev, &r.CreatedAt, &r.UpdatedAt, &r.FinishedAt)
	if r.Inputs == nil {
		r.Inputs = map[string]steps.ArtifactRef{}
	}
	return r, err
}

func oneRun(rows pgx.Rows, err error, id string) (Run, error) {
	if err != nil {
		return Run{}, fmt.Errorf("query pipeline run: %w", err)
	}
	r, err := pgx.CollectExactlyOneRow(rows, scanRun)
	if errors.Is(err, pgx.ErrNoRows) {
		return Run{}, problems.NotFound.New("no pipeline run %q", id)
	}
	if err != nil {
		return Run{}, fmt.Errorf("read pipeline run %s: %w", id, err)
	}
	return r, nil
}

func insertRun(ctx context.Context, tx pgx.Tx, r Run) (Run, error) {
	rows, err := tx.Query(ctx, `INSERT INTO pipeline_runs (id, project_id, pipeline, source, ref, commit_sha, version, definition,
		inputs, run_id, fresh, priority, actor) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11, $12, $13)
		RETURNING `+runCols, r.ID, r.ProjectID, r.Pipeline, r.Source, r.Ref, r.Commit, r.Version, r.Definition, r.Inputs,
		r.RunID, r.Fresh, r.Priority, r.Actor)
	return oneRun(rows, err, r.ID)
}

// saveRun writes a run's state and error, bumping its revision.
func saveRun(ctx context.Context, tx pgx.Tx, r Run) (Run, error) {
	rows, err := tx.Query(ctx, `UPDATE pipeline_runs SET state = $2, error = NULLIF($3, ''), finished_at = $4, rev = rev + 1,
		updated_at = now() WHERE id = $1 RETURNING `+runCols, r.ID, r.State, r.Error, r.FinishedAt)
	return oneRun(rows, err, r.ID)
}

// GetRun returns a pipeline run without its steps.
func GetRun(ctx context.Context, q storage.Querier, id string) (Run, error) {
	rows, err := q.Query(ctx, "SELECT "+runCols+" FROM pipeline_runs WHERE id = $1", id)
	return oneRun(rows, err, id)
}

func lockRun(ctx context.Context, tx pgx.Tx, id string) (Run, error) {
	rows, err := tx.Query(ctx, "SELECT "+runCols+" FROM pipeline_runs WHERE id = $1 FOR UPDATE", id)
	return oneRun(rows, err, id)
}

// Get returns a pipeline run with its steps in execution order.
func Get(ctx context.Context, q storage.Querier, id string) (Run, error) {
	r, err := GetRun(ctx, q, id)
	if err != nil {
		return Run{}, err
	}
	r.Steps, err = stepsOf(ctx, q, id, false)
	return r, err
}

// ListFilter narrows ListRuns.
type ListFilter struct {
	ProjectID string
	State     string
	Pipeline  string
	RunID     string
	Limit     int
}

// ListRuns returns pipeline runs without their steps, newest first.
func ListRuns(ctx context.Context, q storage.Querier, f ListFilter) ([]Run, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, "SELECT "+runCols+` FROM pipeline_runs WHERE ($1 = '' OR project_id = $1) AND ($2 = '' OR state = $2)
		AND ($3 = '' OR pipeline = $3) AND ($4 = '' OR run_id = $4) ORDER BY created_at DESC, id DESC LIMIT $5`,
		f.ProjectID, f.State, f.Pipeline, f.RunID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list pipeline runs: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanRun)
	if err != nil {
		return nil, fmt.Errorf("read pipeline runs: %w", err)
	}
	return out, nil
}

// ---------------------------------------------------------------- steps

const stepCols = `id, pipeline_run_id, project_id, step, position, kind, kind_version, step_kind_version_id, state, params,
	departures, wiring, inputs, produces, outputs, resources, secret_names, estimate_seconds, coalesce(input_hash, ''),
	coalesce(reused_from, ''), attempts, attempt_log, coalesce(job_id, ''), error, metrics, rev, created_at, updated_at,
	started_at, finished_at, auxiliaries`

func scanStep(row pgx.CollectableRow) (StepRow, error) {
	var s StepRow
	err := row.Scan(&s.ID, &s.PipelineRunID, &s.ProjectID, &s.Step, &s.Position, &s.Kind, &s.KindVersion, &s.StepKindVersionID,
		&s.State, &s.Params, &s.Departures, &s.Wiring, &s.Inputs, &s.Produces, &s.Outputs, &s.Resources, &s.SecretNames,
		&s.EstimateSeconds, &s.InputHash, &s.ReusedFrom, &s.Attempts, &s.AttemptLog, &s.JobID, &s.Error, &s.Metrics, &s.Rev,
		&s.CreatedAt, &s.UpdatedAt, &s.StartedAt, &s.FinishedAt, &s.Auxiliaries)
	if s.Params == nil {
		s.Params = map[string]any{}
	}
	if s.Departures == nil {
		s.Departures = []Departure{}
	}
	if s.Wiring == nil {
		s.Wiring = map[string]string{}
	}
	if s.AttemptLog == nil {
		s.AttemptLog = []Attempt{}
	}
	return s, err
}

func stepsOf(ctx context.Context, q storage.Querier, runID string, forUpdate bool) ([]StepRow, error) {
	sql := "SELECT " + stepCols + " FROM pipeline_steps WHERE pipeline_run_id = $1 ORDER BY position"
	if forUpdate {
		sql += " FOR UPDATE"
	}
	rows, err := q.Query(ctx, sql, runID)
	if err != nil {
		return nil, fmt.Errorf("query pipeline steps: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanStep)
	if err != nil {
		return nil, fmt.Errorf("read pipeline steps: %w", err)
	}
	return out, nil
}

// StepByJob returns the step whose current attempt is job jobID.
func StepByJob(ctx context.Context, q storage.Querier, jobID string) (StepRow, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+stepCols+" FROM pipeline_steps WHERE job_id = $1", jobID)
	if err != nil {
		return StepRow{}, false, fmt.Errorf("query pipeline step: %w", err)
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return StepRow{}, false, nil
	}
	if err != nil {
		return StepRow{}, false, fmt.Errorf("read pipeline step: %w", err)
	}
	return s, true, nil
}

func insertStep(ctx context.Context, tx pgx.Tx, s StepRow) error {
	if s.SecretNames == nil {
		s.SecretNames = []string{}
	}
	if s.Auxiliaries == nil {
		s.Auxiliaries = map[string]steps.RegistryRef{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO pipeline_steps (id, pipeline_run_id, project_id, step, position, kind, kind_version,
		step_kind_version_id, params, departures, wiring, produces, resources, secret_names, estimate_seconds, auxiliaries)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		s.ID, s.PipelineRunID, s.ProjectID, s.Step, s.Position, s.Kind, s.KindVersion, s.StepKindVersionID, s.Params,
		s.Departures, s.Wiring, s.Produces, s.Resources, s.SecretNames, s.EstimateSeconds, s.Auxiliaries); err != nil {
		return fmt.Errorf("insert pipeline step %s: %w", s.Step, err)
	}
	return nil
}

// saveStep writes a step's mutable fields, bumping its revision, and returns it as stored.
func saveStep(ctx context.Context, tx pgx.Tx, s StepRow) (StepRow, error) {
	rows, err := tx.Query(ctx, `UPDATE pipeline_steps SET state = $2, inputs = $3, outputs = $4, input_hash = NULLIF($5, ''),
		reused_from = NULLIF($6, ''), attempts = $7, attempt_log = $8, job_id = NULLIF($9, ''), error = $10, metrics = $11,
		started_at = $12, finished_at = $13, rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+stepCols,
		s.ID, s.State, s.Inputs, s.Outputs, s.InputHash, s.ReusedFrom, s.Attempts, s.AttemptLog, s.JobID, s.Error, s.Metrics,
		s.StartedAt, s.FinishedAt)
	if err != nil {
		return StepRow{}, fmt.Errorf("update pipeline step %s: %w", s.Step, err)
	}
	out, err := pgx.CollectExactlyOneRow(rows, scanStep)
	if err != nil {
		return StepRow{}, fmt.Errorf("update pipeline step %s: %w", s.Step, err)
	}
	return out, nil
}

// reusable returns the newest finished step of the project with inputHash.
func reusable(ctx context.Context, q storage.Querier, projectID, inputHash string) (StepRow, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+stepCols+` FROM pipeline_steps WHERE project_id = $1 AND input_hash = $2 AND state = 'done'
		ORDER BY finished_at DESC LIMIT 1`, projectID, inputHash)
	if err != nil {
		return StepRow{}, false, fmt.Errorf("query reusable step: %w", err)
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanStep)
	if errors.Is(err, pgx.ErrNoRows) {
		return StepRow{}, false, nil
	}
	if err != nil {
		return StepRow{}, false, fmt.Errorf("read reusable step: %w", err)
	}
	return s, true, nil
}

// ---------------------------------------------------------------- events

func runDraft(r Run, typ string) events.Draft {
	summary := r
	summary.Steps = nil
	return events.Draft{
		Topic: Topic(r.ID), Type: typ, ProjectID: r.ProjectID,
		Entity:  &events.EntityRef{Kind: RunKind, ID: r.ID, Rev: r.Rev},
		Payload: map[string]any{"pipelineRun": summary},
	}
}

func stepDraft(r Run, s StepRow) events.Draft {
	p := map[string]any{"pipelineRunId": r.ID, "runState": r.State, "step": s}
	if r.RunID != "" {
		p["runId"] = r.RunID // the facade entity (run_…, evl_…): notifications leave an eval's steps to the eval
	}
	return events.Draft{
		Topic: Topic(r.ID), Type: EventStepChanged, ProjectID: r.ProjectID,
		Entity:  &events.EntityRef{Kind: StepKind, ID: s.ID, Rev: s.Rev},
		Payload: p,
	}
}
