package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Service runs training runs on the pipeline engine.
type Service struct {
	Pool     *pgxpool.Pool
	Engine   *pipelines.Engine
	CAS      *cas.Store
	Defaults func() *defaults.Defaults // defaults.Get when nil
	// RenderVersion renders a base model version as the contract's RegistryVersion (the server's mapping); its
	// summary when nil.
	RenderVersion func(registry.Version) any
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

// Install registers the output hooks (checkpoint, calibration) on hooks and makes the engine report changes of a
// run's pipeline run (Observe). Hooks are idempotent, so installing twice on one registry only repeats no-ops.
func (s *Service) Install(hooks *steps.Hooks) {
	hooks.On(TypeCheckpoint, s.checkpointHook)
	hooks.On(TypeCalibration, s.calibrationHook)
	s.Engine.SetObserver(s.Observe)
}

// NewInput starts a run (runs.new, runs.stage).
type NewInput struct {
	ProjectID   string
	Actor       auth.Actor
	Init        string
	BaseModel   string
	Checkpoint  string
	ParentRunID string
	Steps       *int
	Seed        *int
	GPUs        *int
	Precision   string
	Compute     string
	Mix         string
	MixRevision int
	Pipeline    string
	Ref         string
	Params      map[string]any
	PeakLR      *float64
	Priority    int
}

// Prepared is a run checked and planned, not yet started: what a dry run answers and what Create starts.
type Prepared struct {
	In         NewInput
	Base       registry.Version
	Family     Family
	Checkpoint *Checkpoint
	Mix        RenderedMix
	Source     pipelines.Source
	TrainStep  string
	Start      pipelines.StartInput
	Plan       pipelines.Plan
	Estimate   Estimate
	Params     map[string]any // the train step's overrides
	Seed       *int
}

// Prepare resolves a run's start, family, mix and recipe, plans its pipeline and estimates it; it writes nothing
// but the rendered mix and base-model blobs (content-addressed, idempotent).
func (s *Service) Prepare(ctx context.Context, q storage.Querier, in NewInput) (Prepared, error) {
	d := s.defaults()
	if _, err := projects.GetByID(ctx, q, in.ProjectID); err != nil {
		return Prepared{}, err
	}
	in.Init = or(in.Init, d.Training.Init.Value)
	switch {
	case in.Init == InitCheckpoint && in.Checkpoint == "":
		return Prepared{}, problems.Validation([]problems.FieldError{{Path: "/checkpoint", Message: "required when init is checkpoint"}})
	case in.Init != InitCheckpoint && in.Checkpoint != "":
		return Prepared{}, problems.Validation([]problems.FieldError{{Path: "/checkpoint", Message: "only with init checkpoint"}})
	case !d.Training.Init.Range.Allows(in.Init):
		return Prepared{}, problems.Validation([]problems.FieldError{{Path: "/init", Message: fmt.Sprintf("%q is not one of %v", in.Init, d.Training.Init.Range.Values)}})
	}
	p := Prepared{In: in}
	var err error
	if in.Init == InitCheckpoint {
		c, err := GetCheckpoint(ctx, q, in.Checkpoint)
		if err != nil {
			return Prepared{}, err
		}
		if c.ProjectID != in.ProjectID {
			return Prepared{}, problems.NotFound.New("the project has no checkpoint %q", in.Checkpoint)
		}
		p.Checkpoint = &c
		if p.In.ParentRunID == "" {
			p.In.ParentRunID = c.RunID
		}
		if p.Base, err = checkpointBase(ctx, q, in.ProjectID, c.ID); err != nil {
			return Prepared{}, err
		}
	} else if p.Base, err = registry.Resolve(ctx, q, in.ProjectID, registry.KindBaseModel, or(in.BaseModel, d.Wizard.BaseModel.Value)); err != nil {
		return Prepared{}, err
	}
	if p.Family, err = FamilyOf(ctx, q, p.Base); err != nil {
		return Prepared{}, err
	}
	trainKind, err := p.Family.Role(RoleTrain)
	if err != nil {
		return Prepared{}, err
	}
	mixID, content, rev, err := ResolveMix(ctx, q, in.ProjectID, in.Mix, in.MixRevision)
	if err != nil {
		return Prepared{}, err
	}
	if p.Mix, err = RenderMix(ctx, q, s.CAS, in.ProjectID, mixID, content, rev, d.Estimates.BytesPerAudioHour.Value); err != nil {
		return Prepared{}, err
	}
	proj, err := projects.GetByID(ctx, q, in.ProjectID)
	if err != nil {
		return Prepared{}, err
	}
	if p.Source, err = s.Engine.Load(ctx, proj, or(in.Pipeline, d.Training.Pipeline.Value), in.Ref); err != nil {
		return Prepared{}, err
	}
	var trainPin string
	for _, st := range p.Source.Pipeline.Steps {
		if name, _, ok := st.KindRef(); ok && name == trainKind {
			if p.TrainStep != "" {
				return Prepared{}, problems.RecipeMismatch.New("pipeline %s has two steps of the train kind %s (%s, %s); a run is one stage",
					p.Source.Pipeline.Name, trainKind, p.TrainStep, st.ID)
			}
			p.TrainStep, trainPin = st.ID, st.Kind
		}
	}
	if p.TrainStep == "" {
		return Prepared{}, problems.RecipeMismatch.New(
			"pipeline %s has no step of kind %s, the train role of model family %s (the family of %s); edit pipelines/%s.yaml or name another pipeline",
			p.Source.Pipeline.Name, trainKind, p.Family.Name, p.Base.Name, p.Source.Pipeline.Name)
	}
	p.Params = maps.Clone(in.Params)
	if p.Params == nil {
		p.Params = map[string]any{}
	}
	name, version, _ := pipelines.Step{Kind: trainPin}.KindRef()
	if k, found, err := (pipelines.RegistryKinds{}).Lookup(ctx, q, name, version); err != nil {
		return Prepared{}, err
	} else if found {
		if err := setParam(p.Params, k, "steps", in.Steps); err != nil {
			return Prepared{}, err
		}
		if err := setParam(p.Params, k, "seed", in.Seed); err != nil {
			return Prepared{}, err
		}
		if in.Precision != "" && hasParam(k, "precision") {
			p.Params["precision"] = in.Precision
		}
		if in.PeakLR != nil {
			lr, err := peakLRParam(k)
			if err != nil {
				return Prepared{}, err
			}
			p.Params[lr] = *in.PeakLR
		}
	}
	st := start{Mix: p.Mix}
	if st.Base, err = RenderBaseModel(s.CAS, p.Base, p.Family); err != nil {
		return Prepared{}, err
	}
	if p.Checkpoint != nil {
		ref, err := sizedRef(ctx, q, s.CAS, steps.ArtifactRef{Hash: p.Checkpoint.Artifact, Type: TypeCheckpoint, Meta: p.Checkpoint.Meta})
		if err != nil {
			return Prepared{}, err
		}
		st.Checkpoint = &ref
	}
	inputs, err := inputsFor(p.Source.Pipeline.Inputs, st)
	if err != nil {
		return Prepared{}, err
	}
	p.Start = pipelines.StartInput{
		ProjectID: in.ProjectID, Name: p.Source.Pipeline.Name, Ref: in.Ref, Version: p.Source.Version, Inputs: inputs,
		Params: map[string]map[string]any{p.TrainStep: p.Params}, Actor: in.Actor, Priority: in.Priority,
	}
	if _, p.Plan, err = s.Engine.Prepare(ctx, q, p.Start); err != nil {
		return Prepared{}, err
	}
	resolved := d.Training.Steps.Value
	for _, ps := range p.Plan.Steps {
		if ps.Step != p.TrainStep {
			continue
		}
		if v, ok := ps.Params["steps"].(float64); ok {
			resolved = int(v)
		}
		if v, ok := ps.Params["seed"].(float64); ok {
			seed := int(v)
			p.Seed = &seed
		}
	}
	if p.Estimate, err = EstimateRun(ctx, q, d, Input{
		ProjectID: in.ProjectID, Init: in.Init, Checkpoint: in.Checkpoint, Steps: &resolved, Precision: in.Precision,
		GPUs: in.GPUs, Compute: in.Compute, Base: &p.Base, Mix: &p.Mix, SessionID: in.Actor.SessionID,
	}); err != nil {
		return Prepared{}, err
	}
	p.Start.Estimates = map[string]float64{p.TrainStep: p.Estimate.DurationSeconds.Value}
	return p, nil
}

// setParam sets the train step's parameter name from v when given; a train kind without it cannot take it.
func setParam(params map[string]any, k pipelines.Kind, name string, v *int) error {
	if v == nil {
		return nil
	}
	if !hasParam(k, name) {
		return problems.RecipeMismatch.New("the train step kind %s has no %s parameter", k.Ref(), name)
	}
	params[name] = *v
	return nil
}

// Create starts a prepared run in tx: the pipeline run (with the run id), the run row mirroring it, the outputs of
// reused steps registered, and run.created.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, p Prepared) (View, []events.Draft, error) {
	id := "run_" + uuid.Must(uuid.NewV7()).String()
	p.Start.RunID = id
	pr, drafts, err := s.Engine.Start(ctx, tx, p.Start)
	if err != nil {
		return View{}, nil, err
	}
	est, err := json.Marshal(s.estimateView(p.Estimate))
	if err != nil {
		return View{}, nil, fmt.Errorf("marshal estimate: %w", err)
	}
	x := row{
		ID: id, ProjectID: p.In.ProjectID, Init: p.In.Init, BaseVersionID: p.Base.ID, ParentRunID: p.In.ParentRunID,
		Family: p.Family.Name, FamilyVersionID: p.Family.VersionID, Mix: p.Mix.Ref,
		Recipe:        Recipe{Pipeline: pr.Pipeline, Source: pr.Source, Ref: pr.Ref, Commit: pr.Commit, Version: pr.Version},
		PipelineRunID: pr.ID, TrainStep: p.TrainStep, Steps: p.Estimate.Steps, Seed: p.Seed, GPUs: p.Estimate.GPUs,
		Precision: p.Estimate.Precision, Params: p.Params, Estimate: est, Actor: p.In.Actor,
		Card: Card{ComputeID: p.Estimate.Slot.Host.ID, Host: p.Estimate.Slot.Host.Name, Index: p.Estimate.Slot.Card.Index,
			CardClass: p.Estimate.Slot.Card.CardClass, MemoryCapGB: p.Estimate.Slot.Card.MemoryCapGB},
	}
	if p.Checkpoint != nil {
		x.CheckpointID = p.Checkpoint.ID
	}
	for _, ps := range p.Plan.Steps {
		if ps.Step == p.TrainStep {
			x.Runtime = runtimeOf(ctx, tx, ps.Kind)
		}
	}
	if x.Status, x.Error, x.FinishedAt, err = derive(ctx, tx, pr.ID); err != nil {
		return View{}, nil, err
	}
	if x, err = insertRow(ctx, tx, x); err != nil {
		return View{}, nil, err
	}
	full, err := pipelines.Get(ctx, tx, pr.ID)
	if err != nil {
		return View{}, nil, err
	}
	more, err := s.registerExisting(ctx, tx, x, full)
	if err != nil {
		return View{}, nil, err
	}
	drafts = append(append(drafts, statusDraft(x, EventCreated)), more...)
	v, err := s.view(ctx, tx, x)
	return v, drafts, err
}

