//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/exports"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Phase 4 · stream I against the real control plane: datasets.export plans and starts an export of a frozen dataset
// version (shar_export, dataset_export, hf_push), the export output hook completes it and records the audio it placed
// on a mount as blob copies, a Hub push is checked and gated for everyone, and a noise bank mined from calls
// registers from its registered source.

func exportKind(name string, params []string, extra map[string]any) map[string]any {
	props := map[string]any{}
	for _, p := range params {
		props[p] = map[string]any{"type": "string", "default": "", "x-cadence": map[string]any{"default": "",
			"description": p, "source": "test", "range": "any"}}
	}
	k := map[string]any{
		"name": name, "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "neutral": true,
		"params":   map[string]any{"type": "object", "properties": props},
		"consumes": map[string]string{"dataset": "dataset"}, "produces": map[string]string{"export": "export"},
		"resources": map[string]any{"gpu": false, "jobKind": "export"}, "help": "steps." + strings.ReplaceAll(name, "_", "-"),
	}
	for key, v := range extra {
		k[key] = v
	}
	return k
}

type exportView struct {
	ID            string `json:"id"`
	VersionID     string `json:"versionId"`
	Collection    string `json:"collection"`
	ProjectID     string `json:"projectId"`
	Format        string `json:"format"`
	Target        string `json:"target"`
	State         string `json:"state"`
	PipelineRunID string `json:"pipelineRunId"`
	StepKind      string `json:"stepKind"`
	Artifact      string `json:"artifact"`
	Files         int    `json:"files"`
	Bytes         int64  `json:"bytes"`
	Copies        int    `json:"copies"`
	Sample        []struct {
		Path, Hash string
		Bytes      int64
	} `json:"sample"`
	Hub *struct{ Repo, Commit string } `json:"hub"`
}

type exportPlanView struct {
	VersionID string   `json:"versionId"`
	Format    string   `json:"format"`
	ProjectID string   `json:"projectId"`
	Project   string   `json:"project"`
	Target    string   `json:"target"`
	StepKind  string   `json:"stepKind"`
	Licence   string   `json:"licence"`
	Sources   []string `json:"sources"`
	Approval  bool     `json:"approval"`
	Copies    bool     `json:"copies"`
}

