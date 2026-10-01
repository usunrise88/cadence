package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Checkpoint kinds.
const (
	CheckpointTrained  = "trained"
	CheckpointAveraged = "averaged"
)

// Checkpoint is a registered checkpoint of a run. Its JSON form is the contract's Checkpoint.
type Checkpoint struct {
	ID            string          `json:"id"`
	RunID         string          `json:"runId"`
	ProjectID     string          `json:"projectId"`
	Artifact      string          `json:"artifact"`
	Kind          string          `json:"kind"`
	Step          *int64          `json:"step,omitempty"`
	ValWER        *float64        `json:"valWer,omitempty"`
	Family        string          `json:"family,omitempty"`
	WeightsHash   string          `json:"weightsHash,omitempty"`
	AveragedFrom  []string        `json:"averagedFrom,omitempty"`
	Kept          bool            `json:"kept"`
	Rank          *int            `json:"rank,omitempty"`
	PipelineRunID string          `json:"pipelineRunId,omitempty"`
	StepID        string          `json:"stepId,omitempty"`
	Meta          json.RawMessage `json:"meta,omitempty"`
	CreatedAt     time.Time       `json:"createdAt"`
}

const ckpCols = `id, run_id, project_id, artifact_hash, kind, step, val_wer, family, weights_hash, averaged_from, kept, rank,
	coalesce(pipeline_run_id, ''), coalesce(step_id, ''), meta, created_at`

func scanCheckpoint(r pgx.CollectableRow) (Checkpoint, error) {
	var c Checkpoint
	err := r.Scan(&c.ID, &c.RunID, &c.ProjectID, &c.Artifact, &c.Kind, &c.Step, &c.ValWER, &c.Family, &c.WeightsHash,
		&c.AveragedFrom, &c.Kept, &c.Rank, &c.PipelineRunID, &c.StepID, &c.Meta, &c.CreatedAt)
	return c, err
}

// GetCheckpoint returns checkpoint id, or not-found.
func GetCheckpoint(ctx context.Context, q storage.Querier, id string) (Checkpoint, error) {
	rows, err := q.Query(ctx, "SELECT "+ckpCols+" FROM checkpoints WHERE id = $1", id)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("query checkpoint: %w", err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scanCheckpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return Checkpoint{}, problems.NotFound.New("no checkpoint %q", id)
	}
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read checkpoint: %w", err)
	}
	return c, nil
}

// CheckpointFilter narrows ListCheckpoints.
type CheckpointFilter struct {
	ProjectID string
	RunID     string
	Kept      bool
	Limit     int
}

// ListCheckpoints returns checkpoints best validation WER first (those without one last, newest first).
func ListCheckpoints(ctx context.Context, q storage.Querier, f CheckpointFilter) ([]Checkpoint, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, "SELECT "+ckpCols+` FROM checkpoints WHERE ($1 = '' OR project_id = $1) AND ($2 = '' OR run_id = $2)
		AND (NOT $3 OR kept) ORDER BY val_wer ASC NULLS LAST, step DESC NULLS LAST, created_at DESC LIMIT $4`,
		f.ProjectID, f.RunID, f.Kept, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("list checkpoints: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanCheckpoint)
	if err != nil {
		return nil, fmt.Errorf("read checkpoints: %w", err)
	}
	return out, nil
}

// checkpointMeta is the neutral metadata of a `checkpoint` artifact (R42).
type checkpointMeta struct {
	Step        *float64 `json:"step"`
	ValWER      *float64 `json:"valWer"`
	Family      string   `json:"family"`
	WeightsHash string   `json:"weightsHash"`
}

// checkpointHook registers a `checkpoint` output of a run's pipeline run (idempotent per run and artifact: hooks run
// for reused outputs too) and re-ranks the run's top k. An output outside a run (a plain pipelines.run) registers
// nothing; a run that is being created registers its reused outputs itself (registerExisting).
func (s *Service) checkpointHook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	if out.RunID == "" {
		return nil, nil
	}
	x, err := lockRow(ctx, tx, out.RunID)
	if pe, ok := problems.As(err); err != nil && ok && pe.Type == problems.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s.register(ctx, tx, x, out)
}

