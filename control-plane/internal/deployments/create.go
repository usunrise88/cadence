package deployments

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/modelexports"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/serving"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// NewInput is a deployments.new request.
type NewInput struct {
	ProjectID string
	Version   string
	Profile   string
	Format    string
	Against   string
	Replay    ReplayConfig
	Actor     auth.Actor
}

// DefaultChannelRoles are a call's channel roles when neither the request nor a sidecar names them.
var DefaultChannelRoles = []string{"caller", "bot"}

// Prepare checks a deployments.new request and answers the shadow deployment it would create: an exported export of
// the model version, a staging target that serves it and is up, a replay mount, path and registered source, and the
// model it is compared with.
func (s *Service) Prepare(ctx context.Context, q storage.Querier, in NewInput) (Deployment, error) {
	m, x, found, err := s.Exports.ResolveExport(ctx, q, in.ProjectID, in.Version, in.Profile, in.Format)
	if err != nil {
		return Deployment{}, err
	}
	if !found || x.State != modelexports.StateExported {
		state := "no export"
		if found {
			state = "an export that is " + x.State
		}
		return Deployment{}, problems.ExportMissing.New("%s has %s at profile %s in format %s; export it first (models.export)",
			m.Label(), state, x.Profile, x.Format)
	}
	staging, err := s.Serving.Check(ctx, q, "", serving.Deployable{Family: m.Family.Name, Format: x.Format, Profile: x.Profile})
	if err != nil {
		return Deployment{}, err
	}
	rep, err := s.checkReplay(ctx, q, in.Replay)
	if err != nil {
		return Deployment{}, err
	}
	if rep.Language == "" {
		if set, err := s.Evals.SampleGoldenSet(ctx, q, in.ProjectID, "", m.Payload.EvalID); err == nil {
			rep.Language = set.Language
		}
	}
	against, err := s.resolveAgainst(ctx, q, in.ProjectID, in.Against, m.Version.ID)
	if err != nil {
		return Deployment{}, err
	}
	var dup string
	if err := q.QueryRow(ctx, `SELECT id FROM deployments WHERE project_id = $1 AND export_id = $2 AND stage = 'shadow'
		AND state IN ('active', 'pending-delivery') LIMIT 1`, in.ProjectID, x.ID).Scan(&dup); err == nil {
		return Deployment{}, problems.Conflict.New("%s at %s already has the shadow deployment %s; one shadow per export", m.Label(), x.Profile, dup)
	} else if !noRows(err) {
		return Deployment{}, fmt.Errorf("look up shadow deployments: %w", err)
	}
	now := s.now()
	return Deployment{ID: newID("dep_"), ProjectID: in.ProjectID, ModelVersionID: m.Version.ID, ExportID: x.ID, Profile: x.Profile,
		Format: x.Format, TargetID: staging.ID, Stage: StageShadow, State: StateActive, Decoding: Decoding{}.norm(),
		Replay: &rep, Against: &against, Rev: 1, CreatedBy: in.Actor, CreatedAt: now, UpdatedAt: now}, nil
}

// checkReplay checks the replay's mount, path and source ("no licence, no ingest").
func (s *Service) checkReplay(ctx context.Context, q storage.Querier, r ReplayConfig) (ReplayConfig, error) {
	var fields []problems.FieldError
	m, err := mounts.Get(ctx, q, strings.TrimSpace(r.Mount))
	if err != nil {
		if !isNotFound(err) {
			return ReplayConfig{}, err
		}
		fields = append(fields, problems.FieldError{Path: "/replay/mount", Message: fmt.Sprintf("no mount %q (mounts.list names them)", r.Mount)})
	}
	p := strings.Trim(strings.TrimSpace(r.Path), "/")
	if p != "" && (path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") || strings.ContainsAny(p, "#?*[")) {
		fields = append(fields, problems.FieldError{Path: "/replay/path", Message: fmt.Sprintf("%q is not a plain relative path under the mount", r.Path)})
	}
	if len(fields) > 0 {
		return ReplayConfig{}, problems.Validation(fields)
	}
	src, err := data.IngestAllowed(ctx, q, strings.TrimSpace(r.Source))
	if err != nil {
		return ReplayConfig{}, err
	}
	roles := r.ChannelRoles
	if len(roles) == 0 {
		roles = DefaultChannelRoles
	}
	return ReplayConfig{Mount: m.Name, Path: p, Source: src.Name, Language: strings.TrimSpace(r.Language), ChannelRoles: roles}, nil
}

