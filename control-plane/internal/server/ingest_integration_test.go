//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Phase 4 · stream D against the real control plane: sources.new with its clearance and ingest history, "no licence,
// no ingest" in the pipeline engine, a draft dataset version from dataset_freeze (mode draft) that is previewed,
// searched and refused for training, and datasets.freeze — the leakage check, the cut pipeline run and the hook that
// makes the draft frozen.

// freezeKind is dataset_freeze@1 as a worker publishes it (only what the engine reads).
var freezeKind = map[string]any{
	"name": "dataset_freeze", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "neutral": true,
	"params": map[string]any{"type": "object", "properties": map[string]any{
		"mode": map[string]any{"type": "string", "default": "draft", "x-cadence": map[string]any{"default": "draft",
			"description": "draft or cut", "source": "test", "range": map[string]any{"values": []string{"draft", "cut"}}}},
		"name": map[string]any{"type": "string", "default": "", "x-cadence": map[string]any{"default": "",
			"description": "collection", "source": "test", "range": "any"}},
		"draft_version": map[string]any{"type": "string", "default": "", "x-cadence": map[string]any{"default": "",
			"description": "the draft", "source": "test", "range": "any"}},
	}},
	"consumes": map[string]string{"segments": "segments"}, "produces": map[string]string{"dataset": "dataset"},
	"resources": map[string]any{"gpu": false, "jobKind": "data"}, "help": "steps.dataset-freeze",
}

// ingestKind is an sdp_ingest-like kind whose source parameter names a registry source.
var ingestKind = map[string]any{
	"name": "fx_ingest", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test", "neutral": true,
	"params": map[string]any{"type": "object", "properties": map[string]any{
		"source": map[string]any{"type": "string", "default": "", "x-cadence": map[string]any{"default": "",
			"description": "registered source", "source": "test", "range": "any", "registry": "source"}},
	}},
	"consumes": map[string]string{}, "produces": map[string]string{"segments": "segments"},
	"resources": map[string]any{"gpu": false, "jobKind": "data"}, "help": "steps.sdp-ingest",
}

type seg struct {
	audio, text, split string
	dur                float64
	speaker            string
}

// blob stores the fixture audio of name (the same bytes the artifact helper writes) and returns its hash and size.
func blob(t *testing.T, store *cas.Store, name string) (string, int64) {
	t.Helper()
	body := []byte("RIFF-fake-audio:" + name)
	h, err := store.PutBytes(body)
	if err != nil {
		t.Fatal(err)
	}
	return h, int64(len(body))
}

