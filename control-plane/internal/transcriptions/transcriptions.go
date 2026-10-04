// Package transcriptions is the manual transcription test and its live channel (R47–R50; docs/spec/06-platform.md
// "Media"; spike A5): transcriptions.new resolves one to three targets (a checkpoint, a model version or a base model
// version, each at a latency profile, in a language, with an optional boost list) to the family's live role kind and
// queues an interactive job (R49) for it; the browser's WebSocket and the worker's dial-out meet in the relay
// (relay.go), which forwards their frames unchanged. Nothing outlives the session but the record of who, when, which
// targets and the GPU time (R47).
//
// Like internal/runs and internal/evals this package never names a model family: the live kind comes from the
// family descriptor's roles and its card memory from the descriptor's interactive block.
package transcriptions

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/langpacks"
	"github.com/usunrise88/cadence/control-plane/internal/media"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Names of the live channel.
const (
	// Kind is the entity kind of a session (topic entity.transcription.{id}).
	Kind = "transcription"
	// RoleLive is the family role whose step kind serves sessions.
	RoleLive = "live"
	// EnvLiveToken is the lease environment variable holding the token the worker dials with.
	EnvLiveToken = "CADENCE_LIVE_TOKEN" //nolint:gosec // a variable name, not a credential
	// HeaderLiveToken carries that token on workerLive.connect.
	HeaderLiveToken = "Cadence-Live-Token" //nolint:gosec // a header name, not a credential

	EventOpened = "transcription.opened"
	EventEnded  = "transcription.ended"

	StateQueued  = "queued"
	StateLoading = "loading"
	StateLive    = "live"
	StateEnded   = "ended"

	typeCheckpoint = runs.TypeCheckpoint
	typeBaseModel  = runs.TypeBaseModel
	typeBoostList  = "boost_list"
	typeAudio      = "audio"

	maxTargets = 3

	// HandlerTimeout bounds the live job's handler: a session's queue wait and its cap, with room to spare.
	HandlerTimeout = 3 * time.Hour
)

// Topic is the entity topic of session id.
func Topic(id string) string { return "entity." + Kind + "." + id }

// Service runs transcription sessions on one database.
type Service struct {
	Pool     *pgxpool.Pool
	Jobs     *jobs.Service
	Leases   steps.Leases
	CAS      *cas.Store
	Repo     pipelines.Repo
	Defaults func() *defaults.Defaults
	Log      *slog.Logger
	Now      func() time.Time
	// AllowedOrigins are origins (scheme://host[:port]) a page may open the socket from besides the server's own host
	// (CADENCE_ALLOWED_ORIGINS, for a front end on another host name).
	AllowedOrigins []string
	// Timing overrides the durations defaults.yaml gives (tests); zero fields keep the defaults.
	Timing Timing
	// Wake tells waiting claims that the queue changed (workers.Service.Wake); nil when there is none.
	Wake func()

	hub hub
}

