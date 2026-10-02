package pipelinestest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The training fixtures: a model family that exists only in tests (FamilyName), its role step kinds (fx_calibrate,
// fx_train, fx_average) and a base model of the family. Runs, calibration and checkpoints are tested with them, so
// no Go test depends on a real family's name (R41, R45).
const (
	FamilyName    = "fixture-family"
	BaseModel     = "base-model/fixture-base"
	KindCalibrate = "fx_calibrate"
	KindTrain     = "fx_train"
	KindAverage   = "fx_average"
)

// TrainStage is a train-stage pipeline of the fixture family, as a project repository would hold it.
const TrainStage = `name: train-stage
description: One stage of the fixture family (calibrate, then train)
inputs: { mix: mix, base: base_model }
steps:
  - id: calibrate
    kind: fx_calibrate@1
    in: { base: $inputs.base, data: $inputs.mix }
  - id: train
    kind: fx_train@1
    in: { base: $inputs.base, data: $inputs.mix }
    params: { steps: 200 }
`

func xc(def any, desc string, rng map[string]any) map[string]any {
	return map[string]any{"default": def, "description": desc, "source": "Cadence recommendation (test fixture)", "range": rng}
}

// TrainingFixtures are the fixture family's step kinds as a worker publishes them.
var TrainingFixtures = []map[string]any{
	{
		"name": KindCalibrate, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "role": "calibrate",
		"params": map[string]any{"type": "object", "properties": map[string]any{
			"timed_steps": map[string]any{"type": "integer", "minimum": 1, "default": 5, "x-cadence": xc(5, "Timed steps", map[string]any{"min": 1, "max": 100})},
			"precision": map[string]any{"type": "string", "enum": []string{"bf16", "fp16", "fp32"}, "x-cadence": map[string]any{
				"defaultRef": "training.precision", "default": "bf16", "description": "Precision", "source": "defaults.yaml",
				"range": map[string]any{"values": []string{"bf16", "fp16", "fp32"}}}},
		}},
		"consumes": map[string]string{"base": "base_model", "data": "mix"}, "produces": map[string]string{"calibration": "calibration"},
		"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 20, "jobKind": "training"}, "help": "steps.fx-calibrate",
		"estimateSeconds": 60,
	},
	{
		"name": KindTrain, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "role": "train",
		"params": map[string]any{"type": "object", "required": []string{"steps"}, "properties": map[string]any{
			"steps": map[string]any{"type": "integer", "minimum": 1, "x-cadence": map[string]any{
				"defaultRef": "training.steps", "default": 3000, "description": "Optimiser steps", "source": "defaults.yaml",
				"range": map[string]any{"min": 1, "max": 200000}}},
			"peak_lr": map[string]any{"type": "number", "default": 0.0002, "x-cadence": xc(0.0002, "Peak learning rate", map[string]any{"min": 1e-7, "max": 1})},
			"seed":    map[string]any{"type": "integer", "default": 0, "x-cadence": xc(0, "Seed", map[string]any{"min": 0, "max": 2147483647})},
			"precision": map[string]any{"type": "string", "enum": []string{"bf16", "fp16", "fp32"}, "x-cadence": map[string]any{
				"defaultRef": "training.precision", "default": "bf16", "description": "Precision", "source": "defaults.yaml",
				"range": map[string]any{"values": []string{"bf16", "fp16", "fp32"}}}},
		}},
		"consumes":  map[string]string{"base": "base_model", "data": "mix"},
		"produces":  map[string]string{"checkpoint": "checkpoint", "state": "training-state"},
		"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 20, "jobKind": "training"}, "help": "steps.fx-train",
	},
	{
		"name": KindAverage, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "role": "average",
		"params":   map[string]any{"type": "object", "properties": map[string]any{}},
		"consumes": map[string]string{"checkpoints": "checkpoint"}, "produces": map[string]string{"checkpoint": "checkpoint"},
		"resources": map[string]any{"gpu": false, "jobKind": "training"}, "help": "steps.fx-average",
	},
}

// Family is the fixture family descriptor.
var Family = map[string]any{
	"name": FamilyName, "version": "1", "title": "Fixture family (tests)", "framework": "none", "architecture": "none",
	"latencyProfiles": []map[string]any{{"name": "offline", "latencyMs": 0}, {"name": "160ms", "latencyMs": 160, "label": "160 ms"}},
	"capabilities":    map[string]any{"streaming": true, "boosting": "fixture-boost"},
	"roles": map[string]string{"calibrate": KindCalibrate, "train": KindTrain, "average": KindAverage,
		"materialize": KindMaterialize, "transcribe": KindTranscribe},
}

var worker = auth.Actor{Kind: auth.KindAutomation, ID: "worker", Name: "test worker"}

// RegisterTraining publishes the fixture family, its step kinds and a base model of the family in the registry.
func RegisterTraining(ctx context.Context, pool *pgxpool.Pool) error {
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		reg := func(kind, name string, payload any) error {
			b, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			_, _, _, err = registry.Register(ctx, tx, registry.RegisterInput{Kind: kind, Name: name, Payload: b, Freeze: true, Actor: worker}, time.Now())
			return err
		}
		for _, f := range TrainingFixtures {
			if err := reg(registry.KindStepKind, "step-kind/"+f["name"].(string), f); err != nil {
				return err
			}
		}
		return reg(registry.KindModelFamily, "model-family/"+FamilyName, Family)
	}); err != nil {
		return err
	}
	return RegisterBaseModel(ctx, pool)
}

