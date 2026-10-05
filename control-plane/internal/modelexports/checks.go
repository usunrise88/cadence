package modelexports

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// Generated pipeline names (inline sources).
const (
	ParityPipeline    = "model-parity"
	BenchmarkPipeline = "model-benchmark"
)

// Serve step paces (the serve role's pace parameter).
const (
	PaceFast     = "fast"
	PaceRealtime = "realtime"
)

// scoreEstimateSeconds is a scoring step's estimate (CPU, seconds).
const scoreEstimateSeconds = 30

// CheckInput is a models.parity or models.benchmark request.
type CheckInput struct {
	ProjectID string
	Actor     auth.Actor
	Version   string
	Profile   string
	Format    string
	GoldenSet string
	Priority  int
	// Benchmark only.
	Target          string
	Streams         []int
	SecondsPerLevel float64
}

// SampleView is the contract's ModelCheckSample.
type SampleView struct {
	GoldenSetVersionID string  `json:"goldenSetVersionId"`
	Name               string  `json:"name"`
	Version            string  `json:"version"`
	Locale             string  `json:"locale"`
	Language           string  `json:"language,omitempty"`
	Utterances         int     `json:"utterances"`
	Hours              float64 `json:"hours"`
	DatasetHash        string  `json:"datasetHash"`
	Selection          string  `json:"selection"`
}

// TargetView is the contract's ModelCheckTarget.
type TargetView struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	ServerVersion string `json:"serverVersion,omitempty"`
	CardClass     string `json:"cardClass,omitempty"`
	Concurrency   int    `json:"concurrency,omitempty"`
}

func targetView(t targets.Target) TargetView {
	return TargetView{ID: t.ID, Name: t.Name, Kind: t.Kind, ServerVersion: t.Server.Version, CardClass: t.CardClass, Concurrency: t.Concurrency}
}

// StepView is the contract's ModelCheckStep.
type StepView struct {
	Step    string `json:"step"`
	Kind    string `json:"kind"`
	JobKind string `json:"jobKind,omitempty"`
}

// Thresholds are the parity thresholds (deploy.parity_*).
type Thresholds struct {
	MaxWERDelta       float64 `json:"maxWerDelta"`
	MinIdenticalShare float64 `json:"minIdenticalShare"`
	MaxDisagreement   float64 `json:"maxDisagreement"`
}

// ParityPlan is a models.parity request checked and planned (the contract's ModelParityPlan).
type ParityPlan struct {
	ModelVersionID string              `json:"modelVersionId"`
	ExportID       string              `json:"exportId"`
	Profile        string              `json:"profile"`
	Format         string              `json:"format"`
	Sample         SampleView          `json:"sample"`
	Staging        TargetView          `json:"staging"`
	Concurrency    int                 `json:"concurrency"`
	Thresholds     Thresholds          `json:"thresholds"`
	Steps          []StepView          `json:"steps"`
	Estimate       pipelines.Estimate  `json:"estimate"`
	Warnings       []pipelines.Warning `json:"warnings"`
	PipelineRun    *pipelines.Run      `json:"pipelineRun,omitempty"`

	check checkStart
}

// BenchmarkPlan is a models.benchmark request checked and planned (the contract's ModelBenchmarkPlan).
type BenchmarkPlan struct {
	ModelVersionID  string              `json:"modelVersionId"`
	ExportID        string              `json:"exportId"`
	Profile         string              `json:"profile"`
	Format          string              `json:"format"`
	Sample          SampleView          `json:"sample"`
	Staging         TargetView          `json:"staging"`
	Target          *TargetView         `json:"target,omitempty"`
	Streams         []int               `json:"streams"`
	TargetStreams   int                 `json:"targetStreams"`
	BudgetMs        float64             `json:"budgetMs"`
	SecondsPerLevel float64             `json:"secondsPerLevel"`
	Steps           []StepView          `json:"steps"`
	Estimate        pipelines.Estimate  `json:"estimate"`
	Warnings        []pipelines.Warning `json:"warnings"`
	PipelineRun     *pipelines.Run      `json:"pipelineRun,omitempty"`

	check checkStart
}

// GPUHours is a plan's GPU estimate for the policy.
func (pl ParityPlan) GPUHours() float64 { return gpuHours(pl.Estimate) }

// Unknown reports a GPU step without an estimate.
func (pl ParityPlan) Unknown() bool { return pl.Estimate.UnknownGPU }

