package deployments

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/notify"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

func newID(prefix string) string { return prefix + uuid.Must(uuid.NewV7()).String() }

// ShadowView is the contract's DeploymentShadow.
type ShadowView struct {
	Hours        float64     `json:"hours"`
	Calls        int         `json:"calls"`
	Utterances   int         `json:"utterances"`
	Nights       int         `json:"nights"`
	MinHours     float64     `json:"minHours"`
	Divergence   *Divergence `json:"divergence,omitempty"`
	Against      *Against    `json:"against,omitempty"`
	LastReplayAt *time.Time  `json:"lastReplayAt,omitempty"`
	NextReplayAt *time.Time  `json:"nextReplayAt,omitempty"`
}

// PendingView is the contract's DeploymentPending.
type PendingView struct {
	RecordID     string     `json:"recordId"`
	Kind         string     `json:"kind"`
	Stage        string     `json:"stage"`
	TargetID     string     `json:"targetId"`
	Slot         string     `json:"slot,omitempty"`
	TrafficShare *float64   `json:"trafficShare,omitempty"`
	ConfigOnly   bool       `json:"configOnly,omitempty"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
}

// View is the contract's Deployment.
type View struct {
	ID             string        `json:"id"`
	ProjectID      string        `json:"projectId"`
	ModelVersionID string        `json:"modelVersionId"`
	ModelVersion   string        `json:"modelVersion"`
	Family         string        `json:"family,omitempty"`
	ExportID       string        `json:"exportId"`
	Profile        string        `json:"profile"`
	Format         string        `json:"format"`
	DeployableHash string        `json:"deployableHash,omitempty"`
	TargetID       string        `json:"targetId"`
	TargetName     string        `json:"targetName"`
	Slot           string        `json:"slot,omitempty"`
	ModelName      string        `json:"modelName,omitempty"`
	Stage          string        `json:"stage"`
	State          string        `json:"state"`
	TrafficShare   *float64      `json:"trafficShare,omitempty"`
	Decoding       Decoding      `json:"decoding"`
	Replay         *ReplayConfig `json:"replay,omitempty"`
	Shadow         *ShadowView   `json:"shadow,omitempty"`
	Pending        *PendingView  `json:"pending,omitempty"`
	History        []Step        `json:"history"`
	Rev            int           `json:"rev"`
	CreatedBy      auth.Actor    `json:"createdBy"`
	CreatedAt      time.Time     `json:"createdAt"`
	UpdatedAt      time.Time     `json:"updatedAt"`
}

// View answers a deployment as the contract shows it, with its history and pending promotion.
func (s *Service) View(ctx context.Context, q storage.Querier, d Deployment) (View, error) {
	v := View{ID: d.ID, ProjectID: d.ProjectID, ModelVersionID: d.ModelVersionID, ExportID: d.ExportID, Profile: d.Profile,
		Format: d.Format, TargetID: d.TargetID, Slot: d.Slot, ModelName: d.ModelName, Stage: d.Stage, State: d.State,
		TrafficShare: d.TrafficShare, Decoding: d.Decoding.norm(), Replay: d.Replay, Rev: d.Rev, CreatedBy: d.CreatedBy,
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, ModelVersion: d.ModelVersionID, TargetName: d.TargetID}
	if mv, err := registry.GetVersion(ctx, q, registry.KindModel, d.ModelVersionID); err == nil {
		v.ModelVersion = mv.Name + " " + mv.Version
		var mp struct {
			FamilyID string `json:"familyId"`
		}
		if json.Unmarshal(mv.Payload, &mp) == nil {
			v.Family = mp.FamilyID
		}
	}
	if t, err := targets.Get(ctx, q, d.TargetID); err == nil {
		v.TargetName = t.Name
	}
	var hash string
	if err := q.QueryRow(ctx, `SELECT coalesce(deployable_hash, '') FROM model_exports WHERE id = $1`, d.ExportID).Scan(&hash); err == nil {
		v.DeployableHash = hash
	}
	if d.Replay != nil || d.Stage == StageShadow || d.Shadow.Nights > 0 {
		sv := &ShadowView{Hours: d.Shadow.Hours, Calls: d.Shadow.Calls, Utterances: d.Shadow.Utterances, Nights: d.Shadow.Nights,
			MinHours: s.defaults().Deploy.ShadowMinHours.Value, Divergence: d.Shadow.Divergence, Against: d.Against,
			LastReplayAt: d.Shadow.LastReplayAt}
		if d.Stage == StageShadow && d.Replay != nil && d.Live() {
			if next, err := s.nextReplay(ctx, q); err == nil {
				sv.NextReplayAt = &next
			}
		}
		v.Shadow = sv
	}
	hist, err := History(ctx, q, d.ID)
	if err != nil {
		return View{}, err
	}
	if hist == nil {
		hist = []Step{}
	}
	v.History = hist
	if d.PendingRecordID != "" {
		for _, st := range hist {
			if st.RecordID == d.PendingRecordID && (st.Kind == StepPromotion || st.Kind == StepRollback) {
				at := st.CreatedAt
				v.Pending = &PendingView{RecordID: st.RecordID, Kind: st.Kind, Stage: st.ToStage, TargetID: st.TargetID,
					Slot: st.Slot, TrafficShare: st.TrafficShare, CreatedAt: &at,
					ConfigOnly: st.Kind == StepPromotion && st.FromStage == st.ToStage}
			}
		}
	}
	return v, nil
}

// nextReplay is the next moment the nightly replay starts (deploy.shadow_replay_at in policies.timezone).
func (s *Service) nextReplay(ctx context.Context, q storage.Querier) (time.Time, error) {
	d := s.defaults()
	p, err := policies.Get(ctx, q, d)
	if err != nil {
		return time.Time{}, err
	}
	return notify.Next(s.now(), notify.MustClock(d.Deploy.ShadowReplayAt.Value), p.Location()).UTC(), nil
}
