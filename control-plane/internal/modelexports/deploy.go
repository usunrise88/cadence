package modelexports

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// What deployments (phase 5 · stream D4, internal/deployments) read of exports: the export of a request's model
// version, profile and format, the family's serve role as a step, and the deployable's file list for the signed
// promotion record.

// ResolveExport resolves a model version (ver_…, @alias or model/<name>) for a project, its export format (default
// the family's first) and profile (default the primary profile), and the export of the three; found is false when
// there is none yet.
func (s *Service) ResolveExport(ctx context.Context, q storage.Querier, projectID, version, profile, format string) (evals.DeployModel, Export, bool, error) {
	p, err := projects.GetByID(ctx, q, projectID)
	if err != nil {
		return evals.DeployModel{}, Export{}, false, err
	}
	m, err := s.Evals.ResolveModelVersion(ctx, q, p.ID, version)
	if err != nil {
		return evals.DeployModel{}, Export{}, false, err
	}
	if format, err = chooseFormat(m, format); err != nil {
		return evals.DeployModel{}, Export{}, false, err
	}
	if profile, err = s.resolveProfile(ctx, p, m, profile, "/profile"); err != nil {
		return evals.DeployModel{}, Export{}, false, err
	}
	x, found, err := Find(ctx, q, m.Version.ID, profile, format, false)
	if err != nil {
		return evals.DeployModel{}, Export{}, false, err
	}
	if !found {
		x = Export{ModelVersionID: m.Version.ID, Profile: profile, Format: format}
	}
	return m, x, found, nil
}

// ServeStep is the family's serve role as a pipeline step: its kind and the ports it reads and writes.
type ServeStep struct {
	Kind       pipelines.Kind
	Deployable string // the port of the deployable
	Data       string // the port of the dataset it decodes
	Hypotheses string // the port of the hypotheses it writes
	sk         serveKind
}

// ServeStep answers the family's serve role kind of model m with its ports (recipe-mismatch when the kind does not
// consume a deployable and a dataset and produce hypotheses and serving_timings).
func (s *Service) ServeStep(ctx context.Context, q storage.Querier, m evals.DeployModel) (ServeStep, error) {
	sk, err := s.serveKind(ctx, q, m)
	if err != nil {
		return ServeStep{}, err
	}
	return ServeStep{Kind: sk.kind, Deployable: sk.deployable, Data: sk.data, Hypotheses: sk.hypotheses, sk: sk}, nil
}

// Params are the serve step's parameters for decoding a dataset through staging at profile, in language, with
// concurrency streams at once, as fast as the server answers (shadow replay is throughput work).
func (st ServeStep) Params(staging targets.Target, profile, language string, concurrency int) map[string]any {
	return st.sk.params(staging, profile, language, concurrency, PaceFast, 0, 0)
}

// DeployableFile is one file of a deployable's model directory as deployable.json lists it.
type DeployableFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// DeployableFiles reads deployable.json's family, model directory files and manifest SHA-256 (what a promotion
// record names and the production host checks with standard tools).
func (s *Service) DeployableFiles(hash string) (family string, files []DeployableFile, manifest string, err error) {
	b, err := s.dirFile(hash, "deployable.json", 16<<20)
	if err != nil {
		return "", nil, "", err
	}
	var d struct {
		Family         string           `json:"family"`
		ManifestSHA256 string           `json:"manifestSha256"`
		Files          []DeployableFile `json:"files"`
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return "", nil, "", fmt.Errorf("deployable.json of %s: %w", hash, err)
	}
	if d.ManifestSHA256 == "" || len(d.Files) == 0 {
		return "", nil, "", fmt.Errorf("deployable.json of %s lists no model files or no manifestSha256", hash)
	}
	return d.Family, d.Files, d.ManifestSHA256, nil
}

// LatestBenchmarks are an export's finished benchmarks, newest first (the promotion checks read their verdicts).
func LatestBenchmarks(ctx context.Context, q storage.Querier, exportID string) ([]BenchmarkView, error) {
	list, err := queryChecks(ctx, q, `export_id = $1 AND kind = 'benchmark' ORDER BY created_at DESC, id DESC`, exportID)
	if err != nil {
		return nil, err
	}
	out := make([]BenchmarkView, 0, len(list))
	for _, c := range list {
		out = append(out, c.benchmarkView())
	}
	return out, nil
}

// LatestParityView is an export's newest parity check (nil when none ran).
func LatestParityView(ctx context.Context, q storage.Querier, exportID string) (*ParityView, error) {
	list, err := queryChecks(ctx, q, `export_id = $1 AND kind = 'parity' ORDER BY created_at DESC, id DESC LIMIT 1`, exportID)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	v := list[0].parityView()
	return &v, nil
}