func putFiles(t *testing.T, store *cas.Store, files map[string][]byte, extra ...cas.File) string {
	t.Helper()
	var list []cas.File
	for name, body := range files {
		h, err := store.PutBytes(body)
		if err != nil {
			t.Fatal(err)
		}
		list = append(list, cas.File{Path: name, Hash: h, Size: int64(len(body))})
	}
	h, err := store.PutManifest(cas.Manifest{Files: append(list, extra...)})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// draftArtifact writes a cadence.dataset-draft/1 artifact of segs from source.
func draftArtifact(t *testing.T, store *cas.Store, source string, segs []seg) steps.ArtifactRef {
	t.Helper()
	var lines []string
	counts, hours := map[string]int{}, 0.0
	for i, s := range segs {
		h, size := blob(t, store, s.audio)
		l := map[string]any{"uri": fmt.Sprintf("mount://corpora/%s/r1/call%d.wav#t=0,%g&ch=0", source, i, s.dur), "hash": h,
			"bytes": size, "duration": s.dur, "sampleRate": 16000, "channels": 1, "language": "sr-RS", "text": s.text,
			"origin": "human", "split": s.split, "role": "caller", "speaker": s.speaker}
		b, _ := json.Marshal(l)
		lines = append(lines, string(b))
		counts[s.split]++
		hours += s.dur / 3600
	}
	header := map[string]any{"format": data.FormatDraft, "source": map[string]any{"name": source}, "splitRule": "speaker-disjoint",
		"counts": counts, "hours": hours, "card": "card.md",
		"quality": map[string]any{"passed": true, "checks": []any{map[string]any{"name": "silence_share", "status": "pass", "value": 0.1, "threshold": 0.5}}},
		"stats":   map[string]any{"speakers": 2, "durationHistogram": map[string]any{"edges": []int{0, 1, 2}, "counts": []int{0, 1, 2}}}}
	hb, _ := json.Marshal(header)
	h := putFiles(t, store, map[string][]byte{data.HeaderFile: hb, data.ManifestFile: []byte(strings.Join(lines, "\n") + "\n"),
		"card.md": []byte("# dataset/parla-sr\n")})
	return steps.ArtifactRef{Hash: h, Type: data.ArtifactType, Size: 999}
}

// cutArtifact writes the cadence.dataset/1 cut of segs that freezes draftID.
func cutArtifact(t *testing.T, store *cas.Store, source, draftID string, segs []seg) steps.ArtifactRef {
	t.Helper()
	var (
		lines []string
		audio []cas.File
	)
	counts, hours, bytes := map[string]int{}, 0.0, int64(0)
	for i, s := range segs {
		h, size := blob(t, store, s.audio)
		path := "audio/" + h[3:5] + "/" + h[3:] + ".wav"
		audio = append(audio, cas.File{Path: path, Hash: h, Size: size})
		l := map[string]any{"audio": path, "duration": s.dur, "sampleRate": 16000, "channels": 1, "language": "sr-RS", "text": s.text,
			"origin": "human", "split": s.split, "role": "caller", "speaker": s.speaker,
			"uri": fmt.Sprintf("mount://corpora/%s/r1/call%d.wav#t=0,%g&ch=0", source, i, s.dur)}
		b, _ := json.Marshal(l)
		lines = append(lines, string(b))
		counts[s.split]++
		hours += s.dur / 3600
		bytes += size
	}
	header := map[string]any{"format": data.FormatV1, "source": map[string]any{"name": source}, "splitRule": "speaker-disjoint",
		"counts": counts, "hours": hours, "card": "card.md", "draftVersionId": draftID,
		"quality": map[string]any{"passed": false, "checks": []any{map[string]any{"name": "clipping_share", "status": "warn", "value": 0.2, "threshold": 0.01}}},
		"shards":  []any{map[string]any{"index": 0, "cuts": "shards/cuts.000000.jsonl.gz", "utterances": len(segs), "bytes": bytes, "seconds": hours * 3600}}}
	hb, _ := json.Marshal(header)
	h := putFiles(t, store, map[string][]byte{data.HeaderFile: hb, data.ManifestFile: []byte(strings.Join(lines, "\n") + "\n"),
		"card.md": []byte("# dataset/parla-sr (frozen)\n"), "shards/cuts.000000.jsonl.gz": []byte("gz")}, audio...)
	return steps.ArtifactRef{Hash: h, Type: data.ArtifactType, Size: 1999}
}

func (e *env) runOutput(out steps.Output) error {
	e.t.Helper()
	ctx := context.Background()
	return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		drafts, err := e.admin.StepHooks.Run(ctx, tx, out)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, auth.Actor{Kind: auth.KindAutomation, ID: out.PipelineRunID}, nil, drafts)
	})
}

type dsDraftView struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Dataset struct {
		Frozen     *bool   `json:"frozen"`
		Utterances int     `json:"utterances"`
		Hours      float64 `json:"hours"`
		Artifact   struct{ Hash string }
		Segments   struct{ Hash string }
		Card       struct{ Hash string }
		Quality    struct {
			Passed bool
			Checks []struct{ Name, Status string }
		}
		Stats struct {
			Speakers int
		}
		Shards []struct {
			Index    int
			Hash     string
			Location string
		}
		Recipe struct {
			ProjectID string `json:"projectId"`
			StepKind  string `json:"stepKind"`
		}
		ContentFingerprint string `json:"contentFingerprint"`
		Freeze             struct {
			PipelineRunID string `json:"pipelineRunId"`
		}
	} `json:"dataset"`
}