// Timing holds the relay's clocks.
type Timing struct {
	Idle         time.Duration // without audio, keepalive or ping
	Cap          time.Duration // longest live session (before the allowance's own limit)
	QueueWait    time.Duration // longest wait for a card and the worker's dial
	WorkerDial   time.Duration // a worker socket no browser joins is closed after this
	Backpressure time.Duration // a full upstream queue closes the socket after this
	Poll         time.Duration // how often a waiting session re-reads its place
	Drain        time.Duration // how long the relay waits for the summary after it injected end
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func pick(override time.Duration, fallback time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return fallback
}

func (s *Service) timing() Timing {
	d := s.defaults().Transcriptions
	return Timing{
		Idle:         pick(s.Timing.Idle, time.Duration(d.IdleMinutes.Value)*time.Minute),
		Cap:          pick(s.Timing.Cap, time.Duration(d.SessionMaxMinutes.Value)*time.Minute),
		QueueWait:    pick(s.Timing.QueueWait, time.Duration(d.QueueWaitMinutes.Value)*time.Minute),
		WorkerDial:   pick(s.Timing.WorkerDial, time.Duration(d.WorkerDialTimeoutSeconds.Value)*time.Second),
		Backpressure: pick(s.Timing.Backpressure, time.Duration(d.BackpressureWaitSeconds.Value)*time.Second),
		Poll:         pick(s.Timing.Poll, 2*time.Second),
		Drain:        pick(s.Timing.Drain, 15*time.Second),
	}
}

// ---------------------------------------------------------------- the request

// TargetIn is one target of transcriptions.new.
type TargetIn struct {
	CheckpointID       string
	ModelVersionID     string
	BaseModelVersionID string
	Profile            string
	Language           string
	Boost              string
	BoostWeight        *float64
}

// Input is what the session transcribes (the contract's TranscriptionInput).
type Input struct {
	Kind        string   `json:"kind"`
	UtteranceID string   `json:"utteranceId,omitempty"`
	Start       *float64 `json:"start,omitempty"`
	End         *float64 `json:"end,omitempty"`
	Channel     *int     `json:"channel,omitempty"`
}

// Telephony is the phone-line simulation (the contract's TelephonySimulation).
type Telephony struct {
	Codec      string `json:"codec"`
	SampleRate int    `json:"sampleRate"`
}

// NewInput is transcriptions.new.
type NewInput struct {
	ProjectID string
	Actor     auth.Actor
	Input     Input
	Targets   []TargetIn
	Telephony *Telephony
	Pace      string
	Blind     bool
}

// ---------------------------------------------------------------- the answer

// Boost is a lane's boost list (the contract's TranscriptionBoost).
type Boost struct {
	List   string  `json:"list"`
	Commit string  `json:"commit,omitempty"`
	Weight float64 `json:"weight"`
	Terms  int     `json:"terms"`
}

// Lane is one target as the page shows it (the contract's TranscriptionLane).
type Lane struct {
	Target     string `json:"target"`
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Label      string `json:"label"`
	Family     string `json:"family"`
	Profile    string `json:"profile"`
	LatencyMs  *int   `json:"latencyMs,omitempty"`
	ChunkMs    *int   `json:"chunkMs,omitempty"`
	Language   string `json:"language"`
	Boost      *Boost `json:"boost,omitempty"`
	WeightsKey string `json:"weightsKey"`

	model    string // the job input of its weights (model.<n> or base.<n>)
	boostKey string // the job input of its boost list
}

// Limits are the session's limits (the contract's TranscriptionLimits).
type Limits struct {
	SessionSeconds   int `json:"sessionSeconds"`
	IdleSeconds      int `json:"idleSeconds"`
	TicketSeconds    int `json:"ticketSeconds"`
	FrameMs          int `json:"frameMs"`
	MaxFileSeconds   int `json:"maxFileSeconds"`
	MaxFileBytes     int `json:"maxFileBytes"`
	MaxMessageBytes  int `json:"maxMessageBytes"`
	QueueWaitSeconds int `json:"queueWaitSeconds"`
}

// Allowance is the project's manual-test allowance today (the contract's ManualTestAllowance).
type Allowance struct {
	GPUHoursPerDay    float64 `json:"gpuHoursPerDay"`
	UsedGPUHours      float64 `json:"usedGpuHours"`
	RemainingGPUHours float64 `json:"remainingGpuHours"`
}

// Session is a session as transcriptions.new answers it (the contract's TranscriptionSession).
type Session struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"projectId"`
	JobID           string     `json:"jobId,omitempty"`
	State           string     `json:"state"`
	StreamURL       string     `json:"streamUrl,omitempty"`
	Ticket          string     `json:"ticket,omitempty"`
	TicketExpiresAt *time.Time `json:"ticketExpiresAt,omitempty"`
	Input           Input      `json:"input"`
	Targets         []Lane     `json:"targets"`
	Telephony       *Telephony `json:"telephony,omitempty"`
	Pace            string     `json:"pace"`
	Blind           bool       `json:"blind"`
	Family          string     `json:"family"`
	LiveKind        string     `json:"liveKind"`
	ReservationMB   int        `json:"reservationMb"`
	Position        *int       `json:"position,omitempty"`
	Reason          string     `json:"reason,omitempty"`
	Limits          Limits     `json:"limits"`
	Allowance       Allowance  `json:"allowance"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// Plan is a resolved transcriptions.new: the session it opens and the interactive job's spec without the ids.
type Plan struct {
	Session
	userID   string
	actor    auth.Actor
	kind     pipelines.Kind
	inputs   map[string]steps.ArtifactRef
	gpu      bool
	slug     string
	priority int
}

// ---------------------------------------------------------------- family descriptors

type profile struct {
	Name      string `json:"name"`
	LatencyMs int    `json:"latencyMs"`
	ChunkMs   int    `json:"chunkMs"`
}

type family struct {
	runs.Family
	Profiles    []profile
	Boosting    string
	MemoryMB    int
	ExtraMB     int
	baseVersion registry.Version // a base model of the family (its locale tags)
}

func familyOf(ctx context.Context, q storage.Querier, base registry.Version) (family, error) {
	f, err := runs.FamilyOf(ctx, q, base)
	if err != nil {
		return family{}, err
	}
	v, err := registry.GetVersion(ctx, q, registry.KindModelFamily, f.VersionID)
	if err != nil {
		return family{}, err
	}
	var d struct {
		LatencyProfiles []profile `json:"latencyProfiles"`
		Capabilities    struct {
			Boosting string `json:"boosting"`
		} `json:"capabilities"`
		Interactive struct {
			MemoryMB          int `json:"memoryMb"`
			ExtraCheckpointMB int `json:"extraCheckpointMb"`
		} `json:"interactive"`
	}
	if err := json.Unmarshal(v.Payload, &d); err != nil {
		return family{}, fmt.Errorf("decode model family %s: %w", v.ID, err)
	}
	return family{Family: f, Profiles: d.LatencyProfiles, Boosting: d.Capabilities.Boosting, MemoryMB: d.Interactive.MemoryMB,
		ExtraMB: d.Interactive.ExtraCheckpointMB, baseVersion: base}, nil
}

// target is a resolved target before it becomes a lane.
type target struct {
	kind, id, label string
	weightsKey      string
	family          family
	artifact        steps.ArtifactRef
	base            registry.Version
	// trainedLang is the language a checkpoint (or the checkpoint a model version was registered from) was trained
	// under, from its run's language parameter: the default the lane decodes in, so a model fine-tuned under a
	// neighbour's prompt (Serbian under hr-HR) is heard as it was trained.
	trainedLang string
}

// trainParamsLang names the train-step parameters that carry the training language (runs.CheckLanguages reads the
// same names).
var trainParamsLang = []string{"target_lang", "language", "lang", "locale"}

// trainedLanguage is the language checkpoint ckp's run was trained under, or "" when the run set none.
func trainedLanguage(ctx context.Context, q storage.Querier, ckp string) (string, error) {
	if ckp == "" {
		return "", nil
	}
	var params map[string]any
	err := q.QueryRow(ctx, "SELECT r.params FROM checkpoints c JOIN runs r ON r.id = c.run_id WHERE c.id = $1", ckp).Scan(&params)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read the training language of checkpoint %s: %w", ckp, err)
	}
	return languageParam(params), nil
}

// languageParam is the training language a run's params set, or "".
func languageParam(params map[string]any) string {
	for _, n := range trainParamsLang {
		if v, _ := params[n].(string); strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// laneLanguage is the language a lane decodes in: the one asked for, else the one the model was trained under, else
// the project's first locale ("" when there is none).
func laneLanguage(asked, trained string, locales []string) string {
	if l := strings.TrimSpace(asked); l != "" {
		return l
	}
	if trained != "" {
		return trained
	}
	if len(locales) > 0 {
		return locales[0]
	}
	return ""
}

func (s *Service) resolveTarget(ctx context.Context, q storage.Querier, p projects.Project, i int, t TargetIn) (target, error) {
	at := fmt.Sprintf("/targets/%d", i)
	n := 0
	for _, v := range []string{t.CheckpointID, t.ModelVersionID, t.BaseModelVersionID} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	if n != 1 {
		return target{}, problems.Validation([]problems.FieldError{{Path: at,
			Message: "name exactly one of checkpointId (ckp_…), modelVersionId or baseModelVersionId"}})
	}
	switch {
	case t.CheckpointID != "":
		id := strings.TrimSpace(t.CheckpointID)
		c, err := runs.GetCheckpoint(ctx, q, id)
		if err != nil {
			return target{}, err
		}
		if c.ProjectID != p.ID {
			return target{}, problems.NotFound.New("the project has no checkpoint %q", id)
		}
		var baseID string
		if err := q.QueryRow(ctx, "SELECT base_version_id FROM runs WHERE id = $1", c.RunID).Scan(&baseID); err != nil {
			return target{}, fmt.Errorf("read the base model of run %s: %w", c.RunID, err)
		}
		base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, baseID)
		if err != nil {
			return target{}, err
		}
		f, err := familyOf(ctx, q, base)
		if err != nil {
			return target{}, err
		}
		label := c.RunID
		if c.Step != nil {
			label = fmt.Sprintf("%s step %d", c.RunID, *c.Step)
		}
		key := c.WeightsHash
		if key == "" {
			key = c.Artifact
		}
		lang, err := trainedLanguage(ctx, q, c.ID)
		if err != nil {
			return target{}, err
		}
		return target{kind: "checkpoint", id: c.ID, label: label, weightsKey: key, family: f, base: base, trainedLang: lang,
			artifact: steps.ArtifactRef{Hash: c.Artifact, Type: typeCheckpoint, Meta: c.Meta}}, nil
	case t.ModelVersionID != "":
		v, err := registry.Resolve(ctx, q, p.ID, registry.KindModel, strings.TrimSpace(t.ModelVersionID))
		if err != nil {
			return target{}, err
		}
		var mp struct {
			WeightsHash        string `json:"weightsHash"`
			CheckpointHash     string `json:"checkpointHash"`
			BaseModelVersionID string `json:"baseModelVersionId"`
			CheckpointID       string `json:"checkpointId"`
		}
		if err := json.Unmarshal(v.Payload, &mp); err != nil {
			return target{}, fmt.Errorf("decode model %s: %w", v.ID, err)
		}
		base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, mp.BaseModelVersionID)
		if err != nil {
			return target{}, err
		}
		f, err := familyOf(ctx, q, base)
		if err != nil {
			return target{}, err
		}
		key := mp.WeightsHash
		if key == "" {
			key = mp.CheckpointHash
		}
		lang, err := trainedLanguage(ctx, q, mp.CheckpointID)
		if err != nil {
			return target{}, err
		}
		return target{kind: "model", id: v.ID, label: v.Name + " " + v.Version, weightsKey: key, family: f, base: base, trainedLang: lang,
			artifact: steps.ArtifactRef{Hash: mp.CheckpointHash, Type: typeCheckpoint}}, nil
	default:
		v, err := registry.Resolve(ctx, q, p.ID, registry.KindBaseModel, strings.TrimSpace(t.BaseModelVersionID))
		if err != nil {
			return target{}, err
		}
		f, err := familyOf(ctx, q, v)
		if err != nil {
			return target{}, err
		}
		if s.CAS == nil {
			return target{}, errors.New("transcriptions: no content store to render the base model")
		}
		ref, err := runs.RenderBaseModel(s.CAS, v, f.Family)
		if err != nil {
			return target{}, err
		}
		return target{kind: "base_model", id: v.ID, label: v.Name + " " + v.Version, weightsKey: "base:" + v.ID, family: f, base: v,
			artifact: ref}, nil
	}
}

