package deployments

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/modelexports"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/notify"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/serving"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// Shadow replay (03 "Shadow replay"): every night at deploy.shadow_replay_at a shadow deployment replays the newest
// calls of its replay mount it has not replayed yet, up to deploy.shadow_replay_max_hours. The control plane picks the
// calls (it reads the mount for their times), then runs a pipeline generated per night:
//
//	index: sdp_ingest (the picked calls, one track per party) → audio: segments_cut (the caller's segments)
//	  → candidate: the family's serve role (this deployment's export, job kind shadow)
//	  ‖ current: the comparison model — its export through the same server, else the family's transcribe role
//	  → score: shadow_score (shadow_report: per-segment WER between the two, the night's divergence)
//
// The shadow_report hook records the night, the calls (each counts once, by its duration, toward shadow.hours) and
// the most divergent segments. The texts are production-derived: they leave with the night's artifacts after
// deploy.shadow_artifact_retention_days (Retention); the night's summary stays.

// Kinds and types the shadow pipeline uses (neutral core kinds, found by name; the family's roles by role).
const (
	ShadowPipeline   = "shadow-replay"
	IngestKind       = "sdp_ingest"
	CutKind          = "segments_cut"
	ScoreKind        = "shadow_score"
	RoleTranscribe   = "transcribe"
	RoleMaterialize  = "materialize"
	TypeShadowReport = "shadow_report"
	TypeSegments     = "segments"
	TypeDataset      = "dataset"
	TypeHypotheses   = "hypotheses"
	TypeDeployable   = "deployable"
	TypeCheckpoint   = "checkpoint"
	TypeBaseModel    = "base_model"
)

// Replay triggers and states.
const (
	TriggerNightly = "nightly"
	TriggerManual  = "manual"

	ReplayPlanned = "planned"
	ReplayRunning = "running"
	ReplayDone    = "done"
	ReplayFailed  = "failed"
	ReplaySkipped = "skipped"
)

const replayPrefix = "srp_"

// audioSuffixes are the files a replay picks (sdp_ingest decodes these).
var audioSuffixes = []string{".wav", ".flac", ".mp3", ".ogg", ".opus", ".m4a", ".aac", ".webm"}

// telephoneBytesPerSecond estimates a call's length from its size when its header does not say: 8 kHz G.711, two
// channels.
const telephoneBytesPerSecond = 16000

// ReplayAgainst is the comparison model of one night and how it was decoded.
type ReplayAgainst struct {
	Kind      string `json:"kind"`
	VersionID string `json:"versionId"`
	Label     string `json:"label"`
	DecodedBy string `json:"decodedBy"` // serve | transcribe
}

// Confidence is a night's mean confidence on each side.
type Confidence struct {
	Candidate *float64 `json:"candidate,omitempty"`
	Current   *float64 `json:"current,omitempty"`
}

// Replay is a shadow_replays row (the contract's ShadowReplay).
type Replay struct {
	ID             string              `json:"id"`
	DeploymentID   string              `json:"deploymentId"`
	ProjectID      string              `json:"projectId"`
	Night          string              `json:"night"`
	Trigger        string              `json:"trigger"`
	State          string              `json:"state"`
	Reason         string              `json:"reason,omitempty"`
	PipelineRunID  string              `json:"pipelineRunId,omitempty"`
	Calls          int                 `json:"calls"`
	Hours          float64             `json:"hours"`
	Utterances     int                 `json:"utterances"`
	Divergence     *Divergence         `json:"divergence,omitempty"`
	Confidence     *Confidence         `json:"confidence,omitempty"`
	Against        *ReplayAgainst      `json:"against,omitempty"`
	ReportHash     string              `json:"reportHash,omitempty"`
	Worst          []json.RawMessage   `json:"worst,omitempty"`
	Estimate       *pipelines.Estimate `json:"estimate,omitempty"`
	TextsEvictedAt *time.Time          `json:"textsEvictedAt,omitempty"`
	CreatedAt      time.Time           `json:"createdAt"`
	FinishedAt     *time.Time          `json:"finishedAt,omitempty"`

	selected []string
}

const replayCols = `id, deployment_id, project_id, night, trigger, state, reason, coalesce(pipeline_run_id, ''), calls, hours,
	utterances, divergence, confidence, against, coalesce(report_hash, ''), worst, selected, texts_evicted_at, created_at,
	finished_at`

func scanReplay(row pgx.CollectableRow) (Replay, error) {
	var (
		r                       Replay
		night                   time.Time
		div, conf, ag, worst, s []byte
	)
	if err := row.Scan(&r.ID, &r.DeploymentID, &r.ProjectID, &night, &r.Trigger, &r.State, &r.Reason, &r.PipelineRunID,
		&r.Calls, &r.Hours, &r.Utterances, &div, &conf, &ag, &r.ReportHash, &worst, &s, &r.TextsEvictedAt, &r.CreatedAt,
		&r.FinishedAt); err != nil {
		return r, err
	}
	r.Night = night.Format(time.DateOnly)
	if len(div) > 0 && string(div) != "null" {
		r.Divergence = &Divergence{}
		_ = json.Unmarshal(div, r.Divergence)
	}
	if len(conf) > 0 && string(conf) != "null" {
		r.Confidence = &Confidence{}
		_ = json.Unmarshal(conf, r.Confidence)
	}
	if len(ag) > 0 && string(ag) != "null" {
		r.Against = &ReplayAgainst{}
		_ = json.Unmarshal(ag, r.Against)
	}
	if len(worst) > 0 && string(worst) != "null" {
		_ = json.Unmarshal(worst, &r.Worst)
	}
	_ = json.Unmarshal(s, &r.selected)
	return r, nil
}

