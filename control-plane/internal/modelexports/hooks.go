package modelexports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Run ids of the pipeline runs this package starts (pipelines.StartInput.RunID): an export's (mex_…) and a check's
// (mxc_…). The observer recognises its runs by them.
const (
	exportRunPrefix = "mex_"
	checkRunPrefix  = "mxc_"
)

// Install registers the output hooks (a deployable records its export, a parity_report or benchmark_report its
// check) and the engine observer that fails an export or a check whose pipeline run ended without its output.
func (s *Service) Install(hooks *steps.Hooks) {
	hooks.On(TypeDeployable, s.onDeployable)
	hooks.On(TypeParityReport, s.onParityReport)
	hooks.On(TypeBenchmarkReport, s.onBenchmarkReport)
	if s.Engine != nil {
		s.Engine.SetNamedObserver("modelexports", s.Observe)
	}
}

func (s *Service) onDeployable(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	step, err := stepName(ctx, tx, out.StepID)
	if err != nil {
		return nil, err
	}
	return s.recordDeployable(ctx, tx, out.PipelineRunID, step, out.Artifact)
}

func (s *Service) onParityReport(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	return s.recordReport(ctx, tx, out.PipelineRunID, CheckParity, out.Artifact)
}

func (s *Service) onBenchmarkReport(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	return s.recordReport(ctx, tx, out.PipelineRunID, CheckBenchmark, out.Artifact)
}

func stepName(ctx context.Context, q storage.Querier, stepID string) (string, error) {
	var step string
	if err := q.QueryRow(ctx, "SELECT step FROM pipeline_steps WHERE id = $1", stepID).Scan(&step); errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("read pipeline step %s: %w", stepID, err)
	}
	return step, nil
}

// applyRun records what a run's finished steps produced and, when the run ended, its end (a run the engine
// finished at Start: every step reused).
func (s *Service) applyRun(ctx context.Context, tx pgx.Tx, r pipelines.Run) ([]events.Draft, error) {
	var drafts []events.Draft
	for _, st := range r.Steps {
		if st.State != pipelines.StepDone && st.State != pipelines.StepReused {
			continue
		}
		for _, name := range sortedKeys(st.Outputs) {
			ref := st.Outputs[name]
			var (
				d   []events.Draft
				err error
			)
			switch ref.Type {
			case TypeDeployable:
				d, err = s.recordDeployable(ctx, tx, r.ID, st.Step, ref)
			case TypeParityReport:
				d, err = s.recordReport(ctx, tx, r.ID, CheckParity, ref)
			case TypeBenchmarkReport:
				d, err = s.recordReport(ctx, tx, r.ID, CheckBenchmark, ref)
			}
			if err != nil {
				return nil, err
			}
			drafts = append(drafts, d...)
		}
	}
	more, err := s.Observe(ctx, tx, r)
	return append(drafts, more...), err
}

// deployableJSON is what the control plane reads of deployable.json (cadence.deployable/1): data, never interpreted
// beyond what the export, the delivery bundle and the promotion checks compare.
type deployableJSON struct {
	Schema         string          `json:"schema"`
	Format         string          `json:"format"`
	Family         string          `json:"family"`
	Profile        string          `json:"profile"`
	Precision      string          `json:"precision"`
	ManifestSHA256 string          `json:"manifestSha256"`
	Serving        json.RawMessage `json:"serving"`
}

// DeployableSummary is the contract's ModelExportDeployable, read from deployable.json.
type DeployableSummary struct {
	Format           string          `json:"format,omitempty"`
	Server           json.RawMessage `json:"server,omitempty"`
	Engine           json.RawMessage `json:"engine,omitempty"`
	ModelDir         string          `json:"modelDir,omitempty"`
	MemoryMb         *int            `json:"memoryMb,omitempty"`
	CudaMemoryPoolMb *int            `json:"cudaMemoryPoolMb,omitempty"`
	MaxStreams       *int            `json:"maxStreams,omitempty"`
	ChunkMs          *int            `json:"chunkMs,omitempty"`
	Precision        string          `json:"precision,omitempty"`
	ManifestSHA256   string          `json:"manifestSha256,omitempty"`
}