// language is a locale's primary language subtag, lower case.
func language(locale string) string {
	l, _, _ := strings.Cut(strings.ReplaceAll(locale, "_", "-"), "-")
	return strings.ToLower(strings.TrimSpace(l))
}

// knowsLanguage reports whether a base model whose tags list locale:<code> (none: unknown, so yes) decodes lang.
func knowsLanguage(tags []string, lang string) (bool, []string) {
	var known []string
	for _, t := range tags {
		if code, ok := strings.CutPrefix(t, "locale:"); ok && code != "" {
			known = append(known, code)
		}
	}
	if len(known) == 0 || lang == "auto" {
		return true, nil
	}
	for _, k := range known {
		if language(k) == language(lang) {
			return true, known
		}
	}
	return false, known
}

var (
	boostPathRe = regexp.MustCompile(`^lang/[A-Za-z0-9_-]+/boost/[A-Za-z0-9._-]+\.txt$`)
	refRe       = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,100}$`)
	languageRe  = regexp.MustCompile(`^([a-zA-Z]{2,3}(-[A-Za-z0-9]{2,8})*|auto)$`)
)

// boostList reads lang/<locale>/boost/<file>.txt[@ref] from the project repository and renders it as a boost_list
// artifact (the same rendering evals use: the language pack's {terms, weight}).
func (s *Service) boostList(ctx context.Context, p projects.Project, at, spec string, weight *float64) (*Boost, steps.ArtifactRef, error) {
	path, ref, hasRef := strings.Cut(spec, "@")
	if !hasRef {
		ref = "main"
	}
	if !boostPathRe.MatchString(path) || !refRe.MatchString(ref) {
		return nil, steps.ArtifactRef{}, problems.Validation([]problems.FieldError{{Path: at,
			Message: fmt.Sprintf("%q is not none or lang/<locale>/boost/<file>.txt[@<commit>]", spec)}})
	}
	if s.Repo == nil || !s.Repo.Exists(p.Slug) {
		return nil, steps.ArtifactRef{}, problems.Validation([]problems.FieldError{{Path: at, Message: "the project has no repository to read the boost list from"}})
	}
	b, commit, err := s.Repo.ReadFile(ctx, p.Slug, ref, path)
	if err != nil {
		msg := fmt.Sprintf("%s cannot be read at %s: %v", path, ref, err)
		if errors.Is(err, repos.ErrNotFound) {
			msg = fmt.Sprintf("%s does not exist at %s", path, ref)
		}
		return nil, steps.ArtifactRef{}, problems.Validation([]problems.FieldError{{Path: at, Message: msg}})
	}
	domain := strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".txt")
	bl, err := langpacks.ParseBoost(domain, b, s.defaults().Langpacks.BoostMaxTerms.Value)
	if err != nil {
		return nil, steps.ArtifactRef{}, problems.Validation([]problems.FieldError{{Path: at, Message: fmt.Sprintf("%s: %v", path, err)}})
	}
	if weight != nil {
		bl.Weight = *weight
	}
	if s.CAS == nil {
		return nil, steps.ArtifactRef{}, errors.New("transcriptions: no content store")
	}
	raw := bl.Artifact()
	h, err := s.CAS.PutBytes(raw)
	if err != nil {
		return nil, steps.ArtifactRef{}, err
	}
	meta, _ := json.Marshal(map[string]any{"path": path, "commit": commit, "terms": len(bl.Terms), "sha256": bl.SHA256()})
	return &Boost{List: path, Commit: commit, Weight: bl.Weight, Terms: len(bl.Terms)},
		steps.ArtifactRef{Hash: h, Type: typeBoostList, Size: int64(len(raw)), Meta: meta}, nil
}

func consumes(k pipelines.Kind, typ string) bool {
	for _, t := range k.Consumes {
		if t == typ {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- Prepare

// Prepare resolves transcriptions.new: the targets, their family's live kind and memory, the input, the allowance
// and the person's other sessions. It writes nothing but rendered blobs (content-addressed).
func (s *Service) Prepare(ctx context.Context, q storage.Querier, in NewInput) (Plan, error) {
	d := s.defaults()
	if in.Actor.Kind != auth.KindUser || in.Actor.ID == "" {
		return Plan{}, problems.Forbidden.New("a transcription is a manual test: a person runs it (agents test models with evals.new)")
	}
	p, err := projects.GetByID(ctx, q, in.ProjectID)
	if err != nil {
		return Plan{}, err
	}
	if p.Archived() {
		return Plan{}, problems.Conflict.New("project %s is archived", p.Slug)
	}
	t := s.timing()
	tr := d.Transcriptions
	pl := Plan{userID: in.Actor.ID, actor: in.Actor, slug: p.Slug, priority: tr.InteractiveJobPriority.Value, inputs: map[string]steps.ArtifactRef{}}
	pl.ProjectID = p.ID
	pl.State = StateQueued
	pl.Blind = in.Blind
	pl.Limits = Limits{
		SessionSeconds: int(t.Cap / time.Second), IdleSeconds: int(t.Idle / time.Second), TicketSeconds: tr.TicketTTLSeconds.Value,
		FrameMs: tr.FrameMs.Value, MaxFileSeconds: tr.MaxFileMinutes.Value * 60, MaxFileBytes: tr.MaxFileMB.Value << 20,
		MaxMessageBytes: tr.MaxMessageKB.Value << 10, QueueWaitSeconds: int(t.QueueWait / time.Second),
	}
	var fields []problems.FieldError
	switch in.Pace {
	case "":
		pl.Pace = "realtime"
	case "realtime", "fast":
		pl.Pace = in.Pace
	default:
		fields = append(fields, problems.FieldError{Path: "/pace", Message: "realtime or fast"})
	}
	if in.Telephony != nil {
		tel := *in.Telephony
		if tel.Codec == "" {
			tel.Codec = "ulaw"
		}
		if tel.SampleRate == 0 {
			tel.SampleRate = 8000
		}
		if tel.Codec != "ulaw" && tel.Codec != "alaw" && tel.Codec != "none" {
			fields = append(fields, problems.FieldError{Path: "/telephony/codec", Message: "ulaw, alaw or none"})
		}
		if tel.SampleRate != 8000 {
			fields = append(fields, problems.FieldError{Path: "/telephony/sampleRate", Message: "8000"})
		}
		pl.Telephony = &tel
	}
	if err := s.input(ctx, q, &pl, in.Input); err != nil {
		var pe *problems.Error
		if errors.As(err, &pe) && pe.Type == problems.ValidationFailed {
			fields = append(fields, pe.Errors...)
		} else {
			return Plan{}, err
		}
	}
	if len(in.Targets) < 1 || len(in.Targets) > maxTargets {
		fields = append(fields, problems.FieldError{Path: "/targets", Message: "one to three targets"})
	}
	if len(fields) > 0 {
		return Plan{}, problems.Validation(fields)
	}
	if err := s.targets(ctx, q, p, &pl, in.Targets); err != nil {
		return Plan{}, err
	}
	if pl.Allowance, err = s.allowance(ctx, q, p.ID); err != nil {
		return Plan{}, err
	}
	if pl.Allowance.RemainingGPUHours <= 0 {
		return Plan{}, problems.TranscriptionAllowanceExhausted.New(
			"project %s used its %.2g GPU-hours of manual tests today (budgets.manual_test_gpu_hours_per_project_per_day); test with evals.new, or try again tomorrow",
			p.Slug, pl.Allowance.GPUHoursPerDay)
	}
	if left := int(pl.Allowance.RemainingGPUHours * 3600); left < pl.Limits.SessionSeconds {
		pl.Limits.SessionSeconds = max(left, 1)
	}
	var open string
	err = q.QueryRow(ctx, "SELECT id FROM transcriptions WHERE user_id = $1 AND state <> 'ended'", pl.userID).Scan(&open)
	switch {
	case err == nil:
		return Plan{}, problems.TranscriptionInProgress.New("you already have transcription session %s open; end it (or close its page) first", open)
	case !errors.Is(err, pgx.ErrNoRows):
		return Plan{}, fmt.Errorf("read open sessions: %w", err)
	}
	pl.CreatedAt = s.now().UTC()
	return pl, nil
}

// input checks the session's input; a span is resolved to the utterance's stored audio.
func (s *Service) input(ctx context.Context, q storage.Querier, pl *Plan, in Input) error {
	pl.Input = Input{Kind: in.Kind}
	switch in.Kind {
	case "microphone", "file":
		if in.UtteranceID != "" || in.Start != nil || in.End != nil || in.Channel != nil {
			return problems.Validation([]problems.FieldError{{Path: "/input", Message: "utteranceId, start, end and channel belong to kind span"}})
		}
		return nil
	case "span":
	default:
		return problems.Validation([]problems.FieldError{{Path: "/input/kind", Message: "microphone, file or span"}})
	}
	if strings.TrimSpace(in.UtteranceID) == "" {
		return problems.Validation([]problems.FieldError{{Path: "/input/utteranceId", Message: "a span names its utterance (utt_… or its audio's b3: hash)"}})
	}
	u, err := media.Lookup(ctx, q, strings.TrimSpace(in.UtteranceID))
	if err != nil {
		return err
	}
	start, end, ch := 0.0, u.Duration, 0
	if in.Start != nil {
		start = *in.Start
	}
	if in.End != nil {
		end = min(*in.End, u.Duration)
	}
	if in.Channel != nil {
		ch = *in.Channel
	}
	var fields []problems.FieldError
	if end <= start {
		fields = append(fields, problems.FieldError{Path: "/input/end", Message: fmt.Sprintf("the span must end after it starts (the utterance is %.2f s)", u.Duration)})
	}
	if ch >= max(u.Channels, 1) {
		fields = append(fields, problems.FieldError{Path: "/input/channel", Message: fmt.Sprintf("the utterance has %d channel(s)", u.Channels)})
	}
	if end-start > float64(pl.Limits.MaxFileSeconds) {
		fields = append(fields, problems.FieldError{Path: "/input", Message: fmt.Sprintf("a span is at most %d s", pl.Limits.MaxFileSeconds)})
	}
	if len(fields) > 0 {
		return problems.Validation(fields)
	}
	pl.Input = Input{Kind: "span", UtteranceID: u.ID, Start: &start, End: &end, Channel: &ch}
	pl.inputs["audio"] = steps.ArtifactRef{Hash: u.Hash, Type: typeAudio}
	return nil
}

// targets resolves the targets into lanes, the job's inputs, the live kind and the reservation.
func (s *Service) targets(ctx context.Context, q storage.Querier, p projects.Project, pl *Plan, in []TargetIn) error {
	d := s.defaults()
	resolved := make([]target, 0, len(in))
	for i, t := range in {
		r, err := s.resolveTarget(ctx, q, p, i, t)
		if err != nil {
			return err
		}
		if len(resolved) > 0 && r.family.Name != resolved[0].family.Name {
			return problems.Validation([]problems.FieldError{{Path: fmt.Sprintf("/targets/%d", i), Message: fmt.Sprintf(
				"every target of a session is of one model family (%s, not %s): one worker job serves them all", resolved[0].family.Name, r.family.Name)}})
		}
		resolved = append(resolved, r)
	}
	f := resolved[0].family
	kindName, err := f.Role(RoleLive)
	if err != nil {
		return problems.FamilyUnavailable.New("model family %s names no step kind for the live role: its runtime's pack cannot serve transcription sessions", f.Name)
	}
	k, err := runs.RoleKind(ctx, q, kindName)
	if err != nil {
		return err
	}
	pl.kind, pl.Family, pl.LiveKind = k, f.Name, k.Ref()
	pl.gpu = k.Resources.GPU
	var fields []problems.FieldError
	models := map[string]string{} // weights key → input name
	lanes := make([]Lane, 0, len(resolved))
	for i, r := range resolved {
		at := fmt.Sprintf("/targets/%d", i)
		ln := Lane{Kind: r.kind, ID: r.id, Label: r.label, Family: f.Name, WeightsKey: r.weightsKey}
		prof, ok := chooseProfile(d, f.Profiles, in[i].Profile)
		if !ok {
			names := make([]string, 0, len(f.Profiles))
			for _, pr := range f.Profiles {
				names = append(names, pr.Name)
			}
			fields = append(fields, problems.FieldError{Path: at + "/profile", Message: fmt.Sprintf("model family %s has no latency profile %q (it has %s)",
				f.Name, in[i].Profile, strings.Join(names, ", "))})
		} else {
			ln.Profile = prof.Name
			lat := prof.LatencyMs
			ln.LatencyMs = &lat
			if prof.ChunkMs > 0 {
				ch := prof.ChunkMs
				ln.ChunkMs = &ch
			}
		}
		lang := laneLanguage(in[i].Language, r.trainedLang, p.Locales)
		switch {
		case lang == "":
			fields = append(fields, problems.FieldError{Path: at + "/language", Message: "name the language to decode in (the project has no locale)"})
		case !languageRe.MatchString(lang):
			fields = append(fields, problems.FieldError{Path: at + "/language", Message: fmt.Sprintf("%q is not a BCP 47 language tag", lang)})
		default:
			if ok, known := knowsLanguage(r.base.Tags, lang); !ok {
				fields = append(fields, problems.FieldError{Path: at + "/language", Message: fmt.Sprintf("%s is not a language %s knows (it knows %s); pick a close one it knows",
					lang, r.base.Name, strings.Join(known, ", "))})
			}
		}
		ln.Language = lang
		if b := strings.TrimSpace(in[i].Boost); b != "" && b != "none" {
			if f.Boosting == "" || !consumes(k, typeBoostList) {
				fields = append(fields, problems.FieldError{Path: at + "/boost", Message: fmt.Sprintf(
					"model family %s (live step %s) does not decode with a boost list", f.Name, k.Ref())})
			} else {
				boost, ref, err := s.boostList(ctx, p, at+"/boost", b, in[i].BoostWeight)
				if err != nil {
					var pe *problems.Error
					if errors.As(err, &pe) && pe.Type == problems.ValidationFailed {
						fields = append(fields, pe.Errors...)
						continue
					}
					return err
				}
				ln.Boost = boost
				ln.boostKey = fmt.Sprintf("boost.%d", i)
				pl.inputs[ln.boostKey] = ref
			}
		} else if in[i].BoostWeight != nil {
			fields = append(fields, problems.FieldError{Path: at + "/boostWeight", Message: "a weight needs a boost list"})
		}
		name, seen := models[r.weightsKey]
		if !seen {
			prefix := "model"
			if r.artifact.Type == typeBaseModel {
				prefix = "base"
			}
			name = fmt.Sprintf("%s.%d", prefix, len(models))
			models[r.weightsKey] = name
			pl.inputs[name] = r.artifact
		}
		ln.model = name
		lanes = append(lanes, ln)
	}
	if len(fields) > 0 {
		return problems.Validation(fields)
	}
	order := identity(len(lanes))
	if pl.Blind {
		order = shuffled(len(lanes))
	}
	pl.Targets = make([]Lane, len(lanes))
	for i, j := range order {
		ln := lanes[j]
		ln.Target = string(rune('A' + i))
		pl.Targets[i] = ln
	}
	mem := f.MemoryMB
	if mem <= 0 {
		mem = int(k.Resources.MemoryGB * 1024)
	}
	if len(models) > 1 {
		mem += f.ExtraMB * (len(models) - 1)
	}
	if pl.gpu {
		pl.ReservationMB = mem
	}
	return nil
}

// chooseProfile is the requested profile, else the eval's primary profile when the family declares it, else its first
// streaming profile, else its first.
func chooseProfile(d *defaults.Defaults, ps []profile, want string) (profile, bool) {
	want = strings.TrimSpace(want)
	find := func(name string) (profile, bool) {
		for _, p := range ps {
			if p.Name == name {
				return p, true
			}
		}
		return profile{}, false
	}
	if want != "" {
		return find(want)
	}
	if p, ok := find(d.Eval.PrimaryProfile.Value); ok {
		return p, true
	}
	for _, p := range ps {
		if p.ChunkMs > 0 {
			return p, true
		}
	}
	if len(ps) > 0 {
		return ps[0], true
	}
	return profile{}, false
}

func identity(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// shuffled is a uniformly random permutation of 0..n-1 (crypto/rand: a blind lane order must not be guessable).
func shuffled(n int) []int {
	out := identity(n)
	for i := n - 1; i > 0; i-- {
		j, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			continue
		}
		out[i], out[j.Int64()] = out[j.Int64()], out[i]
	}
	return out
}

// ---------------------------------------------------------------- the allowance

// allowance is the project's manual-test allowance today: interactive leases on GPU cards, wall time (the card is
// held for the session, RTF 0.10–0.17 notwithstanding; spike A5 proposal 4). What is left is also net of what the
// project's open sessions were granted and have not leased yet (session_seconds), so sessions opened side by side
// cannot together spend more than the allowance (Open grants under a per-project lock).
func (s *Service) allowance(ctx context.Context, q storage.Querier, projectID string) (Allowance, error) {
	d := s.defaults()
	now := s.now()
	since, err := runs.DayStart(ctx, q, d, now)
	if err != nil {
		return Allowance{}, err
	}
	var used float64
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(extract(epoch FROM
			least(coalesce(l.ended_at, $4::timestamptz), $3::timestamptz) - greatest(l.created_at, $2::timestamptz))), 0) / 3600
		FROM leases l JOIN step_jobs s ON s.job_id = l.job_id
		WHERE l.job_kind = 'interactive' AND l.card_index IS NOT NULL AND s.project_id = $1
			AND l.created_at < $3 AND coalesce(l.ended_at, $4::timestamptz) > $2`, projectID, since, now, now).Scan(&used); err != nil {
		return Allowance{}, fmt.Errorf("meter manual tests: %w", err)
	}
	var committed float64
	if err := q.QueryRow(ctx, `SELECT coalesce(sum(greatest(0, t.session_seconds - `+leasedSQL+`)), 0) / 3600
		FROM transcriptions t WHERE t.project_id = $1 AND t.state <> 'ended' AND t.session_seconds > 0`, projectID, now).Scan(&committed); err != nil {
		return Allowance{}, fmt.Errorf("meter open manual tests: %w", err)
	}
	per := d.Budgets.ManualTestGPUHoursPerProjectPerDay.Value
	used = round3(used)
	return Allowance{GPUHoursPerDay: per, UsedGPUHours: used, RemainingGPUHours: round3(per - used - committed)}, nil
}

