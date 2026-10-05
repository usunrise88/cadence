// Package deployments is the deployment half of block 4 (phase 5 · stream D4; docs/spec/02-domain-projects-
// registry.md "Deployment entities", R30–R33, R46): a model version's export served as a shadow on the staging
// target and replayed every night against the comparison model (shadow.go), then promoted to a delivery target's slot
// as canary and production, or rolled back (promote.go). Promotions and rollbacks pass the checks of 02 first, then
// an approval for everyone, then a signed promotion record (internal/promotions) and its delivery bundle
// (internal/delivery) in the transaction that decides the approval; the stage moves only when promotions.verify
// confirms the record (Service.Confirm, the promotions.Stager) and returns when the record is withdrawn.
//
// Go never names a family, a server or its stream protocol (internal/contract/seams_test.go): the serve, transcribe
// and materialize roles come from the family descriptor, the scorer is the neutral shadow_score.
package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/delivery"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/modelexports"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/serving"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Operations, as the policy engine and the audit log name them.
const (
	OpNew       = "deployments.new"
	OpPromote   = "deployments.promote"
	OpRollback  = "deployments.rollback"
	OpReplayNew = "shadowReplays.new"
)

// Stages and states (02 "Deployments").
const (
	StageShadow     = "shadow"
	StageCanary     = "canary"
	StageProduction = "production"
	StageRetired    = "retired"

	StateActive          = "active"
	StatePendingDelivery = "pending-delivery"
	StateRolledBack      = "rolled-back"
	StateRetired         = "retired"
)

// Step kinds of a deployment's history (deployment_steps.kind).
const (
	StepCreated      = "created"
	StepPromotion    = "promotion"
	StepRollback     = "rollback"
	StepConfirmation = "confirmation"
	StepWithdrawal   = "withdrawal"
	StepRetired      = "retired"
	StepRestored     = "restored"
)

// EntityKind names deployments in events.
const EntityKind = "deployment"

// Event types: on Topic (deploy.{id}) and ShadowTopic (shadow.{id}).
const (
	EventCreated       = "deployment.created"
	EventStageChanged  = "deployment.stage_changed"
	EventShadowStarted = "shadow.started"
	EventShadowFailed  = "shadow.failed"
	EventShadowSkipped = "shadow.skipped"
	EventReplayed      = "shadow.replayed"
)

// Topic is a deployment's topic (06 "Topic scheme"): stage changes and promotion records.
func Topic(id string) string { return promotions.DeployTopic(id) }

// ShadowTopic is the topic of a deployment's shadow replays.
func ShadowTopic(id string) string { return "shadow." + id }

// MountOpener opens a mount's reader on the control plane (mounts.Open with the server's secrets and client).
type MountOpener func(ctx context.Context, m mounts.Mount) (mounts.Reader, error)

// Evictor queues the eviction of artifacts in tx (the eviction service's job, as the system actor).
type Evictor func(ctx context.Context, tx pgx.Tx, hashes []string) ([]events.Draft, error)

