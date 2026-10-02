//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Golden sets, scoring normalizers and the leakage checks against the real control plane (phase 3 · stream G):
// the seeded normalizers; goldenSets.freeze refusals, dry run, the registry-scope approval and its replay; leakage at
// freeze, in mixes, runs and pipelines, and at projects.adopt.

type normalizerView struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	State      string   `json:"state"`
	Tags       []string `json:"tags"`
	Normalizer struct {
		Locale      string `json:"locale"`
		Unicode     string `json:"unicode"`
		Casefold    bool   `json:"casefold"`
		Punctuation string `json:"punctuation"`
		RemoveMarks bool   `json:"removeMarks"`
		Mappings    []struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"mappings"`
		Numbers string `json:"numbers"`
	} `json:"normalizer"`
}

type goldenSetView struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	State     string   `json:"state"`
	Tags      []string `json:"tags"`
	Licence   string   `json:"licence"`
	GoldenSet struct {
		DatasetVersionID    string  `json:"datasetVersionId"`
		DatasetHash         string  `json:"datasetHash"`
		NormalizerVersionID string  `json:"normalizerVersionId"`
		Locale              string  `json:"locale"`
		Domain              string  `json:"domain"`
		Utterances          int     `json:"utterances"`
		Hours               float64 `json:"hours"`
		Fingerprint         string  `json:"fingerprint"`
		Groups              string  `json:"groups"`
		ApprovalID          string  `json:"approvalId"`
	} `json:"goldenSet"`
}

func (e *env) normalizer(name string) normalizerView {
	e.t.Helper()
	var l struct{ Items []normalizerView }
	e.ok(e.do("GET", "/api/registry/normalizers?collection="+name, ""), 200, &l)
	if len(l.Items) != 1 {
		e.t.Fatalf("%s: %d versions", name, len(l.Items))
	}
	return l.Items[0]
}

// freezeGolden asks for a freeze (202 with a registry-scope approval), has the admin approve it and returns the
// replay's status and golden set.
func (e *env) freezeGolden(body string) (int, goldenSetView) {
	e.t.Helper()
	var a accepted
	resp := e.ok(e.do("POST", "/api/registry/golden-sets:freeze", body, "Idempotency-Key", e.key()), 202, &a)
	if resp.Header.Get("Cadence-Approval-Id") != a.ApprovalID {
		e.t.Fatalf("approval header %q, body %+v", resp.Header.Get("Cadence-Approval-Id"), a)
	}
	ap := e.approval(a.ApprovalID)
	if ap.Scope != "registry" || ap.ProjectID != "" || ap.Rule != "golden-set-freeze" || ap.Operation != "goldenSets.freeze" {
		e.t.Fatalf("approval %+v", ap)
	}
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+a.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	if decided.State != "approved" || decided.Result == nil {
		e.t.Fatalf("decided %+v", decided)
	}
	var gs goldenSetView
	if err := json.Unmarshal(decided.Result.Body, &gs); err != nil {
		e.t.Fatalf("replay result %s: %v", decided.Result.Body, err)
	}
	if gs.GoldenSet.ApprovalID == "" {
		e.t.Fatalf("golden set without its approval: %+v", gs)
	}
	return decided.Result.Status, gs
}

// uttID finds the utterance of a fixture audio name (artifact writes "RIFF-fake-audio:<name>").
func (e *env) uttID(audio string) string {
	e.t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), "SELECT id FROM utterances WHERE content_hash = $1",
		cas.Hash([]byte("RIFF-fake-audio:"+audio))).Scan(&id); err != nil {
		e.t.Fatalf("utterance %s: %v", audio, err)
	}
	return id
}

// nearDuplicate gives two utterances a shared acoustic fingerprint, as a phase-4 fingerprint step would.
func (e *env) nearDuplicate(a, b, value string) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO utterance_fingerprints (utterance_id, kind, value)
		VALUES ($1, 'acoustic', $3), ($2, 'acoustic', $3)`, e.uttID(a), e.uttID(b), value); err != nil {
		e.t.Fatal(err)
	}
}