// GPUHours is a plan's GPU estimate for the policy.
func (pl BenchmarkPlan) GPUHours() float64 { return gpuHours(pl.Estimate) }

// Unknown reports a GPU step without an estimate.
func (pl BenchmarkPlan) Unknown() bool { return pl.Estimate.UnknownGPU }

// checkStart is what Start needs of a planned check.
type checkStart struct {
	kind      string
	exportID  string
	versionID string
	stagingID string
	targetID  string
	request   any
	start     *pipelines.StartInput
}

// checked is the common part of a parity check and a benchmark: the model, its exported export, the staging target
// and the sample.
type checked struct {
	project projects.Project
	model   evals.DeployModel
	export  Export
	format  string
	staging targets.Target
	set     evals.SampleSet
	sample  Sample
}

func (s *Service) prepareCheck(ctx context.Context, q storage.Querier, in CheckInput, profile string, reuseSample bool) (checked, error) {
	var c checked
	var err error
	if c.project, err = projects.GetByID(ctx, q, in.ProjectID); err != nil {
		return c, err
	}
	if c.model, err = s.Evals.ResolveModelVersion(ctx, q, c.project.ID, in.Version); err != nil {
		return c, err
	}
	if c.format, err = chooseFormat(c.model, in.Format); err != nil {
		return c, err
	}
	if profile, err = s.resolveProfile(ctx, c.project, c.model, profile, "/profile"); err != nil {
		return c, err
	}
	x, found, err := Find(ctx, q, c.model.Version.ID, profile, c.format, false)
	if err != nil {
		return c, err
	}
	if !found || x.State != StateExported {
		state := "no export"
		if found {
			state = "an export that is " + x.State
		}
		return c, ExportMissing.New("%s has %s at profile %s in format %s; export it first (models.export)", c.model.Label(), state, profile, c.format)
	}
	c.export = x
	staging, ok, err := s.StagingTarget(ctx, q)
	if err != nil {
		return c, err
	}
	if !ok {
		// TODO(D2): serving-unavailable when the staging target's health check says down.
		return c, problems.Conflict.New("no active staging deployment target serves checks (defaults.yaml serving.staging_target)")
	}
	c.staging = staging
	if c.set, err = s.Evals.SampleGoldenSet(ctx, q, c.project.ID, in.GoldenSet, c.model.Payload.EvalID); err != nil {
		return c, err
	}
	n := s.defaults().Deploy.ParitySampleUtterances.Value
	if reuseSample && strings.TrimSpace(in.GoldenSet) == "" {
		// A benchmark streams the audio the export's parity decoded, when it has one.
		if pc, ok, err := LatestParity(ctx, q, x.ID); err != nil {
			return c, err
		} else if ok {
			var req parityRequest
			_ = json.Unmarshal(pc.Request, &req)
			if req.Sample.GoldenSetVersionID != "" && req.Sample.GoldenSetVersionID != c.set.VersionID {
				if v, err := registry.GetVersion(ctx, q, registry.KindGoldenSet, req.Sample.GoldenSetVersionID); err == nil {
					if set, err := s.Evals.SampleGoldenSet(ctx, q, c.project.ID, v.ID, c.model.Payload.EvalID); err == nil {
						c.set = set
					}
				}
			}
		}
	}
	if c.sample, err = BuildSample(s.CAS, c.set.DatasetHash, n, c.set.Name+" "+c.set.Version); err != nil {
		return c, err
	}
	return c, nil
}

func (c checked) sampleView() SampleView {
	return SampleView{GoldenSetVersionID: c.set.VersionID, Name: c.set.Name, Version: c.set.Version, Locale: c.set.Locale,
		Language: c.set.Language, Utterances: c.sample.Utterances, Hours: c.sample.Hours, DatasetHash: c.sample.Ref.Hash,
		Selection: SelectionFirstByHash}
}

func (c checked) sampleRef() SampleRef {
	return SampleRef{GoldenSetVersionID: c.set.VersionID, DatasetHash: c.sample.Ref.Hash, Utterances: c.sample.Utterances,
		Selection: SelectionFirstByHash}
}

// serveKind is the family's serve role kind and its ports.
type serveKind struct {
	kind                pipelines.Kind
	deployable, data    string
	hypotheses, timings string
	localeParam         string
}