// runtimeOf reads the runtime a step kind runs in (name and image digest from its registry version).
func runtimeOf(ctx context.Context, q storage.Querier, k pipelines.Kind) Runtime {
	rt := Runtime{Name: k.Runtime, VersionID: k.RuntimeVersionID}
	if k.RuntimeVersionID == "" {
		return rt
	}
	v, err := registry.GetVersion(ctx, q, registry.KindRuntime, k.RuntimeVersionID)
	if err != nil {
		return rt
	}
	var d struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}
	if json.Unmarshal(v.Payload, &d) == nil {
		rt.Digest = d.Digest
		if rt.Name == "" {
			rt.Name = d.Name
		}
	}
	return rt
}

// ---------------------------------------------------------------- status

// Observe mirrors a change of a run's own pipeline run onto the run (pipelines.RunObserver): the status is derived
// again and, when it changed, saved with run.status_changed.
func (s *Service) Observe(ctx context.Context, tx pgx.Tx, pr pipelines.Run) ([]events.Draft, error) {
	x, found, err := rowByPipelineRun(ctx, tx, pr.ID)
	if err != nil || !found {
		return nil, err
	}
	return s.refresh(ctx, tx, x)
}

// JobChanged re-derives the status of the run a step job belongs to (jobs.pause and jobs.resume change it outside
// the engine).
func (s *Service) JobChanged(ctx context.Context, tx pgx.Tx, jobID string) ([]events.Draft, error) {
	st, found, err := pipelines.StepByJob(ctx, tx, jobID)
	if err != nil || !found {
		return nil, err
	}
	x, found, err := rowByPipelineRun(ctx, tx, st.PipelineRunID)
	if err != nil || !found {
		return nil, err
	}
	return s.refresh(ctx, tx, x)
}