// ReadDeployable reads deployable.json of a deployable artifact.
func ReadDeployable(s *Service, hash string) (DeployableSummary, error) {
	b, err := s.dirFile(hash, "deployable.json", 16<<20)
	if err != nil {
		return DeployableSummary{}, err
	}
	var d deployableJSON
	if err := json.Unmarshal(b, &d); err != nil {
		return DeployableSummary{}, fmt.Errorf("deployable.json of %s: %w", hash, err)
	}
	var sv struct {
		Server           json.RawMessage `json:"server"`
		Engine           json.RawMessage `json:"engine"`
		ModelDir         string          `json:"modelDir"`
		MemoryMb         *int            `json:"memoryMb"`
		CudaMemoryPoolMb *int            `json:"cudaMemoryPoolMb"`
		MaxStreams       *int            `json:"maxStreams"`
		ChunkMs          *int            `json:"chunkMs"`
	}
	_ = json.Unmarshal(d.Serving, &sv)
	if strings.Trim(sv.ModelDir, "/") == "" {
		return DeployableSummary{}, fmt.Errorf("deployable.json of %s names no serving.modelDir", hash)
	}
	return DeployableSummary{Format: d.Format, Server: sv.Server, Engine: sv.Engine, ModelDir: sv.ModelDir, MemoryMb: sv.MemoryMb,
		CudaMemoryPoolMb: sv.CudaMemoryPoolMb, MaxStreams: sv.MaxStreams, ChunkMs: sv.ChunkMs, Precision: d.Precision,
		ManifestSHA256: d.ManifestSHA256}, nil
}

// dirFile reads one file of a directory artifact.
func (s *Service) dirFile(hash, path string, limit int64) ([]byte, error) {
	m, err := s.CAS.ReadManifest(hash)
	if err != nil {
		return nil, fmt.Errorf("%s is not a directory artifact: %w", hash, err)
	}
	for _, f := range m.Files {
		if f.Path == path {
			return readBlob(s.CAS, f.Hash, limit)
		}
	}
	return nil, fmt.Errorf("%s has no %s", hash, path)
}

// recordDeployable records the deployable an export step produced on its export (exported).
func (s *Service) recordDeployable(ctx context.Context, tx pgx.Tx, runID, step string, ref steps.ArtifactRef) ([]events.Draft, error) {
	list, err := queryExports(ctx, tx, "pipeline_run_id = $1 AND step = $2 FOR UPDATE", runID, step)
	if err != nil || len(list) == 0 {
		return nil, err // a deployable of a pipeline models.export did not start: nothing to record
	}
	x := list[0]
	if x.State == StateExported && x.DeployableHash == ref.Hash {
		return nil, nil
	}
	sum, err := ReadDeployable(s, ref.Hash)
	if err != nil {
		return nil, err
	}
	if sum.Format != "" && sum.Format != x.Format {
		return nil, fmt.Errorf("the deployable of export %s is %s, the export asked for %s", x.ID, sum.Format, x.Format)
	}
	b, err := json.Marshal(sum)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE model_exports SET state = 'exported', deployable_hash = $2, deployable = $3, error = NULL,
		rev = rev + 1, updated_at = now() WHERE id = $1`, x.ID, ref.Hash, b); err != nil {
		return nil, fmt.Errorf("record the deployable of export %s: %w", x.ID, err)
	}
	x, err = Get(ctx, tx, x.ID)
	if err != nil {
		return nil, err
	}
	return []events.Draft{draft(x, EventExported, map[string]any{"deployableHash": ref.Hash})}, nil
}

// parityResult is what the parity check keeps of a parity_report (cadence.parity/1 report.json).
type parityResult struct {
	Verdict        string   `json:"verdict"`
	WERDelta       *float64 `json:"werDelta,omitempty"`
	IdenticalShare *float64 `json:"identicalShare,omitempty"`
	Disagreement   *float64 `json:"disagreement,omitempty"`
	Compared       string   `json:"compared,omitempty"`
	Utterances     *int     `json:"utterances,omitempty"`
	Reasons        []string `json:"reasons,omitempty"`
}

// benchmarkResult is what a benchmark keeps of a benchmark_report (cadence.benchmark/1).
type benchmarkResult struct {
	Verdict                string   `json:"verdict"`
	P95ChunkLatencyMs      *float64 `json:"p95ChunkLatencyMs,omitempty"`
	P95TimeToFinalMs       *float64 `json:"p95TimeToFinalMs,omitempty"`
	MaxStreamsWithinBudget *int     `json:"maxStreamsWithinBudget,omitempty"`
	Contended              *bool    `json:"contended,omitempty"`
	CardClass              string   `json:"cardClass,omitempty"`
	ServerVersion          string   `json:"serverVersion,omitempty"`
}

// benchmarkReport is the part of cadence.benchmark/1 the result reads.
type benchmarkReport struct {
	TargetStreams          int    `json:"targetStreams"`
	MaxStreamsWithinBudget *int   `json:"maxStreamsWithinBudget"`
	Contended              *bool  `json:"contended"`
	CardClass              string `json:"cardClass"`
	Verdict                string `json:"verdict"`
	Server                 struct {
		Version string `json:"version"`
	} `json:"server"`
	Levels []struct {
		Streams        int `json:"streams"`
		ChunkLatencyMs struct {
			P95 *float64 `json:"p95"`
		} `json:"chunkLatencyMs"`
		TimeToFinalMs struct {
			P95 *float64 `json:"p95"`
		} `json:"timeToFinalMs"`
	} `json:"levels"`
}

// reportJSON reads a report artifact: report.json of a directory, or the blob itself.
func (s *Service) reportJSON(ref steps.ArtifactRef) ([]byte, error) {
	if _, err := s.CAS.ReadManifest(ref.Hash); err == nil {
		return s.dirFile(ref.Hash, "report.json", 64<<20)
	}
	return readBlob(s.CAS, ref.Hash, 64<<20)
}

// recordReport records a check's report (the check is done) and its summary.
func (s *Service) recordReport(ctx context.Context, tx pgx.Tx, runID, kind string, ref steps.ArtifactRef) ([]events.Draft, error) {
	c, found, err := checkByRun(ctx, tx, runID, true)
	if err != nil || !found || c.Kind != kind {
		return nil, err
	}
	if c.ReportHash == ref.Hash && c.State == CheckDone {
		return nil, nil
	}
	b, err := s.reportJSON(ref)
	if err != nil {
		return nil, err
	}
	var result any
	switch kind {
	case CheckParity:
		var r parityResult
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("parity report %s: %w", ref.Hash, err)
		}
		result = r
	default:
		var rep benchmarkReport
		if err := json.Unmarshal(b, &rep); err != nil {
			return nil, fmt.Errorf("benchmark report %s: %w", ref.Hash, err)
		}
		r := benchmarkResult{Verdict: rep.Verdict, MaxStreamsWithinBudget: rep.MaxStreamsWithinBudget, Contended: rep.Contended,
			CardClass: rep.CardClass, ServerVersion: rep.Server.Version}
		for _, l := range rep.Levels {
			if l.Streams == rep.TargetStreams {
				r.P95ChunkLatencyMs, r.P95TimeToFinalMs = l.ChunkLatencyMs.P95, l.TimeToFinalMs.P95
			}
		}
		result = r
	}
	rb, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE model_export_checks SET state = 'done', result = $2, report_hash = $3, error = NULL,
		finished_at = now() WHERE id = $1`, c.ID, rb, ref.Hash); err != nil {
		return nil, fmt.Errorf("record the %s report of %s: %w", kind, c.ID, err)
	}
	return s.checkDraft(ctx, tx, c.ID)
}

