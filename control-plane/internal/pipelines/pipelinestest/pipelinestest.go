// Package pipelinestest runs the pipeline engine without a worker: Leases is a steps.Leases that executes two
// fixture step kinds inside the test process (echo@1 copies text with a prefix, tally@1 counts it) and writes
// their outputs into the content store, with scripted failures and hangs per step and attempt. RegisterKinds
// publishes the fixtures in the registry the way the worker protocol stores what workers publish.
package pipelinestest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Fixtures are the fixture step kinds as a worker publishes them (StepKindDescriptor plus name, runtime and runtimeVersionId).
var Fixtures = []map[string]any{
	{
		"name": "echo", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
		"params": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prefix": map[string]any{"type": "string", "default": "", "x-cadence": map[string]any{
					"default": "", "description": "Text written before the copied input", "source": "Cadence recommendation",
					"range": map[string]any{"maxLength": 64},
				}},
			},
		},
		"consumes": map[string]string{"text": "text"}, "produces": map[string]string{"text": "text"},
		"resources": map[string]any{"gpu": false, "jobKind": "data"}, "neutral": true, "help": "steps.echo",
	},
	{
		"name": "tally", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
		"params": map[string]any{
			"type":     "object",
			"required": []string{"steps"},
			"properties": map[string]any{
				"steps": map[string]any{"type": "integer", "minimum": 1, "x-cadence": map[string]any{
					"defaultRef": "training.steps", "default": 3000, "description": "Optimiser steps", "source": "defaults.yaml",
					"range": map[string]any{"min": 1, "max": 200000},
				}},
				"precision": map[string]any{"type": "string", "enum": []string{"bf16", "fp16", "fp32"}, "x-cadence": map[string]any{
					"defaultRef": "training.precision", "default": "bf16", "description": "Precision", "source": "defaults.yaml",
					"range": map[string]any{"values": []string{"bf16", "fp16", "fp32"}},
				}},
			},
		},
		"consumes": map[string]string{"text": "text"}, "produces": map[string]string{"tally": "tally"},
		"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 24, "jobKind": "training"}, "help": "steps.tally",
		"estimateSeconds": 1800,
	},
}

// RegisterKinds publishes the fixture step kinds in the registry.
func RegisterKinds(ctx context.Context, pool *pgxpool.Pool) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		for _, f := range Fixtures {
			b, err := json.Marshal(f)
			if err != nil {
				return err
			}
			if _, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{
				Kind: registry.KindStepKind, Name: "step-kind/" + f["name"].(string), Payload: b, Freeze: true,
				Actor: auth.Actor{Kind: auth.KindAutomation, ID: "worker", Name: "test worker"},
			}, time.Now()); err != nil {
				return err
			}
		}
		return nil
	})
}

// Action scripts one attempt of a step.
type Action struct {
	Fail  *steps.StepError // fail the attempt with this error
	Block bool             // wait until the job is cancelled
}

// Call is one Await the fake served.
type Call struct {
	JobID string
	Step  string // the step's id in the pipeline file
	Spec  steps.Spec
}

// Leases runs fixture steps in-process.
type Leases struct {
	Pool   *pgxpool.Pool
	CAS    *cas.Store
	Engine *pipelines.Engine // marks steps running (Leased), as the worker protocol does when it grants a lease

	mu     sync.Mutex
	script map[string][]Action // step id → action per attempt (1-based index = attempt)
	calls  []Call
}

// Script sets what attempt n (1-based) of step does; attempts beyond the list run normally.
func (l *Leases) Script(step string, actions ...Action) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.script == nil {
		l.script = map[string][]Action{}
	}
	l.script[step] = actions
}

// Calls returns the Awaits served so far.
func (l *Leases) Calls() []Call {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Call(nil), l.calls...)
}

// CallsOf returns the Awaits served for one step.
func (l *Leases) CallsOf(step string) []Call {
	var out []Call
	for _, c := range l.Calls() {
		if c.Step == step {
			out = append(out, c)
		}
	}
	return out
}

// Await implements steps.Leases.
func (l *Leases) Await(ctx context.Context, jobID string) (steps.Outcome, error) {
	var raw []byte
	if err := l.Pool.QueryRow(ctx, `SELECT rj.args FROM river_job rj JOIN jobs j ON j.river_id = rj.id WHERE j.id = $1`, jobID).Scan(&raw); err != nil {
		return steps.Outcome{}, fmt.Errorf("read step job %s: %w", jobID, err)
	}
	var args struct {
		Args steps.Spec `json:"args"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return steps.Outcome{}, fmt.Errorf("decode step job %s: %w", jobID, err)
	}
	spec := args.Args
	var step string
	if err := l.Pool.QueryRow(ctx, "SELECT step FROM pipeline_steps WHERE id = $1", spec.StepID).Scan(&step); err != nil {
		return steps.Outcome{}, fmt.Errorf("read step %s: %w", spec.StepID, err)
	}
	if l.Engine != nil {
		if err := pgx.BeginFunc(ctx, l.Pool, func(tx pgx.Tx) error {
			drafts, err := l.Engine.Leased(ctx, tx, jobID)
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, jobs.System, nil, drafts)
		}); err != nil {
			return steps.Outcome{}, err
		}
	}
	l.mu.Lock()
	l.calls = append(l.calls, Call{JobID: jobID, Step: step, Spec: spec})
	var act Action
	if acts := l.script[step]; spec.Attempt <= len(acts) {
		act = acts[spec.Attempt-1]
	}
	l.mu.Unlock()
	switch {
	case act.Block:
		<-ctx.Done()
		return steps.Outcome{}, ctx.Err()
	case act.Fail != nil:
		return steps.Outcome{State: steps.StateFailed, Error: act.Fail}, nil
	}
	return l.run(spec)
}

func (l *Leases) run(spec steps.Spec) (steps.Outcome, error) {
	in, ok := spec.Inputs["text"]
	if !ok {
		return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrInput, Message: "no text input"}}, nil
	}
	f, err := l.CAS.Open(in.Hash)
	if err != nil {
		return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrInput, Message: err.Error()}}, nil
	}
	text, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		return steps.Outcome{}, err
	}
	var params map[string]any
	_ = json.Unmarshal(spec.Params, &params)
	var out []byte
	var name, typ string
	switch spec.Kind {
	case "echo":
		prefix, _ := params["prefix"].(string)
		out, name, typ = []byte(prefix+string(text)), "text", "text"
	case "tally":
		out, _ = json.Marshal(map[string]any{"chars": len(text), "words": len(strings.Fields(string(text))), "steps": params["steps"], "batchScale": spec.Overrides.BatchScale})
		name, typ = "tally", "tally"
	default:
		return steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: "unknown fixture kind " + spec.Kind}}, nil
	}
	hash, err := l.CAS.PutBytes(out)
	if err != nil {
		return steps.Outcome{}, err
	}
	return steps.Outcome{
		State:   steps.StateDone,
		Outputs: map[string]steps.ArtifactRef{name: {Hash: hash, Type: typ, Size: int64(len(out))}},
		Metrics: map[string]float64{"bytes": float64(len(out))},
	}, nil
}