func (s *Service) serveKind(ctx context.Context, q storage.Querier, m evals.DeployModel) (serveKind, error) {
	name, err := m.Family.Role(RoleServe)
	if err != nil {
		return serveKind{}, err
	}
	k, err := runs.RoleKind(ctx, q, name)
	if err != nil {
		return serveKind{}, err
	}
	sk := serveKind{kind: k, localeParam: evals.LocaleParam(k.Params)}
	var ok1, ok2, ok3, ok4 bool
	sk.deployable, ok1 = consumesType(k, TypeDeployable)
	sk.data, ok2 = consumesType(k, TypeDataset)
	sk.hypotheses, ok3 = producesType(k, TypeHypotheses)
	sk.timings, ok4 = producesType(k, TypeServingTimings)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return serveKind{}, problems.RecipeMismatch.New("the serve step kind %s must consume a deployable and a dataset and produce hypotheses and serving_timings (it consumes %v, produces %v)",
			k.Ref(), k.Consumes, k.Produces)
	}
	return sk, nil
}

// serveParams are the serve step's parameters the control plane sets (the kind's own names; others keep defaults).
func (sk serveKind) params(staging targets.Target, profile, language string, concurrency int, pace string, seconds, warmup float64) map[string]any {
	p := map[string]any{}
	set := func(k string, v any) {
		if hasParam(sk.kind, k) {
			p[k] = v
		}
	}
	set("target", staging.ID)
	set("profile", profile)
	set("concurrency", concurrency)
	set("pace", pace)
	if seconds > 0 {
		set("seconds", seconds)
	}
	if warmup > 0 {
		set("warmup_seconds", warmup)
	}
	if sk.localeParam != "" && language != "" {
		p[sk.localeParam] = language
	}
	return p
}

// scorer is the newest published neutral judging kind name.
func scorer(ctx context.Context, q storage.Querier, name string) (pipelines.Kind, error) {
	return runs.RoleKind(ctx, q, name)
}