func (s *Service) refresh(ctx context.Context, tx pgx.Tx, x row) ([]events.Draft, error) {
	status, msg, finished, err := derive(ctx, tx, x.PipelineRunID)
	if err != nil {
		return nil, err
	}
	if status == x.Status && msg == x.Error {
		return nil, nil
	}
	x.Status, x.Error, x.FinishedAt = status, msg, finished
	if x, err = saveStatus(ctx, tx, x); err != nil {
		return nil, err
	}
	return []events.Draft{statusDraft(x, EventStatusChanged)}, nil
}

// derive is a run's status from its pipeline run: an ended pipeline run gives done, failed or cancelled; while it
// runs, a paused step job makes the run paused, a step a worker holds makes it running, and otherwise it is queued
// (a step waits for a card, or the next step is about to be queued).
func derive(ctx context.Context, q storage.Querier, plrID string) (string, string, *time.Time, error) {
	pr, err := pipelines.Get(ctx, q, plrID)
	if err != nil {
		return "", "", nil, err
	}
	switch pr.State {
	case pipelines.RunDone:
		return StatusDone, "", pr.FinishedAt, nil
	case pipelines.RunFailed:
		return StatusFailed, pr.Error, pr.FinishedAt, nil
	case pipelines.RunCancelled:
		return StatusCancelled, pr.Error, pr.FinishedAt, nil
	}
	status := StatusQueued
	for _, st := range pr.Steps {
		if st.State != pipelines.StepQueued && st.State != pipelines.StepRunning {
			continue
		}
		paused, leased, err := jobState(ctx, q, st.JobID)
		if err != nil {
			return "", "", nil, err
		}
		switch {
		case paused:
			return StatusPaused, "", nil, nil
		case leased || (st.State == pipelines.StepRunning && !leased && !queuedAgain(ctx, q, st.JobID)):
			status = StatusRunning
		}
	}
	return status, "", nil, nil
}