// leasedSQL is the card time (seconds, at $2) the job of transcription row t has leased so far.
const leasedSQL = `coalesce((SELECT sum(extract(epoch FROM coalesce(l.ended_at, $2::timestamptz) - l.created_at)) FROM leases l
		WHERE l.job_id = t.job_id AND l.job_kind = 'interactive' AND l.card_index IS NOT NULL), 0)`

// lockAllowance serialises the grants of a project's manual tests until the transaction ends.
func lockAllowance(ctx context.Context, tx pgx.Tx, projectID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('transcriptions.allowance:' || $1))`, projectID); err != nil {
		return fmt.Errorf("lock the manual-test allowance: %w", err)
	}
	return nil
}

// grantLeft is how much of its grant session id's job has not leased yet; ok is false for a session granted nothing
// (a CPU session, which spends no allowance).
func (s *Service) grantLeft(ctx context.Context, q storage.Querier, id string) (time.Duration, bool, error) {
	var granted int
	var leased float64
	err := q.QueryRow(ctx, `SELECT t.session_seconds, `+leasedSQL+` FROM transcriptions t WHERE t.id = $1`, id, s.now()).
		Scan(&granted, &leased)
	if err != nil {
		return 0, false, fmt.Errorf("read the session's grant: %w", err)
	}
	if granted <= 0 {
		return 0, false, nil
	}
	return time.Duration((float64(granted) - leased) * float64(time.Second)), true, nil
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// ---------------------------------------------------------------- Open

// liveParams are the live role kind's parameters for this session (every pack's live kind reads them).
type liveParams struct {
	Session        string        `json:"session"`
	Targets        []targetParam `json:"targets"`
	Input          Input         `json:"input"`
	Telephony      *Telephony    `json:"telephony,omitempty"`
	Pace           string        `json:"pace"`
	MaxFileSeconds int           `json:"maxFileSeconds"`
	MaxFileBytes   int           `json:"maxFileBytes"`
	FrameMs        int           `json:"frameMs"`
}

type targetParam struct {
	Target   string `json:"target"`
	Model    string `json:"model"`
	Profile  string `json:"profile"`
	Language string `json:"language"`
	Boost    string `json:"boost,omitempty"`
}

// targetRecord is what the session's record keeps of a lane (R47: which targets, nothing else).
type targetRecord struct {
	Target   string `json:"target"`
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Profile  string `json:"profile"`
	Language string `json:"language"`
	Boost    string `json:"boost,omitempty"`
}

func newToken() (string, string) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	t := base64.RawURLEncoding.EncodeToString(b)
	return t, hashToken(t)
}

func hashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

// Open records the session, queues its interactive job and mints the single-use ticket, inside the command's
// transaction.
func (s *Service) Open(ctx context.Context, tx pgx.Tx, pl Plan) (Session, []events.Draft, error) {
	if s.Jobs == nil {
		return Session{}, nil, errors.New("transcriptions: no job service")
	}
	ses := pl.Session
	granted := 0 // card seconds granted from the allowance; a CPU session spends none
	if pl.gpu {
		// Grant under the project's lock from what is left net of the other open sessions' grants, so sessions opened
		// at the same time cannot together spend more than the allowance.
		if err := lockAllowance(ctx, tx, ses.ProjectID); err != nil {
			return Session{}, nil, err
		}
		a, err := s.allowance(ctx, tx, ses.ProjectID)
		if err != nil {
			return Session{}, nil, err
		}
		left := int(a.RemainingGPUHours * 3600)
		if left <= 0 {
			return Session{}, nil, problems.TranscriptionAllowanceExhausted.New(
				"project %s used its %.2g GPU-hours of manual tests today, open sessions included (budgets.manual_test_gpu_hours_per_project_per_day); test with evals.new, or try again tomorrow",
				pl.slug, a.GPUHoursPerDay)
		}
		ses.Allowance = a
		ses.Limits.SessionSeconds = max(1, min(ses.Limits.SessionSeconds, left))
		granted = ses.Limits.SessionSeconds
	}
	ses.ID = "trs_" + uuid.Must(uuid.NewV7()).String()
	params := liveParams{Session: ses.ID, Input: ses.Input, Telephony: ses.Telephony, Pace: ses.Pace,
		MaxFileSeconds: ses.Limits.MaxFileSeconds, MaxFileBytes: ses.Limits.MaxFileBytes, FrameMs: ses.Limits.FrameMs}
	records := make([]targetRecord, 0, len(ses.Targets))
	for _, ln := range ses.Targets {
		params.Targets = append(params.Targets, targetParam{Target: ln.Target, Model: ln.model, Profile: ln.Profile, Language: ln.Language, Boost: ln.boostKey})
		r := targetRecord{Target: ln.Target, Kind: ln.Kind, ID: ln.ID, Profile: ln.Profile, Language: ln.Language}
		if ln.Boost != nil {
			r.Boost = ln.Boost.List + "@" + ln.Boost.Commit
		}
		records = append(records, r)
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return Session{}, nil, fmt.Errorf("live params: %w", err)
	}
	estimate := float64(ses.Limits.SessionSeconds + ses.Limits.QueueWaitSeconds)
	spec := steps.Spec{
		StepID: "live", PipelineRunID: ses.ID, ProjectID: ses.ProjectID, Kind: pl.kind.Name, KindVersion: pl.kind.Version,
		Params: rawParams, Inputs: pl.inputs, Outputs: map[string]string{},
		Resources: steps.Resources{GPU: pl.gpu, GPUs: pl.kind.Resources.GPUs, MemoryGB: float64(pl.ReservationMB) / 1024,
			DiskGB: pl.kind.Resources.DiskGB, JobKind: steps.JobInteractive},
		Priority: pl.priority, EstimateSeconds: &estimate, Attempt: 1,
	}
	if err := spec.Validate(); err != nil {
		return Session{}, nil, err
	}
	now := s.now().UTC()
	ticket, ticketHash := newToken()
	expires := now.Add(time.Duration(ses.Limits.TicketSeconds) * time.Second)
	if _, err := tx.Exec(ctx, `INSERT INTO transcriptions (id, project_id, user_id, actor, targets, reservation_mb, state,
			ticket_hash, ticket_expires_at, created_at, session_seconds)
		VALUES ($1, $2, $3, $4, $5, $6, 'queued', $7, $8, $9, $10)`,
		ses.ID, ses.ProjectID, pl.userID, pl.actor, records, ses.ReservationMB, ticketHash, expires, now, granted); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Session{}, nil, problems.TranscriptionInProgress.New("you already have a transcription session open; end it (or close its page) first")
		}
		return Session{}, nil, fmt.Errorf("record the session: %w", err)
	}
	j, drafts, err := s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: steps.LiveJobKind, ProjectID: ses.ProjectID, Args: spec})
	if err != nil {
		return Session{}, nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE transcriptions SET job_id = $2 WHERE id = $1", ses.ID, j.ID); err != nil {
		return Session{}, nil, fmt.Errorf("record the session's job: %w", err)
	}
	ses.JobID = j.ID
	ses.CreatedAt = now
	ses.Ticket = ticket
	ses.TicketExpiresAt = &expires
	ses.StreamURL = "/api/transcriptions/" + ses.ID + "/stream?ticket=" + url.QueryEscape(ticket)
	var ahead int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM step_jobs WHERE state = 'waiting' AND job_kind = 'interactive'`).Scan(&ahead); err != nil {
		return Session{}, nil, fmt.Errorf("read the queue: %w", err)
	}
	pos := ahead + 1
	ses.Position = &pos
	drafts = append(drafts, events.Draft{Topic: Topic(ses.ID), Type: EventOpened, ProjectID: ses.ProjectID, Payload: map[string]any{
		"id": ses.ID, "jobId": j.ID, "state": ses.State, "targets": records, "reservationMb": ses.ReservationMB, "liveKind": ses.LiveKind}})
	return ses, drafts, nil
}