func (s *Service) checkDraft(ctx context.Context, tx pgx.Tx, checkID string) ([]events.Draft, error) {
	list, err := queryChecks(ctx, tx, "id = $1", checkID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	c := list[0]
	x, err := Get(ctx, tx, c.ExportID)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"checkId": c.ID, "pipelineRunId": c.PipelineRunID, "checkState": c.State}
	typ := EventParityChecked
	if c.Kind == CheckBenchmark {
		typ = EventBenchmarked
		payload["benchmark"] = c.benchmarkView()
	} else {
		payload["parity"] = c.parityView()
	}
	return []events.Draft{draft(x, typ, payload)}, nil
}

// Observe mirrors the end of an export's or a check's pipeline run (pipelines.RunObserver): a run that failed or was
// cancelled fails its exports still exporting and its check still running, with the run's error; a run done whose
// export step left no deployable (or whose scorer left no report) fails them too.
func (s *Service) Observe(ctx context.Context, tx pgx.Tx, r pipelines.Run) ([]events.Draft, error) {
	if r.State == pipelines.RunRunning {
		return nil, nil
	}
	msg := r.Error
	switch r.State {
	case pipelines.RunCancelled:
		msg = "cancelled: " + r.Error
	case pipelines.RunDone:
		msg = ""
	}
	switch {
	case strings.HasPrefix(r.RunID, exportRunPrefix):
		list, err := queryExports(ctx, tx, "pipeline_run_id = $1 AND state = 'exporting' FOR UPDATE", r.ID)
		if err != nil {
			return nil, err
		}
		var drafts []events.Draft
		for _, x := range list {
			m := msg
			if m == "" {
				m = "the export step ended without a deployable"
			}
			if _, err := tx.Exec(ctx, `UPDATE model_exports SET state = 'failed', error = $2, rev = rev + 1, updated_at = now()
				WHERE id = $1`, x.ID, m); err != nil {
				return nil, fmt.Errorf("fail export %s: %w", x.ID, err)
			}
			x, err := Get(ctx, tx, x.ID)
			if err != nil {
				return nil, err
			}
			drafts = append(drafts, draft(x, EventExported, map[string]any{"error": m}))
		}
		return drafts, nil
	case strings.HasPrefix(r.RunID, checkRunPrefix):
		c, found, err := checkByRun(ctx, tx, r.ID, true)
		if err != nil || !found || c.State != CheckRunning {
			return nil, err
		}
		m := msg
		if m == "" {
			m = "the scoring step ended without a report"
		}
		if _, err := tx.Exec(ctx, `UPDATE model_export_checks SET state = 'failed', error = $2, finished_at = now() WHERE id = $1`,
			c.ID, m); err != nil {
			return nil, fmt.Errorf("fail check %s: %w", c.ID, err)
		}
		return s.checkDraft(ctx, tx, c.ID)
	}
	return nil, nil
}