// jobState reads whether a step job is paused and whether a worker holds it now.
func jobState(ctx context.Context, q storage.Querier, jobID string) (paused, leased bool, _ error) {
	if jobID == "" {
		return false, false, nil
	}
	err := q.QueryRow(ctx, `SELECT j.paused_at IS NOT NULL, coalesce(s.state = 'leased', false)
		FROM jobs j LEFT JOIN step_jobs s ON s.job_id = j.id WHERE j.id = $1`, jobID).Scan(&paused, &leased)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read step job %s: %w", jobID, err)
	}
	return paused, leased, nil
}

// queuedAgain reports whether a step job went back to the worker queue (a pause or a window close requeued it).
func queuedAgain(ctx context.Context, q storage.Querier, jobID string) bool {
	var waiting bool
	if err := q.QueryRow(ctx, "SELECT state = 'waiting' FROM step_jobs WHERE job_id = $1", jobID).Scan(&waiting); err != nil {
		return false
	}
	return waiting
}

// ---------------------------------------------------------------- resume and stage

// ResumePlan is what runs.resume would continue from.
type ResumePlan struct {
	State     string // training-state artifact hash
	FromStep  int64  // the optimiser step it was saved at (0 when unknown)
	Remaining int    // steps left of the budget
	Estimate  Estimate
}

// PlanResume checks that run id can be resumed and estimates the steps it has left.
func (s *Service) PlanResume(ctx context.Context, q storage.Querier, id string, sessionID string) (ResumePlan, error) {
	x, err := getRow(ctx, q, id)
	if err != nil {
		return ResumePlan{}, err
	}
	if x.Status != StatusFailed && x.Status != StatusCancelled {
		return ResumePlan{}, problems.Conflict.New("run %s is %s; runs.resume continues a cancelled or failed run (a paused step job resumes with jobs.resume)", id, x.Status)
	}
	hash, step, err := s.lastTrainingState(ctx, q, x)
	if err != nil {
		return ResumePlan{}, err
	}
	if hash == "" {
		return ResumePlan{}, problems.NoTrainingState.New("run %s saved no training state (its train step never stopped at a checkpoint boundary); start the stage again with runs.new, or a new stage from a checkpoint with runs.stage", id)
	}
	rp := ResumePlan{State: hash, FromStep: step, Remaining: max(1, x.Steps-int(step))}
	base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, x.BaseVersionID)
	if err != nil {
		return ResumePlan{}, err
	}
	rp.Estimate, err = EstimateRun(ctx, q, s.defaults(), Input{ProjectID: x.ProjectID, Init: InitBase, Steps: &rp.Remaining,
		Precision: x.Precision, GPUs: &x.GPUs, Compute: x.Card.ComputeID, Base: &base, SessionID: sessionID,
		Mix: &RenderedMix{Ref: x.Mix, Data: Data{Datasets: []string{}}}})
	return rp, err
}