func TestNormalizersAreSeeded(t *testing.T) {
	e := start(t)
	var l struct{ Items []normalizerView }
	e.ok(e.do("GET", "/api/registry/normalizers", ""), 200, &l)
	if len(l.Items) != 2 {
		t.Fatalf("normalizers %+v", l.Items)
	}
	basic, he := e.normalizer("normalizer/basic"), e.normalizer("normalizer/he-il")
	if basic.State != "frozen" || basic.Normalizer.Locale != "*" || basic.Normalizer.Unicode != "NFKC" || !basic.Normalizer.Casefold ||
		basic.Normalizer.Punctuation != "strip" || basic.Normalizer.RemoveMarks || len(basic.Normalizer.Mappings) != 0 {
		t.Errorf("basic %+v", basic)
	}
	maps := map[string]string{}
	for _, m := range he.Normalizer.Mappings {
		maps[m.From] = m.To
	}
	if he.Normalizer.Locale != "he-IL" || !he.Normalizer.RemoveMarks || maps["\u05f4"] != "" || maps["\u05be"] != " " ||
		maps["\u200f"] != "" || strings.Join(he.Tags, ",") != "locale:he-IL" {
		t.Errorf("he-il %+v", he)
	}
	var got normalizerView
	e.ok(e.do("GET", "/api/registry/normalizers/"+he.ID, ""), 200, &got)
	if got.ID != he.ID || got.Name != "normalizer/he-il" {
		t.Errorf("get %+v", got)
	}
	expectProblem(t, e.do("GET", "/api/registry/normalizers/"+e.datasetIn("dataset/fleurs-he-smoke").ID, ""), 404, "not-found")
}