func queryReplays(ctx context.Context, q storage.Querier, where string, args ...any) ([]Replay, error) {
	rows, err := q.Query(ctx, "SELECT "+replayCols+" FROM shadow_replays WHERE "+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read shadow replays: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanReplay)
	if err != nil {
		return nil, fmt.Errorf("read shadow replays: %w", err)
	}
	return out, nil
}

// Replays lists a deployment's nights, newest first, without their segments.
func Replays(ctx context.Context, q storage.Querier, deploymentID string, limit int) ([]Replay, error) {
	if limit <= 0 {
		limit = 60
	}
	list, err := queryReplays(ctx, q, "deployment_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2", deploymentID, limit)
	for i := range list {
		list[i].Worst = nil
	}
	return list, err
}

// GetReplay reads one night with its most divergent segments.
func GetReplay(ctx context.Context, q storage.Querier, id string) (Replay, error) {
	list, err := queryReplays(ctx, q, "id = $1", id)
	if err != nil {
		return Replay{}, err
	}
	if len(list) == 0 {
		return Replay{}, problems.NotFound.New("no shadow replay %q", id)
	}
	return list[0], nil
}

// ---------------------------------------------------------------- choosing the night's calls

type call struct {
	rel     string // relative to the mount's root
	uri     string // mount://<mount>/<rel>
	seconds float64
	stamp   int64
}

func isAudio(name string) bool {
	low := strings.ToLower(name)
	return slices.ContainsFunc(audioSuffixes, func(s string) bool { return strings.HasSuffix(low, s) })
}

// wavSeconds reads a WAV header's byte rate and data size (nil when the file is not a RIFF/WAVE with both).
func wavSeconds(r io.Reader) (float64, bool) {
	buf := make([]byte, 64<<10)
	n, _ := io.ReadFull(r, buf)
	buf = buf[:n]
	if len(buf) < 12 || string(buf[0:4]) != "RIFF" || string(buf[8:12]) != "WAVE" {
		return 0, false
	}
	var rate uint32
	for i := 12; i+8 <= len(buf); {
		id, size := string(buf[i:i+4]), binary.LittleEndian.Uint32(buf[i+4:i+8])
		switch id {
		case "fmt ":
			if i+16 <= len(buf) {
				rate = binary.LittleEndian.Uint32(buf[i+16 : i+20])
			}
		case "data":
			if rate == 0 {
				return 0, false
			}
			return float64(size) / float64(rate), true
		}
		i += 8 + int(size) + int(size%2)
	}
	return 0, false
}

func (s *Service) callSeconds(ctx context.Context, rd mounts.Reader, rel string, size int64) float64 {
	if strings.HasSuffix(strings.ToLower(rel), ".wav") {
		if f, err := rd.Open(ctx, rel); err == nil {
			sec, ok := wavSeconds(f)
			_ = f.Close()
			if ok {
				return sec
			}
		}
	}
	return float64(size) / telephoneBytesPerSecond
}

// selectCalls picks the night's calls: the newest under the replay path the deployment has not replayed, up to
// deploy.shadow_replay_max_hours (at least one).
func (s *Service) selectCalls(ctx context.Context, q storage.Querier, d Deployment) (mounts.Mount, []call, error) {
	if s.OpenMount == nil {
		return mounts.Mount{}, nil, problems.Internal.New("no mount reader: shadow replay cannot read calls")
	}
	m, err := mounts.Get(ctx, q, d.Replay.Mount)
	if err != nil {
		return mounts.Mount{}, nil, err
	}
	rd, err := s.OpenMount(ctx, m)
	if err != nil {
		return m, nil, problems.MountUnhealthy.New("mount %s cannot be read: %v", m.Name, err)
	}
	rows, err := q.Query(ctx, "SELECT call FROM shadow_calls WHERE deployment_id = $1", d.ID)
	if err != nil {
		return m, nil, fmt.Errorf("read replayed calls: %w", err)
	}
	done, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return m, nil, fmt.Errorf("read replayed calls: %w", err)
	}
	replayed := make(map[string]bool, len(done))
	for _, c := range done {
		replayed[c] = true
	}
	type file struct {
		rel   string
		size  int64
		stamp int64
	}
	var files []file
	visit := func(rel string, size int64, stamp string) error {
		if !isAudio(rel) || replayed[m.URIPrefix()+rel] {
			return nil
		}
		st, _ := strconv.ParseInt(stamp, 10, 64)
		files = append(files, file{rel: rel, size: size, stamp: st})
		return nil
	}
	if sr, ok := rd.(mounts.StampedReader); ok {
		err = sr.WalkStamped(ctx, d.Replay.Path, visit)
	} else {
		err = rd.Walk(ctx, d.Replay.Path, func(rel string, size int64) error { return visit(rel, size, "") })
	}
	if err != nil {
		return m, nil, problems.MountUnhealthy.New("mount %s: listing %s failed: %v", m.Name, orRoot(d.Replay.Path), err)
	}
	slices.SortFunc(files, func(a, b file) int {
		if a.stamp != b.stamp {
			if a.stamp > b.stamp {
				return -1
			}
			return 1
		}
		return strings.Compare(b.rel, a.rel)
	})
	budget := s.defaults().Deploy.ShadowReplayMaxHours.Value * 3600
	var (
		out   []call
		total float64
	)
	for _, f := range files {
		if total >= budget {
			break
		}
		sec := s.callSeconds(ctx, rd, f.rel, f.size)
		if len(out) > 0 && total+sec > budget*1.05 {
			continue // a long call that would overrun the night's budget waits for a later night
		}
		out = append(out, call{rel: f.rel, uri: m.URIPrefix() + f.rel, seconds: sec, stamp: f.stamp})
		total += sec
	}
	return m, out, nil
}