// ---------------------------------------------------------------- leases and the end of a job

// Granted is the worker protocol's grant hook (workers.Service.OnGranted): an interactive job's lease gets a fresh
// live token (only its hash is kept), and the session moves to loading.
func (s *Service) Granted(ctx context.Context, tx pgx.Tx, _, jobID string, spec steps.Spec) (map[string]string, error) {
	if spec.Resources.JobKind != steps.JobInteractive {
		return nil, nil
	}
	token, h := newToken()
	tag, err := tx.Exec(ctx, `UPDATE transcriptions SET live_token_hash = $2,
			state = CASE WHEN state = 'queued' THEN 'loading' ELSE state END
		WHERE job_id = $1 AND state <> 'ended'`, jobID, h)
	if err != nil {
		return nil, fmt.Errorf("mint the live token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil // the session ended meanwhile: the worker's dial is refused
	}
	return map[string]string{EnvLiveToken: token}, nil
}

// Register registers the live job kind (River kind live on the steps queue); call it before the job service starts.
func (s *Service) Register(j *jobs.Service) {
	s.Jobs = j
	j.Register(steps.LiveJobKind, s.Handle, jobs.KindOptions{Timeout: HandlerTimeout, Queue: jobs.QueueSteps})
}

// Handle is the live job's handler: it waits for the session's step to end on a worker, then closes the record.
func (s *Service) Handle(ctx context.Context, run *jobs.Run) (any, error) {
	if s.Leases == nil {
		return nil, steps.ErrNoWorkerProtocol
	}
	out, err := s.Leases.Await(ctx, run.Job.ID)
	if err != nil {
		switch {
		case ctx.Err() == nil:
			out = steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: err.Error()}}
		default:
			j, jerr := jobs.Get(context.WithoutCancel(ctx), s.Pool, run.Job.ID)
			switch {
			case jerr == nil && j.CancelRequestedAt != nil: // the relay (or a person) cancelled the session's job
				out = steps.Outcome{State: steps.StateCancelled, Error: &steps.StepError{Type: steps.ErrCancelled, Message: "the session's job was cancelled"}}
			case errors.Is(ctx.Err(), context.Canceled):
				// The control plane is stopping: the job (and a worker's lease on it) lives on; the handler waits again
				// on the next start, and the sweep ends the session, whose socket is gone.
				return nil, fmt.Errorf("live session: %w", jobs.ErrInterrupted)
			default:
				out = steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: "the live job's handler timed out"}}
			}
		}
	}
	if err := s.finish(context.WithoutCancel(ctx), run.Job.ID, out); err != nil {
		return nil, err
	}
	switch {
	case out.State == steps.StateDone:
		return map[string]any{"state": out.State}, nil
	case out.Error != nil:
		return nil, out.Error // the job ends failed, or cancelled when its cancel was asked for
	default:
		return nil, fmt.Errorf("live session ended %s", out.State)
	}
}

