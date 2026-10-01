//go:build integration

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The dataset hook against the real control plane (phase 2 · stream D, R18): a CAS-backed dataset artifact registers
// a source, utterances, transcripts and a frozen dataset version; re-imports dedupe; the fingerprint is the content's;
// eval-only versions are refused by mixes and runs until a person clears the source; sources.edit and archive rules;
// utterances paging.

type fixtureUtt struct {
	audio, text, split, lang, speaker string
}

// artifact writes a dataset artifact into store: one blob per audio (its bytes are the audio name, so equal names
// are equal content), manifest.jsonl in the given order, dataset.json with counts and hours.
func artifact(t *testing.T, store *cas.Store, h data.Header, utts []fixtureUtt) steps.ArtifactRef {
	t.Helper()
	var (
		files []cas.File
		lines []string
		hours float64
	)
	h.Counts = map[string]int{}
	seen := map[string]bool{}
	for _, u := range utts {
		body := []byte("RIFF-fake-audio:" + u.audio)
		hash, err := store.PutBytes(body)
		if err != nil {
			t.Fatal(err)
		}
		path := "audio/" + u.audio + ".wav"
		if !seen[path] {
			files = append(files, cas.File{Path: path, Hash: hash, Size: int64(len(body))})
			seen[path] = true
		}
		line := map[string]any{"audio": path, "duration": 2.5, "sampleRate": 16000, "language": u.lang, "text": u.text,
			"origin": "human", "split": u.split}
		if u.speaker != "" {
			line["speaker"] = u.speaker
		}
		b, _ := json.Marshal(line)
		lines = append(lines, string(b))
		h.Counts[u.split]++
		hours += 2.5 / 3600
	}
	h.Hours = hours
	for name, body := range map[string][]byte{data.ManifestFile: []byte(strings.Join(lines, "\n") + "\n"), data.HeaderFile: mustJSON(t, h)} {
		hash, err := store.PutBytes(body)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, cas.File{Path: name, Hash: hash, Size: int64(len(body))})
	}
	hash, err := store.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		t.Fatal(err)
	}
	return steps.ArtifactRef{Hash: hash, Type: data.ArtifactType, Size: 1234}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fleursHeader(name string) data.Header {
	return data.Header{Format: data.FormatV1, Name: name, SplitRule: "source",
		Source: data.HeaderSource{Name: "fleurs", Licence: "CC-BY-4.0", Kind: "public", Languages: []string{"he-IL"},
			URL: "hf://datasets/google/fleurs", Revision: "70bb2e84b976b7e960aa89f1c648e09c59f894dd", Subset: "he_il"}}
}

var heUtts = []fixtureUtt{
	{"a", "שלום עולם.", "train", "he-IL", "spk1"},
	{"b", "מה שלומך?", "train", "he-IL", "spk2"},
	{"c", "תודה רבה.", "validation", "he-IL", "spk3"},
}

// runHook calls the step hooks as the pipeline engine does: inside one transaction, appending their events.
func (e *env) runHook(ref steps.ArtifactRef, runID string, params string) error {
	e.t.Helper()
	ctx := context.Background()
	return pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		out := steps.Output{PipelineRunID: runID, StepID: "pls_import", Name: "dataset", Artifact: ref,
			Spec: steps.Spec{Kind: "dataset_import", KindVersion: "2", Params: json.RawMessage(params)}}
		drafts, err := e.admin.StepHooks.Run(ctx, tx, out)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, auth.Actor{Kind: auth.KindAutomation, ID: runID}, nil, drafts)
	})
}

type sourceView struct {
	ID              string      `json:"id"`
	Name            string      `json:"name"`
	Licence         string      `json:"licence"`
	Kind            string      `json:"kind"`
	Languages       []string    `json:"languages"`
	TrainingCleared bool        `json:"trainingCleared"`
	ClearedBy       *auth.Actor `json:"clearedBy"`
	Archived        bool        `json:"archived"`
	Rev             int         `json:"rev"`
	Utterances      int         `json:"utterances"`
	Hours           float64     `json:"hours"`
	Datasets        []string    `json:"datasets"`
}

type uttView struct {
	ID          string `json:"id"`
	ContentHash string `json:"contentHash"`
	SourceName  string `json:"sourceName"`
	Language    string `json:"language"`
	Speaker     string `json:"speaker"`
	Split       string `json:"split"`
	Transcripts []struct {
		Text, Origin string
	} `json:"transcripts"`
	Fingerprints map[string]string `json:"fingerprints"`
	Datasets     []struct {
		DatasetVersionID string `json:"datasetVersionId"`
		Split            string `json:"split"`
	} `json:"datasets"`
}