func TestSourcesNewAndHistory(t *testing.T) {
	e := start(t)
	body := `{"name":"parla","licence":"CC-BY-SA-4.0","kind":"public","languages":["sr-RS","sr-RS"],"url":"https://example.org/parla"}`
	e.ok(e.do("POST", "/api/registry/sources?dryRun=true", body, "Idempotency-Key", e.key()), 200, nil)
	if n := e.count("SELECT count(*) FROM sources"); n != 0 {
		t.Fatalf("a dry run wrote %d sources", n)
	}
	var src struct {
		ID         string
		Languages  []string
		Clearances []struct{ Change, Licence string }
		Ingests    []any
	}
	e.ok(e.do("POST", "/api/registry/sources", body, "Idempotency-Key", e.key()), 201, &src)
	if len(src.Languages) != 1 || len(src.Clearances) != 1 || src.Clearances[0].Change != "created" {
		t.Fatalf("source: %+v", src)
	}
	expectProblem(t, e.do("POST", "/api/registry/sources", body, "Idempotency-Key", e.key()), 409, "conflict")
	expectProblem(t, e.do("POST", "/api/registry/sources", `{"name":"x","licence":"MIT","kind":"public"}`, "Idempotency-Key", e.key()), 422, "validation-failed")

	// Clearing and a licence change are history.
	e.ok(e.do("PATCH", "/api/registry/sources/parla", `{"trainingCleared":true,"licence":"CC-BY-4.0"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	e.ok(e.do("GET", "/api/registry/sources/parla", ""), 200, &src)
	var changes []string
	for _, c := range src.Clearances {
		changes = append(changes, c.Change+":"+c.Licence)
	}
	if strings.Join(changes, ",") != "created:CC-BY-SA-4.0,licence:CC-BY-4.0,cleared:CC-BY-4.0" {
		t.Errorf("clearances %v", changes)
	}
}

func TestIngestNeedsALicence(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.Register(ctx, e.pool, ingestKind); err != nil {
		t.Fatal(err)
	}
	p := e.newProject("ingest-licence")
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"mystery","licence":"unknown","kind":"public"}`, "Idempotency-Key", e.key()), 201, nil)
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"parla","licence":"CC-BY-4.0","kind":"public"}`, "Idempotency-Key", e.key()), 201, nil)
	prepare := func(source string) error {
		_, _, err := e.admin.Pipelines.Prepare(ctx, e.pool, pipelines.StartInput{ProjectID: p.ID, Pipeline: &pipelines.Pipeline{
			Name: "ingest", Steps: []pipelines.Step{{ID: "index", Kind: "fx_ingest@1", Params: map[string]any{"source": source}}}}})
		return err
	}
	for _, src := range []string{"", "nowhere", "mystery"} {
		err := prepare(src)
		if pe, ok := problems.As(err); !ok || pe.Type != problems.SourceUnlicensed || !strings.Contains(pe.Detail, "step index") {
			t.Errorf("source %q: %v", src, err)
		}
	}
	if err := prepare("parla"); err != nil {
		t.Errorf("a licensed source: %v", err)
	}
}

func TestDraftPreviewSearchAndFreeze(t *testing.T) {
	e, store := startData(t)
	ctx := context.Background()
	if err := pipelinestest.Register(ctx, e.pool, freezeKind); err != nil {
		t.Fatal(err)
	}
	p := e.newProject("ingest-freeze")
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"parla","licence":"CC-BY-4.0","kind":"public","languages":["sr-RS"]}`, "Idempotency-Key", e.key()), 201, nil)
	segs := []seg{
		{"s1", "Dobar dan, kako ste?", "train", 2.0, "spk1"},
		{"s2", "Hvala, dobro.", "train", 12.0, "spk1"},
		{"s3", "Doviđenja.", "validation", 1.5, "spk2"},
	}
	segBody := []byte(`{"format":"cadence.segments/1"}`)
	segRef := steps.ArtifactRef{Hash: putFiles(t, store, map[string][]byte{"segments.json": segBody}), Type: data.SegmentsType, Size: int64(len(segBody))}
	draftOut := steps.Output{ProjectID: p.ID, PipelineRunID: "plr_ingest", StepID: "pls_draft", Name: "dataset",
		Artifact: draftArtifact(t, store, "parla", segs),
		Spec: steps.Spec{Kind: "dataset_freeze", KindVersion: "1", Params: json.RawMessage(`{"mode":"draft","name":"parla-sr"}`),
			Inputs: map[string]steps.ArtifactRef{"segments": segRef}}}

	// An unregistered source: no licence, no ingest.
	bad := draftOut
	bad.Artifact = draftArtifact(t, store, "nowhere", segs[:1])
	if err := e.runOutput(bad); err == nil || !strings.Contains(err.Error(), "sources.new") {
		t.Fatalf("a draft of an unregistered source: %v", err)
	}

	if err := e.runOutput(draftOut); err != nil {
		t.Fatal(err)
	}
	var list struct{ Items []dsDraftView }
	e.ok(e.do("GET", "/api/registry/datasets?collection=dataset/parla-sr", ""), 200, &list)
	if len(list.Items) != 1 {
		t.Fatalf("%d versions", len(list.Items))
	}
	d := list.Items[0]
	if d.State != "draft" || d.Dataset.Frozen == nil || *d.Dataset.Frozen || d.Dataset.Utterances != 3 ||
		d.Dataset.Segments.Hash != segRef.Hash || d.Dataset.Recipe.StepKind != "dataset_freeze@1" || d.Dataset.Recipe.ProjectID != p.ID ||
		d.Dataset.Card.Hash == "" || !d.Dataset.Quality.Passed || d.Dataset.Stats.Speakers != 2 || d.Dataset.ContentFingerprint == "" {
		t.Fatalf("draft: %+v", d)
	}
	var src struct {
		Ingests []struct {
			DatasetVersionID string `json:"datasetVersionId"`
			Frozen           bool
			Utterances       int
		}
	}
	e.ok(e.do("GET", "/api/registry/sources/parla", ""), 200, &src)
	if len(src.Ingests) != 1 || src.Ingests[0].DatasetVersionID != d.ID || src.Ingests[0].Frozen || src.Ingests[0].Utterances != 3 {
		t.Errorf("ingests %+v", src.Ingests)
	}

	// A draft is never trained on.
	v, err := registry.GetVersion(ctx, e.pool, registry.KindDataset, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pe, ok := problems.As(data.Trainable(ctx, e.pool, v)); !ok || pe.Type != problems.DatasetNotFrozen {
		t.Errorf("Trainable(draft): %v", pe)
	}

	// Preview after filters.
	var pv struct {
		Frozen     bool
		Utterances int
		Cells      []struct {
			Language, Split string
			Utterances      int
		}
		Dropped map[string]int
	}
	e.ok(e.do("POST", "/api/registry/datasets:preview", `{"version":"`+d.ID+`","maxDuration":10}`, "Idempotency-Key", e.key()), 200, &pv)
	if pv.Frozen || pv.Utterances != 2 || pv.Dropped["duration"] != 1 || len(pv.Cells) != 2 || pv.Cells[0].Split != "train" {
		t.Errorf("preview: %+v", pv)
	}
	e.ok(e.do("POST", "/api/registry/datasets:preview", `{"version":"`+d.ID+`","languages":["sr"],"splits":["validation"]}`, "Idempotency-Key", e.key()), 200, &pv)
	if pv.Utterances != 1 || pv.Dropped["split"] != 2 {
		t.Errorf("preview by split: %+v", pv)
	}

	// Search.
	var found uttList
	e.ok(e.do("GET", "/api/registry/utterances:search?q=HVALA&dataset="+d.ID, ""), 200, &found)
	if len(found.Items) != 1 || found.Items[0].Split != "train" {
		t.Errorf("search q: %+v", found)
	}
	e.ok(e.do("GET", "/api/registry/utterances:search?speaker=spk1&maxDuration=5&origin=human", ""), 200, &found)
	if len(found.Items) != 1 || found.Items[0].Transcripts[0].Text != "Dobar dan, kako ste?" {
		t.Errorf("search speaker: %+v", found)
	}
	e.ok(e.do("GET", "/api/registry/utterances:search?q=100%25", ""), 200, &found)
	if len(found.Items) != 0 {
		t.Errorf("a LIKE wildcard matched: %+v", found)
	}

	// Freeze: a dry run checks leakage and starts nothing.
	var fr struct {
		Version       dsDraftView
		Leakage       struct{ Passed bool }
		PipelineRunID string `json:"pipelineRunId"`
	}
	e.ok(e.do("POST", "/api/registry/datasets:freeze?dryRun=true", `{"version":"`+d.ID+`"}`, "Idempotency-Key", e.key()), 200, &fr)
	if !fr.Leakage.Passed || fr.Version.State != "draft" || e.count("SELECT count(*) FROM pipeline_runs") != 0 {
		t.Fatalf("dry run: %+v", fr)
	}
	// The real call starts the cut in the draft's project and answers the step's job.
	e.useWorkers() // no worker: the cut stays queued
	var job struct {
		JobID string `json:"jobId"`
	}
	e.ok(e.do("POST", "/api/registry/datasets:freeze", `{"version":"`+d.ID+`"}`, "Idempotency-Key", e.key()), 202, &job)
	if job.JobID == "" {
		t.Fatal("no job id")
	}
	var params string
	if err := e.pool.QueryRow(ctx, `SELECT s.params::text FROM pipeline_steps s JOIN pipeline_runs r ON r.id = s.pipeline_run_id
		WHERE r.project_id = $1`, p.ID).Scan(&params); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(params, `"mode": "cut"`) && !strings.Contains(params, `"mode":"cut"`) || !strings.Contains(params, d.ID) {
		t.Errorf("cut params %s", params)
	}
	e.ok(e.do("POST", "/api/registry/datasets:freeze", `{"version":"`+d.ID+`"}`, "Idempotency-Key", e.key()), 200, &fr)
	if fr.PipelineRunID == "" || fr.Version.Dataset.Freeze.PipelineRunID != fr.PipelineRunID {
		t.Errorf("a second freeze while the first runs: %+v", fr)
	}

	// The cut's hook freezes the draft; a cut that differs from the draft is refused.
	other := cutArtifact(t, store, "parla", d.ID, segs[:2])
	if err := e.runOutput(steps.Output{ProjectID: p.ID, PipelineRunID: fr.PipelineRunID, Name: "dataset", Artifact: other,
		Spec: steps.Spec{Kind: "dataset_freeze", KindVersion: "1"}}); err == nil || !strings.Contains(err.Error(), "does not hold the draft") {
		t.Fatalf("a different cut: %v", err)
	}
	cut := cutArtifact(t, store, "parla", d.ID, segs)
	cutOut := steps.Output{ProjectID: p.ID, PipelineRunID: fr.PipelineRunID, Name: "dataset", Artifact: cut,
		Spec: steps.Spec{Kind: "dataset_freeze", KindVersion: "1"}}
	if err := e.runOutput(cutOut); err != nil {
		t.Fatal(err)
	}
	var frozen dsDraftView
	e.ok(e.do("GET", "/api/registry/datasets/"+d.ID, ""), 200, &frozen)
	if frozen.State != "frozen" || frozen.Dataset.Frozen == nil || !*frozen.Dataset.Frozen || frozen.Dataset.Artifact.Hash != cut.Hash ||
		len(frozen.Dataset.Shards) != 1 || frozen.Dataset.Shards[0].Location != "cas" || frozen.Dataset.Quality.Passed ||
		frozen.Dataset.Card.Hash == d.Dataset.Card.Hash {
		t.Fatalf("frozen: %+v", frozen)
	}
	if err := e.runOutput(cutOut); err != nil {
		t.Errorf("the same cut again: %v", err)
	}
	if types := eventTypes(e.events("entity.dataset_version." + d.ID)); strings.Join(types, ",") != "dataset_version.registered,dataset_version.frozen" {
		t.Errorf("events %v", types)
	}
	v, _ = registry.GetVersion(ctx, e.pool, registry.KindDataset, d.ID)
	if pe, ok := problems.As(data.Trainable(ctx, e.pool, v)); !ok || pe.Type != problems.EvalOnlyDataset {
		t.Errorf("Trainable(frozen, source not cleared) = %v, want eval-only-dataset", pe)
	}
	e.ok(e.do("POST", "/api/registry/datasets:freeze", `{"version":"`+d.ID+`"}`, "Idempotency-Key", e.key()), 200, &fr)
	if fr.Version.State != "frozen" {
		t.Errorf("freezing a frozen version: %+v", fr)
	}
}