// Resume continues run id (at revision rev) from its last training state: the train step runs again as a new
// attempt of the same pipeline run with overrides.resumeFrom.
func (s *Service) Resume(ctx context.Context, tx pgx.Tx, id string, rev int, actor auth.Actor) (View, []events.Draft, error) {
	x, err := lockRow(ctx, tx, id)
	if err != nil {
		return View{}, nil, err
	}
	if err := commands.CheckRev("run", rev, x.Rev); err != nil {
		return View{}, nil, err
	}
	rp, err := s.PlanResume(ctx, tx, id, actor.SessionID)
	if err != nil {
		return View{}, nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE runs SET resumed_from = $2 WHERE id = $1", id, rp.State); err != nil {
		return View{}, nil, fmt.Errorf("record resume: %w", err)
	}
	_, drafts, err := s.Engine.Retry(ctx, tx, x.PipelineRunID, pipelines.AnyRev, pipelines.RetryInput{Step: x.TrainStep, ResumeFrom: rp.State})
	if err != nil {
		return View{}, nil, err
	}
	if x, err = getRow(ctx, tx, id); err != nil {
		return View{}, nil, err
	}
	v, err := s.view(ctx, tx, x)
	return v, drafts, err
}

// lastTrainingState finds the newest training-state a step of the run saved that is still in the store: from a
// released lease's outputs (a stop, a pause, a failure after saving), else from the train step's own outputs.
func (s *Service) lastTrainingState(ctx context.Context, q storage.Querier, x row) (string, int64, error) {
	rows, err := q.Query(ctx, `SELECT o.value->>'hash', coalesce(o.value->'meta', '{}')
		FROM leases l JOIN step_jobs sj ON sj.job_id = l.job_id,
		     jsonb_each(CASE WHEN jsonb_typeof(l.outcome->'outputs') = 'object' THEN l.outcome->'outputs' ELSE '{}'::jsonb END) o
		WHERE sj.spec->>'runId' = $1 AND sj.spec->>'pipelineRunId' = $2 AND o.value->>'type' = $3
		ORDER BY l.ended_at DESC NULLS LAST, l.created_at DESC`, x.ID, x.PipelineRunID, TypeTrainingState)
	if err != nil {
		return "", 0, fmt.Errorf("find training state: %w", err)
	}
	type cand struct {
		hash string
		meta json.RawMessage
	}
	var list []cand
	var c cand
	if _, err := pgx.ForEachRow(rows, []any{&c.hash, &c.meta}, func() error { list = append(list, c); return nil }); err != nil {
		return "", 0, fmt.Errorf("find training state: %w", err)
	}
	pr, err := pipelines.Get(ctx, q, x.PipelineRunID)
	if err != nil {
		return "", 0, err
	}
	for _, st := range pr.Steps {
		if st.Step != x.TrainStep {
			continue
		}
		for _, ref := range st.Outputs {
			if ref.Type == TypeTrainingState {
				list = append(list, cand{hash: ref.Hash, meta: ref.Meta})
			}
		}
	}
	for _, c := range list {
		if s.CAS != nil {
			if ok, _, err := s.CAS.Has(c.hash); err != nil || !ok {
				continue
			}
		}
		var m struct {
			Step float64 `json:"step"`
		}
		_ = json.Unmarshal(c.meta, &m)
		return c.hash, int64(math.Max(0, m.Step)), nil
	}
	return "", 0, nil
}

// StageInput starts a new stage from a checkpoint of a parent run (runs.stage).
type StageInput struct {
	Checkpoint  string
	PeakLR      float64
	Mix         string
	MixRevision int
	Steps       *int
	Seed        *int
	Precision   string
	Compute     string
	Pipeline    string
	Ref         string
	Params      map[string]any
	Priority    int
}