func orRoot(p string) string {
	if p == "" {
		return "the mount's root"
	}
	return p
}

// ---------------------------------------------------------------- planning a night

// replayPlan is a night planned: the calls, the pipeline and what the comparison is decoded by.
type replayPlan struct {
	calls    []call
	hours    float64
	against  ReplayAgainst
	start    *pipelines.StartInput
	estimate pipelines.Estimate
}

func hasParam(k pipelines.Kind, name string) bool {
	var sch struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(k.Params, &sch)
	_, ok := sch.Properties[name]
	return ok
}

func port(m map[string]string, typ string) (string, bool) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if m[k] == typ {
			return k, true
		}
	}
	return "", false
}

func mismatch(k pipelines.Kind, want string) error {
	return problems.RecipeMismatch.New("step kind %s must %s (it consumes %v, produces %v)", k.Ref(), want, k.Consumes, k.Produces)
}

// planReplay chooses the night's calls and plans the shadow-replay pipeline of d.
func (s *Service) planReplay(ctx context.Context, q storage.Querier, d Deployment, actor auth.Actor) (replayPlan, error) {
	if d.Stage != StageShadow || !d.Live() || d.Replay == nil {
		return replayPlan{}, problems.Conflict.New("deployment %s is a %s (%s): only a shadow deployment with a replay mount replays calls", d.ID, d.Stage, d.State)
	}
	dp, err := s.deployedOf(ctx, q, d)
	if err != nil {
		return replayPlan{}, err
	}
	fam := dp.model.Family.Name
	staging, err := s.Serving.Check(ctx, q, d.TargetID, serving.Deployable{Family: fam, Format: d.Format, Profile: d.Profile})
	if err != nil {
		return replayPlan{}, err
	}
	m, calls, err := s.selectCalls(ctx, q, d)
	if err != nil {
		return replayPlan{}, err
	}
	if len(calls) == 0 {
		return replayPlan{}, problems.Conflict.New("no calls under mount://%s/%s that shadow %s has not replayed yet", m.Name, d.Replay.Path, d.ID)
	}
	rp := replayPlan{calls: calls}
	for _, c := range calls {
		rp.hours += c.seconds / 3600
	}
	ingest, err := runs.RoleKind(ctx, q, IngestKind)
	if err != nil {
		return replayPlan{}, err
	}
	segOut, ok := port(ingest.Produces, TypeSegments)
	if !ok {
		return replayPlan{}, mismatch(ingest, "produce segments")
	}
	cut, err := runs.RoleKind(ctx, q, CutKind)
	if err != nil {
		return replayPlan{}, err
	}
	cutIn, ok1 := port(cut.Consumes, TypeSegments)
	cutOut, ok2 := port(cut.Produces, TypeDataset)
	if !ok1 || !ok2 {
		return replayPlan{}, mismatch(cut, "consume segments and produce a dataset")
	}
	score, err := runs.RoleKind(ctx, q, ScoreKind)
	if err != nil {
		return replayPlan{}, err
	}
	candidate, err := s.Exports.ServeStep(ctx, q, dp.model)
	if err != nil {
		return replayPlan{}, err
	}
	lang := d.Replay.Language
	conc := s.defaults().Deploy.ShadowConcurrency.Value
	audioS := rp.hours * 3600
	gpuS := math.Round(audioS*s.Evals.GPUHoursPerAudioHour() + 60)

	p := pipelines.Pipeline{Name: ShadowPipeline, Description: fmt.Sprintf("Shadow replay of %s (%s) against %s: %d call(s), %.2f h (deployments %s)",
		dp.label, d.Profile, d.Against.Label, len(calls), rp.hours, d.ID),
		Inputs: map[string]string{"candidate": TypeDeployable}}
	start := &pipelines.StartInput{ProjectID: d.ProjectID, Pipeline: &p, Inputs: map[string]steps.ArtifactRef{
		"candidate": {Hash: dp.export.DeployableHash, Type: TypeDeployable}}, Params: map[string]map[string]any{},
		Estimates: map[string]float64{}, Actor: actor, Fresh: true, JobKinds: map[string]string{}}

	files := make([]string, 0, len(calls))
	prefix := strings.Trim(d.Replay.Path, "/")
	for _, c := range calls {
		rel := c.rel
		if prefix != "" {
			rel = strings.TrimPrefix(rel, prefix+"/")
		}
		files = append(files, rel)
	}
	ip := map[string]any{"source": d.Replay.Source, "path": m.URIPrefix() + prefix}
	set := func(k pipelines.Kind, into map[string]any, name string, v any) {
		if hasParam(k, name) {
			into[name] = v
		}
	}
	set(ingest, ip, "files", files)
	set(ingest, ip, "exclude", []string{})
	set(ingest, ip, "channels", "split")
	set(ingest, ip, "channel_roles", d.Replay.ChannelRoles)
	set(ingest, ip, "max_hours", math.Ceil(rp.hours*1.1*100)/100)
	if lang != "" {
		set(ingest, ip, "language", lang)
	}
	p.Steps = append(p.Steps,
		pipelines.Step{ID: "index", Kind: ingest.Ref()},
		pipelines.Step{ID: "audio", Kind: cut.Ref(), In: map[string]string{cutIn: "index." + segOut}},
		pipelines.Step{ID: "candidate", Kind: candidate.Kind.Ref(), In: map[string]string{candidate.Deployable: "$inputs.candidate",
			candidate.Data: "audio." + cutOut}})
	start.Params["index"] = ip
	ap := map[string]any{}
	set(cut, ap, "which", "unlabelled")
	start.Params["audio"] = ap
	start.Params["candidate"] = candidate.Params(staging, d.Profile, lang, conc)
	start.JobKinds["candidate"] = steps.JobShadow
	start.Estimates["index"] = math.Round(audioS/50 + 30)
	start.Estimates["audio"] = math.Round(audioS/100 + 30)
	start.Estimates["candidate"] = gpuS

	currentHyp, err := s.planCurrent(ctx, q, d, dp, staging, start, &p, &rp, "audio."+cutOut, lang, conc, gpuS)
	if err != nil {
		return replayPlan{}, err
	}
	scoreIn := map[string]string{}
	for portName, typ := range score.Consumes {
		switch {
		case typ == TypeSegments:
			scoreIn[portName] = "index." + segOut
		case typ == TypeHypotheses && portName == "candidate":
			scoreIn[portName] = "candidate." + candidate.Hypotheses
		case typ == TypeHypotheses && portName == "current":
			scoreIn[portName] = currentHyp
		default:
			return replayPlan{}, problems.RecipeMismatch.New("the shadow scorer %s consumes %s (%s), which a shadow replay cannot fill", score.Ref(), portName, typ)
		}
	}
	if _, ok := port(score.Produces, TypeShadowReport); !ok {
		return replayPlan{}, mismatch(score, "produce a shadow_report")
	}
	p.Steps = append(p.Steps, pipelines.Step{ID: "score", Kind: score.Ref(), In: scoreIn})
	start.Params["score"] = map[string]any{}
	start.Estimates["score"] = 60
	_, plan, err := s.Engine.Prepare(ctx, q, *start)
	if err != nil {
		return replayPlan{}, err
	}
	for _, w := range plan.Warnings {
		if w.Code == pipelines.WarningStepKindUnavailable {
			return replayPlan{}, problems.FamilyUnavailable.New("%s", w.Message)
		}
	}
	if len(plan.Skipped) > 0 {
		return replayPlan{}, problems.FamilyUnavailable.New("step %s (%s@%s) cannot run now: no live worker publishes it",
			plan.Skipped[0].Step, plan.Skipped[0].Name, plan.Skipped[0].Version)
	}
	rp.start, rp.estimate = start, plan.Estimate
	return rp, nil
}

