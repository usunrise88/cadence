package modelexports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// ExportPipeline names the generated export pipeline (an inline source).
const ExportPipeline = "model-export"

// PrimaryProfile in deploy.export_profiles stands for the project's primary profile (eval.primary_profile, or its
// gates.yaml primaryProfile).
const PrimaryProfile = "primary"

// Plan item actions.
const (
	ActionExport    = "export"
	ActionExported  = "exported"
	ActionExporting = "exporting"
)

// ExportInput is a models.export request.
type ExportInput struct {
	ProjectID string
	Actor     auth.Actor
	Version   string
	Profiles  []string
	Format    string
	Priority  int
}

// ExportPlanItem is one profile of an export plan (the contract's ModelExportPlanItem).
type ExportPlanItem struct {
	Profile        string `json:"profile"`
	Action         string `json:"action"`
	ExportID       string `json:"exportId,omitempty"`
	Step           string `json:"step,omitempty"`
	DeployableHash string `json:"deployableHash,omitempty"`
}

// ExportPlan is a models.export request checked and planned (the contract's ModelExportPlan); StartExport starts it.
type ExportPlan struct {
	ModelVersionID  string              `json:"modelVersionId"`
	ModelVersion    string              `json:"modelVersion"`
	Family          string              `json:"family"`
	Format          string              `json:"format"`
	Kind            string              `json:"kind"`
	Profiles        []ExportPlanItem    `json:"profiles"`
	StagingTargetID string              `json:"stagingTargetId,omitempty"`
	Estimate        pipelines.Estimate  `json:"estimate"`
	Warnings        []pipelines.Warning `json:"warnings"`
	PipelineRun     *pipelines.Run      `json:"pipelineRun,omitempty"`
	Exports         []View              `json:"exports,omitempty"`

	start *pipelines.StartInput // nil when nothing is left to export
}

// Started reports whether the plan has a run to start.
func (pl ExportPlan) Started() bool { return pl.start != nil }

// GPUHours is the plan's GPU estimate for the policy.
func (pl ExportPlan) GPUHours() float64 { return gpuHours(pl.Estimate) }

// Unknown reports a GPU step without an estimate (the policy then asks a person).
func (pl ExportPlan) Unknown() bool { return pl.Estimate.UnknownGPU }

func gpuHours(e pipelines.Estimate) float64 {
	if e.GPUHours == nil {
		return 0
	}
	return *e.GPUHours
}

// ExportFormat is one deployable format a family's export role writes (descriptor exportFormats, data).
type ExportFormat struct {
	Format  string `json:"format"`
	Server  string `json:"server,omitempty"`
	Default bool   `json:"default,omitempty"`
}

// formats reads the family descriptor's exportFormats.
func formats(descriptor json.RawMessage) []ExportFormat {
	var d struct {
		ExportFormats []ExportFormat `json:"exportFormats"`
	}
	_ = json.Unmarshal(descriptor, &d)
	return d.ExportFormats
}

// chooseFormat is the request's format when the family writes it, else the family's default (else its first).
func chooseFormat(m evals.DeployModel, want string) (string, error) {
	fs := formats(m.Descriptor)
	if len(fs) == 0 {
		return "", problems.FamilyUnavailable.New("model family %s declares no export format (exportFormats); its pack cannot export", m.Family.Name)
	}
	want = strings.TrimSpace(want)
	var names []string
	def := fs[0].Format
	for _, f := range fs {
		names = append(names, f.Format)
		if f.Default {
			def = f.Format
		}
	}
	if want == "" {
		return def, nil
	}
	if !slices.Contains(names, want) {
		return "", problems.Validation([]problems.FieldError{{Path: "/format",
			Message: fmt.Sprintf("model family %s exports %s, not %s", m.Family.Name, strings.Join(names, ", "), want)}})
	}
	return want, nil
}

