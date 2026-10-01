//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// TestCalibrationCacheTrust: the shared calibration of a key (which answers estimates for every project) is
// replaced only by the calibrate role's step of runs.calibrate or of a training run from the base model; any other
// calibration output — an ad-hoc pipeline keyed by its own metadata, another kind in a run's recipe, other inputs,
// another precision — is recorded unshared and leaves the entry alone.
func TestCalibrationCacheTrust(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.RegisterTraining(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	trainingProject(t, e, "cal")
	var cal struct{ PipelineRun struct{ ID string } }
	e.ok(e.do("POST", "/api/projects/cal/runs:calibrate", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`,
		"Idempotency-Key", e.key()), 201, &cal)
	e.waitPipelineRun(cal.PipelineRun.ID, "done")
	key := fmt.Sprintf("base_model = '%s' AND card_class = 'blackwell-48gb' AND memory_cap_gb = 22 AND precision = 'bf16'", pipelinestest.BaseModel)
	if n := e.count("SELECT count(*) FROM calibrations WHERE seconds_per_step = 0.5 AND " + key); n != 1 {
		t.Fatalf("runs.calibrate cached %d calibrations of the key", n)
	}
	if n := e.count("SELECT count(*) FROM calibration_observations WHERE shared AND pipeline_run_id = '" + cal.PipelineRun.ID + "'"); n != 1 {
		t.Fatalf("%d shared observations of runs.calibrate", n)
	}

	// A training run from the base model: its calibrate step is reused and shared again (same artifact).
	var r runView
	e.ok(e.do("POST", "/api/projects/cal/runs", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`, "Idempotency-Key", e.key()), 201, &r)
	r = e.waitRun(r.ID, "done")
	if n := e.count("SELECT count(*) FROM calibration_observations WHERE shared AND run_id = '" + r.ID + "'"); n != 1 {
		t.Fatalf("%d shared observations of the run's calibrate step", n)
	}
	var runBase, calStep string
	if err := e.pool.QueryRow(ctx, `SELECT inputs->'base'->>'hash' FROM pipeline_runs WHERE id = $1`, r.PipelineRunID).Scan(&runBase); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT id FROM pipeline_steps WHERE pipeline_run_id = $1 AND step = 'calibrate'`, r.PipelineRunID).Scan(&calStep); err != nil {
		t.Fatal(err)
	}

	// Crafted calibration outputs, as any pipeline step could report them, all claiming 0.001 s per step.
	fast := func(meta map[string]any) steps.ArtifactRef {
		h, err := e.admin.CAS.PutBytes(fmt.Appendf(nil, "calibration %v", time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
		meta["secondsPerStep"] = 0.001
		b, _ := json.Marshal(meta)
		return steps.ArtifactRef{Hash: h, Type: "calibration", Meta: b}
	}
	baseIn := map[string]steps.ArtifactRef{"base": {Hash: runBase, Type: "base_model"}}
	other, err := e.admin.CAS.PutBytes([]byte("another base model"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		out    steps.Output
		reason string
	}{
		{name: "ad-hoc pipeline keyed by its metadata", reason: "not measured by runs.calibrate",
			out: steps.Output{PipelineRunID: "plr_adhoc", StepID: "pls_adhoc", Artifact: fast(map[string]any{
				"baseModel": pipelinestest.BaseModel, "cardClass": "blackwell-48gb", "memoryCapGb": 22, "precision": "bf16"}),
				Spec: steps.Spec{Kind: pipelinestest.KindCalibrate, Inputs: baseIn}}},
		{name: "another kind in the run's recipe", reason: "is not fx_calibrate, the calibrate role",
			out: steps.Output{PipelineRunID: r.PipelineRunID, StepID: "pls_train", RunID: r.ID, Artifact: fast(map[string]any{}),
				Spec: steps.Spec{Kind: pipelinestest.KindTrain, Inputs: baseIn}}},
		{name: "another base model", reason: `input "base" is not the base model`,
			out: steps.Output{PipelineRunID: r.PipelineRunID, StepID: calStep, RunID: r.ID, Artifact: fast(map[string]any{}),
				Spec: steps.Spec{Kind: pipelinestest.KindCalibrate, Inputs: map[string]steps.ArtifactRef{"base": {Hash: other, Type: "base_model"}}}}},
		{name: "another precision", reason: "measured precision fp32",
			out: steps.Output{PipelineRunID: r.PipelineRunID, StepID: calStep, RunID: r.ID, Artifact: fast(map[string]any{}),
				Spec: steps.Spec{Kind: pipelinestest.KindCalibrate, Inputs: baseIn, Params: json.RawMessage(`{"precision":"fp32"}`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
				_, err := e.admin.StepHooks.Run(ctx, tx, tc.out)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			var (
				shared bool
				reason string
			)
			if err := e.pool.QueryRow(ctx, `SELECT shared, reason FROM calibration_observations WHERE artifact_hash = $1`,
				tc.out.Artifact.Hash).Scan(&shared, &reason); err != nil {
				t.Fatal(err)
			}
			if shared || !strings.Contains(reason, tc.reason) {
				t.Errorf("observation shared=%v reason %q, want unshared because %q", shared, reason, tc.reason)
			}
			if n := e.count("SELECT count(*) FROM calibrations WHERE seconds_per_step = 0.5 AND " + key); n != 1 {
				t.Errorf("the shared calibration changed")
			}
		})
	}

	// The same calibrate step with the run's own inputs and precision is trusted.
	ok := steps.Output{PipelineRunID: r.PipelineRunID, StepID: calStep, RunID: r.ID, Artifact: fast(map[string]any{}),
		Spec: steps.Spec{Kind: pipelinestest.KindCalibrate, Inputs: baseIn, Params: json.RawMessage(`{"precision":"bf16"}`)}}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := e.admin.StepHooks.Run(ctx, tx, ok)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT count(*) FROM calibrations WHERE seconds_per_step = 0.001 AND " + key); n != 1 {
		t.Errorf("a trusted calibration did not replace the entry")
	}
}