// planCurrent adds the comparison model's decode: its export through the same server when it has one at the
// deployment's profile and format, else the family's transcribe role (after materialize for a base model). It
// answers the step output the scorer reads.
func (s *Service) planCurrent(ctx context.Context, q storage.Querier, d Deployment, dp deployed, staging targets.Target,
	start *pipelines.StartInput, p *pipelines.Pipeline, rp *replayPlan, data, lang string, conc int, gpuS float64) (string, error) {
	ag := d.Against
	rp.against = ReplayAgainst{Kind: ag.Kind, VersionID: ag.VersionID, Label: ag.Label}
	if ag.Kind == registry.KindModel {
		cm, cx, found, err := s.Exports.ResolveExport(ctx, q, d.ProjectID, ag.VersionID, d.Profile, d.Format)
		if err == nil && found && cx.State == modelexports.StateExported && cx.DeployableHash != "" &&
			staging.Serving(cm.Family.Name, cx.Format, cx.Profile) {
			sv, err := s.Exports.ServeStep(ctx, q, cm)
			if err != nil {
				return "", err
			}
			p.Inputs["current"] = TypeDeployable
			start.Inputs["current"] = steps.ArtifactRef{Hash: cx.DeployableHash, Type: TypeDeployable}
			p.Steps = append(p.Steps, pipelines.Step{ID: "current", Kind: sv.Kind.Ref(),
				In: map[string]string{sv.Deployable: "$inputs.current", sv.Data: data}})
			start.Params["current"] = sv.Params(staging, d.Profile, lang, conc)
			start.JobKinds["current"] = steps.JobShadow
			start.Estimates["current"] = gpuS
			rp.against.DecodedBy = "serve"
			return "current." + sv.Hypotheses, nil
		}
	}
	v, err := registry.GetVersion(ctx, q, "", ag.VersionID)
	if err != nil {
		return "", err
	}
	w, err := s.Evals.WeightsOf(ctx, q, v)
	if err != nil {
		return "", err
	}
	tname, err := w.Family.Role(RoleTranscribe)
	if err != nil {
		return "", err
	}
	tk, err := runs.RoleKind(ctx, q, tname)
	if err != nil {
		return "", err
	}
	ckIn, ok1 := port(tk.Consumes, TypeCheckpoint)
	dataIn, ok2 := port(tk.Consumes, TypeDataset)
	hyp, ok3 := port(tk.Produces, TypeHypotheses)
	if !ok1 || !ok2 || !ok3 {
		return "", mismatch(tk, "consume a checkpoint and a dataset and produce hypotheses")
	}
	weights := "$inputs.current"
	switch w.Kind {
	case registry.KindBaseModel:
		mname, err := w.Family.Role(RoleMaterialize)
		if err != nil {
			return "", err
		}
		mk, err := runs.RoleKind(ctx, q, mname)
		if err != nil {
			return "", err
		}
		baseIn, ok1 := port(mk.Consumes, TypeBaseModel)
		ckOut, ok2 := port(mk.Produces, TypeCheckpoint)
		if !ok1 || !ok2 {
			return "", mismatch(mk, "consume a base_model and produce a checkpoint")
		}
		p.Inputs["current"] = TypeBaseModel
		p.Steps = append(p.Steps, pipelines.Step{ID: "materialize", Kind: mk.Ref(), In: map[string]string{baseIn: "$inputs.current"}})
		start.Params["materialize"] = map[string]any{}
		weights = "materialize." + ckOut
	default:
		p.Inputs["current"] = TypeCheckpoint
	}
	start.Inputs["current"] = w.Artifact
	p.Steps = append(p.Steps, pipelines.Step{ID: "current", Kind: tk.Ref(), In: map[string]string{ckIn: weights, dataIn: data}})
	tp := map[string]any{}
	if hasParam(tk, "profile") {
		tp["profile"] = d.Profile
	}
	if lp := evals.LocaleParam(tk.Params); lp != "" && lang != "" {
		tp[lp] = lang
	}
	start.Params["current"] = tp
	start.JobKinds["current"] = steps.JobShadow
	start.Estimates["current"] = gpuS
	rp.against.DecodedBy = "transcribe"
	return "current." + hyp, nil
}