func (e *env) exportMounts(root string) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO mounts (id, name, kind, root, read_only, created_by) VALUES
			('mnt_exports', 'exports', 'local', $1, false, '{"kind":"user","id":"usr_admin"}')`, root); err != nil {
		e.t.Fatal(err)
	}
	e.corporaMount() // read-only
}

// exportArtifact writes an export artifact: export.json naming files (path → content) and the target.
func exportArtifact(t *testing.T, store *cas.Store, format, target, version string, files map[string][]byte) steps.ArtifactRef {
	t.Helper()
	var list []exports.File
	for p, b := range files {
		list = append(list, exports.File{Path: p, Hash: cas.Hash(b), Bytes: int64(len(b))})
	}
	m := exports.Manifest{Format: exports.ManifestFormat, ExportFormat: format, Target: target, Version: version,
		Utterances: 3, Files: list}
	h := putFiles(t, store, map[string][]byte{exports.ManifestFile: mustJSON(t, m)})
	return steps.ArtifactRef{Hash: h, Type: exports.ArtifactType, Size: 100}
}

// sized gives a fixture dataset artifact its real size (the files' bytes), as a step's output carries it.
func sized(t *testing.T, store *cas.Store, ref steps.ArtifactRef) steps.ArtifactRef {
	t.Helper()
	m, err := store.ReadManifest(ref.Hash)
	if err != nil {
		t.Fatal(err)
	}
	ref.Size = 0
	for _, f := range m.Files {
		ref.Size += f.Size
	}
	return ref
}

func (e *env) runExportOutput(x exportView, ref, dataset steps.ArtifactRef) error {
	e.t.Helper()
	ctx := context.Background()
	var stepID string
	if err := e.pool.QueryRow(ctx, "SELECT id FROM pipeline_steps WHERE pipeline_run_id = $1", x.PipelineRunID).Scan(&stepID); err != nil {
		e.t.Fatal(err)
	}
	return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		out := steps.Output{ProjectID: x.ProjectID, PipelineRunID: x.PipelineRunID, StepID: stepID, Name: "export", Artifact: ref,
			Spec: steps.Spec{Kind: "dataset_export", KindVersion: "1", Inputs: map[string]steps.ArtifactRef{"dataset": dataset}}}
		drafts, err := e.admin.StepHooks.Run(ctx, tx, out)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, auth.Actor{Kind: auth.KindAutomation, ID: x.PipelineRunID}, nil, drafts)
	})
}

func TestDatasetExportsPlanStartAndRecordCopies(t *testing.T) {
	e, store := startData(t)
	ctx := context.Background()
	root := t.TempDir()
	e.exportMounts(root)
	if err := pipelinestest.Register(ctx, e.pool,
		exportKind("shar_export", []string{"target", "version", "name"}, nil),
		exportKind("dataset_export", []string{"format", "target", "version", "name"}, map[string]any{
			"consumes": map[string]string{"dataset": "dataset", "record": "registry_record"}, "optionalInputs": []string{"record"}}),
		exportKind("hf_push", []string{"repo", "licence", "version", "name"}, map[string]any{"secrets": []string{"hf-token"}}),
	); err != nil {
		t.Fatal(err)
	}
	p := e.newProject("interop")
	ref := sized(t, store, artifact(t, store, fleursHeader("fleurs-he"), heUtts))
	if err := e.runHook(ref, "plr_import", `{}`); err != nil {
		t.Fatal(err)
	}
	ds := e.datasetIn("dataset/fleurs-he")
	body := func(format, extra string) string {
		return fmt.Sprintf(`{"version":%q,"format":%q%s}`, ds.ID, format, extra)
	}

	// The version was imported outside a project: the export names one.
	expectProblem(t, e.do("POST", "/api/registry/datasets:export?dryRun=true", body("lhotse-shar", ""), "Idempotency-Key", e.key()), 422, "validation-failed")
	var plan exportPlanView
	e.ok(e.do("POST", "/api/registry/datasets:export?dryRun=true", body("lhotse-shar", `,"project":"interop"`), "Idempotency-Key", e.key()), 200, &plan)
	if plan.StepKind != "shar_export@1" || plan.ProjectID != p.ID || plan.Approval || plan.Copies || plan.Licence != "CC-BY-4.0" ||
		!strings.HasPrefix(plan.Target, "mount://exports/fleurs-he/") || !strings.HasSuffix(plan.Target, "/lhotse-shar") ||
		len(plan.Sources) != 1 || plan.Sources[0] != "fleurs" {
		t.Fatalf("shar plan %+v", plan)
	}
	e.ok(e.do("POST", "/api/registry/datasets:export?dryRun=true", body("nemo-manifest", `,"project":"interop","target":"cas"`), "Idempotency-Key", e.key()), 200, &plan)
	if plan.StepKind != "dataset_export@1" || plan.Target != "cas" || plan.Copies {
		t.Fatalf("manifest plan to cas %+v", plan)
	}
	pr := expectProblem(t, e.do("POST", "/api/registry/datasets:export?dryRun=true", body("nemo-manifest", `,"project":"interop","target":"mount://corpora/out"`), "Idempotency-Key", e.key()), 422, "validation-failed")
	if !strings.Contains(fmt.Sprint(pr.Errors), "read-only") {
		t.Errorf("a read-only target: %+v", pr)
	}
	if n := e.count("SELECT count(*) FROM pipeline_runs"); n != 0 {
		t.Fatalf("dry runs started %d pipeline runs", n)
	}

	// The real call starts the export in the project and records it; no worker runs it here.
	e.useWorkers()
	var x exportView
	e.ok(e.do("POST", "/api/registry/datasets:export", body("nemo-manifest", `,"project":"interop","target":"mount://exports/he"`), "Idempotency-Key", e.key()), 201, &x)
	if x.State != "running" || x.PipelineRunID == "" || x.Target != "mount://exports/he" || x.StepKind != "dataset_export@1" ||
		x.VersionID != ds.ID || x.Collection != "dataset/fleurs-he" || x.ProjectID != p.ID {
		t.Fatalf("started %+v", x)
	}
	var params string
	if err := e.pool.QueryRow(ctx, "SELECT params::text FROM pipeline_steps WHERE pipeline_run_id = $1", x.PipelineRunID).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(params, `"nemo-manifest"`) || !strings.Contains(params, ds.ID) || !strings.Contains(params, "mount://exports/he") {
		t.Errorf("step params %s", params)
	}

	// The step's output: the manifest and one audio file of the dataset, unchanged → one blob copy on the mount.
	audio := []byte("RIFF-fake-audio:a")
	out := exportArtifact(t, store, "nemo-manifest", "mount://exports/he", ds.ID, map[string][]byte{
		"audio/a.wav": audio, "manifest.train.jsonl": []byte(`{"audio_filepath":"audio/a.wav"}` + "\n")})
	if err := e.runExportOutput(x, out, ref); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("GET", "/api/exports/"+x.ID, ""), 200, &x)
	if x.State != "done" || x.Files != 2 || x.Copies != 1 || x.Artifact != out.Hash || len(x.Sample) != 2 {
		t.Fatalf("done %+v", x)
	}
	var copyPath string
	if err := e.pool.QueryRow(ctx, "SELECT path FROM blob_copies WHERE hash = $1 AND mount_id = 'mnt_exports'", cas.Hash(audio)).Scan(&copyPath); err != nil {
		t.Fatal(err)
	}
	if copyPath != "he/audio/a.wav" {
		t.Errorf("copy path %q", copyPath)
	}
	var list struct{ Items []exportView }
	e.ok(e.do("GET", "/api/projects/interop/exports?version="+ds.ID, ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].ID != x.ID {
		t.Errorf("exports.list %+v", list.Items)
	}
	// (Both test servers install their hooks on the shared registry: the done hook runs once per server, idempotently.)
	if types := eventTypes(e.events("entity.export." + x.ID)); len(types) < 2 || types[0] != "export.started" || types[1] != "export.done" {
		t.Errorf("events %v", types)
	}

	// A bundle reads the version's registry record, rendered into the content store as the step's second input.
	var bx exportView
	e.ok(e.do("POST", "/api/registry/datasets:export", body("cadence-bundle", `,"project":"interop"`), "Idempotency-Key", e.key()), 201, &bx)
	var inputs string
	if err := e.pool.QueryRow(ctx, "SELECT inputs::text FROM pipeline_runs WHERE id = $1", bx.PipelineRunID).Scan(&inputs); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(inputs, "registry_record") || !strings.HasPrefix(bx.Target, "mount://exports/fleurs-he/") {
		t.Errorf("bundle run inputs %s, target %s", inputs, bx.Target)
	}

	// The Hub: the licence check, then an approval for everyone (registry scope: the admin's).
	e.ok(e.do("POST", "/api/registry/datasets:export?dryRun=true", body("hf-hub", `,"project":"interop","hubRepo":"acme/fleurs-he"`), "Idempotency-Key", e.key()), 200, &plan)
	if !plan.Approval || plan.Target != "hf://datasets/acme/fleurs-he" || plan.StepKind != "hf_push@1" {
		t.Fatalf("hub plan %+v", plan)
	}
	expectProblem(t, e.do("POST", "/api/registry/datasets:export?dryRun=true", body("hf-hub", `,"project":"interop"`), "Idempotency-Key", e.key()), 422, "validation-failed")
	var acc accepted
	e.ok(e.do("POST", "/api/registry/datasets:export", body("hf-hub", `,"project":"interop","hubRepo":"acme/fleurs-he"`), "Idempotency-Key", e.key()), 202, &acc)
	if a := e.approval(acc.ApprovalID); a.Rule != "hub-export" || a.Scope != "registry" {
		t.Fatalf("hub approval %+v", a)
	}

	// Production audio never goes to the Hub; a draft is never exported.
	prod := fleursHeader("calls-prod")
	prod.Source = data.HeaderSource{Name: "calls", Licence: "proprietary", Kind: "production", Languages: []string{"he-IL"}}
	if err := e.runHook(artifact(t, store, prod, []fixtureUtt{{"p1", "שלום.", "train", "he-IL", ""}}), "plr_prod", `{}`); err != nil {
		t.Fatal(err)
	}
	prodVer := e.datasetIn("dataset/calls-prod")
	pr = expectProblem(t, e.do("POST", "/api/registry/datasets:export?dryRun=true",
		fmt.Sprintf(`{"version":%q,"format":"hf-hub","project":"interop","hubRepo":"acme/x"}`, prodVer.ID), "Idempotency-Key", e.key()), 422, "export-not-allowed")
	if !strings.Contains(pr.Detail, "production") {
		t.Errorf("detail %q", pr.Detail)
	}
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"parla","licence":"CC-BY-4.0","kind":"public"}`, "Idempotency-Key", e.key()), 201, nil)
	segRef := steps.ArtifactRef{Hash: putFiles(t, store, map[string][]byte{"segments.json": []byte(`{}`)}), Type: data.SegmentsType}
	if err := e.runOutput(steps.Output{ProjectID: p.ID, PipelineRunID: "plr_ingest", Name: "dataset",
		Artifact: draftArtifact(t, store, "parla", []seg{{"d1", "Dobar dan.", "train", 1, ""}}),
		Spec: steps.Spec{Kind: "dataset_freeze", KindVersion: "1", Params: json.RawMessage(`{"name":"parla-draft"}`),
			Inputs: map[string]steps.ArtifactRef{"segments": segRef}}}); err != nil {
		t.Fatal(err)
	}
	draft := e.datasetIn("dataset/parla-draft")
	expectProblem(t, e.do("POST", "/api/registry/datasets:export?dryRun=true",
		fmt.Sprintf(`{"version":%q,"format":"lhotse-shar"}`, draft.ID), "Idempotency-Key", e.key()), 422, "dataset-not-frozen")
}

