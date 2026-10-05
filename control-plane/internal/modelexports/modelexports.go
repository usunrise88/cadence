// Package modelexports exports registered model versions for serving and checks the exports (phase 5 · stream D1;
// docs/spec/02-domain-projects-registry.md "Deployment entities", 03 "Export, parity and benchmark (phase 5)", spike
// E1). A model version never changes: an export attaches a deployable to it, one per (version, latency profile,
// format), and its parity checks and benchmarks attach to the export. Each operation runs a pipeline generated per
// request, never written into the project repository:
//
//   - models.export: the family's export role, one step per profile (job kind export) → a deployable artifact
//     (cadence.deployable/1), recorded on the export by the deployable hook;
//   - models.parity: the family's parity-reference role ‖ the family's serve role through the staging target (run as
//     job kind eval) → parity_score@1 → a parity_report (cadence.parity/1);
//   - models.benchmark: the serve role at each concurrency level in turn (job kind benchmark: the card alone) →
//     benchmark_score@1 → a benchmark_report (cadence.benchmark/1).
//
// Go never names a family, a server or its stream protocol (internal/contract/seams_test.go): kinds are found by role
// in the family descriptor, ports by artifact type, export formats are the descriptor's data.
package modelexports

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Operations, as the policy engine and the audit log name them.
const (
	OpExport    = "models.export"
	OpParity    = "models.parity"
	OpBenchmark = "models.benchmark"
)

// Artifact types (03 "Export, parity and benchmark (phase 5)").
const (
	TypeDeployable      = "deployable"
	TypeServingTimings  = "serving_timings"
	TypeParityReport    = "parity_report"
	TypeBenchmarkReport = "benchmark_report"
	TypeSmokeInputs     = "smoke_inputs"
	TypeCheckpoint      = "checkpoint"
	TypeDataset         = "dataset"
	TypeHypotheses      = "hypotheses"
	TypeNormalizer      = "normalizer"
)

// Family roles phase 5 fills (R41).
const (
	RoleExport = "export"
	RoleParity = "parity" // the parity reference: the family's own decoder at the eval's batch
	RoleServe  = "serve"
)

// The neutral kinds that judge (CPU, every runtime).
const (
	ParityScorer    = "parity_score"
	BenchmarkScorer = "benchmark_score"
)

// Export states.
const (
	StateExporting = "exporting"
	StateExported  = "exported"
	StateFailed    = "failed"
)

// Check kinds and states.
const (
	CheckParity    = "parity"
	CheckBenchmark = "benchmark"

	CheckRunning = "running"
	CheckDone    = "done"
	CheckFailed  = "failed"
)

// EntityKind is the entity whose topic carries the export events: entity.model.{model version id}.
const EntityKind = "model"

// Event types on Topic(versionID).
const (
	EventExported      = "model.exported"
	EventParityChecked = "model.parity_checked"
	EventBenchmarked   = "model.benchmarked"
)

// Topic is the topic of a model version's export events.
func Topic(versionID string) string { return events.EntityTopic(EntityKind, versionID) }

// Problem types of this package (each with its help article in docs/help/errors/).
var (
	// ExportMissing: parity or a benchmark of a profile and format the model version has no exported deployable of.
	ExportMissing = problems.ExportMissing
)