// StageNew turns a runs.stage request on parent run id (at revision rev, or any when rev < 0) into the new run's
// input: the checkpoint (default the parent's best kept one), the parent's mix and recipe unless named.
func (s *Service) StageNew(ctx context.Context, q storage.Querier, id string, rev int, in StageInput, actor auth.Actor) (NewInput, error) {
	x, err := getRow(ctx, q, id)
	if err != nil {
		return NewInput{}, err
	}
	if rev >= 0 {
		if err := commands.CheckRev("run", rev, x.Rev); err != nil {
			return NewInput{}, err
		}
	}
	ckp := in.Checkpoint
	if ckp == "" {
		best, err := ListCheckpoints(ctx, q, CheckpointFilter{RunID: x.ID, Kept: true, Limit: 1})
		if err != nil {
			return NewInput{}, err
		}
		if len(best) == 0 {
			return NewInput{}, problems.Conflict.New("run %s has no checkpoint yet; a new stage starts from one", id)
		}
		ckp = best[0].ID
	} else {
		c, err := GetCheckpoint(ctx, q, ckp)
		if err != nil {
			return NewInput{}, err
		}
		if c.RunID != x.ID {
			return NewInput{}, problems.Validation([]problems.FieldError{{Path: "/checkpoint", Message: fmt.Sprintf("%s belongs to run %s, not %s", ckp, c.RunID, x.ID)}})
		}
	}
	out := NewInput{
		ProjectID: x.ProjectID, Actor: actor, Init: InitCheckpoint, Checkpoint: ckp, ParentRunID: x.ID, Steps: in.Steps,
		Seed: in.Seed, Precision: or(in.Precision, x.Precision), Compute: in.Compute, Mix: in.Mix, MixRevision: in.MixRevision,
		Pipeline: or(in.Pipeline, x.Recipe.Pipeline), Ref: in.Ref, Params: in.Params, PeakLR: &in.PeakLR, Priority: in.Priority,
	}
	if out.Mix == "" {
		out.Mix = x.Mix.ID
		if out.MixRevision == 0 {
			out.MixRevision = x.Mix.Revision
		}
	}
	return out, nil
}

// ---------------------------------------------------------------- calibrate and average

// CalibrateInput is runs.calibrate.
type CalibrateInput struct {
	ProjectID   string
	Actor       auth.Actor
	BaseModel   string
	Mix         string
	MixRevision int
	Precision   string
	Compute     string
	Params      map[string]any
}

// CalibrationPlan is a calibration checked and planned: the key its measurement is cached by and the one-step
// pipeline of the family's calibrate role.
type CalibrationPlan struct {
	Key      Key
	Family   Family
	Kind     pipelines.Kind
	Mix      RenderedMix
	Start    pipelines.StartInput
	Plan     pipelines.Plan
	Source   pipelines.Source
	Current  *Calibration
	GPUHours float64 // the calibrate kind's own estimate, when it publishes one
}