// A noise bank mined from calls names only its source: the source must be registered with a licence, and the bank
// keeps how it was mined.
func TestMinedNoiseBankNeedsItsSource(t *testing.T) {
	e, store := startData(t)
	h := data.Header{Format: data.FormatV1, Name: "calls-noise", SplitRule: "all-train", Purpose: data.PurposeNoise,
		Tags: []string{"noise-bank", "mined"}, Source: data.HeaderSource{Name: "calls-synth"},
		Mined: json.RawMessage(`{"stepKind":"noise_mine@1","roles":["caller"],"clips":2}`)}
	ref := artifact(t, store, h, []fixtureUtt{{"n1", "", "train", "und", ""}, {"n2", "", "train", "und", ""}})
	if err := e.runHook(ref, "plr_mine", `{}`); err == nil || !strings.Contains(err.Error(), "sources.new") {
		t.Fatalf("an unregistered source: %v", err)
	}
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"calls-synth","licence":"CC-BY-4.0","kind":"synthetic"}`, "Idempotency-Key", e.key()), 201, nil)
	if err := e.runHook(ref, "plr_mine", `{}`); err != nil {
		t.Fatal(err)
	}
	var tags, payload, licence string
	if err := e.pool.QueryRow(context.Background(), `SELECT array_to_string(c.tags, ','), v.payload::text, c.licence
		FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id WHERE c.name = 'noise-bank/calls-noise'`).
		Scan(&tags, &payload, &licence); err != nil {
		t.Fatal(err)
	}
	if tags != "mined,noise-bank,source:calls-synth" || licence != "CC-BY-4.0" || !strings.Contains(payload, `"mined"`) ||
		!strings.Contains(payload, "noise_mine@1") {
		t.Errorf("tags %q licence %q payload %s", tags, licence, payload)
	}
	if n := e.count("SELECT count(*) FROM utterances"); n != 0 {
		t.Errorf("a noise bank wrote %d utterances", n)
	}
}