// RegisterBaseModel registers the base model of the fixture family alone (a test whose worker publishes the family
// and its kinds through the worker protocol).
func RegisterBaseModel(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(map[string]any{
			"hfRepo": "fixture/base", "revision": "0123456789abcdef", "licence": "cc-by-4.0", "familyId": FamilyName, "locales": []string{"he"},
			"checkpointFile": "base.bin",
		})
		_, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindBaseModel, Name: BaseModel, Payload: b, Freeze: true, Actor: worker}, time.Now())
		return err
	})
}

// RegisterDataset registers a frozen dataset version whose training artifact is a small blob in store; evalOnly
// marks it for evaluation only. It returns the version id.
func RegisterDataset(ctx context.Context, pool *pgxpool.Pool, store *cas.Store, name string, hours float64, evalOnly bool) (string, error) {
	hash, err := store.PutBytes([]byte("dataset " + name))
	if err != nil {
		return "", err
	}
	var id string
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		// The artifact as the dataset output hook registers it: a reference, not a bare hash.
		art := steps.ArtifactRef{Hash: hash, Type: "dataset", Size: int64(len("dataset " + name))}
		b, _ := json.Marshal(map[string]any{"hours": hours, "locales": []string{"he"}, "artifact": art, "evalOnly": evalOnly})
		v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindDataset, Name: "dataset/" + name, Payload: b,
			Freeze: true, Actor: worker}, time.Now())
		id = v.ID
		return err
	})
	return id, err
}

// publish has the train fixture publish n validation checkpoints (steps k·steps/(n+1), validation WER falling) as a
// worker does during a lease, each in its own transaction.
func (l *Leases) publish(ctx context.Context, jobID string, spec steps.Spec, n int) error {
	if n <= 0 || spec.Kind != KindTrain || l.Engine == nil {
		return nil
	}
	var params map[string]any
	_ = json.Unmarshal(spec.Params, &params)
	total, _ := params["steps"].(float64)
	for k := 1; k <= n; k++ {
		step := math.Floor(total * float64(k) / float64(n+1))
		wer := math.Round(0.9/float64(k+1)*1e6) / 1e6
		doc := map[string]any{"family": FamilyName, "step": step, "of": spec.StepID}
		b, _ := json.Marshal(doc)
		h, err := l.CAS.PutBytes(b)
		if err != nil {
			return err
		}
		meta, _ := json.Marshal(map[string]any{"family": FamilyName, "step": step, "valWer": wer, "weightsHash": cas.Hash(b)})
		ref := steps.ArtifactRef{Hash: h, Type: "checkpoint", Size: int64(len(b)), Meta: meta}
		if err := pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
			drafts, err := l.Engine.Published(ctx, tx, jobID, spec, "checkpoint", ref, map[string]float64{"val_wer": wer})
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, jobs.System, nil, drafts)
		}); err != nil {
			return fmt.Errorf("publish checkpoint %d: %w", k, err)
		}
	}
	return nil
}