// PlanCalibration resolves the base model, its family's calibrate kind, the mix and the card, and plans the
// one-step pipeline.
func (s *Service) PlanCalibration(ctx context.Context, q storage.Querier, in CalibrateInput) (CalibrationPlan, error) {
	d := s.defaults()
	base, err := registry.Resolve(ctx, q, in.ProjectID, registry.KindBaseModel, or(in.BaseModel, d.Wizard.BaseModel.Value))
	if err != nil {
		return CalibrationPlan{}, err
	}
	precision := or(in.Precision, d.Training.Precision.Value)
	if !d.Training.Precision.Range.Allows(precision) {
		return CalibrationPlan{}, problems.Validation([]problems.FieldError{{Path: "/precision", Message: fmt.Sprintf("%q is not one of %v", precision, d.Training.Precision.Range.Values)}})
	}
	cp := CalibrationPlan{}
	if cp.Family, err = FamilyOf(ctx, q, base); err != nil {
		return CalibrationPlan{}, err
	}
	name, err := cp.Family.Role(RoleCalibrate)
	if err != nil {
		return CalibrationPlan{}, err
	}
	if cp.Kind, err = RoleKind(ctx, q, name); err != nil {
		return CalibrationPlan{}, err
	}
	slot, err := compute.ForJob(ctx, q, in.Compute, compute.JobTraining)
	if err != nil {
		return CalibrationPlan{}, err
	}
	cp.Key = Key{BaseModel: base.Name, CardClass: slot.Card.CardClass, MemoryCapGB: slot.Card.MemoryCapGB, Precision: precision}
	mixID, content, rev, err := ResolveMix(ctx, q, in.ProjectID, in.Mix, in.MixRevision)
	if err != nil {
		return CalibrationPlan{}, err
	}
	if cp.Mix, err = RenderMix(ctx, q, s.CAS, in.ProjectID, mixID, content, rev, d.Estimates.BytesPerAudioHour.Value); err != nil {
		return CalibrationPlan{}, err
	}
	baseRef, err := RenderBaseModel(s.CAS, base, cp.Family)
	if err != nil {
		return CalibrationPlan{}, err
	}
	declared := map[string]string{}
	wires := map[string]string{}
	for in, typ := range cp.Kind.Consumes {
		declared[in], wires[in] = typ, pipelines.InputsRef+in
	}
	inputs, err := inputsFor(declared, start{Base: baseRef, Mix: cp.Mix})
	if err != nil {
		return CalibrationPlan{}, err
	}
	params := maps.Clone(in.Params)
	if params == nil {
		params = map[string]any{}
	}
	if hasParam(cp.Kind, "precision") {
		params["precision"] = precision
	}
	pl := pipelines.Pipeline{Name: "calibrate", Description: "Calibrate " + base.Name + " on " + cp.Key.CardClass + " (runs.calibrate)",
		Inputs: declared, Steps: []pipelines.Step{{ID: "calibrate", Kind: cp.Kind.Ref(), In: wires}}}
	cp.Start = pipelines.StartInput{ProjectID: in.ProjectID, Pipeline: &pl, Inputs: inputs,
		Params: map[string]map[string]any{"calibrate": params}, Actor: in.Actor}
	if cp.Source, cp.Plan, err = s.Engine.Prepare(ctx, q, cp.Start); err != nil {
		return CalibrationPlan{}, err
	}
	if cp.Plan.Estimate.GPUHours != nil {
		cp.GPUHours = *cp.Plan.Estimate.GPUHours
	}
	if cur, found, err := LatestCalibration(ctx, q, cp.Key); err != nil {
		return CalibrationPlan{}, err
	} else if found {
		cp.Current = &cur
	}
	return cp, nil
}