type uttList struct {
	Items []uttView `json:"items"`
	Next  string    `json:"next"`
}

type datasetView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	State       string   `json:"state"`
	Fingerprint string   `json:"fingerprint"`
	Tags        []string `json:"tags"`
	Licence     string   `json:"licence"`
	Dataset     struct {
		Source     string   `json:"source"`
		Locales    []string `json:"locales"`
		Hours      float64  `json:"hours"`
		Utterances int      `json:"utterances"`
		Fixture    bool     `json:"fixture"`
		SourceIds  []string `json:"sourceIds"`
		EvalOnly   bool     `json:"evalOnly"`
		Splits     []struct {
			Name       string `json:"name"`
			Utterances int    `json:"utterances"`
			Speakers   int    `json:"speakers"`
		} `json:"splits"`
		Artifact struct{ Hash, Type string } `json:"artifact"`
		Lineage  struct {
			PipelineRunID string `json:"pipelineRunId"`
			StepKind      string `json:"stepKind"`
		} `json:"lineage"`
	} `json:"dataset"`
}

func startData(t *testing.T) (*env, *cas.Store) {
	t.Helper()
	// Use the environment's own store: both test servers register their dataset hooks on the shared registry.
	var store *cas.Store
	e := startWith(t, func(c *Config) { store = c.CAS })
	return e, store
}

func (e *env) datasetIn(collection string) datasetView {
	e.t.Helper()
	var l struct{ Items []datasetView }
	e.ok(e.do("GET", "/api/registry/datasets?collection="+collection, ""), 200, &l)
	if len(l.Items) != 1 {
		e.t.Fatalf("%s: %d versions", collection, len(l.Items))
	}
	return l.Items[0]
}