// endReason says how a job's outcome ended its session.
func endReason(o steps.Outcome) string {
	switch {
	case o.State == steps.StateDone:
		return "done"
	case o.Error != nil:
		return o.Error.Type + ": " + o.Error.Message
	}
	return o.State
}

// finish closes the session of job jobID: ended, with the GPU time of its card leases (the allowance's meter), and
// tells a browser still on the socket.
func (s *Service) finish(ctx context.Context, jobID string, o steps.Outcome) error {
	var id, projectID string
	var gpu float64
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `UPDATE transcriptions SET state = 'ended', ended_at = now(), ticket_hash = NULL, live_token_hash = NULL,
				end_reason = coalesce(end_reason, $2),
				gpu_seconds = (SELECT coalesce(sum(extract(epoch FROM coalesce(l.ended_at, now()) - l.created_at)), 0)
					FROM leases l WHERE l.job_id = $1 AND l.card_index IS NOT NULL)
			WHERE job_id = $1 AND state <> 'ended' RETURNING id, project_id, gpu_seconds`, jobID, truncate(endReason(o), 500)).
			Scan(&id, &projectID, &gpu)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("end the session: %w", err)
		}
		return events.Append(ctx, tx, jobs.System, nil, []events.Draft{{Topic: Topic(id), Type: EventEnded, ProjectID: projectID,
			Payload: map[string]any{"id": id, "jobId": jobID, "state": StateEnded, "gpuSeconds": round3(gpu)}}})
	})
	if err != nil {
		return err
	}
	if id != "" {
		s.hub.jobEnded(id, o)
	} else {
		s.hub.jobEndedByJob(jobID, o)
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// cancelJob cancels the session's job when it still runs (the relay ended first); a job that ended is left alone.
func (s *Service) cancelJob(ctx context.Context, jobID string) {
	if jobID == "" || s.Jobs == nil {
		return
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		j, err := jobs.Get(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if jobs.Terminal(j.State) || j.CancelRequestedAt != nil {
			return nil
		}
		_, drafts, err := s.Jobs.Cancel(ctx, tx, jobID, j.Rev)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, jobs.System, nil, drafts)
	})
	if err != nil {
		if pe, ok := problems.As(err); ok && (pe.Type == problems.Conflict || pe.Type == problems.PreconditionFailed) {
			return // it ended, or moved, meanwhile
		}
		s.log().WarnContext(ctx, "cancel a live session's job", "job", jobID, "err", err)
		return
	}
	if s.Wake != nil {
		s.Wake()
	}
}

