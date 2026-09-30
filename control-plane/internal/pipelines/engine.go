// Package pipelines is the pipeline engine (docs/spec/03-pipelines-defaults.md "Pipelines and extension points",
// docs/review/2026-09-30-phase-2-plan.md "Pipelines", "The step job"). A pipeline is a YAML file of typed steps
// pinned as kind@version; Plan validates it against the step kinds workers publish and resolves every parameter
// against defaults.yaml; Start records a pipeline run and queues each ready step as a River job of kind "step"
// (steps.JobKind) whose handler waits on the worker protocol (steps.Leases) and then records the step's outputs,
// runs the output hooks and advances the dependents — all in the transaction that marks the step done.
//
// Steps are idempotent by input hash: a finished step of the same project with the same kind, version, resolved
// parameters and input hashes supplies its outputs instead of running again (unless the run asks for fresh). An
// out-of-memory failure gets one automatic retry at steps.OOMBatchScale of the batch, a lost lease one retry; any
// other failure fails the step and the run, and pipelineRuns.retry runs one step again.
package pipelines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// StepTimeout bounds one step job (a training stage runs for hours; availability windows pause it earlier).
const StepTimeout = 7 * 24 * time.Hour

// StepQueue is the River queue of step jobs: each waits on its lease for as long as the step runs, so they get
// their own workers instead of starving the in-process kinds.
const StepQueue = jobs.QueueSteps

// SweepInterval is how often the engine looks for steps whose job ended without an outcome.
const SweepInterval = time.Minute

// Options wire an Engine.
type Options struct {
	Pool      *pgxpool.Pool
	Jobs      *jobs.Service // set by Register when nil
	CAS       *cas.Store
	Hooks     *steps.Hooks
	Leases    steps.Leases
	Kinds     Kinds                     // RegistryKinds when nil
	Defaults  func() *defaults.Defaults // defaults.Get when nil
	Repos     Repo                      // project repositories; nil: bundled templates only
	Templates fs.FS                     // the bundled templates tree (pipelines/*.yaml); templates.FS when nil
	Log       *slog.Logger
}

// Engine runs pipelines.
type Engine struct{ o Options }