func TestDatasetHookRegistersAnImport(t *testing.T) {
	e, store := startData(t)
	ref := artifact(t, store, fleursHeader("fleurs-he"), heUtts)
	if err := e.runHook(ref, "plr_1", `{"name":"fleurs-he-test"}`); err != nil {
		t.Fatal(err)
	}

	// The step's name parameter wins over the header's name.
	ds := e.datasetIn("dataset/fleurs-he-test")
	if ds.State != "frozen" || ds.Licence != "CC-BY-4.0" || ds.Dataset.Utterances != 3 || ds.Dataset.Fixture ||
		ds.Dataset.Source != "hf://datasets/google/fleurs" || ds.Dataset.Artifact.Hash != ref.Hash ||
		ds.Dataset.Lineage.PipelineRunID != "plr_1" || ds.Dataset.Lineage.StepKind != "dataset_import@2" ||
		len(ds.Dataset.Locales) != 1 || ds.Dataset.Locales[0] != "he-IL" || len(ds.Dataset.Splits) != 2 ||
		ds.Dataset.Splits[0].Name != "train" || ds.Dataset.Splits[0].Utterances != 2 || ds.Dataset.Splits[0].Speakers != 2 {
		t.Fatalf("dataset version: %+v", ds)
	}
	want := strings.Join([]string{"eval-only", "locale:he-IL", "source:fleurs"}, ",")
	if strings.Join(ds.Tags, ",") != want {
		t.Errorf("tags %v, want %s (fleurs is not cleared yet)", ds.Tags, want)
	}

	var src sourceView
	resp := e.ok(e.do("GET", "/api/registry/sources/fleurs", ""), 200, &src)
	if !strings.HasPrefix(src.ID, "src_") || src.TrainingCleared || src.Kind != "public" || src.Utterances != 3 ||
		len(src.Datasets) != 1 || src.Datasets[0] != ds.ID || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("source: %+v", src)
	}
	if len(ds.Dataset.SourceIds) != 1 || ds.Dataset.SourceIds[0] != src.ID {
		t.Errorf("sourceIds %v", ds.Dataset.SourceIds)
	}
	if n := e.count("SELECT count(*) FROM transcripts WHERE origin = 'human'"); n != 3 {
		t.Errorf("%d transcripts", n)
	}
	types := eventTypes(e.events("entity.source." + src.ID + ",entity.dataset_version." + ds.ID))
	if strings.Join(types, ",") != "source.created,dataset_version.registered" {
		t.Errorf("events %v", types)
	}

	// The fingerprint is the content's: the same utterances in another order and with other lineage are the same version.
	shuffled := []fixtureUtt{heUtts[2], heUtts[0], heUtts[1]}
	ref2 := artifact(t, store, fleursHeader("fleurs-he"), shuffled)
	if ref2.Hash == ref.Hash {
		t.Fatal("the shuffled artifact should be a different artifact")
	}
	if err := e.runHook(ref2, "plr_2", `{"name":"fleurs-he-test"}`); err != nil {
		t.Fatal(err)
	}
	if again := e.datasetIn("dataset/fleurs-he-test"); again.ID != ds.ID || again.Fingerprint != ds.Fingerprint {
		t.Errorf("re-import registered %s (%s), want %s", again.ID, again.Fingerprint, ds.ID)
	}
	if n := e.count("SELECT count(*) FROM utterances"); n != 3 {
		t.Errorf("%d utterances after the re-import", n)
	}
	if n := e.count("SELECT count(*) FROM dataset_utterances"); n != 3 {
		t.Errorf("%d memberships after the re-import", n)
	}

	// A second corpus version sharing two utterances: they are reused, one is new; a changed transcript is a new
	// transcript and a new fingerprint.
	more := []fixtureUtt{heUtts[0], {"b", "מה שלומך היום?", "train", "he-IL", "spk2"}, {"d", "להתראות.", "test", "he-IL", ""}}
	if err := e.runHook(artifact(t, store, fleursHeader("fleurs-he-2"), more), "plr_3", `{}`); err != nil {
		t.Fatal(err)
	}
	ds2 := e.datasetIn("dataset/fleurs-he-2")
	if ds2.Fingerprint == ds.Fingerprint || ds2.Dataset.Utterances != 3 {
		t.Errorf("second version %+v", ds2)
	}
	if n := e.count("SELECT count(*) FROM utterances"); n != 4 {
		t.Errorf("%d utterances, want 4 (dedupe by content hash)", n)
	}
	if n := e.count("SELECT count(*) FROM transcripts"); n != 5 {
		t.Errorf("%d transcripts, want 5", n)
	}

	// Utterances: filters and paging.
	var page uttList
	e.ok(e.do("GET", "/api/registry/utterances?dataset="+ds.ID+"&split=train", ""), 200, &page)
	if len(page.Items) != 2 || page.Items[0].Split != "train" || page.Next != "" || page.Items[0].SourceName != "fleurs" ||
		len(page.Items[0].Transcripts) != 1 || page.Items[0].Transcripts[0].Origin != "human" {
		t.Fatalf("train split: %+v", page)
	}
	var all []uttView
	after := ""
	for i := 0; i < 5; i++ {
		e.ok(e.do("GET", "/api/registry/utterances?source=fleurs&language=he&limit=3"+after, ""), 200, reset(&page))
		all = append(all, page.Items...)
		if page.Next == "" {
			break
		}
		after = "&after=" + page.Next
	}
	if len(all) != 4 || all[0].ID >= all[3].ID {
		t.Fatalf("paged %d utterances: %+v", len(all), all)
	}
	e.ok(e.do("GET", "/api/registry/utterances?language=ru-RU", ""), 200, reset(&page))
	if len(page.Items) != 0 {
		t.Errorf("ru-RU: %+v", page.Items)
	}
	expectProblem(t, e.do("GET", "/api/registry/utterances?split=train", ""), 422, "validation-failed")

	var u uttView
	e.ok(e.do("GET", "/api/registry/utterances/"+all[1].ContentHash, ""), 200, &u)
	if u.ID != all[1].ID || u.Fingerprints["audio-b3"] != u.ContentHash || len(u.Datasets) != 2 || len(u.Transcripts) != 2 {
		t.Errorf("utterances.get by hash: %+v", u)
	}
	expectProblem(t, e.do("GET", "/api/registry/utterances/utt_nope", ""), 404, "not-found")
}