// Service plans and starts exports, parity checks and benchmarks, and records what their steps produce.
type Service struct {
	Engine   *pipelines.Engine
	CAS      *cas.Store
	Evals    *evals.Service
	Defaults func() *defaults.Defaults // defaults.Get when nil
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

// Export is a model_exports row.
type Export struct {
	ID             string
	ModelVersionID string
	Profile        string
	Format         string
	State          string
	DeployableHash string
	Deployable     json.RawMessage
	ProjectID      string
	PipelineRunID  string
	Step           string
	Error          string
	Rev            int
	CreatedBy      auth.Actor
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const exportCols = `id, model_version_id, profile, format, state, coalesce(deployable_hash, ''), deployable, project_id,
	coalesce(pipeline_run_id, ''), step, coalesce(error, ''), rev, created_by, created_at, updated_at`

func scanExport(row pgx.CollectableRow) (Export, error) {
	var x Export
	err := row.Scan(&x.ID, &x.ModelVersionID, &x.Profile, &x.Format, &x.State, &x.DeployableHash, &x.Deployable, &x.ProjectID,
		&x.PipelineRunID, &x.Step, &x.Error, &x.Rev, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
	return x, err
}

func queryExports(ctx context.Context, q storage.Querier, where string, args ...any) ([]Export, error) {
	rows, err := q.Query(ctx, "SELECT "+exportCols+" FROM model_exports WHERE "+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read model exports: %w", err)
	}
	return pgx.CollectRows(rows, scanExport)
}

// Find returns the export of (version, profile, format) and whether it exists; lock takes the row for update.
func Find(ctx context.Context, q storage.Querier, versionID, profile, format string, lock bool) (Export, bool, error) {
	where := "model_version_id = $1 AND profile = $2 AND format = $3"
	if lock {
		where += " FOR UPDATE"
	}
	list, err := queryExports(ctx, q, where, versionID, profile, format)
	if err != nil || len(list) == 0 {
		return Export{}, false, err
	}
	return list[0], true, nil
}

// Get returns one export by id.
func Get(ctx context.Context, q storage.Querier, id string) (Export, error) {
	list, err := queryExports(ctx, q, "id = $1", id)
	if err != nil {
		return Export{}, err
	}
	if len(list) == 0 {
		return Export{}, problems.NotFound.New("no model export %q", id)
	}
	return list[0], nil
}

// Check is a model_export_checks row: one parity check or benchmark.
type Check struct {
	ID              string
	ExportID        string
	Kind            string
	ProjectID       string
	PipelineRunID   string
	State           string
	TargetID        string
	StagingTargetID string
	Request         json.RawMessage
	Result          json.RawMessage
	ReportHash      string
	Error           string
	CreatedBy       auth.Actor
	CreatedAt       time.Time
	FinishedAt      *time.Time
}

const checkCols = `id, export_id, kind, project_id, pipeline_run_id, state, coalesce(target_id, ''),
	coalesce(staging_target_id, ''), request, result, coalesce(report_hash, ''), coalesce(error, ''), created_by, created_at,
	finished_at`

func scanCheck(row pgx.CollectableRow) (Check, error) {
	var c Check
	err := row.Scan(&c.ID, &c.ExportID, &c.Kind, &c.ProjectID, &c.PipelineRunID, &c.State, &c.TargetID, &c.StagingTargetID,
		&c.Request, &c.Result, &c.ReportHash, &c.Error, &c.CreatedBy, &c.CreatedAt, &c.FinishedAt)
	return c, err
}

func queryChecks(ctx context.Context, q storage.Querier, where string, args ...any) ([]Check, error) {
	rows, err := q.Query(ctx, "SELECT "+checkCols+" FROM model_export_checks WHERE "+where, args...)
	if err != nil {
		return nil, fmt.Errorf("read model export checks: %w", err)
	}
	return pgx.CollectRows(rows, scanCheck)
}

// checkByRun returns the check a pipeline run belongs to.
func checkByRun(ctx context.Context, q storage.Querier, runID string, lock bool) (Check, bool, error) {
	where := "pipeline_run_id = $1"
	if lock {
		where += " FOR UPDATE"
	}
	list, err := queryChecks(ctx, q, where, runID)
	if err != nil || len(list) == 0 {
		return Check{}, false, err
	}
	return list[0], true, nil
}

// ---------------------------------------------------------------- views (the contract's ModelExport)

// ParityView is the contract's ModelExportParity: the export's newest parity check.
type ParityView struct {
	State          string        `json:"state"`
	PipelineRunID  string        `json:"pipelineRunId,omitempty"`
	ReportHash     string        `json:"reportHash,omitempty"`
	WERDelta       *float64      `json:"werDelta,omitempty"`
	IdenticalShare *float64      `json:"identicalShare,omitempty"`
	Disagreement   *float64      `json:"disagreement,omitempty"`
	Compared       string        `json:"compared,omitempty"`
	Utterances     *int          `json:"utterances,omitempty"`
	Reasons        []string      `json:"reasons,omitempty"`
	Sample         *SampleRef    `json:"sample,omitempty"`
	ServedBy       *ServedByView `json:"servedBy,omitempty"`
	Error          string        `json:"error,omitempty"`
	CheckedAt      *time.Time    `json:"checkedAt,omitempty"`
}

// SampleRef is the sample a check decoded.
type SampleRef struct {
	GoldenSetVersionID string `json:"goldenSetVersionId,omitempty"`
	DatasetHash        string `json:"datasetHash,omitempty"`
	Utterances         int    `json:"utterances,omitempty"`
	Selection          string `json:"selection,omitempty"`
}

// ServedByView is the staging server a parity check decoded through.
type ServedByView struct {
	TargetID      string `json:"targetId,omitempty"`
	ServerVersion string `json:"serverVersion,omitempty"`
}

// BenchmarkView is the contract's ModelExportBenchmark.
type BenchmarkView struct {
	State                  string     `json:"state"`
	PipelineRunID          string     `json:"pipelineRunId,omitempty"`
	ReportHash             string     `json:"reportHash,omitempty"`
	TargetID               string     `json:"targetId,omitempty"`
	StagingTargetID        string     `json:"stagingTargetId,omitempty"`
	ServerVersion          string     `json:"serverVersion,omitempty"`
	CardClass              string     `json:"cardClass,omitempty"`
	Streams                int        `json:"streams"`
	Levels                 []int      `json:"levels,omitempty"`
	P95ChunkLatencyMs      *float64   `json:"p95ChunkLatencyMs,omitempty"`
	P95TimeToFinalMs       *float64   `json:"p95TimeToFinalMs,omitempty"`
	BudgetMs               float64    `json:"budgetMs"`
	MaxStreamsWithinBudget *int       `json:"maxStreamsWithinBudget,omitempty"`
	Contended              *bool      `json:"contended,omitempty"`
	Verdict                string     `json:"verdict,omitempty"`
	Error                  string     `json:"error,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	FinishedAt             *time.Time `json:"finishedAt,omitempty"`
}

// View is the contract's ModelExport.
type View struct {
	ID             string          `json:"id"`
	ModelVersionID string          `json:"modelVersionId"`
	Profile        string          `json:"profile"`
	Format         string          `json:"format"`
	State          string          `json:"state"`
	DeployableHash string          `json:"deployableHash,omitempty"`
	Deployable     json.RawMessage `json:"deployable,omitempty"`
	ProjectID      string          `json:"projectId"`
	PipelineRunID  string          `json:"pipelineRunId,omitempty"`
	Error          string          `json:"error,omitempty"`
	Parity         *ParityView     `json:"parity,omitempty"`
	Benchmarks     []BenchmarkView `json:"benchmarks"`
	Rev            int             `json:"rev"`
	CreatedBy      auth.Actor      `json:"createdBy"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
}

// parityRequest is what a parity check's request column holds.
type parityRequest struct {
	Sample        SampleRef `json:"sample"`
	ServerVersion string    `json:"serverVersion,omitempty"`
}

// benchmarkRequest is what a benchmark's request column holds.
type benchmarkRequest struct {
	Sample        SampleRef `json:"sample"`
	Streams       int       `json:"streams"`
	Levels        []int     `json:"levels"`
	BudgetMs      float64   `json:"budgetMs"`
	ServerVersion string    `json:"serverVersion,omitempty"`
	CardClass     string    `json:"cardClass,omitempty"`
}

func (c Check) parityView() ParityView {
	v := ParityView{PipelineRunID: c.PipelineRunID, ReportHash: c.ReportHash, Error: c.Error, CheckedAt: c.FinishedAt}
	var req parityRequest
	_ = json.Unmarshal(c.Request, &req)
	if req.Sample.DatasetHash != "" {
		s := req.Sample
		v.Sample = &s
	}
	if c.StagingTargetID != "" || req.ServerVersion != "" {
		v.ServedBy = &ServedByView{TargetID: c.StagingTargetID, ServerVersion: req.ServerVersion}
	}
	var res parityResult
	if len(c.Result) > 0 && json.Unmarshal(c.Result, &res) == nil {
		v.WERDelta, v.IdenticalShare, v.Disagreement, v.Compared = res.WERDelta, res.IdenticalShare, res.Disagreement, res.Compared
		v.Utterances, v.Reasons = res.Utterances, res.Reasons
	}
	switch {
	case c.State == CheckRunning:
		v.State = "pending"
	case c.State == CheckFailed:
		v.State = "failed"
	case res.Verdict == "passed":
		v.State = "passed"
	default:
		v.State = "failed"
	}
	return v
}

func (c Check) benchmarkView() BenchmarkView {
	var req benchmarkRequest
	_ = json.Unmarshal(c.Request, &req)
	v := BenchmarkView{State: c.State, PipelineRunID: c.PipelineRunID, ReportHash: c.ReportHash, TargetID: c.TargetID,
		StagingTargetID: c.StagingTargetID, ServerVersion: req.ServerVersion, CardClass: req.CardClass, Streams: req.Streams,
		Levels: req.Levels, BudgetMs: req.BudgetMs, Error: c.Error, CreatedAt: c.CreatedAt, FinishedAt: c.FinishedAt}
	var res benchmarkResult
	if len(c.Result) > 0 && json.Unmarshal(c.Result, &res) == nil {
		v.P95ChunkLatencyMs, v.P95TimeToFinalMs = res.P95ChunkLatencyMs, res.P95TimeToFinalMs
		v.MaxStreamsWithinBudget, v.Contended, v.Verdict = res.MaxStreamsWithinBudget, res.Contended, res.Verdict
		if res.CardClass != "" {
			v.CardClass = res.CardClass
		}
		if res.ServerVersion != "" {
			v.ServerVersion = res.ServerVersion
		}
	}
	return v
}

// Views returns the exports of a model version with their newest parity check and their benchmarks (newest first),
// ordered by profile and format: models.get's exports[].
func Views(ctx context.Context, q storage.Querier, versionID string) ([]View, error) {
	list, err := queryExports(ctx, q, "model_version_id = $1 ORDER BY profile, format", versionID)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(list))
	for _, x := range list {
		v, err := view(ctx, q, x)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func view(ctx context.Context, q storage.Querier, x Export) (View, error) {
	v := View{ID: x.ID, ModelVersionID: x.ModelVersionID, Profile: x.Profile, Format: x.Format, State: x.State,
		DeployableHash: x.DeployableHash, ProjectID: x.ProjectID, PipelineRunID: x.PipelineRunID, Error: x.Error,
		Benchmarks: []BenchmarkView{}, Rev: x.Rev, CreatedBy: x.CreatedBy, CreatedAt: x.CreatedAt, UpdatedAt: x.UpdatedAt}
	if len(x.Deployable) > 0 && string(x.Deployable) != "null" {
		v.Deployable = x.Deployable
	}
	checks, err := queryChecks(ctx, q, "export_id = $1 ORDER BY created_at DESC, id DESC", x.ID)
	if err != nil {
		return View{}, err
	}
	for _, c := range checks {
		switch c.Kind {
		case CheckParity:
			if v.Parity == nil {
				p := c.parityView()
				v.Parity = &p
			}
		case CheckBenchmark:
			v.Benchmarks = append(v.Benchmarks, c.benchmarkView())
		}
	}
	return v, nil
}

// ViewOf returns one export's view.
func ViewOf(ctx context.Context, q storage.Querier, id string) (View, error) {
	x, err := Get(ctx, q, id)
	if err != nil {
		return View{}, err
	}
	return view(ctx, q, x)
}

// LatestParity is the export's newest parity check that finished, with its parsed result (found false when none).
func LatestParity(ctx context.Context, q storage.Querier, exportID string) (Check, bool, error) {
	list, err := queryChecks(ctx, q, `export_id = $1 AND kind = 'parity' AND state = 'done' AND report_hash IS NOT NULL
		ORDER BY finished_at DESC, id DESC LIMIT 1`, exportID)
	if err != nil || len(list) == 0 {
		return Check{}, false, err
	}
	return list[0], true, nil
}

func draft(x Export, typ string, payload map[string]any) events.Draft {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["exportId"], payload["profile"], payload["format"], payload["state"] = x.ID, x.Profile, x.Format, x.State
	return events.Draft{Topic: Topic(x.ModelVersionID), Type: typ, Entity: &events.EntityRef{Kind: EntityKind, ID: x.ModelVersionID, Rev: x.Rev},
		Payload: payload}
}