// PrepareParity checks a models.parity request and plans its pipeline: the family's parity reference and its serve
// step through the staging target decode the sample, parity_score@1 compares them.
func (s *Service) PrepareParity(ctx context.Context, q storage.Querier, in CheckInput) (ParityPlan, error) {
	c, err := s.prepareCheck(ctx, q, in, in.Profile, false)
	if err != nil {
		return ParityPlan{}, err
	}
	d := s.defaults().Deploy
	refName, err := c.model.Family.Role(RoleParity)
	if err != nil {
		return ParityPlan{}, err
	}
	ref, err := runs.RoleKind(ctx, q, refName)
	if err != nil {
		return ParityPlan{}, err
	}
	refModel, ok1 := consumesType(ref, TypeCheckpoint)
	refData, ok2 := consumesType(ref, TypeDataset)
	refHyp, ok3 := producesType(ref, TypeHypotheses)
	if !ok1 || !ok2 || !ok3 {
		return ParityPlan{}, problems.RecipeMismatch.New("the parity reference step kind %s must consume a checkpoint and a dataset and produce hypotheses (it consumes %v, produces %v)",
			ref.Ref(), ref.Consumes, ref.Produces)
	}
	refSmoke, hasSmoke := producesType(ref, TypeSmokeInputs)
	sk, err := s.serveKind(ctx, q, c.model)
	if err != nil {
		return ParityPlan{}, err
	}
	score, err := scorer(ctx, q, ParityScorer)
	if err != nil {
		return ParityPlan{}, err
	}
	normRef, err := s.renderNormalizer(ctx, q, c.set.NormalizerVersionID)
	if err != nil {
		return ParityPlan{}, err
	}
	pl := ParityPlan{ModelVersionID: c.model.Version.ID, ExportID: c.export.ID, Profile: c.export.Profile, Format: c.format,
		Sample: c.sampleView(), Staging: targetView(c.staging), Concurrency: d.ParityConcurrency.Value,
		Thresholds: Thresholds{MaxWERDelta: d.ParityMaxWERDelta.Value, MinIdenticalShare: d.ParityMinIdenticalShare.Value,
			MaxDisagreement: d.ParityMaxDisagreement.Value}}
	p := pipelines.Pipeline{Name: ParityPipeline, Description: fmt.Sprintf("Parity of %s at %s through %s (models.parity)",
		c.model.Label(), c.export.Profile, c.staging.Name),
		Inputs: map[string]string{"model": TypeCheckpoint, "deployable": TypeDeployable, "data": TypeDataset, "norm": TypeNormalizer}}
	deployRef := steps.ArtifactRef{Hash: c.export.DeployableHash, Type: TypeDeployable}
	start := &pipelines.StartInput{ProjectID: c.project.ID, Pipeline: &p,
		Inputs: map[string]steps.ArtifactRef{"model": c.model.Checkpoint, "deployable": deployRef, "data": c.sample.Ref, "norm": normRef},
		Params: map[string]map[string]any{}, Estimates: map[string]float64{}, Actor: in.Actor, Priority: in.Priority, Fresh: true,
		JobKinds: map[string]string{"served": steps.JobEval}}
	refParams := map[string]any{}
	if hasParam(ref, "profile") {
		refParams["profile"] = c.export.Profile
	}
	if lp := evals.LocaleParam(ref.Params); lp != "" && c.set.Language != "" {
		refParams[lp] = c.set.Language
	}
	p.Steps = append(p.Steps,
		pipelines.Step{ID: "reference", Kind: ref.Ref(), In: map[string]string{refModel: "$inputs.model", refData: "$inputs.data"}},
		pipelines.Step{ID: "served", Kind: sk.kind.Ref(), In: map[string]string{sk.deployable: "$inputs.deployable", sk.data: "$inputs.data"}})
	start.Params["reference"] = refParams
	start.Params["served"] = sk.params(c.staging, c.export.Profile, c.set.Language, d.ParityConcurrency.Value, PaceFast, 0, 0)
	scoreIn := map[string]string{}
	for port, typ := range score.Consumes {
		switch {
		case port == "reference" && typ == TypeHypotheses:
			scoreIn[port] = "reference." + refHyp
		case port == "served" && typ == TypeHypotheses:
			scoreIn[port] = "served." + sk.hypotheses
		case typ == TypeDataset:
			scoreIn[port] = "$inputs.data"
		case typ == TypeNormalizer:
			scoreIn[port] = "$inputs.norm"
		case typ == TypeSmokeInputs:
			if hasSmoke {
				scoreIn[port] = "reference." + refSmoke
			}
		default:
			return ParityPlan{}, problems.RecipeMismatch.New("the parity scorer %s consumes %s (%s), which a parity check cannot fill", score.Ref(), port, typ)
		}
	}
	p.Steps = append(p.Steps, pipelines.Step{ID: "score", Kind: score.Ref(), In: scoreIn})
	audioS := c.sample.Hours * 3600
	start.Estimates["reference"] = math.Round(audioS*s.Evals.GPUHoursPerAudioHour() + 60)
	start.Estimates["served"] = math.Round(audioS*s.Evals.GPUHoursPerAudioHour() + 60)
	start.Estimates["score"] = scoreEstimateSeconds
	_, plan, err := s.Engine.Prepare(ctx, q, *start)
	if err != nil {
		return ParityPlan{}, err
	}
	if err := refuseSkipped(plan); err != nil {
		return ParityPlan{}, err
	}
	pl.Steps = []StepView{{Step: "reference", Kind: ref.Ref(), JobKind: ref.Resources.JobKind},
		{Step: "served", Kind: sk.kind.Ref(), JobKind: steps.JobEval}, {Step: "score", Kind: score.Ref(), JobKind: score.Resources.JobKind}}
	pl.Estimate, pl.Warnings = plan.Estimate, nonNilWarnings(plan.Warnings)
	pl.check = checkStart{kind: CheckParity, exportID: c.export.ID, versionID: c.model.Version.ID, stagingID: c.staging.ID,
		request: parityRequest{Sample: c.sampleRef(), ServerVersion: c.staging.Server.Version}, start: start}
	return pl, nil
}

