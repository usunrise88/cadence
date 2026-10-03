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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/auxiliary"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/data"
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
	// Prober checks the services auxiliary references name before a run starts (dry runs included); nil checks
	// nothing.
	Prober auxiliary.Prober
}

// Engine runs pipelines.
type Engine struct {
	o          Options
	observerMu sync.Mutex
	observers  atomic.Pointer[map[string]RunObserver]
}

// RunObserver is told about every change of a pipeline run that belongs to a facade's entity (RunID set: a training
// run, an eval), inside the transaction that made it, after the run and its steps were saved; the events it returns
// join that transaction's. A facade (internal/runs, internal/evals) uses it to mirror the pipeline run's state onto
// its own entity and ignores runs that are not its own. It is called after
// Start, after a step outcome is applied, when a worker leases a step (Leased), and after Cancel and Retry.
type RunObserver func(ctx context.Context, tx pgx.Tx, r Run) ([]events.Draft, error)

// SetObserver installs the run observer of training runs (a later call replaces it); SetNamedObserver adds others.
func (e *Engine) SetObserver(fn RunObserver) { e.SetNamedObserver("", fn) }

// SetNamedObserver installs (or replaces) the observer called name: one per facade (runs, evals), each told about
// every pipeline run with a RunID and left to recognise its own (an eval's pipeline run carries the eval's id).
// Observers run in name order.
func (e *Engine) SetNamedObserver(name string, fn RunObserver) {
	e.observerMu.Lock()
	defer e.observerMu.Unlock()
	cur := e.observers.Load()
	next := map[string]RunObserver{}
	if cur != nil {
		maps.Copy(next, *cur)
	}
	next[name] = fn
	e.observers.Store(&next)
}

// observe calls the observers for a run that belongs to a facade's entity.
func (e *Engine) observe(ctx context.Context, tx pgx.Tx, r Run) ([]events.Draft, error) {
	obs := e.observers.Load()
	if obs == nil || r.RunID == "" {
		return nil, nil
	}
	var drafts []events.Draft
	for _, name := range slices.Sorted(maps.Keys(*obs)) {
		ev, err := (*obs)[name](ctx, tx, r)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, ev...)
	}
	return drafts, nil
}

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

// SetProber replaces the check of auxiliary services (tests); call it before the engine plans runs.
func (e *Engine) SetProber(p auxiliary.Prober) { e.o.Prober = p }

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
	RunID     string                    // the facade entity it belongs to: a training run (run_…) or an eval (evl_…)
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
	plan, err := e.Plan(ctx, q, src.Pipeline, PlanInput{Inputs: in.Inputs, Params: in.Params, Estimates: in.Estimates,
		ProjectID: in.ProjectID})
	if err == nil {
		err = e.trainable(ctx, q, plan, in.Inputs)
	}
	if err == nil {
		err = licensed(ctx, q, plan)
	}
	for _, ps := range plan.Steps {
		if err != nil {
			break
		}
		// A service an auxiliary names must answer before anything is queued: Cadence never starts one (R26).
		err = auxiliary.CheckServices(ctx, e.o.Prober, ps.Step, ps.Auxiliaries)
	}
	return src, plan, err
}

// RegistrySource is the x-cadence.registry value of a step parameter that names a registry source (sdp_ingest's
// source): the engine applies "no licence, no ingest" to it.
const RegistrySource = "source"

