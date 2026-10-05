//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

type mexPlanView struct {
	ModelVersionID, Family, Format, Kind, StagingTargetID string
	Profiles                                              []struct{ Profile, Action, ExportID, Step, DeployableHash string }
	Estimate                                              struct {
		Known    bool
		GPUHours *float64
	}
	PipelineRun *struct{ ID, Pipeline, Source string }
}

type checkPlanView struct {
	ExportID, Profile string
	Sample            struct {
		GoldenSetVersionID, DatasetHash, Selection string
		Utterances                                 int
	}
	Staging       struct{ ID, Name string }
	Streams       []int
	TargetStreams int
	Steps         []struct{ Step, Kind, JobKind string }
	PipelineRun   *struct{ ID string }
}

type mexView struct {
	ID, Profile, Format, State, DeployableHash, Error string
	Deployable                                        struct {
		Format         string
		ManifestSha256 string
		MemoryMb       int
		Server         map[string]any
	}
	Parity *struct {
		State, Compared, ReportHash string
		IdenticalShare              float64
		Sample                      struct {
			Utterances int
			Selection  string
		}
		ServedBy struct{ TargetID string }
	}
	Benchmarks []struct {
		State, Verdict              string
		Streams                     int
		Levels                      []int
		P95ChunkLatencyMs           float64
		MaxStreamsWithinBudget      int
		BudgetMs                    float64
		StagingTargetID, ReportHash string
	}
}

type modelExportsView struct {
	ID      string
	Exports []mexView
}

func (m modelExportsView) profile(name string) mexView {
	for _, x := range m.Exports {
		if x.Profile == name {
			return x
		}
	}
	return mexView{}
}