// PrepareBenchmark checks a models.benchmark request and plans its pipeline: the family's serve step at each level
// in turn (job kind benchmark: the card alone), benchmark_score@1 over their timings.
func (s *Service) PrepareBenchmark(ctx context.Context, q storage.Querier, in CheckInput) (BenchmarkPlan, error) {
	d := s.defaults().Deploy
	var target *targets.Target
	if ref := strings.TrimSpace(in.Target); ref != "" {
		t, err := targets.Get(ctx, q, ref)
		if err != nil {
			return BenchmarkPlan{}, err
		}
		target = &t
	}
	profile := in.Profile
	c0, err := s.prepareModel(ctx, q, in)
	if err != nil {
		return BenchmarkPlan{}, err
	}
	if strings.TrimSpace(profile) == "" && target != nil {
		for _, sv := range target.Serves { // the target's primary profile for the family
			if sv.Family == c0.model.Family.Name && len(sv.Profiles) > 0 {
				profile = sv.Profiles[0]
				break
			}
		}
	}
	c, err := s.prepareCheck(ctx, q, in, profile, true)
	if err != nil {
		return BenchmarkPlan{}, err
	}
	sk, err := s.serveKind(ctx, q, c.model)
	if err != nil {
		return BenchmarkPlan{}, err
	}
	score, err := scorer(ctx, q, BenchmarkScorer)
	if err != nil {
		return BenchmarkPlan{}, err
	}
	timingsPort := ""
	for port, typ := range score.Consumes {
		if typ == TypeServingTimings {
			timingsPort = port
		} else {
			return BenchmarkPlan{}, problems.RecipeMismatch.New("the benchmark scorer %s consumes %s (%s), which a benchmark cannot fill", score.Ref(), port, typ)
		}
	}
	if timingsPort == "" {
		return BenchmarkPlan{}, problems.RecipeMismatch.New("the benchmark scorer %s consumes no serving_timings", score.Ref())
	}
	targetStreams := d.TargetConcurrency.Value
	if target != nil && target.Concurrency > 0 {
		targetStreams = target.Concurrency
	}
	levels := append([]int(nil), in.Streams...)
	if len(levels) == 0 {
		levels = append(levels, d.BenchmarkStreams.Value...)
	}
	if !slices.Contains(levels, targetStreams) {
		levels = append(levels, targetStreams)
	}
	slices.Sort(levels)
	levels = slices.Compact(levels)
	seconds := in.SecondsPerLevel
	if seconds <= 0 {
		seconds = d.BenchmarkSecondsPerLevel.Value
	}
	warmup := d.BenchmarkWarmupSeconds.Value
	pl := BenchmarkPlan{ModelVersionID: c.model.Version.ID, ExportID: c.export.ID, Profile: c.export.Profile, Format: c.format,
		Sample: c.sampleView(), Staging: targetView(c.staging), Streams: levels, TargetStreams: targetStreams,
		BudgetMs: d.LatencyBudgetOverChunkMs.Value, SecondsPerLevel: seconds}
	if target != nil {
		tv := targetView(*target)
		pl.Target = &tv
	}
	p := pipelines.Pipeline{Name: BenchmarkPipeline, Description: fmt.Sprintf("Benchmark of %s at %s through %s, %d level(s) (models.benchmark)",
		c.model.Label(), c.export.Profile, c.staging.Name, len(levels)),
		Inputs: map[string]string{"deployable": TypeDeployable, "data": TypeDataset}}
	start := &pipelines.StartInput{ProjectID: c.project.ID, Pipeline: &p,
		Inputs:    map[string]steps.ArtifactRef{"deployable": {Hash: c.export.DeployableHash, Type: TypeDeployable}, "data": c.sample.Ref},
		Params:    map[string]map[string]any{},
		Estimates: map[string]float64{}, Actor: in.Actor, Priority: in.Priority, Fresh: true, JobKinds: map[string]string{}}
	scoreIn := map[string]string{}
	for i, n := range levels {
		id := fmt.Sprintf("level-%d", n)
		p.Steps = append(p.Steps, pipelines.Step{ID: id, Kind: sk.kind.Ref(),
			In: map[string]string{sk.deployable: "$inputs.deployable", sk.data: "$inputs.data"}})
		start.Params[id] = sk.params(c.staging, c.export.Profile, c.set.Language, n, PaceRealtime, seconds, warmup)
		start.Estimates[id] = math.Round(seconds + warmup + 60)
		start.JobKinds[id] = steps.JobBenchmark
		scoreIn[fmt.Sprintf("%s.%d", timingsPort, i)] = id + "." + sk.timings
		pl.Steps = append(pl.Steps, StepView{Step: id, Kind: sk.kind.Ref(), JobKind: steps.JobBenchmark})
	}
	p.Steps = append(p.Steps, pipelines.Step{ID: "score", Kind: score.Ref(), In: scoreIn})
	start.Params["score"] = map[string]any{}
	if hasParam(score, "target_streams") {
		start.Params["score"]["target_streams"] = targetStreams
	}
	start.Estimates["score"] = scoreEstimateSeconds
	pl.Steps = append(pl.Steps, StepView{Step: "score", Kind: score.Ref(), JobKind: score.Resources.JobKind})
	_, plan, err := s.Engine.Prepare(ctx, q, *start)
	if err != nil {
		return BenchmarkPlan{}, err
	}
	if err := refuseSkipped(plan); err != nil {
		return BenchmarkPlan{}, err
	}
	pl.Estimate, pl.Warnings = plan.Estimate, nonNilWarnings(plan.Warnings)
	targetID := ""
	if target != nil {
		targetID = target.ID
	}
	pl.check = checkStart{kind: CheckBenchmark, exportID: c.export.ID, versionID: c.model.Version.ID, stagingID: c.staging.ID,
		targetID: targetID, request: benchmarkRequest{Sample: c.sampleRef(), Streams: targetStreams, Levels: levels,
			BudgetMs: pl.BudgetMs, ServerVersion: c.staging.Server.Version, CardClass: c.staging.CardClass}, start: start}
	return pl, nil
}