// PlanReplay answers what a replay of d would take now (shadowReplays.new's dry run).
func (s *Service) PlanReplay(ctx context.Context, q storage.Querier, d Deployment, actor auth.Actor) (Replay, error) {
	rp, err := s.planReplay(ctx, q, d, actor)
	if err != nil {
		return Replay{}, err
	}
	est := rp.estimate
	return Replay{DeploymentID: d.ID, ProjectID: d.ProjectID, Night: s.night(ctx, q).Format(time.DateOnly), Trigger: TriggerManual,
		State: ReplayPlanned, Calls: len(rp.calls), Hours: round2(rp.hours), Against: &rp.against, Estimate: &est, CreatedAt: s.now()}, nil
}

// GPUHours is a replay plan's GPU estimate for the policy.
func (r Replay) GPUHours() float64 {
	if r.Estimate == nil || r.Estimate.GPUHours == nil {
		return 0
	}
	return *r.Estimate.GPUHours
}

// Unknown reports a GPU step without an estimate.
func (r Replay) Unknown() bool { return r.Estimate != nil && r.Estimate.UnknownGPU }

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// night is today's date in policies.timezone.
func (s *Service) night(ctx context.Context, q storage.Querier) time.Time {
	loc := time.UTC
	if p, err := policies.Get(ctx, q, s.defaults()); err == nil {
		loc = p.Location()
	}
	l := s.now().In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, time.UTC)
}

// StartReplay plans and starts a replay of d now (shadowReplays.new): conflict when one is already running.
func (s *Service) StartReplay(ctx context.Context, tx pgx.Tx, d Deployment, actor auth.Actor) (Replay, []events.Draft, error) {
	rp, err := s.planReplay(ctx, tx, d, actor)
	if err != nil {
		return Replay{}, nil, err
	}
	return s.start(ctx, tx, d, rp, TriggerManual, s.night(ctx, tx), actor)
}

func (s *Service) start(ctx context.Context, tx pgx.Tx, d Deployment, rp replayPlan, trigger string, night time.Time, actor auth.Actor) (Replay, []events.Draft, error) {
	var running string
	if err := tx.QueryRow(ctx, "SELECT id FROM shadow_replays WHERE deployment_id = $1 AND state IN ('planned', 'running')", d.ID).Scan(&running); err == nil {
		return Replay{}, nil, problems.Conflict.New("shadow %s is replaying already (%s); one replay runs at a time", d.ID, running)
	} else if !noRows(err) {
		return Replay{}, nil, fmt.Errorf("look up running replays: %w", err)
	}
	id := newID(replayPrefix)
	sel := make([]string, 0, len(rp.calls))
	for _, c := range rp.calls {
		sel = append(sel, c.uri)
	}
	selJSON, _ := json.Marshal(sel)
	agJSON, _ := json.Marshal(rp.against)
	if _, err := tx.Exec(ctx, `INSERT INTO shadow_replays (id, deployment_id, project_id, night, trigger, state, calls, hours,
			against, selected, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, 'running', $6, $7, $8, $9, $10, $11)`,
		id, d.ID, d.ProjectID, night, trigger, len(rp.calls), round2(rp.hours), agJSON, selJSON, actor, s.now()); err != nil {
		return Replay{}, nil, fmt.Errorf("record the shadow replay: %w", err)
	}
	in := *rp.start
	in.RunID, in.Actor = id, actor
	r, drafts, err := s.Engine.Start(ctx, tx, in)
	if err != nil {
		return Replay{}, nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE shadow_replays SET pipeline_run_id = $2 WHERE id = $1", id, r.ID); err != nil {
		return Replay{}, nil, fmt.Errorf("record the replay's pipeline run: %w", err)
	}
	out, err := GetReplay(ctx, tx, id)
	if err != nil {
		return Replay{}, nil, err
	}
	est := rp.estimate
	out.Estimate = &est
	drafts = append(drafts, events.Draft{Topic: ShadowTopic(d.ID), Type: EventShadowStarted, ProjectID: d.ProjectID,
		Payload: map[string]any{"replayId": id, "deploymentId": d.ID, "night": out.Night, "calls": out.Calls, "hours": out.Hours,
			"pipelineRunId": r.ID, "trigger": trigger}})
	return out, drafts, nil
}