// models.export|parity|benchmark (phase 5 · stream D1) on the in-process fake worker: the fixture family's export,
// parity-reference and serve roles, the neutral judges, the export rows models.get shows, the job kinds a generated
// pipeline gives the serve steps, the export the observer fails, and the smoke set a delivery bundle reads.
func TestModelExports(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	for _, reg := range []func(context.Context, *pgxpool.Pool) error{pipelinestest.RegisterTraining, pipelinestest.RegisterEvaluation, pipelinestest.RegisterDeploy} {
		if err := reg(ctx, e.pool); err != nil {
			t.Fatal(err)
		}
	}
	p := e.newProject("mexp")
	refs := make([]string, 6)
	for i := range refs {
		refs[i] = fmt.Sprintf("utterance number %d of the parity sample", i)
	}
	gs, _, err := pipelinestest.RegisterDirGoldenSet(ctx, e.pool, e.admin.CAS, "fx-parity", "he-IL", refs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := targets.Seed(ctx, e.pool, "staging", "http://triton:8000", targets.Server{Kind: "triton", Version: "26.08"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	staging, err := targets.Get(ctx, e.pool, "staging")
	if err != nil {
		t.Fatal(err)
	}
	base, err := registry.Latest(ctx, e.pool, registry.KindBaseModel, pipelinestest.BaseModel)
	if err != nil {
		t.Fatal(err)
	}
	ckpt, err := e.admin.CAS.PutBytes([]byte("fixture checkpoint of mexp"))
	if err != nil {
		t.Fatal(err)
	}
	var model registry.Version
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(map[string]any{"checkpointId": "ckp_fixture", "weightsHash": "b3:w", "checkpointHash": ckpt,
			"familyId": pipelinestest.FamilyName, "baseModelVersionId": base.ID, "projectId": p.ID, "evalId": "",
			"gate": map[string]any{"verdict": "passed"}, "lineage": map[string]any{"datasetVersionIds": []string{}}})
		v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindModel, Name: "model/mexp", Payload: b,
			Freeze: true, Actor: auth.DevActor()}, time.Now())
		if err != nil {
			return err
		}
		model = v
		_, err = registry.AdoptQuietly(ctx, tx, p.ID, []string{v.ID, gs}, auth.DevActor())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	post := func(verb, _ string, dry bool) string {
		path := "/api/projects/mexp/models:" + verb
		if dry {
			path += "?dryRun=true"
		}
		return path
	}
	body := `{"version":"model/mexp"}`

	// Parity before an export: refused.
	expectProblem(t, e.do("POST", post("parity", body, false), body, "Idempotency-Key", e.key()), 422, "export-missing")

	// The export: the dry run plans one step at the primary profile; the real call runs it.
	var dry mexPlanView
	e.ok(e.do("POST", post("export", body, true), body, "Idempotency-Key", e.key()), 200, &dry)
	if dry.ModelVersionID != model.ID || dry.Format != pipelinestest.FixtureFormat || dry.Kind != pipelinestest.KindExport+"@1" ||
		len(dry.Profiles) != 1 || dry.Profiles[0].Profile != "80ms" || dry.Profiles[0].Action != "export" || dry.PipelineRun != nil ||
		dry.StagingTargetID != staging.ID || !dry.Estimate.Known {
		t.Fatalf("export dry run %+v", dry)
	}
	var run mexPlanView
	e.ok(e.do("POST", post("export", body, false), body, "Idempotency-Key", e.key()), 201, &run)
	if run.PipelineRun == nil || run.PipelineRun.Pipeline != "model-export" || run.Profiles[0].ExportID == "" {
		t.Fatalf("export %+v", run)
	}
	e.waitPipelineRun(run.PipelineRun.ID, "done")
	calls := e.leases.CallsOf("export-80ms")
	var params map[string]any
	_ = json.Unmarshal(calls[0].Spec.Params, &params)
	if params["server_version"] != "26.08" || params["profile"] != "80ms" || calls[0].Spec.Resources.JobKind != steps.JobExport {
		t.Fatalf("export step %+v / %s", params, calls[0].Spec.Resources.JobKind)
	}
	var mv modelExportsView
	e.ok(e.do("GET", "/api/registry/models/"+model.ID, ""), 200, &mv)
	if len(mv.Exports) != 1 || mv.Exports[0].State != "exported" || mv.Exports[0].Deployable.Format != pipelinestest.FixtureFormat ||
		mv.Exports[0].Deployable.ManifestSha256 == "" || mv.Exports[0].Deployable.MemoryMb != 1000 || mv.Exports[0].Parity != nil {
		t.Fatalf("exports %+v", mv.Exports)
	}
	// Again: the export is answered as it is.
	var again mexPlanView
	e.ok(e.do("POST", post("export", body, false), body, "Idempotency-Key", e.key()), 200, &again)
	if again.PipelineRun != nil || again.Profiles[0].Action != "exported" || again.Profiles[0].DeployableHash != mv.Exports[0].DeployableHash {
		t.Fatalf("export again %+v", again)
	}

	// A failed export: the observer fails the row, and asking again runs it again.
	e.leases.Script("export-offline", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "trtexec failed"}})
	off := `{"version":"model/mexp","profiles":["offline"]}`
	e.ok(e.do("POST", post("export", off, false), off, "Idempotency-Key", e.key()), 201, &run)
	e.waitPipelineRun(run.PipelineRun.ID, "failed")
	e.ok(e.do("GET", "/api/registry/models/"+model.ID, ""), 200, &mv)
	if f := mv.profile("offline"); f.State != "failed" || !strings.Contains(f.Error, "trtexec failed") {
		t.Fatalf("failed export %+v", mv.Exports)
	}
	e.ok(e.do("POST", post("export", off, true), off, "Idempotency-Key", e.key()), 200, &dry)
	if dry.Profiles[0].Action != "export" || dry.Profiles[0].ExportID == "" {
		t.Fatalf("failed export asked again %+v", dry)
	}

	// Parity: the reference and the served decode of the sample through the staging target, judged by parity_score.
	var par checkPlanView
	e.ok(e.do("POST", post("parity", body, false), body, "Idempotency-Key", e.key()), 201, &par)
	if par.PipelineRun == nil || par.Sample.Utterances != len(refs) || par.Sample.GoldenSetVersionID != gs ||
		par.Sample.Selection != "first-by-audio-hash" || par.Staging.ID != staging.ID || len(par.Steps) != 3 || par.Steps[1].JobKind != "eval" {
		t.Fatalf("parity %+v", par)
	}
	e.waitPipelineRun(par.PipelineRun.ID, "done")
	served := e.leases.CallsOf("served")
	_ = json.Unmarshal(served[0].Spec.Params, &params)
	if params["target"] != staging.ID || params["pace"] != "fast" || params["concurrency"] != float64(8) || params["target_lang"] != "he-IL" ||
		served[0].Spec.Resources.JobKind != steps.JobEval {
		t.Fatalf("served step %+v", params)
	}
	e.ok(e.do("GET", "/api/registry/models/"+model.ID, ""), 200, &mv)
	x := mv.profile("80ms")
	if x.Parity == nil || x.Parity.State != "passed" || x.Parity.IdenticalShare != 1 || x.Parity.Sample.Utterances != len(refs) ||
		x.Parity.ServedBy.TargetID != staging.ID {
		t.Fatalf("parity on the export %+v", x.Parity)
	}

	// The benchmark: every level a serve step of job kind benchmark, the target concurrency added.
	bench := `{"version":"model/mexp","streams":[1,4]}`
	var bp checkPlanView
	e.ok(e.do("POST", post("benchmark", bench, false), bench, "Idempotency-Key", e.key()), 201, &bp)
	if bp.PipelineRun == nil || !slices.Equal(bp.Streams, []int{1, 4, 32}) || bp.TargetStreams != 32 || len(bp.Steps) != 4 ||
		bp.Steps[2].Step != "level-32" || bp.Steps[2].JobKind != "benchmark" {
		t.Fatalf("benchmark %+v", bp)
	}
	e.waitPipelineRun(bp.PipelineRun.ID, "done")
	if c := e.leases.CallsOf("level-32"); len(c) != 1 || c[0].Spec.Resources.JobKind != steps.JobBenchmark {
		t.Fatalf("level-32 calls %+v", c)
	}
	e.ok(e.do("GET", "/api/registry/models/"+model.ID, ""), 200, &mv)
	for _, ex := range mv.Exports {
		if ex.Profile != "80ms" {
			continue
		}
		if len(ex.Benchmarks) != 1 || ex.Benchmarks[0].Verdict != "passed" || ex.Benchmarks[0].Streams != 32 ||
			ex.Benchmarks[0].P95ChunkLatencyMs != 32*pipelinestest.ServeLatencyPerConc || ex.Benchmarks[0].MaxStreamsWithinBudget != 32 ||
			ex.Benchmarks[0].BudgetMs != 100 || ex.Benchmarks[0].StagingTargetID != staging.ID {
			t.Fatalf("benchmarks %+v", ex.Benchmarks)
		}
		// The smoke set a delivery bundle of this export carries.
		smoke, err := e.admin.modelExports.Smoke(ctx, e.pool, model.ID, ex.DeployableHash)
		if err != nil || len(smoke.Items) != 2 || !strings.HasPrefix(string(smoke.Client), "#!/bin/sh") || smoke.Items[0].Name != "01.json" ||
			!strings.HasPrefix(smoke.Items[0].Text, "utterance number") {
			t.Fatalf("smoke %+v (%v)", smoke, err)
		}
	}
	if n := e.count("SELECT count(*) FROM events WHERE type = 'model.benchmarked'"); n < 2 {
		t.Fatalf("%d model.benchmarked events", n)
	}
}