// ---------------------------------------------------------------- the queue

// place reads where a session's job is: queued (its place among the waiting interactive jobs and why no card takes
// it) or loading (a worker leased it).
func (s *Service) place(ctx context.Context, q storage.Querier, jobID string, reservationMB int) (state string, position int, reason string, err error) {
	var (
		stepState string
		kindRef   string
		enqueued  time.Time
	)
	err = q.QueryRow(ctx, "SELECT state, kind_ref, enqueued_at FROM step_jobs WHERE job_id = $1", jobID).Scan(&stepState, &kindRef, &enqueued)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return StateQueued, 1, "the job is being queued", nil
	case err != nil:
		return "", 0, "", fmt.Errorf("read the session's job: %w", err)
	case stepState == "leased":
		return StateLoading, 0, "", nil
	case stepState == "ended":
		return StateEnded, 0, "", nil
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM step_jobs WHERE state = 'waiting' AND job_kind = 'interactive'
		AND (enqueued_at, job_id) < ($1, $2)`, enqueued, jobID).Scan(&position); err != nil {
		return "", 0, "", fmt.Errorf("read the queue: %w", err)
	}
	position++
	reason, err = s.waitReason(ctx, q, kindRef, reservationMB)
	return StateQueued, position, reason, err
}

// waitReason says why no card takes a waiting session: no worker serves the kind, no card accepts interactive jobs,
// or no card has the reservation free under its cap.
func (s *Service) waitReason(ctx context.Context, q storage.Querier, kindRef string, needMB int) (string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT host_id FROM workers WHERE $1 = ANY(step_kinds) AND last_seen_at > $2`,
		kindRef, s.now().Add(-2*time.Minute))
	if err != nil {
		return "", fmt.Errorf("read workers: %w", err)
	}
	hosts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", fmt.Errorf("read workers: %w", err)
	}
	if len(hosts) == 0 {
		return fmt.Sprintf("no worker online serves %s: start a worker whose runtime carries the family", kindRef), nil
	}
	if needMB <= 0 {
		return "waiting for a worker to claim it", nil
	}
	best, where, accepts := -1, "", false
	for _, hid := range hosts {
		h, err := compute.Get(ctx, q, hid)
		if err != nil {
			return "", err
		}
		for _, c := range h.Cards {
			if !contains(c.AllowedJobKinds, steps.JobInteractive) {
				continue
			}
			accepts = true
			var held int
			var bench bool
			lr, err := q.Query(ctx, `SELECT job_kind, memory_mb FROM leases WHERE host_id = $1 AND card_index = $2 AND state = 'active'`, h.ID, c.Index)
			if err != nil {
				return "", fmt.Errorf("read card leases: %w", err)
			}
			for lr.Next() {
				var k string
				var mb int
				if err := lr.Scan(&k, &mb); err != nil {
					lr.Close()
					return "", fmt.Errorf("read card leases: %w", err)
				}
				held += mb
				bench = bench || k == steps.JobBenchmark
			}
			lr.Close()
			if bench {
				continue
			}
			if free := int(c.MemoryCapGB*1024) - held; free > best {
				best, where = free, fmt.Sprintf("%s card %d", h.Name, c.Index)
			}
		}
	}
	switch {
	case !accepts:
		return "no card accepts interactive jobs (Settings → Compute: allowed job kinds)", nil
	case best < needMB:
		return fmt.Sprintf("no card has %d MB free under its cap (most: %d MB on %s); the session starts when a job there ends",
			needMB, max(best, 0), where), nil
	}
	return "waiting for a worker to claim it", nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- the socket's gates