// New returns an engine; call Register before the job service starts.
func New(o Options) *Engine {
	if o.Kinds == nil {
		o.Kinds = RegistryKinds{}
	}
	if o.Defaults == nil {
		o.Defaults = defaults.Get
	}
	if o.Templates == nil {
		o.Templates = templates.FS
	}
	if o.Hooks == nil {
		o.Hooks = &steps.Hooks{}
	}
	if o.Leases == nil {
		o.Leases = steps.NoLeases{}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Engine{o: o}
}

// SetLeases replaces the worker protocol the step jobs wait on (main sets it once the worker protocol exists).
func (e *Engine) SetLeases(l steps.Leases) { e.o.Leases = l }

// Hooks returns the output hooks the engine runs.
func (e *Engine) Hooks() *steps.Hooks { return e.o.Hooks }

// CAS returns the content store.
func (e *Engine) CAS() *cas.Store { return e.o.CAS }

func (e *Engine) defaults() *defaults.Defaults { return e.o.Defaults() }

// Register adds the step job kind and the sweep to j; call it before j starts.
func (e *Engine) Register(j *jobs.Service) {
	if e.o.Jobs == nil {
		e.o.Jobs = j
	}
	j.Register(steps.JobKind, e.handle, jobs.KindOptions{Timeout: StepTimeout, Queue: StepQueue})
	j.AddPeriodic("pipelines.sweep", SweepInterval, e.Sweep)
}

// ---------------------------------------------------------------- starting a run

// StartInput starts a pipeline run.
type StartInput struct {
	ProjectID string
	Name      string    // the pipeline to read (pipelines/<name>.yaml at Ref, else the bundled template)
	Pipeline  *Pipeline // or a parsed pipeline (a facade's own chain); Name is ignored then
	Ref       string    // default main
	// Version, when not empty or AnyVersion, must equal the pipeline's version (If-Match of pipelines.run).
	Version   string
	Inputs    map[string]steps.ArtifactRef
	Params    map[string]map[string]any // step id → overrides
	Estimates map[string]float64        // step id → seconds (R12), from the facade
	RunID     string                    // the training run this pipeline run belongs to
	Actor     auth.Actor
	Priority  int
	Fresh     bool
}

// Prepare reads (or takes) the pipeline and validates it for a run without writing anything; a dry run answers
// with its plan.
func (e *Engine) Prepare(ctx context.Context, q storage.Querier, in StartInput) (Source, Plan, error) {
	var src Source
	if in.Pipeline != nil {
		if err := in.Pipeline.Check(""); err != nil {
			return Source{}, Plan{}, err
		}
		b, err := json.Marshal(in.Pipeline)
		if err != nil {
			return Source{}, Plan{}, fmt.Errorf("marshal pipeline: %w", err)
		}
		src = Source{Pipeline: *in.Pipeline, Kind: SourceInline, Version: "inline-" + cas.Hash(b)[3:15]}
	} else {
		p, err := projects.GetByID(ctx, q, in.ProjectID)
		if err != nil {
			return Source{}, Plan{}, err
		}
		if src, err = e.Load(ctx, p, in.Name, in.Ref); err != nil {
			return Source{}, Plan{}, err
		}
	}
	if in.Version != "" && in.Version != AnyVersion && in.Version != src.Version {
		return Source{}, Plan{}, problems.PreconditionFailed.New("pipeline %s is at version %s, not %s; re-read it (pipelines.list) and retry",
			src.Pipeline.Name, src.Version, in.Version)
	}
	plan, err := e.Plan(ctx, q, src.Pipeline, PlanInput{Inputs: in.Inputs, Params: in.Params, Estimates: in.Estimates})
	return src, plan, err
}

// Start validates the pipeline, records the run and its steps in tx, and queues the steps that are ready (or
// reuses them). It returns the run with its steps and the events to emit with tx. Inputs must be in the content
// store; they are indexed for the project.
func (e *Engine) Start(ctx context.Context, tx pgx.Tx, in StartInput) (Run, []events.Draft, error) {
	if e.o.Jobs == nil {
		return Run{}, nil, errors.New("pipelines: no job service (Register was not called)")
	}
	src, plan, err := e.Prepare(ctx, tx, in)
	if err != nil {
		return Run{}, nil, err
	}
	var bad Errors
	for _, name := range sortedKeys(in.Inputs) {
		if _, err := artifacts.Record(ctx, tx, e.o.CAS, in.Inputs[name], in.ProjectID, nil); err != nil {
			bad.Add("inputs."+name, "%v", err)
		}
	}
	if err := bad.Err(src.Pipeline.Name); err != nil {
		return Run{}, nil, err
	}
	r := Run{
		ID: "plr_" + uuid.Must(uuid.NewV7()).String(), ProjectID: in.ProjectID, Pipeline: src.Pipeline.Name, Source: src.Kind,
		Ref: src.Ref, Commit: src.Commit, Version: src.Version, Definition: src.Pipeline, Inputs: in.Inputs, RunID: in.RunID,
		Fresh: in.Fresh, Priority: in.Priority, Actor: in.Actor,
	}
	if r.Inputs == nil {
		r.Inputs = map[string]steps.ArtifactRef{}
	}
	if r, err = insertRun(ctx, tx, r); err != nil {
		return Run{}, nil, err
	}
	for _, ps := range plan.Steps {
		row := StepRow{
			ID: "pls_" + uuid.Must(uuid.NewV7()).String(), PipelineRunID: r.ID, ProjectID: r.ProjectID, Step: ps.Step,
			Position: ps.Position, Kind: ps.Kind.Name, KindVersion: ps.Kind.Version, StepKindVersionID: ps.Kind.VersionID,
			Params: ps.Params, Departures: ps.Departures, Wiring: ps.In, Produces: ps.Kind.Produces,
			Resources: ps.Kind.Resources, SecretNames: ps.Kind.Secrets, EstimateSeconds: ps.EstimateSeconds,
		}
		if row.Produces == nil {
			row.Produces = map[string]string{}
		}
		if err := insertStep(ctx, tx, row); err != nil {
			return Run{}, nil, err
		}
	}
	drafts := []events.Draft{runDraft(r, EventStarted)}
	sts, err := stepsOf(ctx, tx, r.ID, true)
	if err != nil {
		return Run{}, nil, err
	}
	more, err := e.advance(ctx, tx, &r, sts)
	if err != nil {
		return Run{}, nil, err
	}
	r.Steps, err = stepsOf(ctx, tx, r.ID, false)
	return r, append(drafts, more...), err
}

// ---------------------------------------------------------------- advancing

// advance queues (or reuses) every waiting step whose inputs are all produced, until nothing changes, and ends
// the run as done once every step is. sts are the run's steps (locked), updated in place.
func (e *Engine) advance(ctx context.Context, tx pgx.Tx, r *Run, sts []StepRow) ([]events.Draft, error) {
	if r.State != RunRunning {
		return nil, nil
	}
	byID := map[string]int{}
	for i, s := range sts {
		byID[s.Step] = i
	}
	var drafts []events.Draft
	for changed := true; changed; {
		changed = false
		for i := range sts {
			s := &sts[i]
			if s.State != StepWaiting {
				continue
			}
			inputs, ready, err := e.inputsOf(*r, *s, sts, byID)
			if err != nil {
				return nil, err
			}
			if !ready {
				continue
			}
			s.Inputs = inputs
			if s.InputHash, err = InputHash(s.Kind, s.KindVersion, s.Params, inputs); err != nil {
				return nil, err
			}
			ev, reused, err := e.reuse(ctx, tx, r, s)
			if err != nil {
				return nil, err
			}
			if !reused {
				reason := ReasonInitial
				if s.Attempts > 0 {
					reason = ReasonRetry
				}
				if ev, err = e.enqueue(ctx, tx, *r, s, reason, 0); err != nil {
					return nil, err
				}
			}
			drafts = append(drafts, ev...)
			changed = true
		}
	}
	all := true
	for _, s := range sts {
		all = all && finished(s.State)
	}
	if all {
		now := time.Now()
		r.State, r.FinishedAt = RunDone, &now
		saved, err := saveRun(ctx, tx, *r)
		if err != nil {
			return nil, err
		}
		*r = saved
		drafts = append(drafts, runDraft(*r, EventStateChanged))
	}
	return drafts, nil
}

// inputsOf resolves a step's inputs; ready is false while a producer has not finished.
func (e *Engine) inputsOf(r Run, s StepRow, sts []StepRow, byID map[string]int) (map[string]steps.ArtifactRef, bool, error) {
	out := map[string]steps.ArtifactRef{}
	for name, wire := range s.Wiring {
		w, ok := ParseWire(wire)
		if !ok {
			return nil, false, fmt.Errorf("step %s: bad wiring %q", s.Step, wire)
		}
		if w.Input != "" {
			ref, ok := r.Inputs[w.Input]
			if !ok {
				return nil, false, fmt.Errorf("step %s: pipeline input %q is missing", s.Step, w.Input)
			}
			out[name] = ref
			continue
		}
		p := sts[byID[w.Step]]
		if !finished(p.State) {
			return nil, false, nil
		}
		ref, ok := p.Outputs[w.Output]
		if !ok {
			return nil, false, fmt.Errorf("step %s: step %s finished without output %q", s.Step, p.Step, w.Output)
		}
		out[name] = ref
	}
	return out, true, nil
}

// reuse finishes s with the outputs of a finished step of the same project and input hash, when there is one
// whose outputs are still in the store. The output hooks run for the reused outputs too (they must be idempotent
// per artifact hash).
func (e *Engine) reuse(ctx context.Context, tx pgx.Tx, r *Run, s *StepRow) ([]events.Draft, bool, error) {
	if r.Fresh {
		return nil, false, nil
	}
	prev, found, err := reusable(ctx, tx, r.ProjectID, s.InputHash)
	if err != nil || !found {
		return nil, false, err
	}
	for _, ref := range prev.Outputs {
		if _, err := artifacts.Verify(e.o.CAS, ref); err != nil {
			e.o.Log.WarnContext(ctx, "pipeline step not reused: an output left the content store", "step", prev.ID, "err", err)
			return nil, false, nil
		}
	}
	for _, name := range sortedKeys(prev.Outputs) {
		if _, err := artifacts.Record(ctx, tx, e.o.CAS, prev.Outputs[name], r.ProjectID, nil); err != nil {
			return nil, false, err
		}
	}
	now := time.Now()
	s.State, s.Outputs, s.ReusedFrom, s.Metrics, s.FinishedAt = StepReused, prev.Outputs, prev.ID, prev.Metrics, &now
	drafts, herr := e.runHooks(ctx, tx, *r, *s, steps.Spec{StepID: s.ID, PipelineRunID: r.ID, ProjectID: r.ProjectID, RunID: r.RunID,
		Kind: s.Kind, KindVersion: s.KindVersion, Params: mustJSON(s.Params), Inputs: s.Inputs, Outputs: s.Produces})
	if herr != nil {
		return nil, false, fmt.Errorf("reuse step %s: %w", s.Step, herr)
	}
	saved, err := saveStep(ctx, tx, *s)
	if err != nil {
		return nil, false, err
	}
	*s = saved
	return append(drafts, stepDraft(*r, *s)), true, nil
}

// enqueue starts a new attempt of s as a step job.
func (e *Engine) enqueue(ctx context.Context, tx pgx.Tx, r Run, s *StepRow, reason string, batchScale float64) ([]events.Draft, error) {
	spec := steps.Spec{
		StepID: s.ID, PipelineRunID: r.ID, ProjectID: r.ProjectID, RunID: r.RunID, Kind: s.Kind, KindVersion: s.KindVersion,
		Params: mustJSON(s.Params), Inputs: s.Inputs, Outputs: s.Produces, Resources: s.Resources, Priority: r.Priority,
		EstimateSeconds: s.EstimateSeconds, Overrides: steps.Overrides{BatchScale: batchScale}, SecretNames: s.SecretNames,
		Attempt: s.Attempts + 1,
	}
	if spec.Inputs == nil {
		spec.Inputs = map[string]steps.ArtifactRef{}
	}
	if err := spec.Validate(); err != nil {
		return nil, fmt.Errorf("step %s: %w", s.Step, err)
	}
	actx := ctx
	if _, ok := auth.FromContext(ctx); !ok {
		actx = auth.WithActor(ctx, r.Actor)
	}
	j, drafts, err := e.o.Jobs.Enqueue(actx, tx, jobs.Spec{Kind: steps.JobKind, ProjectID: r.ProjectID, Args: spec})
	if err != nil {
		return nil, err
	}
	s.Attempts = spec.Attempt
	s.JobID, s.State, s.Error, s.FinishedAt = j.ID, StepQueued, nil, nil
	s.AttemptLog = append(s.AttemptLog, Attempt{Attempt: spec.Attempt, JobID: j.ID, Reason: reason, BatchScale: batchScale, State: StepQueued})
	saved, err := saveStep(ctx, tx, *s)
	if err != nil {
		return nil, err
	}
	*s = saved
	return append(drafts, stepDraft(r, *s)), nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// ---------------------------------------------------------------- the step job

// handle is the step job's handler: it waits for the lease's outcome and applies it.
func (e *Engine) handle(ctx context.Context, run *jobs.Run) (any, error) {
	var spec steps.Spec
	if err := json.Unmarshal(run.Args, &spec); err != nil {
		return nil, fmt.Errorf("step job %s: bad spec: %w", run.Job.ID, err)
	}
	st, found, err := StepByJob(ctx, e.o.Pool, run.Job.ID)
	if err != nil {
		return nil, err
	}
	if !found || !active(st.State) {
		return map[string]any{"skipped": "the step moved on without this job"}, nil
	}
	out, err := e.o.Leases.Await(ctx, run.Job.ID)
	fctx := context.WithoutCancel(ctx)
	if err != nil {
		if ctx.Err() == nil {
			out = steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: err.Error()}}
		} else {
			j, jerr := jobs.Get(fctx, e.o.Pool, run.Job.ID)
			if jerr != nil || j.CancelRequestedAt == nil {
				return nil, fmt.Errorf("step %s: %w", spec.StepID, ctx.Err()) // stopping: the sweep resumes the step
			}
			out = steps.Outcome{State: steps.StateCancelled, Error: &steps.StepError{Type: steps.ErrCancelled, Message: "the step job was cancelled"}}
		}
	}
	if err := e.complete(fctx, run.Job.ID, spec, out); err != nil {
		return nil, err
	}
	switch {
	case out.State == steps.StateDone:
		return map[string]any{"state": out.State, "outputs": out.Outputs, "metrics": out.Metrics}, nil
	case out.Error != nil:
		return nil, out.Error
	default:
		return nil, fmt.Errorf("step ended %s", out.State)
	}
}