// register inserts the checkpoint of out for run x and re-ranks; it returns checkpoint.saved when it was new.
func (s *Service) register(ctx context.Context, tx pgx.Tx, x row, out steps.Output) ([]events.Draft, error) {
	var m checkpointMeta
	if len(out.Artifact.Meta) > 0 {
		if err := json.Unmarshal(out.Artifact.Meta, &m); err != nil {
			return nil, fmt.Errorf("checkpoint meta: %w", err)
		}
	}
	if m.ValWER == nil {
		for _, name := range []string{"val_wer", "valWer"} {
			if v, ok := out.Metrics[name]; ok {
				m.ValWER = &v
				break
			}
		}
	}
	var step *int64
	if m.Step != nil {
		v := int64(*m.Step)
		step = &v
	}
	if m.Family == "" {
		m.Family = x.Family
	}
	kind := CheckpointTrained
	averaged := []string{}
	if out.PipelineRunID != x.PipelineRunID { // a checkpoints.average run of this run
		kind = CheckpointAveraged
		var hashes []string
		for _, name := range sortedKeys(out.Spec.Inputs) {
			if ref := out.Spec.Inputs[name]; ref.Type == TypeCheckpoint {
				hashes = append(hashes, ref.Hash)
			}
		}
		if len(hashes) > 0 {
			rows, err := tx.Query(ctx, `SELECT artifact_hash, id FROM checkpoints WHERE run_id = $1 AND artifact_hash = ANY($2)`, x.ID, hashes)
			if err != nil {
				return nil, fmt.Errorf("read averaged checkpoints: %w", err)
			}
			ids := map[string]string{}
			var h, id string
			if _, err := pgx.ForEachRow(rows, []any{&h, &id}, func() error { ids[h] = id; return nil }); err != nil {
				return nil, fmt.Errorf("read averaged checkpoints: %w", err)
			}
			for _, h := range hashes { // in input order, a checkpoint named twice listed twice
				if id, ok := ids[h]; ok {
					averaged = append(averaged, id)
				}
			}
		}
	}
	meta := out.Artifact.Meta
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	id := "ckp_" + uuid.Must(uuid.NewV7()).String()
	var inserted string
	err := tx.QueryRow(ctx, `INSERT INTO checkpoints (id, run_id, project_id, artifact_hash, kind, step, val_wer, family,
			weights_hash, averaged_from, pipeline_run_id, step_id, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), NULLIF($12, ''), $13)
		ON CONFLICT (run_id, artifact_hash) DO NOTHING RETURNING id`,
		id, x.ID, x.ProjectID, out.Artifact.Hash, kind, step, m.ValWER, m.Family, m.WeightsHash, averaged,
		out.PipelineRunID, out.StepID, meta).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // registered before (a reused output, or a hook registered twice)
	}
	if err != nil {
		return nil, fmt.Errorf("register checkpoint: %w", err)
	}
	if err := s.rerank(ctx, tx, x.ID); err != nil {
		return nil, err
	}
	c, err := GetCheckpoint(ctx, tx, inserted)
	if err != nil {
		return nil, err
	}
	return []events.Draft{{
		Topic: CheckpointsTopic(x.ID), Type: EventCheckpointSaved, ProjectID: x.ProjectID,
		Entity:  &events.EntityRef{Kind: CheckpointKind, ID: c.ID, Rev: 1},
		Payload: map[string]any{"runId": x.ID, "checkpoint": c},
	}}, nil
}

// rerank ranks a run's checkpoints by validation WER (lowest first; none last) and keeps the top k
// (training.keep_top_k).
func (s *Service) rerank(ctx context.Context, tx pgx.Tx, runID string) error {
	k := s.defaults().Training.KeepTopK.Value
	if _, err := tx.Exec(ctx, `WITH ranked AS (
			SELECT id, row_number() OVER (ORDER BY val_wer ASC NULLS LAST, step DESC NULLS LAST, created_at, id) AS r
			FROM checkpoints WHERE run_id = $1)
		UPDATE checkpoints c SET rank = ranked.r, kept = ranked.r <= $2 FROM ranked WHERE c.id = ranked.id`, runID, k); err != nil {
		return fmt.Errorf("rank checkpoints: %w", err)
	}
	return nil
}

// registerExisting runs the checkpoint and calibration registration for outputs the run's pipeline run already has
// when the run row is created (steps reused by input hash finish inside pipelines.Engine.Start, before the row
// exists).
func (s *Service) registerExisting(ctx context.Context, tx pgx.Tx, x row, pr pipelines.Run) ([]events.Draft, error) {
	var drafts []events.Draft
	for _, st := range pr.Steps {
		if st.State != pipelines.StepDone && st.State != pipelines.StepReused {
			continue
		}
		for _, name := range sortedKeys(st.Outputs) {
			ref := st.Outputs[name]
			out := steps.Output{ProjectID: x.ProjectID, PipelineRunID: pr.ID, StepID: st.ID, RunID: x.ID, Name: name, Artifact: ref,
				Metrics: st.Metrics, Spec: stepSpec(st, x.ProjectID, x.ID)}
			switch ref.Type {
			case TypeCheckpoint:
				ev, err := s.register(ctx, tx, x, out)
				if err != nil {
					return nil, err
				}
				drafts = append(drafts, ev...)
			case TypeCalibration:
				if _, err := s.calibrationHook(ctx, tx, out); err != nil {
					return nil, err
				}
			}
		}
	}
	return drafts, nil
}

// stepSpec is the spec an output hook sees for a step that finished before the hook could run (reused inside
// pipelines.Engine.Start): its kind, parameters, inputs and outputs.
func stepSpec(st pipelines.StepRow, projectID, runID string) steps.Spec {
	params, err := json.Marshal(st.Params)
	if err != nil {
		params = json.RawMessage(`{}`)
	}
	return steps.Spec{StepID: st.ID, PipelineRunID: st.PipelineRunID, ProjectID: projectID, RunID: runID, Kind: st.Kind,
		KindVersion: st.KindVersion, Params: params, Inputs: st.Inputs, Outputs: st.Produces}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