// OriginAllowed reports whether a page at r's Origin may open the live socket: the server's own host (as the browser
// reached it, Host or X-Forwarded-Host), or one of AllowedOrigins. A missing Origin is refused (R48).
func (s *Service) OriginAllowed(r *http.Request) bool {
	o := strings.TrimSpace(r.Header.Get("Origin"))
	if o == "" {
		return false
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if fh := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Host"), ",")[0]); fh != "" && strings.EqualFold(u.Host, fh) {
		return true
	}
	for _, a := range s.AllowedOrigins {
		if strings.EqualFold(strings.TrimRight(strings.TrimSpace(a), "/"), strings.TrimRight(o, "/")) {
			return true
		}
	}
	return false
}

// claimed is a session whose ticket was just used.
type claimed struct {
	id, projectID, jobID, userID string
	reservationMB                int
}

// claimTicket uses the ticket of session id for user userID once: unknown, used, expired, someone else's or an ended
// session's ticket is transcription-ticket-invalid.
func (s *Service) claimTicket(ctx context.Context, id, ticket, userID string) (claimed, error) {
	var c claimed
	invalid := problems.TranscriptionTicketInvalid.New("the ticket of %s is unknown, already used or expired (tickets are single-use and live %d s); open a new session with transcriptions.new",
		id, s.defaults().Transcriptions.TicketTTLSeconds.Value)
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var (
			hash    *string
			expires *time.Time
			state   string
			jobID   *string
		)
		err := tx.QueryRow(ctx, `SELECT project_id, user_id, coalesce(job_id, ''), reservation_mb, ticket_hash, ticket_expires_at, state, job_id
			FROM transcriptions WHERE id = $1 FOR UPDATE`, id).
			Scan(&c.projectID, &c.userID, &c.jobID, &c.reservationMB, &hash, &expires, &state, &jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return invalid
		}
		if err != nil {
			return fmt.Errorf("read the session: %w", err)
		}
		if c.userID != userID {
			return problems.Forbidden.New("session %s belongs to someone else", id)
		}
		if hash == nil || state == StateEnded || expires == nil || !s.now().Before(*expires) ||
			hashToken(ticket) != *hash {
			return invalid
		}
		_, err = tx.Exec(ctx, "UPDATE transcriptions SET ticket_hash = NULL WHERE id = $1", id)
		return err
	})
	c.id = id
	return c, err
}

// CheckLiveToken reports whether token is the live token of job jobID's current lease (the worker's dial).
func (s *Service) CheckLiveToken(ctx context.Context, jobID, token string) error {
	var hash *string
	err := s.Pool.QueryRow(ctx, "SELECT live_token_hash FROM transcriptions WHERE job_id = $1 AND state <> 'ended'", jobID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return problems.LeaseEnded.New("job %s serves no open transcription session", jobID)
	}
	if err != nil {
		return fmt.Errorf("read the session: %w", err)
	}
	if hash == nil || token == "" || hashToken(token) != *hash {
		return problems.Forbidden.New("the live token does not belong to job %s's lease", jobID)
	}
	return nil
}

// SessionOfJob is the open session job jobID serves (lease-ended when there is none).
func (s *Service) SessionOfJob(ctx context.Context, jobID string) (string, error) {
	var id string
	err := s.Pool.QueryRow(ctx, "SELECT id FROM transcriptions WHERE job_id = $1 AND state <> 'ended'", jobID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problems.LeaseEnded.New("job %s serves no open transcription session", jobID)
	}
	if err != nil {
		return "", fmt.Errorf("read the session: %w", err)
	}
	return id, nil
}