// Leased marks the step of job jobID running: the worker protocol calls it in the transaction that grants the
// lease. It returns the events to emit (none when the step is not queued on that job).
func (e *Engine) Leased(ctx context.Context, tx pgx.Tx, jobID string) ([]events.Draft, error) {
	st, found, err := StepByJob(ctx, tx, jobID)
	if err != nil || !found || st.State != StepQueued {
		return nil, err
	}
	r, err := lockRun(ctx, tx, st.PipelineRunID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	st.State = StepRunning
	if st.StartedAt == nil {
		st.StartedAt = &now
	}
	if a := st.lastAttempt(); a != nil {
		a.State, a.StartedAt = StepRunning, &now
	}
	saved, err := saveStep(ctx, tx, st)
	if err != nil {
		return nil, err
	}
	return []events.Draft{stepDraft(r, saved)}, nil
}

// complete applies a lease outcome to the step of job jobID in one transaction.
func (e *Engine) complete(ctx context.Context, jobID string, spec steps.Spec, out steps.Outcome) error {
	return pgx.BeginFunc(ctx, e.o.Pool, func(tx pgx.Tx) error {
		r, err := lockRun(ctx, tx, spec.PipelineRunID)
		if err != nil {
			return err
		}
		sts, err := stepsOf(ctx, tx, r.ID, true)
		if err != nil {
			return err
		}
		i := indexOf(sts, spec.StepID)
		if i < 0 || sts[i].JobID != jobID || !active(sts[i].State) {
			return nil // cancelled or superseded meanwhile
		}
		var drafts []events.Draft
		switch out.State {
		case steps.StateDone:
			drafts, err = e.succeed(ctx, tx, &r, sts, i, spec, out)
		case steps.StateCancelled:
			drafts, err = e.cancelRun(ctx, tx, &r, sts, "step "+sts[i].Step+" was cancelled")
		default:
			se := out.Error
			if se == nil {
				se = &steps.StepError{Type: steps.ErrStep, Message: "the step failed without saying why"}
			}
			drafts, err = e.fail(ctx, tx, &r, sts, i, *se)
		}
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
}

func indexOf(sts []StepRow, id string) int {
	for i, s := range sts {
		if s.ID == id {
			return i
		}
	}
	return -1
}

// succeed records the outputs, runs the output hooks in a savepoint of tx, marks the step done and advances the
// run. Missing, mistyped or absent-from-store outputs and failing hooks fail the step instead.
func (e *Engine) succeed(ctx context.Context, tx pgx.Tx, r *Run, sts []StepRow, i int, spec steps.Spec, out steps.Outcome) ([]events.Draft, error) {
	s := &sts[i]
	for _, name := range sortedKeys(s.Produces) {
		ref, ok := out.Outputs[name]
		switch {
		case !ok:
			return e.fail(ctx, tx, r, sts, i, steps.StepError{Type: steps.ErrStep, Message: fmt.Sprintf("the step reported no output %q (%s)", name, s.Produces[name])})
		case ref.Type != s.Produces[name]:
			return e.fail(ctx, tx, r, sts, i, steps.StepError{Type: steps.ErrStep, Message: fmt.Sprintf("output %q is a %s, the kind produces %s", name, ref.Type, s.Produces[name])})
		}
	}
	for name := range out.Outputs {
		if _, ok := s.Produces[name]; !ok {
			return e.fail(ctx, tx, r, sts, i, steps.StepError{Type: steps.ErrStep, Message: fmt.Sprintf("the step reported output %q, which %s does not produce", name, spec.KindRef())})
		}
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("savepoint: %w", err)
	}
	outputs := map[string]steps.ArtifactRef{}
	var herr error
	for _, name := range sortedKeys(out.Outputs) {
		a, err := artifacts.Record(ctx, sp, e.o.CAS, out.Outputs[name], r.ProjectID,
			&artifacts.Producer{PipelineRunID: r.ID, StepID: s.ID, Step: s.Step, Output: name})
		if err != nil {
			herr = fmt.Errorf("output %q: %w", name, err)
			break
		}
		ref := out.Outputs[name]
		ref.Size = a.Size
		outputs[name] = ref
	}
	now := time.Now()
	s.State, s.Outputs, s.Metrics, s.FinishedAt, s.Error = StepDone, outputs, out.Metrics, &now, nil
	var drafts []events.Draft
	if herr == nil {
		drafts, herr = e.runHooks(ctx, sp, *r, *s, spec)
	}
	if herr != nil {
		_ = sp.Rollback(ctx)
		s.Outputs, s.Metrics = nil, nil
		return e.fail(ctx, tx, r, sts, i, steps.StepError{Type: steps.ErrStep, Message: herr.Error()})
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, fmt.Errorf("release savepoint: %w", err)
	}
	if a := s.lastAttempt(); a != nil {
		a.State, a.FinishedAt = StepDone, &now
	}
	saved, err := saveStep(ctx, tx, *s)
	if err != nil {
		return nil, err
	}
	*s = saved
	drafts = append(drafts, stepDraft(*r, *s))
	more, err := e.advance(ctx, tx, r, sts)
	if err != nil {
		return nil, err
	}
	return append(drafts, more...), nil
}

// runHooks runs the output hooks of every output of s, by output name.
func (e *Engine) runHooks(ctx context.Context, tx pgx.Tx, r Run, s StepRow, spec steps.Spec) ([]events.Draft, error) {
	var drafts []events.Draft
	for _, name := range sortedKeys(s.Outputs) {
		ev, err := e.o.Hooks.Run(ctx, tx, steps.Output{
			ProjectID: r.ProjectID, PipelineRunID: r.ID, StepID: s.ID, RunID: r.RunID, Name: name, Artifact: s.Outputs[name],
			Metrics: s.Metrics, Spec: spec,
		})
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, ev...)
	}
	return drafts, nil
}

// fail applies a step failure: an OOM gets one automatic retry at steps.OOMBatchScale, a lost lease one retry at
// the same scale; otherwise the step fails, the run fails and the steps that never started are skipped.
func (e *Engine) fail(ctx context.Context, tx pgx.Tx, r *Run, sts []StepRow, i int, se steps.StepError) ([]events.Draft, error) {
	s := &sts[i]
	now := time.Now()
	scale := 0.0
	if a := s.lastAttempt(); a != nil {
		scale = a.BatchScale
		a.State, a.Error, a.FinishedAt = StepFailed, &se, &now
	}
	if r.State == RunRunning {
		switch {
		case se.Type == steps.ErrOOM && !s.hadAttempt(ReasonOOM):
			return e.enqueue(ctx, tx, *r, s, ReasonOOM, steps.OOMBatchScale)
		case se.Type == steps.ErrLost && !s.hadAttempt(ReasonLost):
			return e.enqueue(ctx, tx, *r, s, ReasonLost, scale)
		}
	}
	s.State, s.Error, s.FinishedAt = StepFailed, &se, &now
	saved, err := saveStep(ctx, tx, *s)
	if err != nil {
		return nil, err
	}
	*s = saved
	drafts := []events.Draft{stepDraft(*r, *s)}
	if r.State != RunRunning {
		return drafts, nil
	}
	for j := range sts {
		if sts[j].State != StepWaiting {
			continue
		}
		sts[j].State = StepSkipped
		if sts[j], err = saveStep(ctx, tx, sts[j]); err != nil {
			return nil, err
		}
		drafts = append(drafts, stepDraft(*r, sts[j]))
	}
	r.State, r.Error, r.FinishedAt = RunFailed, fmt.Sprintf("step %s failed (%s): %s", s.Step, se.Type, se.Message), &now
	if *r, err = saveRun(ctx, tx, *r); err != nil {
		return nil, err
	}
	return append(drafts, runDraft(*r, EventStateChanged)), nil
}

// cancelRun cancels the run: waiting steps are cancelled, queued and running step jobs are cancelled.
func (e *Engine) cancelRun(ctx context.Context, tx pgx.Tx, r *Run, sts []StepRow, reason string) ([]events.Draft, error) {
	now := time.Now()
	var drafts []events.Draft
	for j := range sts {
		s := &sts[j]
		switch {
		case s.State == StepWaiting:
		case active(s.State):
			if a := s.lastAttempt(); a != nil {
				a.State, a.FinishedAt = StepCancelled, &now
			}
			ev, err := e.cancelJob(ctx, tx, s.JobID)
			if err != nil {
				return nil, err
			}
			drafts = append(drafts, ev...)
		default:
			continue
		}
		s.State, s.FinishedAt = StepCancelled, &now
		saved, err := saveStep(ctx, tx, *s)
		if err != nil {
			return nil, err
		}
		*s = saved
		drafts = append(drafts, stepDraft(*r, *s))
	}
	if r.State == RunRunning {
		var err error
		r.State, r.Error, r.FinishedAt = RunCancelled, reason, &now
		if *r, err = saveRun(ctx, tx, *r); err != nil {
			return nil, err
		}
		drafts = append(drafts, runDraft(*r, EventStateChanged))
	}
	return drafts, nil
}

// cancelJob cancels a step job unless it already ended.
func (e *Engine) cancelJob(ctx context.Context, tx pgx.Tx, jobID string) ([]events.Draft, error) {
	if jobID == "" || e.o.Jobs == nil {
		return nil, nil
	}
	j, err := jobs.Get(ctx, tx, jobID)
	if err != nil {
		return nil, err
	}
	if jobs.Terminal(j.State) || j.CancelRequestedAt != nil {
		return nil, nil
	}
	_, drafts, err := e.o.Jobs.Cancel(ctx, tx, jobID, j.Rev)
	return drafts, err
}

// ---------------------------------------------------------------- commands

// Cancel cancels pipeline run id at revision rev (pipelineRuns.cancel).
func (e *Engine) Cancel(ctx context.Context, tx pgx.Tx, id string, rev int) (Run, []events.Draft, error) {
	r, err := lockRun(ctx, tx, id)
	if err != nil {
		return Run{}, nil, err
	}
	if err := commands.CheckRev("pipeline run", rev, r.Rev); err != nil {
		return Run{}, nil, err
	}
	if Terminal(r.State) {
		return Run{}, nil, problems.Conflict.New("pipeline run %s already ended (%s)", id, r.State)
	}
	sts, err := stepsOf(ctx, tx, id, true)
	if err != nil {
		return Run{}, nil, err
	}
	drafts, err := e.cancelRun(ctx, tx, &r, sts, "cancelled by "+actorName(ctx))
	if err != nil {
		return Run{}, nil, err
	}
	r.Steps = sts
	return r, drafts, nil
}

func actorName(ctx context.Context) string {
	a, _ := auth.FromContext(ctx)
	if a.Name != "" {
		return a.Name
	}
	if a.ID != "" {
		return a.ID
	}
	return "a person"
}

// RetryInput names what pipelineRuns.retry runs again.
type RetryInput struct {
	Step       string   // "" = every failed step
	BatchScale *float64 // nil = full batch
}

// Retry runs failed (or cancelled) steps of pipeline run id again as new attempts and reopens the run.
func (e *Engine) Retry(ctx context.Context, tx pgx.Tx, id string, rev int, in RetryInput) (Run, []events.Draft, error) {
	r, err := lockRun(ctx, tx, id)
	if err != nil {
		return Run{}, nil, err
	}
	if err := commands.CheckRev("pipeline run", rev, r.Rev); err != nil {
		return Run{}, nil, err
	}
	sts, err := stepsOf(ctx, tx, id, true)
	if err != nil {
		return Run{}, nil, err
	}
	var targets []int
	for i, s := range sts {
		retryable := s.State == StepFailed || s.State == StepCancelled
		switch {
		case in.Step != "" && s.Step == in.Step:
			if !retryable {
				return Run{}, nil, problems.Conflict.New("step %s is %s; only a failed or cancelled step is retried", s.Step, s.State)
			}
			targets = append(targets, i)
		case in.Step == "" && s.State == StepFailed:
			targets = append(targets, i)
		}
	}
	if len(targets) == 0 {
		if in.Step != "" {
			return Run{}, nil, problems.NotFound.New("pipeline run %s has no step %q", id, in.Step)
		}
		return Run{}, nil, problems.Conflict.New("pipeline run %s has no failed step to retry", id)
	}
	scale := 0.0
	if in.BatchScale != nil {
		if *in.BatchScale <= 0 || *in.BatchScale > 1 {
			return Run{}, nil, problems.BadRequest.New("batchScale must be in (0, 1]")
		}
		if *in.BatchScale < 1 {
			scale = *in.BatchScale
		}
	}
	var drafts []events.Draft
	if r.State != RunRunning {
		r.State, r.Error, r.FinishedAt = RunRunning, "", nil
		if r, err = saveRun(ctx, tx, r); err != nil {
			return Run{}, nil, err
		}
		drafts = append(drafts, runDraft(r, EventStateChanged))
	}
	isTarget := map[int]bool{}
	for _, i := range targets {
		isTarget[i] = true
		s := &sts[i]
		if s.Attempts == 0 || s.Inputs == nil { // cancelled before it ever ran: wait for its inputs again
			s.State, s.Error, s.FinishedAt = StepWaiting, nil, nil
			if *s, err = saveStep(ctx, tx, *s); err != nil {
				return Run{}, nil, err
			}
			drafts = append(drafts, stepDraft(r, *s))
			continue
		}
		ev, err := e.enqueue(ctx, tx, r, s, ReasonRetry, scale)
		if err != nil {
			return Run{}, nil, err
		}
		drafts = append(drafts, ev...)
	}
	for i := range sts {
		s := &sts[i]
		if isTarget[i] || (s.State != StepSkipped && s.State != StepCancelled) {
			continue
		}
		s.State, s.Error, s.FinishedAt = StepWaiting, nil, nil
		if *s, err = saveStep(ctx, tx, *s); err != nil {
			return Run{}, nil, err
		}
		drafts = append(drafts, stepDraft(r, *s))
	}
	more, err := e.advance(ctx, tx, &r, sts)
	if err != nil {
		return Run{}, nil, err
	}
	r.Steps, err = stepsOf(ctx, tx, id, false)
	return r, append(drafts, more...), err
}

// Wait returns the run once it ends or when timeout passes.
func (e *Engine) Wait(ctx context.Context, id string, timeout, poll time.Duration) (Run, error) {
	deadline := time.Now().Add(timeout)
	for {
		r, err := Get(ctx, e.o.Pool, id)
		if err != nil || Terminal(r.State) || !time.Now().Before(deadline) {
			return r, err
		}
		select {
		case <-ctx.Done():
			return r, nil
		case <-time.After(min(poll, time.Until(deadline))):
		}
	}
}

// ---------------------------------------------------------------- sweep

// Sweep finds steps whose current job ended without applying an outcome (the control plane stopped while the
// step job waited) and treats them as lost: one retry, then failure.
func (e *Engine) Sweep(ctx context.Context) error {
	rows, err := e.o.Pool.Query(ctx, `SELECT s.job_id, s.pipeline_run_id, s.id FROM pipeline_steps s
		JOIN jobs j ON j.id = s.job_id LEFT JOIN river_job rj ON rj.id = j.river_id
		WHERE s.state IN ('queued', 'running')
		  AND (j.state IN ('done', 'failed', 'cancelled') OR rj.state IN ('completed', 'discarded', 'cancelled'))
		ORDER BY s.updated_at LIMIT 100`)
	if err != nil {
		return fmt.Errorf("sweep pipeline steps: %w", err)
	}
	type orphan struct{ job, run, step string }
	var list []orphan
	var o orphan
	if _, err := pgx.ForEachRow(rows, []any{&o.job, &o.run, &o.step}, func() error { list = append(list, o); return nil }); err != nil {
		return fmt.Errorf("sweep pipeline steps: %w", err)
	}
	for _, o := range list {
		e.o.Log.WarnContext(ctx, "pipeline step lost its job", "step", o.step, "job", o.job)
		err := e.complete(ctx, o.job, steps.Spec{StepID: o.step, PipelineRunID: o.run}, steps.Outcome{
			State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrLost, Message: "the step job ended without an outcome (the control plane stopped while it waited)"},
		})
		if err != nil {
			return err
		}
	}
	return nil
}
