package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity kinds and event types.
const (
	Kind           = "run"
	CheckpointKind = "checkpoint"

	EventCreated       = "run.created"
	EventStatusChanged = "run.status_changed"
	// EventCheckpointSaved goes out on run.{id}.checkpoints when a checkpoint is registered (notify: progress).
	EventCheckpointSaved = "checkpoint.saved"
)

// Run statuses (the contract's RunStatus).
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Ended reports whether a run status is final (runs.resume can reopen failed and cancelled runs).
func Ended(status string) bool {
	return status == StatusDone || status == StatusFailed || status == StatusCancelled
}

// StatusTopic is run.{id}.status, where run.created and run.status_changed go out.
func StatusTopic(id string) string { return "run." + id + ".status" }

// CheckpointsTopic is run.{id}.checkpoints, where checkpoint.saved goes out.
func CheckpointsTopic(id string) string { return "run." + id + ".checkpoints" }

// Recipe is the pipeline a run executes, read at a commit.
type Recipe struct {
	Pipeline string `json:"pipeline"`
	Source   string `json:"source"`
	Ref      string `json:"ref,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Version  string `json:"version"`
}

// Runtime is the runtime the train step runs in.
type Runtime struct {
	Name      string `json:"name,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

// Card is the card an estimate was made for.
type Card struct {
	ComputeID string `json:"computeId,omitempty"`
	Host      string `json:"host,omitempty"`
	Index     int    `json:"index"`
	CardClass string `json:"cardClass,omitempty"`
	// MemoryCapGB is the training share of the card's cap (its cap minus its serving reserve): what a training step
	// gets, and the key of calibrations and estimate rows.
	MemoryCapGB float64 `json:"memoryCapGb,omitempty"`
}

// row is a runs row.
type row struct {
	ID              string
	ProjectID       string
	Init            string
	BaseVersionID   string
	CheckpointID    string
	ParentRunID     string
	Family          string
	FamilyVersionID string
	Mix             MixRef
	Recipe          Recipe
	PipelineRunID   string
	TrainStep       string
	Steps           int
	Seed            *int
	GPUs            int
	Precision       string
	Params          map[string]any
	Runtime         Runtime
	Card            Card
	Estimate        json.RawMessage
	Status          string
	Error           string
	ResumedFrom     string
	Actor           auth.Actor
	Rev             int
	CreatedAt       time.Time
	UpdatedAt       time.Time
	FinishedAt      *time.Time
	ExperimentID    string // the experiment the run belongs to (phase 3)
	SweepID         string // the sweep that generated it
}

const rowCols = `id, project_id, init, base_version_id, coalesce(checkpoint_id, ''), coalesce(parent_run_id, ''), family,
	family_version_id, mix_id, mix_rev, mix_name, mix_hash, recipe, pipeline_run_id, train_step, steps, seed, gpus, precision,
	params, runtime, card, estimate, status, coalesce(error, ''), coalesce(resumed_from, ''), actor, rev, created_at,
	updated_at, finished_at, coalesce(experiment_id, ''), coalesce(sweep_id, '')`

func scanRow(r pgx.CollectableRow) (row, error) {
	var x row
	err := r.Scan(&x.ID, &x.ProjectID, &x.Init, &x.BaseVersionID, &x.CheckpointID, &x.ParentRunID, &x.Family,
		&x.FamilyVersionID, &x.Mix.ID, &x.Mix.Revision, &x.Mix.Name, &x.Mix.Hash, &x.Recipe, &x.PipelineRunID, &x.TrainStep,
		&x.Steps, &x.Seed, &x.GPUs, &x.Precision, &x.Params, &x.Runtime, &x.Card, &x.Estimate, &x.Status, &x.Error,
		&x.ResumedFrom, &x.Actor, &x.Rev, &x.CreatedAt, &x.UpdatedAt, &x.FinishedAt, &x.ExperimentID, &x.SweepID)
	if x.Params == nil {
		x.Params = map[string]any{}
	}
	return x, err
}

func oneRow(rows pgx.Rows, err error, what string) (row, bool, error) {
	if err != nil {
		return row{}, false, fmt.Errorf("query run: %w", err)
	}
	x, err := pgx.CollectExactlyOneRow(rows, scanRow)
	if errors.Is(err, pgx.ErrNoRows) {
		return row{}, false, nil
	}
	if err != nil {
		return row{}, false, fmt.Errorf("read run %s: %w", what, err)
	}
	return x, true, nil
}

// getRow reads run id, or not-found.
func getRow(ctx context.Context, q storage.Querier, id string) (row, error) {
	rows, err := q.Query(ctx, "SELECT "+rowCols+" FROM runs WHERE id = $1", id)
	x, found, err := oneRow(rows, err, id)
	if err == nil && !found {
		return row{}, problems.NotFound.New("no run %q", id)
	}
	return x, err
}

// lockRow reads run id and locks it until tx ends.
func lockRow(ctx context.Context, tx pgx.Tx, id string) (row, error) {
	rows, err := tx.Query(ctx, "SELECT "+rowCols+" FROM runs WHERE id = $1 FOR UPDATE", id)
	x, found, err := oneRow(rows, err, id)
	if err == nil && !found {
		return row{}, problems.NotFound.New("no run %q", id)
	}
	return x, err
}

// rowByPipelineRun locks the run whose own pipeline run is plrID (found is false for any other pipeline run).
func rowByPipelineRun(ctx context.Context, tx pgx.Tx, plrID string) (row, bool, error) {
	rows, err := tx.Query(ctx, "SELECT "+rowCols+" FROM runs WHERE pipeline_run_id = $1 FOR UPDATE", plrID)
	return oneRow(rows, err, plrID)
}

func insertRow(ctx context.Context, tx pgx.Tx, x row) (row, error) {
	rows, err := tx.Query(ctx, `INSERT INTO runs (id, project_id, init, base_version_id, checkpoint_id, parent_run_id, family,
		family_version_id, mix_id, mix_rev, mix_name, mix_hash, recipe, pipeline_run_id, train_step, steps, seed, gpus, precision,
		params, runtime, card, estimate, status, error, actor, finished_at, experiment_id, sweep_id)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), NULLIF($6, ''), $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20,
		$21, $22, $23, $24, NULLIF($25, ''), $26, $27, NULLIF($28, ''), NULLIF($29, '')) RETURNING `+rowCols,
		x.ID, x.ProjectID, x.Init, x.BaseVersionID, x.CheckpointID, x.ParentRunID, x.Family, x.FamilyVersionID, x.Mix.ID,
		x.Mix.Revision, x.Mix.Name, x.Mix.Hash, x.Recipe, x.PipelineRunID, x.TrainStep, x.Steps, x.Seed, x.GPUs, x.Precision,
		x.Params, x.Runtime, x.Card, x.Estimate, x.Status, x.Error, x.Actor, x.FinishedAt, x.ExperimentID, x.SweepID)
	out, _, err := oneRow(rows, err, x.ID)
	return out, err
}

// saveStatus writes a run's status, error, end time and last resume, bumping its revision.
func saveStatus(ctx context.Context, tx pgx.Tx, x row) (row, error) {
	rows, err := tx.Query(ctx, `UPDATE runs SET status = $2, error = NULLIF($3, ''), finished_at = $4, resumed_from = NULLIF($5, ''),
		rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+rowCols, x.ID, x.Status, x.Error, x.FinishedAt, x.ResumedFrom)
	out, _, err := oneRow(rows, err, x.ID)
	return out, err
}

// ListFilter narrows List.
type ListFilter struct {
	ProjectID    string
	Status       string
	ExperimentID string
	Limit        int
}

func listRows(ctx context.Context, q storage.Querier, f ListFilter) ([]row, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, "SELECT "+rowCols+` FROM runs WHERE project_id = $1 AND ($2 = '' OR status = $2)
		AND ($4 = '' OR experiment_id = $4)
		ORDER BY created_at DESC, id DESC LIMIT $3`, f.ProjectID, f.Status, f.Limit, f.ExperimentID)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanRow)
	if err != nil {
		return nil, fmt.Errorf("read runs: %w", err)
	}
	return out, nil
}

// statusDraft is a run.created or run.status_changed event on run.{id}.status; panels re-read runs.get.
func statusDraft(x row, typ string) events.Draft {
	return events.Draft{
		Topic: StatusTopic(x.ID), Type: typ, ProjectID: x.ProjectID,
		Entity: &events.EntityRef{Kind: Kind, ID: x.ID, Rev: x.Rev},
		Payload: map[string]any{"run": map[string]any{
			"id": x.ID, "projectId": x.ProjectID, "status": x.Status, "error": x.Error, "rev": x.Rev,
			"pipelineRunId": x.PipelineRunID, "parentRunId": x.ParentRunID, "updatedAt": x.UpdatedAt, "finishedAt": x.FinishedAt,
			"experimentId": x.ExperimentID, "sweepId": x.SweepID,
		}},
	}
}