// ---------------------------------------------------------------- the nightly schedule

// Tick starts the night's replay of every live shadow deployment with a replay mount once deploy.shadow_replay_at
// has passed in policies.timezone (a periodic job, every minute). A night that cannot run (no new calls, the mount or
// the staging server down) is recorded as skipped with the reason, so it is not retried every minute.
func (s *Service) Tick(ctx context.Context) error {
	d := s.defaults()
	p, err := policies.Get(ctx, s.Pool, d)
	if err != nil {
		return err
	}
	loc, now := p.Location(), s.now()
	at := notify.Today(now, notify.MustClock(d.Deploy.ShadowReplayAt.Value), loc)
	if now.Before(at) {
		return nil
	}
	night := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	due, err := query(ctx, s.Pool, `stage = 'shadow' AND state IN ('active', 'pending-delivery') AND replay IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM shadow_replays r WHERE r.deployment_id = deployments.id AND r.trigger = 'nightly' AND r.night = $1)
		ORDER BY created_at`, night)
	if err != nil {
		return err
	}
	var errs []error
	for _, dep := range due {
		if err := s.nightly(ctx, dep.ID, night); err != nil {
			s.log().ErrorContext(ctx, "nightly shadow replay", "deployment", dep.ID, "err", err)
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) nightly(ctx context.Context, id string, night time.Time) error {
	ctx = auth.WithActor(ctx, jobs.System)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cadence.shadow.' || $1))`, id); err != nil {
			return fmt.Errorf("lock the shadow schedule: %w", err)
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM shadow_replays WHERE deployment_id = $1 AND trigger = 'nightly'
			AND night = $2)`, id, night).Scan(&taken); err != nil {
			return fmt.Errorf("look up tonight's replay: %w", err)
		}
		if taken {
			return nil
		}
		d, err := Get(ctx, tx, id)
		if err != nil {
			return err
		}
		var drafts []events.Draft
		rp, err := s.planInSavepoint(ctx, tx, d)
		if err == nil {
			var r Replay
			r, drafts, err = s.startInSavepoint(ctx, tx, d, rp, night)
			if err == nil {
				s.log().InfoContext(ctx, "nightly shadow replay started", "deployment", d.ID, "replay", r.ID, "calls", r.Calls, "hours", r.Hours)
			}
		}
		if err != nil {
			reason := err.Error()
			if pe, ok := problems.As(err); ok {
				reason = pe.Detail
			}
			if _, ierr := tx.Exec(ctx, `INSERT INTO shadow_replays (id, deployment_id, project_id, night, trigger, state, reason,
					created_by, created_at, finished_at)
				VALUES ($1, $2, $3, $4, 'nightly', 'skipped', $5, $6, $7, $7)`,
				newID(replayPrefix), d.ID, d.ProjectID, night, reason, jobs.System, s.now()); ierr != nil {
				return fmt.Errorf("record the skipped night: %w", ierr)
			}
			drafts = []events.Draft{{Topic: ShadowTopic(d.ID), Type: EventShadowSkipped, ProjectID: d.ProjectID,
				Payload: map[string]any{"deploymentId": d.ID, "night": night.Format(time.DateOnly), "reason": reason}}}
			s.log().InfoContext(ctx, "nightly shadow replay skipped", "deployment", d.ID, "reason", reason)
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
}

// planInSavepoint and startInSavepoint keep a failed plan or start from aborting the night's transaction, so the
// skipped night can still be recorded.
func (s *Service) planInSavepoint(ctx context.Context, tx pgx.Tx, d Deployment) (replayPlan, error) {
	var rp replayPlan
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		var err error
		rp, err = s.planReplay(ctx, sp, d, jobs.System)
		return err
	})
	return rp, err
}

func (s *Service) startInSavepoint(ctx context.Context, tx pgx.Tx, d Deployment, rp replayPlan, night time.Time) (Replay, []events.Draft, error) {
	var (
		r      Replay
		drafts []events.Draft
	)
	err := pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		var err error
		r, drafts, err = s.start(ctx, sp, d, rp, TriggerNightly, night, jobs.System)
		return err
	})
	return r, drafts, err
}

// ---------------------------------------------------------------- recording a night

// Install registers the shadow_report hook and the run observer.
func (s *Service) Install(hooks *steps.Hooks) {
	hooks.On(TypeShadowReport, s.onReport)
	if s.Engine != nil {
		s.Engine.SetNamedObserver("deployments", s.Observe)
	}
}

// report is what the control plane reads of a shadow_report (cadence.shadow/1 report.json).
type report struct {
	Schema     string  `json:"schema"`
	Calls      int     `json:"calls"`
	Hours      float64 `json:"hours"`
	Utterances int     `json:"utterances"`
	Divergence struct {
		WER   *float64  `json:"wer"`
		CI    []float64 `json:"ci"`
		Level float64   `json:"level"`
	} `json:"divergence"`
	Confidence Confidence `json:"confidence"`
	CallList   []struct {
		Call     string  `json:"call"`
		Duration float64 `json:"duration"`
		Segments int     `json:"segments"`
	} `json:"callList"`
	Worst []json.RawMessage `json:"worst"`
}

func readReport(store *cas.Store, hash string) (report, error) {
	var r report
	m, err := store.ReadManifest(hash)
	if err != nil {
		return r, fmt.Errorf("shadow report %s is not a directory: %w", hash, err)
	}
	for _, f := range m.Files {
		if f.Path != "report.json" {
			continue
		}
		rc, err := store.Open(f.Hash)
		if err != nil {
			return r, err
		}
		defer func() { _ = rc.Close() }()
		if err := json.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(&r); err != nil {
			return r, fmt.Errorf("shadow report %s: %w", hash, err)
		}
		return r, nil
	}
	return r, fmt.Errorf("shadow report %s has no report.json", hash)
}

func (s *Service) onReport(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	return s.recordReport(ctx, tx, out.PipelineRunID, out.Artifact)
}

func (s *Service) recordReport(ctx context.Context, tx pgx.Tx, runID string, ref steps.ArtifactRef) ([]events.Draft, error) {
	list, err := queryReplays(ctx, tx, "pipeline_run_id = $1 FOR UPDATE", runID)
	if err != nil || len(list) == 0 {
		return nil, err // a shadow_report of a pipeline no replay started: nothing to record
	}
	r := list[0]
	if r.State == ReplayDone && r.ReportHash == ref.Hash {
		return nil, nil
	}
	if s.CAS == nil {
		return nil, errors.New("deployments: no content store to read the shadow report")
	}
	rep, err := readReport(s.CAS, ref.Hash)
	if err != nil {
		return nil, err
	}
	var div *Divergence
	if rep.Divergence.WER != nil {
		div = &Divergence{WER: *rep.Divergence.WER, CI: rep.Divergence.CI, Level: rep.Divergence.Level}
	}
	divJSON, _ := json.Marshal(div)
	confJSON, _ := json.Marshal(rep.Confidence)
	worst := rep.Worst
	if worst == nil {
		worst = []json.RawMessage{}
	}
	worstJSON, _ := json.Marshal(worst)
	now := s.now()
	if _, err := tx.Exec(ctx, `UPDATE shadow_replays SET state = 'done', reason = '', calls = $2, hours = $3, utterances = $4,
			divergence = $5, confidence = $6, worst = $7, report_hash = $8, finished_at = $9 WHERE id = $1`,
		r.ID, rep.Calls, round2(rep.Hours), rep.Utterances, divJSON, confJSON, worstJSON, ref.Hash, now); err != nil {
		return nil, fmt.Errorf("record shadow replay %s: %w", r.ID, err)
	}
	b := &pgx.Batch{}
	for _, c := range rep.CallList {
		b.Queue(`INSERT INTO shadow_calls (deployment_id, call, replay_id, duration_s, segments) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (deployment_id, call) DO NOTHING`, r.DeploymentID, c.Call, r.ID, c.Duration, c.Segments)
	}
	var worstIDs []string
	for _, w := range worst {
		var seg struct {
			Audio    string  `json:"audio"`
			Duration float64 `json:"duration"`
		}
		if json.Unmarshal(w, &seg) != nil || !steps.ValidHash(seg.Audio) {
			continue
		}
		if len(worstIDs) < 10 {
			worstIDs = append(worstIDs, seg.Audio)
		}
		b.Queue(`INSERT INTO shadow_segments (hash, replay_id, duration_s) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			seg.Audio, r.ID, seg.Duration)
	}
	if b.Len() > 0 {
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return nil, fmt.Errorf("record the calls of shadow replay %s: %w", r.ID, err)
		}
	}
	d, err := Lock(ctx, tx, r.DeploymentID)
	if err != nil {
		return nil, err
	}
	t := Totals{Divergence: div, LastReplayAt: &now}
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(duration_s), 0) / 3600.0, count(*) FROM shadow_calls WHERE deployment_id = $1`,
		d.ID).Scan(&t.Hours, &t.Calls); err != nil {
		return nil, fmt.Errorf("total the shadow of %s: %w", d.ID, err)
	}
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(utterances), 0), count(*) FROM shadow_replays WHERE deployment_id = $1 AND state = 'done'`,
		d.ID).Scan(&t.Utterances, &t.Nights); err != nil {
		return nil, fmt.Errorf("total the shadow of %s: %w", d.ID, err)
	}
	t.Hours = math.Round(t.Hours*1000) / 1000
	tb, _ := json.Marshal(t)
	// The totals change no revision: a promotion approved while a night ran stays valid (If-Match).
	if _, err := tx.Exec(ctx, "UPDATE deployments SET shadow = $2, updated_at = $3 WHERE id = $1", d.ID, tb, now); err != nil {
		return nil, fmt.Errorf("record the shadow of %s: %w", d.ID, err)
	}
	payload := map[string]any{"replayId": r.ID, "deploymentId": d.ID, "night": r.Night, "calls": rep.Calls, "hours": round2(rep.Hours),
		"utterances": rep.Utterances, "totalHours": t.Hours, "worst": worstIDs}
	if div != nil {
		payload["divergence"] = div
	}
	return []events.Draft{
		{Topic: ShadowTopic(d.ID), Type: EventReplayed, ProjectID: d.ProjectID, Payload: payload},
		{Topic: Topic(d.ID), Type: EventReplayed, ProjectID: d.ProjectID, Entity: &events.EntityRef{Kind: EntityKind, ID: d.ID, Rev: d.Rev},
			Payload: map[string]any{"replayId": r.ID, "totalHours": t.Hours}},
	}, nil
}