// StartCalibration starts a planned calibration and records what it measures for (the calibration hook reads it).
func (s *Service) StartCalibration(ctx context.Context, tx pgx.Tx, cp CalibrationPlan) (pipelines.Run, []events.Draft, error) {
	pr, drafts, err := s.Engine.Start(ctx, tx, cp.Start)
	if err != nil {
		return pipelines.Run{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO calibration_requests (pipeline_run_id, project_id, base_model, card_class, memory_cap_gb, precision)
		VALUES ($1, $2, $3, $4, $5, $6)`, pr.ID, cp.Start.ProjectID, cp.Key.BaseModel, cp.Key.CardClass, cp.Key.MemoryCapGB, cp.Key.Precision); err != nil {
		return pipelines.Run{}, nil, fmt.Errorf("record calibration request: %w", err)
	}
	for _, st := range pr.Steps { // reused: cache it now that the request is known
		if st.State != pipelines.StepReused && st.State != pipelines.StepDone {
			continue
		}
		for name, ref := range st.Outputs {
			if ref.Type != TypeCalibration {
				continue
			}
			if _, err := s.calibrationHook(ctx, tx, steps.Output{ProjectID: pr.ProjectID, PipelineRunID: pr.ID, StepID: st.ID, Name: name,
				Artifact: ref, Metrics: st.Metrics}); err != nil {
				return pipelines.Run{}, nil, err
			}
		}
	}
	return pr, drafts, nil
}

// AveragePlan is a checkpoints.average checked and planned.
type AveragePlan struct {
	Run         row
	Kind        pipelines.Kind
	Checkpoints []string
	Start       pipelines.StartInput
	Plan        pipelines.Plan
	GPUHours    float64
}

// PlanAverage checks that ids are checkpoints of run id and plans the family's average step over them.
func (s *Service) PlanAverage(ctx context.Context, q storage.Querier, runID string, ids []string, params map[string]any, actor auth.Actor) (AveragePlan, error) {
	x, err := getRow(ctx, q, runID)
	if err != nil {
		return AveragePlan{}, err
	}
	if len(ids) < 2 {
		return AveragePlan{}, problems.Validation([]problems.FieldError{{Path: "/checkpoints", Message: "averaging needs at least two checkpoints"}})
	}
	f, err := familyVersion(ctx, q, x.FamilyVersionID)
	if err != nil {
		return AveragePlan{}, err
	}
	name, err := f.Role(RoleAverage)
	if err != nil {
		return AveragePlan{}, err
	}
	k, err := RoleKind(ctx, q, name)
	if err != nil {
		return AveragePlan{}, err
	}
	var port string
	for in, typ := range k.Consumes {
		if typ != TypeCheckpoint || port != "" {
			return AveragePlan{}, problems.RecipeMismatch.New("the average step kind %s must consume exactly one input, of type checkpoint (it consumes %v)", k.Ref(), k.Consumes)
		}
		port = in
	}
	if port == "" {
		return AveragePlan{}, problems.RecipeMismatch.New("the average step kind %s consumes no checkpoint", k.Ref())
	}
	declared, wires, inputs := map[string]string{}, map[string]string{}, map[string]steps.ArtifactRef{}
	for i, id := range ids {
		c, err := GetCheckpoint(ctx, q, id)
		if err != nil {
			return AveragePlan{}, err
		}
		if c.RunID != x.ID {
			return AveragePlan{}, problems.Validation([]problems.FieldError{{Path: fmt.Sprintf("/checkpoints/%d", i),
				Message: fmt.Sprintf("%s belongs to run %s, not %s", id, c.RunID, x.ID)}})
		}
		in := fmt.Sprintf("c%d", i)
		declared[in] = TypeCheckpoint
		wires[fmt.Sprintf("%s.%d", port, i)] = pipelines.InputsRef + in
		if inputs[in], err = sizedRef(ctx, q, s.CAS, steps.ArtifactRef{Hash: c.Artifact, Type: TypeCheckpoint, Meta: c.Meta}); err != nil {
			return AveragePlan{}, err
		}
	}
	pl := pipelines.Pipeline{Name: "average", Description: fmt.Sprintf("Average %d checkpoints of %s (checkpoints.average)", len(ids), x.ID),
		Inputs: declared, Steps: []pipelines.Step{{ID: "average", Kind: k.Ref(), In: wires}}}
	ap := AveragePlan{Run: x, Kind: k, Checkpoints: ids}
	ap.Start = pipelines.StartInput{ProjectID: x.ProjectID, Pipeline: &pl, Inputs: inputs, RunID: x.ID, Actor: actor}
	if len(params) > 0 {
		ap.Start.Params = map[string]map[string]any{"average": params}
	}
	if _, ap.Plan, err = s.Engine.Prepare(ctx, q, ap.Start); err != nil {
		return AveragePlan{}, err
	}
	if ap.Plan.Estimate.GPUHours != nil {
		ap.GPUHours = *ap.Plan.Estimate.GPUHours
	}
	return ap, nil
}

// StartAverage starts a planned average; its checkpoint output registers as an averaged checkpoint of the run.
func (s *Service) StartAverage(ctx context.Context, tx pgx.Tx, ap AveragePlan) (pipelines.Run, []events.Draft, error) {
	pr, drafts, err := s.Engine.Start(ctx, tx, ap.Start)
	if err != nil {
		return pipelines.Run{}, nil, err
	}
	x, err := lockRow(ctx, tx, ap.Run.ID)
	if err != nil {
		return pipelines.Run{}, nil, err
	}
	for _, st := range pr.Steps { // a reused average finished inside Start; its hook registered it already (idempotent)
		if st.State != pipelines.StepReused {
			continue
		}
		for name, ref := range st.Outputs {
			if ref.Type != TypeCheckpoint {
				continue
			}
			ev, err := s.register(ctx, tx, x, steps.Output{ProjectID: x.ProjectID, PipelineRunID: pr.ID, StepID: st.ID, RunID: x.ID,
				Name: name, Artifact: ref, Metrics: st.Metrics, Spec: steps.Spec{Inputs: st.Inputs}})
			if err != nil {
				return pipelines.Run{}, nil, err
			}
			drafts = append(drafts, ev...)
		}
	}
	return pr, drafts, nil
}