func TestDatasetHookRefusesBadArtifacts(t *testing.T) {
	e, store := startData(t)
	for _, tc := range []struct {
		name string
		mut  func(*data.Header, *[]fixtureUtt)
		want string
	}{
		{"no licence", func(h *data.Header, _ *[]fixtureUtt) { h.Source.Licence = "" }, "licence"},
		{"bad split rule", func(h *data.Header, _ *[]fixtureUtt) { h.SplitRule = "random" }, "splitRule"},
		{"bad split", func(_ *data.Header, u *[]fixtureUtt) { (*u)[0].split = "dev" }, "split"},
		{"duplicate audio", func(_ *data.Header, u *[]fixtureUtt) { *u = append(*u, (*u)[0]) }, "appears once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, utts := fleursHeader("bad"), append([]fixtureUtt(nil), heUtts...)
			tc.mut(&h, &utts)
			err := e.runHook(artifact(t, store, h, utts), "plr_bad", `{}`)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	if n := e.count("SELECT count(*) FROM sources"); n != 0 {
		t.Errorf("a failed import left %d sources", n)
	}
	// Counts in the header must match the manifest.
	h := fleursHeader("bad")
	ref := artifact(t, store, h, heUtts)
	m, _ := store.ReadManifest(ref.Hash)
	h.Counts, h.Hours = map[string]int{"train": 5}, 3*2.5/3600
	hb, _ := store.PutBytes(mustJSON(t, h))
	for i := range m.Files {
		if m.Files[i].Path == data.HeaderFile {
			m.Files[i].Hash = hb
		}
	}
	bad, _ := store.PutManifest(m)
	if err := e.runHook(steps.ArtifactRef{Hash: bad, Type: data.ArtifactType}, "plr_bad", `{}`); err == nil ||
		!strings.Contains(err.Error(), "5 train utterances") {
		t.Fatalf("counts mismatch: %v", err)
	}
}

func TestEvalOnlyDatasetsAreRefusedForTraining(t *testing.T) {
	e, store := startData(t)
	startSeededCompute(t, e)
	e.newProject("hebrew")
	if err := e.runHook(artifact(t, store, fleursHeader("fleurs-he"), heUtts), "plr_1", `{}`); err != nil {
		t.Fatal(err)
	}
	golden := fleursHeader("replay-golden-he")
	golden.EvalOnly, golden.Tags, golden.SplitRule = true, []string{"golden", "replay"}, "all-test"
	gUtts := []fixtureUtt{{"g1", "אחת.", "test", "he-IL", ""}, {"g2", "שתיים.", "test", "he-IL", ""}}
	if err := e.runHook(artifact(t, store, golden, gUtts), "plr_2", `{}`); err != nil {
		t.Fatal(err)
	}
	g := e.datasetIn("dataset/replay-golden-he")
	if !g.Dataset.EvalOnly || strings.Join(g.Tags, ",") != "eval-only,golden,locale:he-IL,replay,source:fleurs" {
		t.Fatalf("golden version: %+v", g)
	}

	mix := func(ds string) string {
		return `{"name":"m-` + strings.ReplaceAll(ds, "/", "-") + `","groups":[{"name":"target","datasets":["` + ds + `"]}]}`
	}
	p := expectProblem(t, e.do("POST", "/api/projects/hebrew/mixes", mix("dataset/fleurs-he"), "Idempotency-Key", e.key()), 422, "eval-only-dataset")
	if !strings.Contains(p.Detail, "source fleurs is not cleared") || len(p.Errors) != 1 || p.Errors[0].Path != "/groups/0/datasets/0" {
		t.Errorf("mix refusal: %+v", p)
	}
	expectProblem(t, e.do("POST", "/api/projects/hebrew/mixes:preview", mix("dataset/fleurs-he"), "Idempotency-Key", e.key()), 422, "eval-only-dataset")
	expectProblem(t, e.do("POST", "/api/projects/hebrew/runs?dryRun=true", `{"datasets":["dataset/fleurs-he"]}`, "Idempotency-Key", e.key()), 422, "eval-only-dataset")

	// A person clears the source: its versions become trainable without a re-import; a golden set never does.
	var src sourceView
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &src)
	if !src.TrainingCleared || src.ClearedBy == nil || src.ClearedBy.ID != "usr_admin" || src.Rev != 2 {
		t.Fatalf("cleared source: %+v", src)
	}
	e.ok(e.do("POST", "/api/projects/hebrew/mixes", mix("dataset/fleurs-he"), "Idempotency-Key", e.key()), 201, nil)
	e.ok(e.do("POST", "/api/projects/hebrew/runs?dryRun=true", `{"datasets":["dataset/fleurs-he"]}`, "Idempotency-Key", e.key()), 200, nil)
	p = expectProblem(t, e.do("POST", "/api/projects/hebrew/mixes", mix("dataset/replay-golden-he"), "Idempotency-Key", e.key()), 422, "eval-only-dataset")
	if !strings.Contains(p.Detail, "evaluation only") {
		t.Errorf("golden refusal: %+v", p)
	}
	// The phase-1 fixtures have no sources and stay trainable.
	e.ok(e.do("POST", "/api/projects/hebrew/mixes", mix("dataset/fleurs-he-smoke"), "Idempotency-Key", e.key()), 201, nil)
}

// startSeededCompute seeds compute (runs.new estimates need a card), as startSeeded does.
func startSeededCompute(t *testing.T, e *env) {
	t.Helper()
	if _, err := compute.Seed(t.Context(), e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
}

func TestSourcesEditAndArchive(t *testing.T) {
	e, store := startData(t)
	if err := e.runHook(artifact(t, store, fleursHeader("fleurs-he"), heUtts), "plr_1", `{}`); err != nil {
		t.Fatal(err)
	}
	// An agent's edit (clearing included) waits for a person.
	var a accepted
	e.ok(e.agent("PATCH", "/api/registry/sources/fleurs", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 202, &a)
	if !strings.HasPrefix(a.ApprovalID, "apr_") {
		t.Fatalf("agent edit: %+v", a)
	}
	var src sourceView
	e.ok(e.do("GET", "/api/registry/sources/fleurs", ""), 200, &src)
	if src.TrainingCleared || src.Rev != 1 {
		t.Fatalf("the agent's edit applied before approval: %+v", src)
	}
	// An agent never archives (preset rule no-deletes).
	expectProblem(t, e.agent("POST", "/api/registry/sources/fleurs:archive", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 403, "policy-denied")

	// A person edits: licence and description; stale revisions, empty licences and unknown sources fail.
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs", `{"licence":"CC-BY-4.0 (attribution: FLEURS)","description":"FLEURS"}`,
		"Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &src)
	if src.Licence != "CC-BY-4.0 (attribution: FLEURS)" || src.Rev != 2 {
		t.Fatalf("edited: %+v", src)
	}
	p := expectProblem(t, e.do("PATCH", "/api/registry/sources/fleurs", `{"description":"x"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 412, "precondition-failed")
	if p.CurrentRev == nil || *p.CurrentRev != 2 {
		t.Errorf("412 currentRev %v", p.CurrentRev)
	}
	expectProblem(t, e.do("PATCH", "/api/registry/sources/fleurs", `{"licence":"  "}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 422, "validation-failed")
	expectProblem(t, e.do("PATCH", "/api/registry/sources/nope", `{"description":"x"}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 404, "not-found")
	expectProblem(t, e.do("PATCH", "/api/registry/sources/fleurs", `{"description":"x"}`, "Idempotency-Key", e.key()), 428, "precondition-required")

	// A dry run changes nothing.
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs?dryRun=true", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &src)
	e.ok(e.do("GET", "/api/registry/sources/"+src.ID, ""), 200, &src)
	if src.TrainingCleared {
		t.Fatal("the dry run cleared the source")
	}

	// A re-import must carry the source's licence as the registry has it.
	err := e.runHook(artifact(t, store, fleursHeader("fleurs-he"), heUtts), "plr_2", `{}`)
	if err == nil || !strings.Contains(err.Error(), "licence") {
		t.Fatalf("licence mismatch: %v", err)
	}

	// Archive: out of the default list, no edits, no imports.
	e.ok(e.do("POST", "/api/registry/sources/fleurs:archive", "", "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, &src)
	if !src.Archived || src.Rev != 3 {
		t.Fatalf("archived: %+v", src)
	}
	var list struct{ Items []sourceView }
	e.ok(e.do("GET", "/api/registry/sources", ""), 200, &list)
	if len(list.Items) != 0 {
		t.Errorf("sources.list shows archived: %+v", list.Items)
	}
	e.ok(e.do("GET", "/api/registry/sources?archived=true&kind=public&language=he", ""), 200, &list)
	if len(list.Items) != 1 {
		t.Errorf("sources.list archived=true: %+v", list.Items)
	}
	expectProblem(t, e.do("PATCH", "/api/registry/sources/fleurs", `{"description":"x"}`, "Idempotency-Key", e.key(), "If-Match", `"3"`), 409, "conflict")
	h := fleursHeader("fleurs-he")
	h.Source.Licence = "CC-BY-4.0 (attribution: FLEURS)"
	if err := e.runHook(artifact(t, store, h, heUtts), "plr_3", `{}`); err == nil || !strings.Contains(err.Error(), "archived") {
		t.Fatalf("import into an archived source: %v", err)
	}
	types := eventTypes(e.events("entity.source." + src.ID))
	if strings.Join(types, ",") != "source.created,source.edited,source.archived" {
		t.Errorf("source events %v", types)
	}
}