// prepareModel resolves only the project and model of a check (the benchmark reads the family before its profile).
func (s *Service) prepareModel(ctx context.Context, q storage.Querier, in CheckInput) (checked, error) {
	var c checked
	var err error
	if c.project, err = projects.GetByID(ctx, q, in.ProjectID); err != nil {
		return c, err
	}
	c.model, err = s.Evals.ResolveModelVersion(ctx, q, c.project.ID, in.Version)
	return c, err
}

// renderNormalizer renders the golden set's scoring normalizer as the eval pipeline does.
func (s *Service) renderNormalizer(ctx context.Context, q storage.Querier, id string) (steps.ArtifactRef, error) {
	v, err := registry.GetVersion(ctx, q, registry.KindNormalizer, id)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	return evals.RenderNormalizer(s.Evals, v)
}

// StartParity starts a planned parity check.
func (s *Service) StartParity(ctx context.Context, tx pgx.Tx, pl ParityPlan, actor auth.Actor) (ParityPlan, []events.Draft, error) {
	r, drafts, err := s.startCheck(ctx, tx, pl.check, actor)
	if err != nil {
		return ParityPlan{}, nil, err
	}
	pl.PipelineRun = &r
	return pl, drafts, nil
}

// StartBenchmark starts a planned benchmark.
func (s *Service) StartBenchmark(ctx context.Context, tx pgx.Tx, pl BenchmarkPlan, actor auth.Actor) (BenchmarkPlan, []events.Draft, error) {
	r, drafts, err := s.startCheck(ctx, tx, pl.check, actor)
	if err != nil {
		return BenchmarkPlan{}, nil, err
	}
	pl.PipelineRun = &r
	return pl, drafts, nil
}

func (s *Service) startCheck(ctx context.Context, tx pgx.Tx, c checkStart, actor auth.Actor) (pipelines.Run, []events.Draft, error) {
	if c.start == nil {
		return pipelines.Run{}, nil, fmt.Errorf("modelexports: the %s check was not planned", c.kind)
	}
	x, err := Get(ctx, tx, c.exportID)
	if err != nil {
		return pipelines.Run{}, nil, err
	}
	if x.State != StateExported {
		return pipelines.Run{}, nil, ExportMissing.New("export %s is %s", x.ID, x.State)
	}
	id := "mxc_" + uuid.Must(uuid.NewV7()).String()
	in := *c.start
	in.Actor, in.RunID = actor, id
	r, drafts, err := s.Engine.Start(ctx, tx, in)
	if err != nil {
		return pipelines.Run{}, nil, err
	}
	req, err := json.Marshal(c.request)
	if err != nil {
		return pipelines.Run{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO model_export_checks (id, export_id, kind, project_id, pipeline_run_id, target_id,
			staging_target_id, request, created_by)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), $8, $9)`,
		id, c.exportID, c.kind, in.ProjectID, r.ID, c.targetID, c.stagingID, req, actor); err != nil {
		return pipelines.Run{}, nil, fmt.Errorf("record the %s check: %w", c.kind, err)
	}
	typ := EventParityChecked
	if c.kind == CheckBenchmark {
		typ = EventBenchmarked
	}
	drafts = append(drafts, draft(x, typ, map[string]any{"checkId": id, "pipelineRunId": r.ID, "checkState": CheckRunning}))
	more, err := s.applyRun(ctx, tx, r) // a run the engine finished at once (every step reused)
	if err != nil {
		return pipelines.Run{}, nil, err
	}
	return r, append(drafts, more...), nil
}