// resolveProfile maps a requested profile ("primary" or a family profile name) to the family's profile.
func (s *Service) resolveProfile(ctx context.Context, p projects.Project, m evals.DeployModel, name, path string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || name == PrimaryProfile {
		prim, err := s.Evals.PrimaryProfile(ctx, p.Slug)
		if err != nil {
			return "", err
		}
		name = prim
	}
	var have []string
	for _, pr := range m.Profiles {
		if pr.Name == name {
			return name, nil
		}
		have = append(have, pr.Name)
	}
	return "", problems.Validation([]problems.FieldError{{Path: path,
		Message: fmt.Sprintf("model family %s has no latency profile %s (it has %s)", m.Family.Name, name, strings.Join(have, ", "))}})
}

// StagingTarget is the staging deployment target the checks run through and the export builds its engine for:
// serving.staging_target's name when it is an active staging target, else the first active one.
func (s *Service) StagingTarget(ctx context.Context, q storage.Querier) (targets.Target, bool, error) {
	if name := s.defaults().Serving.StagingTarget.Value.Name; name != "" {
		t, err := targets.Get(ctx, q, name)
		if err == nil && t.Kind == targets.KindStaging && t.State == targets.StateActive {
			return t, true, nil
		}
		if pe, ok := problems.As(err); err != nil && (!ok || pe.Type != problems.NotFound) {
			return targets.Target{}, false, err
		}
	}
	list, err := targets.List(ctx, q, false)
	if err != nil {
		return targets.Target{}, false, err
	}
	for _, t := range list {
		if t.Kind == targets.KindStaging && t.State == targets.StateActive {
			return t, true, nil
		}
	}
	return targets.Target{}, false, nil
}