// resolveAgainst finds the model a shadow is compared with: the request's, else the model of the project's newest
// production deployment, else the project's @baseline (else its default base model), never the model itself.
func (s *Service) resolveAgainst(ctx context.Context, q storage.Querier, projectID, ref, self string) (Against, error) {
	var a Against
	if strings.TrimSpace(ref) == "" {
		prods, err := query(ctx, q, `project_id = $1 AND stage = 'production' AND state IN ('active', 'pending-delivery')
			AND model_version_id <> $2 ORDER BY updated_at DESC LIMIT 1`, projectID, self)
		if err != nil {
			return a, err
		}
		if len(prods) > 0 {
			w, err := s.weightsOfVersion(ctx, q, prods[0].ModelVersionID)
			if err != nil {
				return a, err
			}
			return Against{Kind: w.Kind, VersionID: w.VersionID, Label: w.Label, DeploymentID: prods[0].ID}, nil
		}
	}
	w, err := s.Evals.ResolveWeights(ctx, q, projectID, ref)
	if err != nil {
		if pe, ok := problems.As(err); ok && pe.Type == problems.EvalBaselineMissing {
			return a, problems.Validation([]problems.FieldError{{Path: "/against",
				Message: "the project has no production deployment, no @baseline and no default base model: name the model to compare with"}})
		}
		return a, err
	}
	if w.VersionID == self {
		return a, problems.Validation([]problems.FieldError{{Path: "/against",
			Message: fmt.Sprintf("%s is the model this shadow deploys; compare it with another (production, @baseline or a base model)", w.Label)}})
	}
	return Against{Kind: w.Kind, VersionID: w.VersionID, Label: w.Label}, nil
}

func (s *Service) weightsOfVersion(ctx context.Context, q storage.Querier, versionID string) (evals.Weights, error) {
	v, err := registry.GetVersion(ctx, q, "", versionID)
	if err != nil {
		return evals.Weights{}, err
	}
	return s.Evals.WeightsOf(ctx, q, v)
}

// Create inserts a prepared shadow deployment with its first step.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, d Deployment) (Deployment, []events.Draft, error) {
	rep, err := json.Marshal(d.Replay)
	if err != nil {
		return Deployment{}, nil, err
	}
	ag, err := json.Marshal(d.Against)
	if err != nil {
		return Deployment{}, nil, err
	}
	dec, err := json.Marshal(d.Decoding.norm())
	if err != nil {
		return Deployment{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO deployments (id, project_id, model_version_id, export_id, profile, format, target_id,
			stage, state, decoding, replay, against, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'shadow', 'active', $8, $9, $10, $11, $12, $12)`,
		d.ID, d.ProjectID, d.ModelVersionID, d.ExportID, d.Profile, d.Format, d.TargetID, dec, rep, ag, d.CreatedBy, d.CreatedAt); err != nil {
		return Deployment{}, nil, fmt.Errorf("insert deployment: %w", err)
	}
	if err := s.addStep(ctx, tx, d.ID, Step{Kind: StepCreated, ToStage: StageShadow, TargetID: d.TargetID, Actor: d.CreatedBy}); err != nil {
		return Deployment{}, nil, err
	}
	out, err := Get(ctx, tx, d.ID)
	if err != nil {
		return Deployment{}, nil, err
	}
	payload := map[string]any{"modelVersionId": out.ModelVersionID, "exportId": out.ExportID, "profile": out.Profile, "targetId": out.TargetID}
	return out, []events.Draft{draft(out, EventCreated, payload)}, nil
}

// stagingOf answers the staging target that serves d's export: the deployment's own while it is a shadow, else the
// default staging target (serving.default_target) — a canary's export is still served on staging for manual tests.
func (s *Service) stagingOf(ctx context.Context, q storage.Querier, d Deployment, family string) (targets.Target, error) {
	ref := ""
	if d.Stage == StageShadow {
		ref = d.TargetID
	}
	return s.Serving.Check(ctx, q, ref, serving.Deployable{Family: family, Format: d.Format, Profile: d.Profile})
}