// Observe fails a replay whose pipeline run ended without its report (pipelines.RunObserver).
func (s *Service) Observe(ctx context.Context, tx pgx.Tx, r pipelines.Run) ([]events.Draft, error) {
	if r.State == pipelines.RunRunning || !strings.HasPrefix(r.RunID, replayPrefix) {
		return nil, nil
	}
	list, err := queryReplays(ctx, tx, "pipeline_run_id = $1 AND state IN ('planned', 'running') FOR UPDATE", r.ID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	rp := list[0]
	msg := r.Error
	switch {
	case r.State == pipelines.RunCancelled:
		msg = "cancelled: " + r.Error
	case r.State == pipelines.RunDone:
		msg = "the scoring step ended without a shadow report"
	case msg == "":
		msg = "the pipeline run failed"
	}
	if _, err := tx.Exec(ctx, "UPDATE shadow_replays SET state = 'failed', reason = $2, finished_at = $3 WHERE id = $1", rp.ID, msg, s.now()); err != nil {
		return nil, fmt.Errorf("fail shadow replay %s: %w", rp.ID, err)
	}
	return []events.Draft{{Topic: ShadowTopic(rp.DeploymentID), Type: EventShadowFailed, ProjectID: rp.ProjectID,
		Payload: map[string]any{"replayId": rp.ID, "deploymentId": rp.DeploymentID, "error": msg}}}, nil
}

// ---------------------------------------------------------------- retention

// Retention clears the texts of nights older than deploy.shadow_artifact_retention_days: the worst segments kept in
// the row, the segment audio audio.get plays, and every artifact the night's pipeline run wrote (the cut audio, both
// transcripts, the report), through the eviction job. The night's summary stays (a daily periodic job).
func (s *Service) Retention(ctx context.Context) (int, error) {
	days := s.defaults().Deploy.ShadowArtifactRetentionDays.Value
	cutoff := s.now().Add(-time.Duration(days) * 24 * time.Hour)
	list, err := queryReplays(ctx, s.Pool, `texts_evicted_at IS NULL AND state IN ('done', 'failed', 'skipped')
		AND coalesce(finished_at, created_at) < $1 ORDER BY created_at`, cutoff)
	if err != nil {
		return 0, err
	}
	ctx = auth.WithActor(ctx, jobs.System)
	n := 0
	for _, r := range list {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var drafts []events.Draft
			if r.PipelineRunID != "" && s.Evict != nil {
				rows, err := tx.Query(ctx, "SELECT hash FROM artifacts WHERE pipeline_run_id = $1 AND evicted_at IS NULL ORDER BY hash", r.PipelineRunID)
				if err != nil {
					return fmt.Errorf("list the artifacts of %s: %w", r.ID, err)
				}
				hashes, err := pgx.CollectRows(rows, pgx.RowTo[string])
				if err != nil {
					return fmt.Errorf("list the artifacts of %s: %w", r.ID, err)
				}
				if len(hashes) > 0 {
					if drafts, err = s.Evict(ctx, tx, hashes); err != nil {
						return err
					}
				}
			}
			if _, err := tx.Exec(ctx, "UPDATE shadow_replays SET worst = NULL, texts_evicted_at = $2 WHERE id = $1", r.ID, s.now()); err != nil {
				return fmt.Errorf("clear the texts of %s: %w", r.ID, err)
			}
			if _, err := tx.Exec(ctx, "DELETE FROM shadow_segments WHERE replay_id = $1", r.ID); err != nil {
				return fmt.Errorf("clear the segments of %s: %w", r.ID, err)
			}
			return events.Append(ctx, tx, jobs.System, nil, drafts)
		})
		if err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// SegmentAudio is a replayed segment's audio while its night's texts are kept (media's lookup by hash).
func SegmentAudio(ctx context.Context, q storage.Querier, hash string) (float64, bool, error) {
	var dur float64
	err := q.QueryRow(ctx, `SELECT s.duration_s FROM shadow_segments s JOIN shadow_replays r ON r.id = s.replay_id
		WHERE s.hash = $1 AND r.texts_evicted_at IS NULL LIMIT 1`, hash).Scan(&dur)
	if noRows(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("look up shadow segment %s: %w", hash, err)
	}
	return dur, true, nil
}