// PrepareExport resolves the model version, the format and the profiles, answers the profiles already exported (or
// exporting) as they are and plans one export step per remaining profile; it writes nothing.
func (s *Service) PrepareExport(ctx context.Context, q storage.Querier, in ExportInput) (ExportPlan, error) {
	p, err := projects.GetByID(ctx, q, in.ProjectID)
	if err != nil {
		return ExportPlan{}, err
	}
	m, err := s.Evals.ResolveModelVersion(ctx, q, p.ID, in.Version)
	if err != nil {
		return ExportPlan{}, err
	}
	format, err := chooseFormat(m, in.Format)
	if err != nil {
		return ExportPlan{}, err
	}
	name, err := m.Family.Role(RoleExport)
	if err != nil {
		return ExportPlan{}, err
	}
	kind, err := runs.RoleKind(ctx, q, name)
	if err != nil {
		return ExportPlan{}, err
	}
	modelPort, ok := consumesType(kind, TypeCheckpoint)
	if _, out := producesType(kind, TypeDeployable); !ok || !out || len(kind.Consumes) != 1 {
		return ExportPlan{}, problems.RecipeMismatch.New("the export step kind %s must consume one checkpoint and produce a deployable (it consumes %v, produces %v)",
			kind.Ref(), kind.Consumes, kind.Produces)
	}
	requested := in.Profiles
	if len(requested) == 0 {
		requested = s.defaults().Deploy.ExportProfiles.Value
	}
	var profiles []string
	for i, r := range requested {
		pr, err := s.resolveProfile(ctx, p, m, r, fmt.Sprintf("/profiles/%d", i))
		if err != nil {
			return ExportPlan{}, err
		}
		if !slices.Contains(profiles, pr) {
			profiles = append(profiles, pr)
		}
	}
	pl := ExportPlan{ModelVersionID: m.Version.ID, ModelVersion: m.Label(), Family: m.Family.Name, Format: format, Kind: kind.Ref(),
		Profiles: []ExportPlanItem{}, Warnings: []pipelines.Warning{},
		Estimate: pipelines.Estimate{Known: true, UnknownSteps: []string{}}}
	staging, hasStaging, err := s.StagingTarget(ctx, q)
	if err != nil {
		return ExportPlan{}, err
	}
	if hasStaging {
		pl.StagingTargetID = staging.ID
	}
	p0 := pipelines.Pipeline{Name: ExportPipeline, Description: fmt.Sprintf("Export %s (%s) for serving (models.export)", m.Label(), format),
		Inputs: map[string]string{"model": TypeCheckpoint}}
	start := &pipelines.StartInput{ProjectID: p.ID, Pipeline: &p0, Inputs: map[string]steps.ArtifactRef{"model": m.Checkpoint},
		Params: map[string]map[string]any{}, Estimates: map[string]float64{}, Actor: in.Actor, Priority: in.Priority, Export: true}
	for _, pr := range profiles {
		x, found, err := Find(ctx, q, m.Version.ID, pr, format, false)
		if err != nil {
			return ExportPlan{}, err
		}
		item := ExportPlanItem{Profile: pr, Action: ActionExport}
		if found {
			item.ExportID = x.ID
			switch x.State {
			case StateExported:
				item.Action, item.DeployableHash = ActionExported, x.DeployableHash
			case StateExporting:
				// An export whose run ended without its deployable (cancelled, or the observer missed it) runs again.
				if live, err := runLive(ctx, q, x.PipelineRunID); err != nil {
					return ExportPlan{}, err
				} else if live {
					item.Action, item.Step = ActionExporting, x.Step
				}
			}
		}
		if item.Action == ActionExport {
			id := "export-" + stepSuffix(pr)
			item.Step = id
			params := map[string]any{}
			set := func(k string, v any) {
				if hasParam(kind, k) {
					params[k] = v
				}
			}
			set("profile", pr)
			set("format", format)
			if hasStaging {
				if staging.CardClass != "" {
					set("card_class", staging.CardClass)
				}
				if staging.Server.Version != "" {
					set("server_version", staging.Server.Version)
				}
			}
			p0.Steps = append(p0.Steps, pipelines.Step{ID: id, Kind: kind.Ref(), In: map[string]string{modelPort: pipelines.InputsRef + "model"}})
			start.Params[id] = params
			start.Estimates[id] = s.defaults().Deploy.ExportSeconds.Value
		}
		pl.Profiles = append(pl.Profiles, item)
	}
	if len(p0.Steps) == 0 {
		return pl, nil
	}
	_, plan, err := s.Engine.Prepare(ctx, q, *start)
	if err != nil {
		return ExportPlan{}, err
	}
	if err := refuseSkipped(plan); err != nil {
		return ExportPlan{}, err
	}
	pl.Estimate, pl.Warnings, pl.start = plan.Estimate, nonNilWarnings(plan.Warnings), start
	return pl, nil
}