func TestFreezeRefusesGoldenSetLeakage(t *testing.T) {
	e, store := startData(t)
	ctx := context.Background()
	p := e.newProject("ingest-leak")
	e.ok(e.do("POST", "/api/registry/sources", `{"name":"parla","licence":"CC-BY-4.0","kind":"public"}`, "Idempotency-Key", e.key()), 201, nil)
	// A golden set holding the audio "g1".
	if err := e.runHook(artifact(t, store, fleursHeader("golden-sr"), []fixtureUtt{{"g1", "Zdravo.", "test", "sr-RS", ""}}), "plr_g", `{}`); err != nil {
		t.Fatal(err)
	}
	golden := e.datasetIn("dataset/golden-sr")
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		gs, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindGoldenSet, Name: "golden-set/sr",
			Payload: []byte(`{"dataset":"` + golden.ID + `"}`), Freeze: true, Actor: auth.Actor{Kind: auth.KindUser, ID: "usr_admin"}}, time.Now())
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO golden_sets (version_id, dataset_version_id, normalizer_version_id)
			SELECT $1, $2, v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
			WHERE c.name = 'normalizer/basic' LIMIT 1`, gs.ID, golden.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	segRef := steps.ArtifactRef{Hash: putFiles(t, store, map[string][]byte{"segments.json": []byte(`{}`)}), Type: data.SegmentsType}
	if err := e.runOutput(steps.Output{ProjectID: p.ID, PipelineRunID: "plr_ingest", Name: "dataset",
		Artifact: draftArtifact(t, store, "parla", []seg{{"g1", "Zdravo.", "train", 1, ""}, {"n1", "Novo.", "train", 1, ""}}),
		Spec: steps.Spec{Kind: "dataset_freeze", KindVersion: "1", Params: json.RawMessage(`{"name":"parla-leak"}`),
			Inputs: map[string]steps.ArtifactRef{"segments": segRef}}}); err != nil {
		t.Fatal(err)
	}
	var l struct{ Items []dsDraftView }
	e.ok(e.do("GET", "/api/registry/datasets?collection=dataset/parla-leak", ""), 200, &l)
	pr := expectProblem(t, e.do("POST", "/api/registry/datasets:freeze?dryRun=true", `{"version":"`+l.Items[0].ID+`"}`, "Idempotency-Key", e.key()), 422, "golden-set-leakage")
	if !strings.Contains(pr.Detail, "cannot be frozen") {
		t.Errorf("detail %q", pr.Detail)
	}
}