// runTraining executes the training fixtures; ok is false for other kinds.
func (l *Leases) runTraining(spec steps.Spec) (steps.Outcome, bool, error) {
	var params map[string]any
	_ = json.Unmarshal(spec.Params, &params)
	put := func(doc any, name, typ string, meta map[string]any) (steps.ArtifactRef, error) {
		b, err := json.Marshal(doc)
		if err != nil {
			return steps.ArtifactRef{}, err
		}
		h, err := l.CAS.PutBytes(b)
		if err != nil {
			return steps.ArtifactRef{}, err
		}
		m, _ := json.Marshal(meta)
		return steps.ArtifactRef{Hash: h, Type: typ, Size: int64(len(b)), Meta: m}, nil
	}
	switch spec.Kind {
	case KindCalibrate:
		sps := l.SecondsPerStep
		if sps == 0 {
			sps = 0.5
		}
		ref, err := put(map[string]any{"family": FamilyName, "base": spec.Inputs["base"].Hash, "data": spec.Inputs["data"].Hash,
			"secondsPerStep": sps}, "calibration", "calibration",
			map[string]any{"family": FamilyName, "secondsPerStep": sps, "plusMinus": 0.1, "batchSizes": map[string]int{"b1": 16},
				"leaseOverheadSeconds": 20})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{"calibration": ref},
			Metrics: map[string]float64{"seconds_per_step": sps}}, true, nil
	case KindTrain:
		n, _ := params["steps"].(float64)
		lr, _ := params["peak_lr"].(float64)
		seed, _ := params["seed"].(float64)
		wer := math.Round((0.5/(1+n/100)+lr)*1e6) / 1e6
		doc := map[string]any{"family": FamilyName, "steps": n, "peak_lr": lr, "seed": seed, "start": spec.Inputs["base"].Type,
			"startHash": spec.Inputs["base"].Hash, "resumeFrom": spec.Overrides.ResumeFrom, "batchScale": spec.Overrides.BatchScale}
		ckp, err := put(doc, "checkpoint", "checkpoint", map[string]any{"family": FamilyName, "step": n, "valWer": wer,
			"weightsHash": cas.Hash([]byte(fmt.Sprint(doc)))})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		state, err := put(map[string]any{"family": FamilyName, "step": n, "of": ckp.Hash}, "state", "training-state",
			map[string]any{"family": FamilyName, "step": n})
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{"checkpoint": ckp, "state": state},
			Metrics: map[string]float64{"val_wer": wer}}, true, nil
	case KindAverage:
		best, step := math.Inf(1), 0.0
		var from []string
		for _, name := range sortedNames(spec.Inputs) {
			in := spec.Inputs[name]
			var m struct {
				Step   float64  `json:"step"`
				ValWer *float64 `json:"valWer"`
			}
			_ = json.Unmarshal(in.Meta, &m)
			if m.ValWer != nil && *m.ValWer < best {
				best = *m.ValWer
			}
			step = math.Max(step, m.Step)
			from = append(from, in.Hash)
		}
		if len(from) < 2 {
			return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrInput, Message: "averaging needs two checkpoints"}}, true, nil
		}
		meta := map[string]any{"family": FamilyName, "step": step, "averagedFrom": from}
		if !math.IsInf(best, 1) {
			meta["valWer"] = math.Round((best-0.01)*1e6) / 1e6
		}
		ref, err := put(map[string]any{"family": FamilyName, "averaged": from}, "checkpoint", "checkpoint", meta)
		if err != nil {
			return steps.Outcome{}, true, err
		}
		return steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{"checkpoint": ref}}, true, nil
	}
	return steps.Outcome{}, false, nil
}

func sortedNames(m map[string]steps.ArtifactRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