// StartExport starts the planned export run in tx and records the exports it runs (exporting); a step the engine
// reused at once is recorded exported. A plan with nothing to export starts nothing.
func (s *Service) StartExport(ctx context.Context, tx pgx.Tx, pl ExportPlan, actor auth.Actor) (ExportPlan, []events.Draft, error) {
	if pl.start == nil {
		return pl, nil, nil
	}
	ids := map[string]string{} // profile → export id
	for i, it := range pl.Profiles {
		if it.Action != ActionExport {
			continue
		}
		x, found, err := Find(ctx, tx, pl.ModelVersionID, it.Profile, pl.Format, true)
		if err != nil {
			return ExportPlan{}, nil, err
		}
		live := false
		if found && x.State == StateExporting {
			if live, err = runLive(ctx, tx, x.PipelineRunID); err != nil {
				return ExportPlan{}, nil, err
			}
		}
		if found && (x.State == StateExported || live) { // another request got there first
			return ExportPlan{}, nil, problems.Conflict.New("the %s export of %s is already %s (%s); ask again to see it", it.Profile, pl.ModelVersion, x.State, x.ID)
		}
		id := x.ID
		if !found {
			id = "mex_" + uuid.Must(uuid.NewV7()).String()
		}
		ids[it.Profile] = id
		pl.Profiles[i].ExportID = id
	}
	in := *pl.start
	in.Actor = actor
	for _, it := range pl.Profiles {
		if it.Action == ActionExport {
			in.RunID = ids[it.Profile]
			break
		}
	}
	r, drafts, err := s.Engine.Start(ctx, tx, in)
	if err != nil {
		return ExportPlan{}, nil, err
	}
	for _, it := range pl.Profiles {
		if it.Action != ActionExport {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO model_exports (id, model_version_id, profile, format, state, project_id, pipeline_run_id,
				step, created_by)
			VALUES ($1, $2, $3, $4, 'exporting', $5, $6, $7, $8)
			ON CONFLICT (id) DO UPDATE SET state = 'exporting', project_id = $5, pipeline_run_id = $6, step = $7, error = NULL,
				deployable_hash = NULL, deployable = NULL, rev = model_exports.rev + 1, updated_at = now()`,
			ids[it.Profile], pl.ModelVersionID, it.Profile, pl.Format, in.ProjectID, r.ID, it.Step, actor); err != nil {
			return ExportPlan{}, nil, fmt.Errorf("record the %s export: %w", it.Profile, err)
		}
		x, err := Get(ctx, tx, ids[it.Profile])
		if err != nil {
			return ExportPlan{}, nil, err
		}
		drafts = append(drafts, draft(x, EventExported, map[string]any{"pipelineRunId": r.ID}))
	}
	// Steps the engine reused (the same weights and export kind already produced a deployable) are done already.
	more, err := s.applyRun(ctx, tx, r)
	if err != nil {
		return ExportPlan{}, nil, err
	}
	drafts = append(drafts, more...)
	pl.PipelineRun = &r
	if pl.Exports, err = Views(ctx, tx, pl.ModelVersionID); err != nil {
		return ExportPlan{}, nil, err
	}
	return pl, drafts, nil
}

// runLive reports whether pipeline run id is still running.
func runLive(ctx context.Context, q storage.Querier, id string) (bool, error) {
	if id == "" {
		return false, nil
	}
	var state string
	if err := q.QueryRow(ctx, "SELECT state FROM pipeline_runs WHERE id = $1", id).Scan(&state); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("read pipeline run %s: %w", id, err)
	}
	return state == pipelines.RunRunning, nil
}

// stepSuffix makes a profile name a step id suffix (lowercase letters, digits, -).
func stepSuffix(profile string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(profile) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// refuseSkipped refuses a plan the engine would run partly: a step whose kind no live worker publishes.
func refuseSkipped(plan pipelines.Plan) error {
	for _, w := range plan.Warnings {
		if w.Code == pipelines.WarningStepKindUnavailable {
			return problems.FamilyUnavailable.New("%s", w.Message)
		}
	}
	if len(plan.Skipped) > 0 {
		return problems.FamilyUnavailable.New("step %s (%s@%s) cannot run now: no live worker publishes it", plan.Skipped[0].Step,
			plan.Skipped[0].Name, plan.Skipped[0].Version)
	}
	return nil
}

func nonNilWarnings(w []pipelines.Warning) []pipelines.Warning {
	if w == nil {
		return []pipelines.Warning{}
	}
	return w
}

func hasParam(k pipelines.Kind, name string) bool {
	var sch struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(k.Params, &sch)
	_, ok := sch.Properties[name]
	return ok
}

func consumesType(k pipelines.Kind, typ string) (string, bool) {
	for _, name := range sortedKeys(k.Consumes) {
		if k.Consumes[name] == typ {
			return name, true
		}
	}
	return "", false
}

func producesType(k pipelines.Kind, typ string) (string, bool) {
	for _, name := range sortedKeys(k.Produces) {
		if k.Produces[name] == typ {
			return name, true
		}
	}
	return "", false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