func TestGoldenSetFreezeNeedsTheAdmin(t *testing.T) {
	e, store := startData(t)
	if err := e.runHook(artifact(t, store, fleursHeader("fleurs-he"), heUtts), "plr_1", `{}`); err != nil {
		t.Fatal(err)
	}
	golden := fleursHeader("replay-golden-he")
	golden.EvalOnly, golden.Tags, golden.SplitRule = true, []string{"golden", "replay", "domain:read-speech"}, "all-test"
	gold := artifact(t, store, golden, []fixtureUtt{{"g1", "אחת.", "test", "he-IL", ""}, {"g2", "שתיים.", "test", "he-IL", ""}})
	if err := e.runHook(gold, "plr_2", `{}`); err != nil {
		t.Fatal(err)
	}
	goldID := e.datasetIn("dataset/replay-golden-he").ID
	basic := e.normalizer("normalizer/basic")
	freeze := "/api/registry/golden-sets:freeze"

	// Refused before anyone is asked: a trainable dataset, an unknown normalizer, a resampling unit the data lacks.
	expectProblem(t, e.do("POST", freeze, `{"datasetVersionId":"dataset/fleurs-he"}`, "Idempotency-Key", e.key()), 422, "golden-set-not-eval-only")
	expectProblem(t, e.do("POST", freeze, `{"datasetVersionId":"`+goldID+`","normalizerVersionId":"normalizer/nope"}`,
		"Idempotency-Key", e.key()), 422, "normalizer-unknown")
	expectProblem(t, e.do("POST", freeze, `{"datasetVersionId":"`+goldID+`","normalizerVersionId":"`+goldID+`"}`,
		"Idempotency-Key", e.key()), 422, "normalizer-unknown")
	p := expectProblem(t, e.do("POST", freeze, `{"datasetVersionId":"`+goldID+`","groups":"speaker"}`, "Idempotency-Key", e.key()),
		422, "validation-failed")
	if len(p.Errors) != 1 || p.Errors[0].Path != "/groups" {
		t.Errorf("groups refusal %+v", p)
	}
	expectProblem(t, e.do("POST", freeze, `{"datasetVersionId":"`+goldID+`","name":"Upper/Case"}`, "Idempotency-Key", e.key()),
		422, "validation-failed")
	if n := e.count("SELECT count(*) FROM approvals"); n != 0 {
		t.Fatalf("%d approvals requested for refused freezes", n)
	}

	// A dry run checks everything and answers the would-be golden set; the real call would wait for the admin.
	var dry goldenSetView
	resp := e.ok(e.do("POST", freeze+"?dryRun=true", `{"datasetVersionId":"dataset/replay-golden-he"}`, "Idempotency-Key", e.key()), 200, &dry)
	if !strings.Contains(resp.Header.Get("Cadence-Policy"), "rule=golden-set-freeze") {
		t.Errorf("Cadence-Policy %q", resp.Header.Get("Cadence-Policy"))
	}
	g := dry.GoldenSet
	if dry.Name != "golden-set/replay-golden-he" || dry.Kind != "golden_set" || dry.State != "frozen" || g.DatasetVersionID != goldID ||
		g.DatasetHash != gold.Hash || g.NormalizerVersionID != basic.ID || g.Locale != "he-IL" || g.Domain != "read-speech" ||
		g.Utterances != 2 || g.Groups != "utterance" || g.Fingerprint != e.datasetIn("dataset/replay-golden-he").Fingerprint ||
		strings.Join(dry.Tags, ",") != "domain:read-speech,golden,locale:he-IL" || dry.Licence != "CC-BY-4.0" {
		t.Errorf("dry run %+v", dry)
	}
	if n := e.count("SELECT count(*) FROM golden_sets") + e.count("SELECT count(*) FROM registry_collections WHERE kind = 'golden_set'"); n != 0 {
		t.Fatalf("the dry run wrote %d rows", n)
	}

	// The real call waits for a registry-scope approval; the admin approves and the replay registers it.
	status, gs := e.freezeGolden(`{"datasetVersionId":"` + goldID + `","normalizerVersionId":"normalizer/he-IL"}`)
	if status != 201 || gs.Name != "golden-set/replay-golden-he" || gs.GoldenSet.NormalizerVersionID != e.normalizer("normalizer/he-il").ID {
		t.Fatalf("frozen %d %+v", status, gs)
	}
	var list struct{ Items []goldenSetView }
	e.ok(e.do("GET", "/api/registry/golden-sets", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].ID != gs.ID {
		t.Fatalf("golden sets %+v", list.Items)
	}
	var got goldenSetView
	e.ok(e.do("GET", "/api/registry/golden-sets/"+gs.ID, ""), 200, &got)
	if got.GoldenSet != gs.GoldenSet {
		t.Errorf("get %+v, frozen %+v", got, gs)
	}
	evs := e.events("entity.golden_set." + gs.ID)
	if len(evs) != 1 || evs[0].Type != "golden_set.frozen" || evs[0].CausedBy == nil || evs[0].CausedBy.ApprovalID != gs.GoldenSet.ApprovalID {
		t.Fatalf("events %+v", evs)
	}
	if e.count("SELECT count(*) FROM golden_sets WHERE dataset_version_id = '"+goldID+"'") != 1 {
		t.Fatal("golden_sets row missing")
	}

	// Freezing the same content again answers the version already there.
	status, again := e.freezeGolden(`{"datasetVersionId":"` + goldID + `","normalizerVersionId":"normalizer/he-il"}`)
	if status != 200 || again.ID != gs.ID {
		t.Fatalf("refreeze %d %+v", status, again)
	}
}

func TestGoldenSetLeakage(t *testing.T) {
	e, store := startData(t)
	startSeededCompute(t, e)
	if err := pipelinestest.Register(context.Background(), e.pool,
		evalOnlyKind("fit", "dataset", "training"), evalOnlyKind("fitmix", "mix", "training")); err != nil {
		t.Fatal(err)
	}
	hebrew := e.newProject("hebrew")
	e.newProject("other")
	e.commitPipeline("hebrew", "fit", "name: fit\ninputs: {data: dataset}\nsteps:\n  - {id: fit, kind: fit@1, in: {data: $inputs.data}}\n")
	e.commitPipeline("hebrew", "fitmix", "name: fitmix\ninputs: {mix: mix}\nsteps:\n  - {id: fit, kind: fitmix@1, in: {data: $inputs.mix}}\n")

	evalOnly := func(name string, utts ...string) steps.ArtifactRef {
		h := fleursHeader(name)
		h.EvalOnly, h.SplitRule = true, "all-test"
		var list []fixtureUtt
		for _, u := range utts {
			list = append(list, fixtureUtt{u, "טקסט " + u, "test", "he-IL", ""})
		}
		ref := artifact(t, store, h, list)
		if err := e.runHook(ref, "plr_"+name, `{}`); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	trainable := func(name string, utts ...string) steps.ArtifactRef {
		var list []fixtureUtt
		for _, u := range utts {
			list = append(list, fixtureUtt{u, "טקסט " + u, "train", "he-IL", "spk-" + u})
		}
		ref := artifact(t, store, fleursHeader(name), list)
		if err := e.runHook(ref, "plr_"+name, `{}`); err != nil {
			t.Fatal(err)
		}
		return ref
	}
	corpus := trainable("fleurs-he", "a", "b", "h1")
	corpusID := e.datasetIn("dataset/fleurs-he").ID
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)

	// At freeze: a test set sharing an utterance (and, through a fingerprint, another) with a trainable corpus.
	evalOnly("replay-golden-xx", "h1", "h2")
	e.nearDuplicate("h2", "b", "dup-h2")
	p := expectProblem(t, e.do("POST", "/api/registry/golden-sets:freeze", `{"datasetVersionId":"dataset/replay-golden-xx"}`,
		"Idempotency-Key", e.key()), 422, "golden-set-leakage")
	if len(p.Errors) != 1 || p.Errors[0].Path != "/overlaps/0" || !strings.Contains(p.Errors[0].Message, corpusID) ||
		!strings.Contains(p.Errors[0].Message, "shares 2 utterances") || !strings.Contains(p.Errors[0].Message, "1 only through a shared fingerprint") {
		t.Errorf("freeze leakage %+v", p)
	}

	// A disjoint test set freezes; afterwards nothing that holds its audio reaches training.
	evalOnly("replay-golden-he", "g1", "g2")
	_, gs := e.freezeGolden(`{"datasetVersionId":"dataset/replay-golden-he"}`)
	leaky := trainable("leaky", "g1", "x")
	leakyID := e.datasetIn("dataset/leaky").ID
	mix := func(ds string) string {
		return `{"name":"m-` + strings.ReplaceAll(ds, "/", "-") + `","groups":[{"name":"target","datasets":["` + ds + `"]}]}`
	}
	p = expectProblem(t, e.do("POST", "/api/projects/hebrew/mixes", mix("dataset/leaky"), "Idempotency-Key", e.key()), 422, "golden-set-leakage")
	if len(p.Errors) != 2 || p.Errors[0].Path != "/groups/0/datasets/0" || p.Errors[1].Path != "/overlaps/0" ||
		!strings.Contains(p.Errors[1].Message, gs.ID) || !strings.Contains(p.Errors[1].Message, "shares 1 utterance with") {
		t.Errorf("mix leakage %+v", p)
	}
	expectProblem(t, e.do("POST", "/api/projects/hebrew/mixes:preview", mix("dataset/leaky"), "Idempotency-Key", e.key()), 422, "golden-set-leakage")
	expectProblem(t, e.do("POST", "/api/projects/hebrew/runs?dryRun=true", `{"datasets":["dataset/leaky"]}`, "Idempotency-Key", e.key()),
		422, "golden-set-leakage")
	e.ok(e.do("POST", "/api/projects/hebrew/mixes", mix("dataset/fleurs-he"), "Idempotency-Key", e.key()), 201, nil)

	run := func(name, input, ref string, dryRun bool) *problem {
		resp := e.do("POST", fmt.Sprintf("/api/projects/hebrew/pipelines/%s:run?dryRun=%t", name, dryRun),
			`{"inputs":{"`+input+`":`+ref+`}}`, "Idempotency-Key", e.key(), "If-Match", "*")
		p := expectProblem(t, resp, 422, "golden-set-leakage")
		return &p
	}
	asRef := func(r steps.ArtifactRef) string { return fmt.Sprintf(`{"hash":%q,"type":%q}`, r.Hash, r.Type) }
	run("fit", "data", asRef(leaky), true)
	run("fit", "data", asRef(leaky), false)
	run("fitmix", "mix", fmt.Sprintf(`{"hash":%q,"type":"mix","meta":{"datasets":[%q]}}`, "b3:"+strings.Repeat("7", 64), leakyID), true)
	if n := e.count("SELECT count(*) FROM pipeline_runs"); n != 0 {
		t.Fatalf("%d pipeline runs started", n)
	}

	// Adoption re-runs the check: hebrew trained on the corpus, which a later fingerprint ties to the golden set.
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO pipeline_runs (id, project_id, pipeline, source, definition, actor)
		VALUES ('plr_trained', $1, 'fit', 'inline', '{}', '{}')`, hebrew.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO pipeline_steps (id, pipeline_run_id, project_id, step, position, kind,
		kind_version, inputs, resources) VALUES ('pls_trained', 'plr_trained', $1, 'fit', 0, 'fit', '1',
		jsonb_build_object('data', jsonb_build_object('hash', $2::text, 'type', 'dataset')), '{"jobKind":"training"}')`,
		hebrew.ID, corpus.Hash); err != nil {
		t.Fatal(err)
	}
	rev := func(slug string) int {
		var pr project
		e.ok(e.do("GET", "/api/projects/"+slug, ""), 200, &pr)
		return pr.Rev
	}
	adopt := func(slug string) *http.Response {
		return e.do("POST", "/api/projects/"+slug+":adopt", `{"version":"`+gs.ID+`"}`, "Idempotency-Key", e.key(),
			"If-Match", strconv.Quote(strconv.Itoa(rev(slug))))
	}
	e.nearDuplicate("g2", "a", "dup-g2")
	before := rev("hebrew")
	p = expectProblem(t, adopt("hebrew"), 422, "golden-set-leakage")
	if len(p.Errors) != 1 || !strings.Contains(p.Errors[0].Message, corpusID) || !strings.Contains(p.Errors[0].Message, gs.ID) ||
		!strings.Contains(p.Errors[0].Message, "1 only through a shared fingerprint") {
		t.Errorf("adoption leakage %+v", p)
	}
	if rev("hebrew") != before || e.count("SELECT count(*) FROM adoptions WHERE version_id = '"+gs.ID+"'") != 0 {
		t.Fatal("the refused adoption was written")
	}
	// A project that never trained on it adopts it.
	e.ok(adopt("other"), 200, nil)
}