// Service creates, promotes and replays deployments.
type Service struct {
	Pool       *pgxpool.Pool
	CAS        *cas.Store
	Engine     *pipelines.Engine
	Exports    *modelexports.Service
	Evals      *evals.Service
	Promotions *promotions.Service
	Delivery   *delivery.Service
	Serving    *serving.Service
	Repo       pipelines.Repo // the project repository (boost lists)
	OpenMount  MountOpener
	Evict      Evictor
	Defaults   func() *defaults.Defaults
	Log        *slog.Logger
	// Now is the clock (tests); time.Now when nil.
	Now func() time.Time
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// BoostList is one static boost list a deployment ships as decoding configuration (03 "Hot words").
type BoostList struct {
	Locale string  `json:"locale"`
	Domain string  `json:"domain"`
	Hash   string  `json:"hash,omitempty"` // the boost_list artifact (b3:…)
	SHA256 string  `json:"sha256"`
	Weight float64 `json:"weight"`
	Terms  int     `json:"terms,omitempty"`
	Commit string  `json:"commit,omitempty"`
}

// Decoding is a deployment's decoding configuration.
type Decoding struct {
	BoostLists []BoostList `json:"boostLists"`
}

func (d Decoding) norm() Decoding {
	if d.BoostLists == nil {
		d.BoostLists = []BoostList{}
	}
	return d
}

// same reports whether two configurations ship the same lists (by content and weight, in order).
func (d Decoding) same(o Decoding) bool {
	if len(d.BoostLists) != len(o.BoostLists) {
		return false
	}
	for i := range d.BoostLists {
		a, b := d.BoostLists[i], o.BoostLists[i]
		if a.Locale != b.Locale || a.Domain != b.Domain || a.SHA256 != b.SHA256 || a.Weight != b.Weight {
			return false
		}
	}
	return true
}

// ReplayConfig is where a shadow deployment's calls come from.
type ReplayConfig struct {
	Mount        string   `json:"mount"`
	Path         string   `json:"path,omitempty"`
	Source       string   `json:"source"`
	Language     string   `json:"language,omitempty"`
	ChannelRoles []string `json:"channelRoles,omitempty"`
}

// Against is the model a shadow is compared with.
type Against struct {
	Kind         string `json:"kind"` // model | base_model
	VersionID    string `json:"versionId"`
	Label        string `json:"label"`
	DeploymentID string `json:"deploymentId,omitempty"`
}

// Divergence is a night's (or the newest night's) divergence between the two models.
type Divergence struct {
	WER   float64   `json:"wer"`
	CI    []float64 `json:"ci,omitempty"`
	Level float64   `json:"level,omitempty"`
}

// Totals are a shadow deployment's running totals (deployments.shadow).
type Totals struct {
	Hours        float64     `json:"hours"`
	Calls        int         `json:"calls"`
	Utterances   int         `json:"utterances"`
	Nights       int         `json:"nights"`
	Divergence   *Divergence `json:"divergence,omitempty"`
	LastReplayAt *time.Time  `json:"lastReplayAt,omitempty"`
}

// Deployment is a deployments row.
type Deployment struct {
	ID              string
	ProjectID       string
	ModelVersionID  string
	ExportID        string
	Profile         string
	Format          string
	TargetID        string
	Slot            string
	ModelName       string
	Stage           string
	State           string
	TrafficShare    *float64
	Decoding        Decoding
	Replay          *ReplayConfig
	Against         *Against
	Shadow          Totals
	PendingRecordID string
	Rev             int
	CreatedBy       auth.Actor
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Live reports a deployment that still serves or waits to (active or pending delivery).
func (d Deployment) Live() bool { return d.State == StateActive || d.State == StatePendingDelivery }

const cols = `id, project_id, model_version_id, export_id, profile, format, target_id, coalesce(slot, ''),
	coalesce(model_name, ''), stage, state, traffic_share, decoding, replay, against, shadow,
	coalesce(pending_record_id, ''), rev, created_by, created_at, updated_at`

func scan(row pgx.CollectableRow) (Deployment, error) {
	var (
		d                       Deployment
		dec, rep, against, shad []byte
	)
	if err := row.Scan(&d.ID, &d.ProjectID, &d.ModelVersionID, &d.ExportID, &d.Profile, &d.Format, &d.TargetID, &d.Slot,
		&d.ModelName, &d.Stage, &d.State, &d.TrafficShare, &dec, &rep, &against, &shad, &d.PendingRecordID, &d.Rev,
		&d.CreatedBy, &d.CreatedAt, &d.UpdatedAt); err != nil {
		return d, err
	}
	_ = json.Unmarshal(dec, &d.Decoding)
	d.Decoding = d.Decoding.norm()
	if len(rep) > 0 && string(rep) != "null" {
		d.Replay = &ReplayConfig{}
		_ = json.Unmarshal(rep, d.Replay)
	}
	if len(against) > 0 && string(against) != "null" {
		d.Against = &Against{}
		_ = json.Unmarshal(against, d.Against)
	}
	_ = json.Unmarshal(shad, &d.Shadow)
	return d, nil
}

func query(ctx context.Context, q storage.Querier, where string, args ...any) ([]Deployment, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM deployments WHERE "+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read deployments: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("read deployments: %w", err)
	}
	return out, nil
}

// Get reads one deployment.
func Get(ctx context.Context, q storage.Querier, id string) (Deployment, error) {
	return get(ctx, q, id, "")
}

// Lock reads one deployment for update.
func Lock(ctx context.Context, tx pgx.Tx, id string) (Deployment, error) {
	return get(ctx, tx, id, " FOR UPDATE")
}

func get(ctx context.Context, q storage.Querier, id, lock string) (Deployment, error) {
	list, err := query(ctx, q, "id = $1"+lock, id)
	if err != nil {
		return Deployment{}, err
	}
	if len(list) == 0 {
		return Deployment{}, problems.NotFound.New("no deployment %q", id)
	}
	return list[0], nil
}

// Filter narrows List.
type Filter struct {
	ModelVersionID string
	Stage          string
	All            bool // retired and rolled back too
}

// List reads a project's deployments, newest first.
func List(ctx context.Context, q storage.Querier, projectID string, f Filter) ([]Deployment, error) {
	return query(ctx, q, `project_id = $1 AND ($2 = '' OR model_version_id = $2) AND ($3 = '' OR stage = $3)
		AND ($4 OR state IN ('active', 'pending-delivery')) ORDER BY created_at DESC, id DESC`,
		projectID, f.ModelVersionID, f.Stage, f.All)
}

// Step is one row of a deployment's history.
type Step struct {
	ID           string     `json:"-"`
	Kind         string     `json:"kind"`
	FromStage    string     `json:"fromStage,omitempty"`
	ToStage      string     `json:"toStage,omitempty"`
	RecordID     string     `json:"recordId,omitempty"`
	TargetID     string     `json:"targetId,omitempty"`
	Slot         string     `json:"slot,omitempty"`
	TrafficShare *float64   `json:"trafficShare,omitempty"`
	Decoding     *Decoding  `json:"decoding,omitempty"`
	OtherID      string     `json:"-"`
	Reason       string     `json:"reason,omitempty"`
	ApprovalID   string     `json:"approvalId,omitempty"`
	Actor        auth.Actor `json:"actor"`
	CreatedAt    time.Time  `json:"createdAt"`
}

const stepCols = `id, kind, coalesce(from_stage, ''), coalesce(to_stage, ''), coalesce(record_id, ''), coalesce(target_id, ''),
	coalesce(slot, ''), traffic_share, decoding, coalesce(other_id, ''), reason, coalesce(approval_id, ''), actor, created_at`

func scanStep(row pgx.CollectableRow) (Step, error) {
	var (
		st  Step
		dec []byte
	)
	if err := row.Scan(&st.ID, &st.Kind, &st.FromStage, &st.ToStage, &st.RecordID, &st.TargetID, &st.Slot, &st.TrafficShare,
		&dec, &st.OtherID, &st.Reason, &st.ApprovalID, &st.Actor, &st.CreatedAt); err != nil {
		return st, err
	}
	if len(dec) > 0 && string(dec) != "null" {
		st.Decoding = &Decoding{}
		_ = json.Unmarshal(dec, st.Decoding)
		*st.Decoding = st.Decoding.norm()
	}
	return st, nil
}

// History reads a deployment's steps, oldest first.
func History(ctx context.Context, q storage.Querier, id string) ([]Step, error) {
	rows, err := q.Query(ctx, "SELECT "+stepCols+" FROM deployment_steps WHERE deployment_id = $1 ORDER BY created_at, id", id)
	if err != nil {
		return nil, fmt.Errorf("read deployment history: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanStep)
	if err != nil {
		return nil, fmt.Errorf("read deployment history: %w", err)
	}
	return out, nil
}

// stepOfRecord reads the step that appended record id (a promotion or a rollback).
func stepOfRecord(ctx context.Context, q storage.Querier, recordID string) (Step, string, bool, error) {
	var depID string
	rows, err := q.Query(ctx, "SELECT "+stepCols+", deployment_id FROM deployment_steps WHERE record_id = $1 AND kind IN ('promotion', 'rollback')", recordID)
	if err != nil {
		return Step{}, "", false, fmt.Errorf("read the step of record %s: %w", recordID, err)
	}
	defer rows.Close()
	if !rows.Next() {
		return Step{}, "", false, rows.Err()
	}
	var (
		st  Step
		dec []byte
	)
	if err := rows.Scan(&st.ID, &st.Kind, &st.FromStage, &st.ToStage, &st.RecordID, &st.TargetID, &st.Slot, &st.TrafficShare,
		&dec, &st.OtherID, &st.Reason, &st.ApprovalID, &st.Actor, &st.CreatedAt, &depID); err != nil {
		return Step{}, "", false, fmt.Errorf("read the step of record %s: %w", recordID, err)
	}
	if len(dec) > 0 && string(dec) != "null" {
		st.Decoding = &Decoding{}
		_ = json.Unmarshal(dec, st.Decoding)
		*st.Decoding = st.Decoding.norm()
	}
	return st, depID, true, nil
}

func (s *Service) addStep(ctx context.Context, tx pgx.Tx, depID string, st Step) error {
	var dec any
	if st.Decoding != nil {
		b, err := json.Marshal(st.Decoding.norm())
		if err != nil {
			return err
		}
		dec = b
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deployment_steps (id, deployment_id, kind, from_stage, to_stage, record_id, target_id,
			slot, traffic_share, decoding, other_id, reason, approval_id, actor, created_at)
		VALUES ($1, $2, $3, nullif($4, ''), nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''), $9, $10,
			nullif($11, ''), $12, nullif($13, ''), $14, $15)`,
		newID("dst_"), depID, st.Kind, st.FromStage, st.ToStage, st.RecordID, st.TargetID, st.Slot, st.TrafficShare, dec,
		st.OtherID, st.Reason, st.ApprovalID, st.Actor, s.now()); err != nil {
		return fmt.Errorf("record the %s step of %s: %w", st.Kind, depID, err)
	}
	return nil
}

func draft(d Deployment, typ string, payload map[string]any) events.Draft {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["id"], payload["stage"], payload["state"], payload["rev"] = d.ID, d.Stage, d.State, d.Rev
	if d.Slot != "" {
		payload["slot"] = d.Slot
	}
	return events.Draft{Topic: Topic(d.ID), Type: typ, ProjectID: d.ProjectID,
		Entity: &events.EntityRef{Kind: EntityKind, ID: d.ID, Rev: d.Rev}, Payload: payload}
}

func isNotFound(err error) bool {
	pe, ok := problems.As(err)
	return ok && pe.Type == problems.NotFound
}

var errNoRows = pgx.ErrNoRows

func noRows(err error) bool { return errors.Is(err, errNoRows) }