// licensed refuses a step whose parameters name a registry source (x-cadence.registry: source) that is missing,
// archived or without a usable licence (data.IngestAllowed: source-unlicensed) — docs/spec/04-blocks.md Block 1,
// "no licence, no ingest". The dataset hook checks the source again when the draft registers.
func licensed(ctx context.Context, q storage.Querier, plan Plan) error {
	for _, ps := range plan.Steps {
		if len(bytes.TrimSpace(ps.Kind.Params)) == 0 || string(ps.Kind.Params) == "null" {
			continue
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(ps.Kind.Params, &schema); err != nil {
			continue // Plan has reported it
		}
		for _, name := range sortedKeys(schema.Properties) {
			if reg, _ := xCadence(schema.Properties[name])["registry"].(string); reg != RegistrySource {
				continue
			}
			v, _ := ps.Params[name].(string)
			if _, err := data.IngestAllowed(ctx, q, v); err != nil {
				if pe, ok := problems.As(err); ok {
					pe.Detail = fmt.Sprintf("step %s (%s), parameter %s: %s", ps.Step, ps.Kind.Ref(), name, pe.Detail)
				}
				return err
			}
		}
	}
	return nil
}

// trainable refuses a run input that a training step reads directly unless all it trains on is registered and
// trainable (data.TrainableArtifact: the type and meta come from the artifact index, a mix's datasets from its
// content). An input only non-training steps read (eval, data, export: resources.jobKind) may be eval-only: golden
// and replay sets are evaluated, never trained. Inputs a training step gets from other steps are checked when it is
// queued (advance).
func (e *Engine) trainable(ctx context.Context, q storage.Querier, plan Plan, inputs map[string]steps.ArtifactRef) error {
	for _, name := range sortedKeys(inputs) {
		for _, ps := range plan.Steps {
			if !trains(ps.Kind.Resources) || !readsInput(ps, name) {
				continue
			}
			if err := data.TrainableArtifact(ctx, q, e.o.CAS, inputs[name]); err != nil {
				return err
			}
			break
		}
	}
	return nil
}

// trains reports whether a step with these resources is a training step (the queue's default job kind).
func trains(r steps.Resources) bool { return r.JobKind == "" || r.JobKind == steps.JobTraining }

// readsInput reports whether step ps is wired to the run input name ($inputs.<name>).
func readsInput(ps PlanStep, name string) bool {
	for _, wire := range ps.In {
		if w, ok := ParseWire(wire); ok && w.Input == name {
			return true
		}
	}
	return false
}

// indexedSize completes an input given as {hash, type} (size is optional in the contract, and checkpoints.list or
// datasets.get name only the hash) with the size and meta the artifact index holds; an unindexed artifact keeps what
// was sent and is verified against the store as it is.
func indexedSize(ctx context.Context, q storage.Querier, ref steps.ArtifactRef) (steps.ArtifactRef, error) {
	if ref.Size != 0 {
		return ref, nil
	}
	a, err := artifacts.Get(ctx, q, ref.Hash)
	if err != nil {
		if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
			return ref, nil
		}
		return ref, err
	}
	ref.Size = a.Size
	if len(ref.Meta) == 0 {
		ref.Meta = a.Meta
	}
	return ref, nil
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
		ref, err := indexedSize(ctx, tx, in.Inputs[name])
		if err != nil {
			return Run{}, nil, err
		}
		in.Inputs[name] = ref
		if _, err := artifacts.Record(ctx, tx, e.o.CAS, ref, in.ProjectID, nil); errors.Is(err, artifacts.ErrEvicted) {
			return Run{}, nil, problems.ArtifactMissing.New("input %s: %v", name, err)
		} else if err != nil {
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
			Auxiliaries: ps.Auxiliaries,
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
	if r.Steps, err = stepsOf(ctx, tx, r.ID, false); err != nil {
		return Run{}, nil, err
	}
	obs, err := e.observe(ctx, tx, r)
	if err != nil {
		return Run{}, nil, err
	}
	return r, append(append(drafts, more...), obs...), nil
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
			if trains(s.Resources) {
				// The indirect path: a step that does not train may pass a golden set (or anything else) through
				// to one that does; the resolved inputs get the run inputs' check before the step is queued or reused.
				refused, err := e.trainableInputs(ctx, tx, *s)
				if err != nil {
					return nil, err
				}
				if refused != "" {
					failed, err := e.fail(ctx, tx, r, sts, i, steps.StepError{Type: steps.ErrInput, Message: refused})
					if err != nil {
						return nil, err
					}
					return append(drafts, failed...), nil
				}
			}
			runtime, err := runtimeOf(ctx, tx, s.StepKindVersionID)
			if err != nil {
				return nil, err
			}
			if s.InputHash, err = InputHash(s.Kind, s.KindVersion, runtime, hashParams(*s), inputs); err != nil {
				return nil, err
			}
			ev, reused, err := e.reuse(ctx, tx, r, s)
			if refused, ok := errors.AsType[*refusedReuse](err); ok {
				// An output hook refused the reused outputs: the step fails like one whose fresh output was
				// refused, and so does the run; nothing else in the caller's transaction is undone.
				failed, err := e.fail(ctx, tx, r, sts, i, steps.StepError{Type: steps.ErrStep, Message: refused.Error()})
				if err != nil {
					return nil, err
				}
				return append(drafts, failed...), nil
			}
			if err != nil {
				return nil, err
			}
			if !reused {
				reason := ReasonInitial
				if s.Attempts > 0 {
					reason = ReasonRetry
				}
				if ev, err = e.enqueue(ctx, tx, *r, s, reason, steps.Overrides{}); err != nil {
					return nil, err
				}
			}
			drafts = append(drafts, ev...)
			changed = true
		}
	}
	all := true
	for _, s := range sts {
		all = all && (finished(s.State) || (r.optional(s.Step) && (s.State == StepFailed || s.State == StepSkipped)))
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

// trainableInputs checks the resolved inputs of training step s (data.TrainableArtifact); a refusal comes back as
// the message the step fails with, any other error as err.
func (e *Engine) trainableInputs(ctx context.Context, q storage.Querier, s StepRow) (string, error) {
	for _, name := range sortedKeys(s.Inputs) {
		err := data.TrainableArtifact(ctx, q, e.o.CAS, s.Inputs[name])
		if pe, ok := problems.As(err); ok {
			return fmt.Sprintf("input %s refused for training (%s): %s", name, pe.Type.Slug, pe.Detail), nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", nil
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
	// The outputs verified here stay in the store until tx ends: an eviction waits for the shared lock.
	if err := artifacts.LockShared(ctx, tx); err != nil {
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
	// The hooks run in a savepoint, as for a fresh output: a refusal undoes their writes and fails this step
	// (refused), never the caller's transaction.
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("savepoint: %w", err)
	}
	drafts, herr := e.runHooks(ctx, sp, *r, *s, steps.Spec{StepID: s.ID, PipelineRunID: r.ID, ProjectID: r.ProjectID, RunID: r.RunID,
		Kind: s.Kind, KindVersion: s.KindVersion, Params: mustJSON(s.Params), Inputs: s.Inputs, Outputs: s.Produces})
	if herr != nil {
		if err := sp.Rollback(ctx); err != nil {
			return nil, false, fmt.Errorf("roll back savepoint: %w", err)
		}
		s.State, s.Outputs, s.ReusedFrom, s.Metrics, s.FinishedAt = StepWaiting, nil, "", nil, nil
		return nil, false, &refusedReuse{from: prev.ID, err: herr}
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("release savepoint: %w", err)
	}
	saved, err := saveStep(ctx, tx, *s)
	if err != nil {
		return nil, false, err
	}
	*s = saved
	return append(drafts, stepDraft(*r, *s)), true, nil
}

// refusedReuse is an output hook's refusal of the outputs a step would reuse.
type refusedReuse struct {
	from string // the step whose outputs were offered
	err  error
}

func (e *refusedReuse) Error() string {
	return fmt.Sprintf("reused outputs of step %s: %v", e.from, e.err)
}

func (e *refusedReuse) Unwrap() error { return e.err }

// enqueue starts a new attempt of s as a step job with overrides ov (batch scale, training state to resume from).
func (e *Engine) enqueue(ctx context.Context, tx pgx.Tx, r Run, s *StepRow, reason string, ov steps.Overrides) ([]events.Draft, error) {
	spec := steps.Spec{
		StepID: s.ID, PipelineRunID: r.ID, ProjectID: r.ProjectID, RunID: r.RunID, Kind: s.Kind, KindVersion: s.KindVersion,
		Params: mustJSON(s.Params), Inputs: s.Inputs, Outputs: s.Produces, Resources: s.Resources, Priority: r.Priority,
		EstimateSeconds: s.EstimateSeconds, Overrides: ov, SecretNames: s.SecretNames,
		Attempt: s.Attempts + 1, Auxiliaries: s.Auxiliaries,
	}
	if len(spec.Auxiliaries) == 0 {
		spec.Auxiliaries = nil
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
	s.AttemptLog = append(s.AttemptLog, Attempt{Attempt: spec.Attempt, JobID: j.ID, Reason: reason, BatchScale: ov.BatchScale,
		ResumeFrom: ov.ResumeFrom, State: StepQueued})
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
				if errors.Is(ctx.Err(), context.Canceled) {
					// The control plane is stopping: the step job (and a worker's lease on it) lives on, and the
					// handler waits for its outcome again on the next start.
					return nil, fmt.Errorf("step %s: %w", spec.StepID, jobs.ErrInterrupted)
				}
				return nil, fmt.Errorf("step %s: %w", spec.StepID, ctx.Err()) // timed out: the sweep retries the step
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
// lease. It returns the events to emit (none when the step is not queued on that job). A step already running (its
// job was paused or stopped by a window and requeued in place) only has the run observer called again.
func (e *Engine) Leased(ctx context.Context, tx pgx.Tx, jobID string) ([]events.Draft, error) {
	st, found, err := StepByJob(ctx, tx, jobID)
	if err != nil || !found {
		return nil, err
	}
	if st.State == StepRunning {
		// A paused (or window-closed) job requeued in place is leased again: the step stays running, but the
		// observer re-derives what depends on the lease (a training run goes from queued back to running).
		r, err := lockRun(ctx, tx, st.PipelineRunID)
		if err != nil {
			return nil, err
		}
		return e.observe(ctx, tx, r)
	}
	if st.State != StepQueued {
		return nil, nil
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
	obs, err := e.observe(ctx, tx, r)
	if err != nil {
		return nil, err
	}
	return append([]events.Draft{stepDraft(r, saved)}, obs...), nil
}

// Published records an intermediate output a running step published during its lease (workerOutputs.new) inside the
// publishing transaction: the artifact is indexed with the step as producer and the output hooks of its type run (a
// validation checkpoint registers on the run at once). Nothing happens for a job without an active pipeline step.
// The step's outputs stay what its release reports.
func (e *Engine) Published(ctx context.Context, tx pgx.Tx, jobID string, spec steps.Spec, name string, ref steps.ArtifactRef,
	metrics map[string]float64) ([]events.Draft, error) {
	st, found, err := StepByJob(ctx, tx, jobID)
	if err != nil || !found || !active(st.State) {
		return nil, err
	}
	r, err := lockRun(ctx, tx, st.PipelineRunID)
	if err != nil {
		return nil, err
	}
	a, err := artifacts.Record(ctx, tx, e.o.CAS, ref, r.ProjectID,
		&artifacts.Producer{PipelineRunID: r.ID, StepID: st.ID, Step: st.Step, Output: name})
	if err != nil {
		return nil, problems.Validation([]problems.FieldError{{Path: "/artifact", Message: err.Error()}})
	}
	ref.Size = a.Size
	return e.o.Hooks.Run(ctx, tx, steps.Output{
		ProjectID: r.ProjectID, PipelineRunID: r.ID, StepID: st.ID, RunID: r.RunID, Name: name, Artifact: ref,
		Metrics: metrics, Spec: spec,
	})
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
		obs, err := e.observe(ctx, tx, r)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, jobs.System, nil, append(drafts, obs...))
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
	var optional []string // outputs the kind may leave unwritten, read only when one is missing
	for _, name := range sortedKeys(s.Produces) {
		ref, ok := out.Outputs[name]
		if !ok && optional == nil {
			k, found, err := (RegistryKinds{}).Lookup(ctx, tx, s.Kind, s.KindVersion)
			if err != nil {
				return nil, err
			}
			optional = []string{}
			if found {
				optional = k.OptionalOutputs
			}
		}
		switch {
		case !ok && slices.Contains(optional, name):
			continue
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

// TypeTrainingState is the artifact type a train-role step resumes from (overrides.resumeFrom).
const TypeTrainingState = "training-state"

// newestPublishedState is the newest training state step stepID published during its leases (workerOutputs.new) that
// is still in the store, or "".
func newestPublishedState(ctx context.Context, q storage.Querier, stepID string) (string, error) {
	var h string
	err := q.QueryRow(ctx, `SELECT hash FROM artifacts WHERE step_id = $1 AND type = $2 AND evicted_at IS NULL
		ORDER BY created_at DESC, hash LIMIT 1`, stepID, TypeTrainingState).Scan(&h)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("newest training state of step %s: %w", stepID, err)
	}
	return h, nil
}

// OOMRetryScale is the batch scale of the automatic retry after an out-of-memory failure of an attempt that ran at
// scale failed (0: the kind's own batch, 1): steps.OOMBatchScale of it, so a retry never grows the batch back.
func OOMRetryScale(failed float64) float64 {
	if failed <= 0 {
		failed = 1
	}
	return failed * steps.OOMBatchScale
}

// fail applies a step failure: an OOM gets one automatic retry at steps.OOMBatchScale of the failed attempt's
// scale, a lost lease one retry at the same scale; otherwise the step fails, the run fails and the steps that never started are skipped.
func (e *Engine) fail(ctx context.Context, tx pgx.Tx, r *Run, sts []StepRow, i int, se steps.StepError) ([]events.Draft, error) {
	s := &sts[i]
	now := time.Now()
	var last steps.Overrides // an automatic retry resumes from what the failed attempt resumed from
	if a := s.lastAttempt(); a != nil {
		last = steps.Overrides{BatchScale: a.BatchScale, ResumeFrom: a.ResumeFrom}
		a.State, a.Error, a.FinishedAt = StepFailed, &se, &now
	}
	if r.State == RunRunning {
		switch {
		case se.Type == steps.ErrOOM && !s.hadAttempt(ReasonOOM):
			return e.enqueue(ctx, tx, *r, s, ReasonOOM, steps.Overrides{BatchScale: OOMRetryScale(last.BatchScale), ResumeFrom: last.ResumeFrom})
		case se.Type == steps.ErrLost && !s.hadAttempt(ReasonLost):
			// A lost lease (a worker or host crash) took the step's scratch with it, but the training states it
			// published meanwhile are in the store: the retry resumes from the newest instead of starting over.
			h, err := newestPublishedState(ctx, tx, s.ID)
			if err != nil {
				return nil, err
			}
			if h != "" {
				last.ResumeFrom = h
			}
			return e.enqueue(ctx, tx, *r, s, ReasonLost, last)
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
	if r.optional(s.Step) {
		// An optional step's failure skips the steps that read it, at any depth (optional too, Check), and the run
		// goes on: it ends done once the rest has.
		failed := map[string]bool{s.Step: true}
		for changed := true; changed; {
			changed = false
			for j := range sts {
				if sts[j].State != StepWaiting || !readsAny(sts[j], failed) {
					continue
				}
				sts[j].State, sts[j].FinishedAt = StepSkipped, &now
				if sts[j], err = saveStep(ctx, tx, sts[j]); err != nil {
					return nil, err
				}
				failed[sts[j].Step], changed = true, true
				drafts = append(drafts, stepDraft(*r, sts[j]))
			}
		}
		more, err := e.advance(ctx, tx, r, sts)
		if err != nil {
			return nil, err
		}
		return append(drafts, more...), nil
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
	obs, err := e.observe(ctx, tx, r)
	if err != nil {
		return Run{}, nil, err
	}
	return r, append(drafts, obs...), nil
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
	// ResumeFrom, with Step, continues that step from a training-state artifact (runs.resume): the attempt's
	// overrides.resumeFrom, reason resume.
	ResumeFrom string
}

// AnyRev skips Retry's revision check: a facade that checked its own entity's revision (runs.resume).
const AnyRev = -1

// RetryEstimate is the GPU time a retry of run id may spend: every step not done yet (the retried steps and those
// waiting behind them) that needs a card, at its recorded estimate. unknown is true when such a step has none.
func RetryEstimate(ctx context.Context, q storage.Querier, id string) (gpuHours float64, unknown bool, err error) {
	sts, err := stepsOf(ctx, q, id, false)
	if err != nil {
		return 0, false, err
	}
	for _, s := range sts {
		if s.State == StepDone || s.State == StepReused || !s.Resources.GPU {
			continue
		}
		if s.EstimateSeconds == nil {
			unknown = true
			continue
		}
		gpuHours += *s.EstimateSeconds / 3600 * float64(max(1, s.Resources.GPUs))
	}
	return gpuHours, unknown, nil
}

// Retry runs failed (or cancelled) steps of pipeline run id again as new attempts and reopens the run.
func (e *Engine) Retry(ctx context.Context, tx pgx.Tx, id string, rev int, in RetryInput) (Run, []events.Draft, error) {
	r, err := lockRun(ctx, tx, id)
	if err != nil {
		return Run{}, nil, err
	}
	if rev != AnyRev {
		if err := commands.CheckRev("pipeline run", rev, r.Rev); err != nil {
			return Run{}, nil, err
		}
	}
	if in.ResumeFrom != "" && (in.Step == "" || !steps.ValidHash(in.ResumeFrom)) {
		return Run{}, nil, problems.BadRequest.New("resuming needs the step and a training-state artifact hash")
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
	if len(targets) == 0 && in.Step == "" { // no failed step: continue a cancelled run from its cancelled steps
		for i, s := range sts {
			if s.State == StepCancelled {
				targets = append(targets, i)
			}
		}
	}
	if len(targets) == 0 {
		if in.Step != "" {
			return Run{}, nil, problems.NotFound.New("pipeline run %s has no step %q", id, in.Step)
		}
		return Run{}, nil, problems.Conflict.New("pipeline run %s has no failed or cancelled step to retry", id)
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
		reason, ov := ReasonRetry, steps.Overrides{BatchScale: scale}
		if in.ResumeFrom != "" {
			reason, ov.ResumeFrom = ReasonResume, in.ResumeFrom
		}
		ev, err := e.enqueue(ctx, tx, r, s, reason, ov)
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
	if r.Steps, err = stepsOf(ctx, tx, id, false); err != nil {
		return Run{}, nil, err
	}
	obs, err := e.observe(ctx, tx, r)
	if err != nil {
		return Run{}, nil, err
	}
	return r, append(append(drafts, more...), obs...), nil
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
