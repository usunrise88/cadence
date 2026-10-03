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
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

type evalInterval struct{ Value, Low, High float64 }

type evalView struct {
	ID, ProjectID, Status, Error, PrimaryProfile, PipelineRunID string
	Rev                                                         int
	Subject, Baseline                                           struct{ Kind, ID, ModelKey, Family, Source string }
	GoldenSets                                                  []struct {
		VersionID, Name string
		Replay          bool
	}
	Profiles []struct{ Name string }
	Decoding []struct {
		Index    int
		Boost    string
		Terms    int
		Artifact string
	}
	Progress struct{ CellsTotal, CellsDone, CellsCached int }
	Estimate struct {
		GpuHours, AudioHours float64
		CellsToCompute       int
	}
	Gate *struct {
		Verdict, GatesSha string
		Checks            []struct {
			Kind, GoldenSet, State, Message string
			Delta                           *evalInterval
			BaselineWer, CandidateWer       *float64
		}
	}
	Cells []struct {
		ID, Role, GoldenSetVersionID, Profile, State, RecordID, Scores, DecodingHash, ModelKey string
		DecodingIndex                                                                          int
		Summary                                                                                *struct {
			Wer           float64
			Sub, Del, Ins int
			RefWords      int
			Utterances    int
		}
		Delta *struct {
			BaselineCellID string
			Wer, Del, Ins  evalInterval
			Significant    bool
			Groups         int
			Error          string
		}
		Worst []struct {
			Audio, Ref, Hyp string
			Errors          int
			Wer             float64
			Ops             [][]string
		}
	}
}

func (e *env) eval(id, query string) evalView {
	e.t.Helper()
	var v evalView
	e.ok(e.do("GET", "/api/evals/"+id+query, ""), 200, &v)
	return v
}

func (e *env) waitEval(id, status string) evalView {
	e.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		v := e.eval(id, "")
		if v.Status == status {
			return v
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("eval %s is %s (%s), want %s", id, v.Status, v.Error, status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// evalProject seeds compute, a he-IL project of the fixture family with a trained checkpoint, and two adopted golden
// sets: a he-IL target set (three speakers) and a ru-RU replay set.
func evalProject(t *testing.T, e *env, slug string) (projectID, checkpointID string, golden map[string]string) {
	t.Helper()
	ctx := context.Background()
	if _, err := compute.Seed(ctx, e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
	p := e.createProject(`{"slug":"`+slug+`","name":"`+slug+`","locales":["he-IL"],"baseModel":"`+pipelinestest.BaseModel+`"}`, slug)
	e.commitPipeline(slug, "train-stage", pipelinestest.TrainStage)
	if _, err := pipelinestest.RegisterDataset(ctx, e.pool, e.admin.CAS, "fx-he", 2, false); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/projects/"+slug+"/mixes", `{"name":"he-mix","groups":[{"name":"he","datasets":["dataset/fx-he"]}]}`,
		"Idempotency-Key", e.key()), 201, nil)
	var cal struct{ PipelineRun struct{ ID string } }
	e.ok(e.do("POST", "/api/projects/"+slug+"/runs:calibrate", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`,
		"Idempotency-Key", e.key()), 201, &cal)
	e.waitPipelineRun(cal.PipelineRun.ID, "done")
	var r runView
	e.ok(e.do("POST", "/api/projects/"+slug+"/runs", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix"}`,
		"Idempotency-Key", e.key()), 201, &r)
	e.waitRun(r.ID, "done")
	cks := e.checkpoints(slug, r.ID)
	if len(cks) == 0 {
		t.Fatal("the run registered no checkpoint")
	}
	golden = map[string]string{}
	var utts []pipelinestest.GoldenUtterance
	for i := range 6 {
		utts = append(utts, pipelinestest.GoldenUtterance{Audio: fmt.Sprintf("b3:%064d", i), Speaker: fmt.Sprintf("spk%d", i%3),
			Ref: fmt.Sprintf("shalom this is call number %d of the golden set", i), DurationS: 3 + float64(i)})
	}
	for _, g := range []struct {
		name, locale string
		hours        float64
		n            int
	}{{"fx-golden-he", "he-IL", 0.5, 6}, {"fx-golden-ru", "ru-RU", 0.2, 3}} {
		id, err := pipelinestest.RegisterGoldenSet(ctx, e.pool, e.admin.CAS, g.name, g.locale, g.hours, utts[:g.n])
		if err != nil {
			t.Fatal(err)
		}
		golden[g.name] = id
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			_, err := registry.AdoptQuietly(ctx, tx, p.ID, []string{id}, auth.DevActor())
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	return p.ID, cks[0].ID, golden
}

// The evaluation loop on the in-process fake worker: evals.new plans the missing cells (materialize → transcribe →
// score) with an estimate, the scores hook writes eval records, the eval mirrors its pipeline run, deltas come with
// paired bootstrap intervals, records are reused across evals and projects, evals.gate gives a verdict from
// gates.yaml (gates.get|edit), models.register publishes a gated checkpoint, boosting is a decoding axis, and a failed
// cell fails the eval.
func TestEvalsEndToEnd(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.RegisterTraining(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	if err := pipelinestest.RegisterEvaluation(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	projectID, ckp, golden := evalProject(t, e, "evalp")
	newEval := func(slug, body string, query ...string) (int, []byte) {
		q := ""
		if len(query) > 0 {
			q = "?" + query[0]
		}
		resp := e.do("POST", "/api/projects/"+slug+"/evals"+q, body, "Idempotency-Key", e.key())
		defer func() { _ = resp.Body.Close() }()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return resp.StatusCode, raw
	}
	body := `{"subject":{"checkpointId":"` + ckp + `"},"baseline":"` + pipelinestest.BaseModel + `"}`

	expectProblem(t, e.do("POST", "/api/projects/evalp/evals", `{"subject":{}}`, "Idempotency-Key", e.key()), 422, "validation-failed")

	// The dry run: target and replay golden sets at the primary profile (the matrix ∩ the family: 80ms), four cells to
	// compute, the base model materialised first, 1.4 audio hours × 0.025 GPU-hours.
	var plan struct {
		Subject, Baseline struct{ Kind, ModelKey, Source string }
		Profiles          []struct{ Name string }
		PrimaryProfile    string
		Cells             []struct {
			Role, Profile string
			Cached        bool
		}
		CellsCached, CellsToCompute int
		Estimate                    struct{ GpuHours, AudioHours float64 }
		Steps                       []struct{ Step, Kind string }
	}
	status, raw := newEval("evalp", body, "dryRun=true")
	if status != 200 {
		t.Fatalf("dry run %d: %s", status, raw)
	}
	_ = json.Unmarshal(raw, &plan)
	if plan.Subject.Kind != "checkpoint" || plan.Baseline.Kind != "base_model" || plan.Baseline.Source != "request" ||
		!strings.HasPrefix(plan.Baseline.ModelKey, "base:") || len(plan.Profiles) != 1 || plan.PrimaryProfile != "80ms" ||
		len(plan.Cells) != 4 || plan.CellsCached != 0 || plan.CellsToCompute != 4 || plan.Estimate.AudioHours != 1.4 ||
		plan.Estimate.GpuHours != 0.035 || len(plan.Steps) != 9 || plan.Steps[2] != (struct{ Step, Kind string }{"materialize-m2", "fx_materialize@1"}) {
		t.Fatalf("plan %s", raw)
	}
	if n := e.count("SELECT count(*) FROM evals"); n != 0 {
		t.Fatalf("a dry run wrote %d evals", n)
	}

	// languages: a locale the models decode as another (a checkpoint fine-tuned under a neighbour's prompt) shows on
	// the golden set and changes the cells' decoding hash; a locale no golden set has is refused.
	type langPlan struct {
		GoldenSets []struct{ Locale, DecodeAs string }
		Cells      []struct{ GoldenSetVersionID, DecodingHash string }
	}
	var plain, mapped langPlan
	_ = json.Unmarshal(raw, &plain)
	mbody := strings.TrimSuffix(body, "}") + `,"languages":{"he-IL":"ar-AR"}}`
	status, mraw := newEval("evalp", mbody, "dryRun=true")
	if status != 200 {
		t.Fatalf("dry run with languages %d: %s", status, mraw)
	}
	_ = json.Unmarshal(mraw, &mapped)
	for i, g := range mapped.GoldenSets {
		if (g.Locale == "he-IL") != (g.DecodeAs == "ar-AR") {
			t.Fatalf("golden set %d decodes as %q: %s", i, g.DecodeAs, mraw)
		}
	}
	for i, c := range mapped.Cells {
		if (c.GoldenSetVersionID == golden["fx-golden-he"]) == (c.DecodingHash == plain.Cells[i].DecodingHash) {
			t.Fatalf("cell %d: decoding hash %s vs %s", i, c.DecodingHash, plain.Cells[i].DecodingHash)
		}
	}
	expectProblem(t, e.do("POST", "/api/projects/evalp/evals?dryRun=true", strings.TrimSuffix(body, "}")+`,"languages":{"xx-XX":"he-IL"}}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")

	// The real eval runs on the fake worker; the scores hook links every cell to a new record.
	status, raw = newEval("evalp", body)
	if status != 201 {
		t.Fatalf("evals.new %d: %s", status, raw)
	}
	var created evalView
	_ = json.Unmarshal(raw, &created)
	if !strings.HasPrefix(created.ID, "evl_") || created.PipelineRunID == "" || created.Progress.CellsTotal != 4 {
		t.Fatalf("created %s", raw)
	}
	ev := e.waitEval(created.ID, "done")
	if ev.Progress.CellsDone != 4 || e.count("SELECT count(*) FROM eval_records") != 4 {
		t.Fatalf("progress %+v", ev.Progress)
	}
	sub, base := -1, -1
	for i, c := range ev.Cells {
		if c.GoldenSetVersionID == golden["fx-golden-he"] && c.Role == "subject" {
			sub = i
		}
		if c.GoldenSetVersionID == golden["fx-golden-he"] && c.Role == "baseline" {
			base = i
		}
	}
	sc, bc := ev.Cells[sub], ev.Cells[base]
	// The trained checkpoint transcribes every word; the materialised base drops two of ten per utterance.
	if sc.State != "done" || sc.Summary == nil || sc.Summary.Wer != 0 || bc.Summary.Del != 12 || bc.Summary.RefWords != 60 ||
		sc.Delta == nil || sc.Delta.BaselineCellID != bc.ID || sc.Delta.Groups != 3 || !sc.Delta.Significant ||
		sc.Delta.Wer.Value >= 0 || sc.Delta.Wer.High >= 0 || bc.Delta != nil {
		t.Fatalf("cells %+v / %+v (delta %+v)", sc, bc, sc.Delta)
	}
	worst := e.eval(ev.ID, "?cell="+bc.ID+"&worst=2")
	if len(worst.Cells) != 1 || len(worst.Cells[0].Worst) != 2 || worst.Cells[0].Worst[0].Errors != 2 ||
		worst.Cells[0].Worst[0].Ops[0][0] != "D" {
		t.Fatalf("worst %+v", worst.Cells)
	}
	if n := e.count("SELECT count(*) FROM events WHERE topic = 'eval." + ev.ID + ".progress'"); n < 5 {
		t.Fatalf("%d progress events", n)
	}

	// models.register needs a gated eval.
	expectProblem(t, e.do("POST", "/api/projects/evalp/models:register", `{"checkpointId":"`+ckp+`"}`, "Idempotency-Key", e.key()), 409, "gate-not-passed")

	// The same eval again: every cell is cached, the eval is done at once without a pipeline run.
	status, raw = newEval("evalp", body)
	var again evalView
	_ = json.Unmarshal(raw, &again)
	if status != 201 || again.Status != "done" || again.PipelineRunID != "" || again.Progress.CellsCached != 4 {
		t.Fatalf("cached eval %d %s", status, raw)
	}

	// Another project reuses the baseline's records: the base model against itself on the he set computes nothing.
	e.createProject(`{"slug":"other","name":"other","locales":["he-IL"],"baseModel":"`+pipelinestest.BaseModel+`"}`, "other")
	status, raw = newEval("other", `{"subject":{"baseModelVersionId":"`+pipelinestest.BaseModel+`"},"baseline":"`+pipelinestest.BaseModel+
		`","goldenSets":["golden-set/fx-golden-he"]}`)
	var cross evalView
	_ = json.Unmarshal(raw, &cross)
	if status != 201 || cross.Status != "done" || cross.Progress.CellsCached != 2 || cross.Cells[0].Delta == nil ||
		cross.Cells[0].Delta.Wer != (evalInterval{}) {
		t.Fatalf("cross-project eval %d %s", status, raw)
	}

	// The gate with the defaults: the he set beats the baseline, the ru replay set does not regress.
	var gated evalView
	e.ok(e.do("POST", "/api/evals/"+ev.ID+":gate", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, ev.Rev)), 200, &gated)
	if gated.Gate == nil || gated.Gate.Verdict != "passed" || gated.Gate.GatesSha != "" || len(gated.Gate.Checks) != 3 {
		t.Fatalf("verdict %+v", gated.Gate)
	}
	// The base model against itself cannot beat itself: inconclusive, so the verdict fails.
	var selfGate evalView
	e.ok(e.do("POST", "/api/evals/"+cross.ID+":gate", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, cross.Rev)), 200, &selfGate)
	if selfGate.Gate.Verdict != "failed" || selfGate.Gate.Checks[0].State != "inconclusive" {
		t.Fatalf("self verdict %+v", selfGate.Gate)
	}

	// gates.get answers the defaults; gates.edit validates and commits gates.yaml.
	var g struct {
		Exists          bool
		Commit, Content string
		Config          struct{ PrimaryProfile string }
		Departures      []struct{ Param string }
	}
	resp := e.ok(e.do("GET", "/api/projects/evalp/gates", ""), 200, &g)
	if g.Exists || resp.Header.Get("ETag") != `"defaults"` || g.Config.PrimaryProfile != "80ms" || !strings.Contains(g.Content, "primaryProfile") {
		t.Fatalf("gates %+v", g)
	}
	bad := `{"content":"target: { goldenSets: [golden-set/not-adopted] }\n"}`
	expectProblem(t, e.do("PATCH", "/api/projects/evalp/gates?dryRun=true", bad, "Idempotency-Key", e.key(), "If-Match", `"defaults"`), 422, "gate-config-invalid")
	expectProblem(t, e.do("PATCH", "/api/projects/evalp/gates", `{"content":"primaryProfile: [1]\n"}`, "Idempotency-Key", e.key(), "If-Match", `"defaults"`), 422, "gate-config-invalid")
	good := `{"config":{"primaryProfile":"80ms","target":{"goldenSets":["golden-set/fx-golden-he"]},"replay":{"goldenSets":["golden-set/fx-golden-r*"],"maxRegression":0.01}}}`
	resp = e.ok(e.do("PATCH", "/api/projects/evalp/gates", good, "Idempotency-Key", e.key(), "If-Match", `"defaults"`), 200, &g)
	if !g.Exists || g.Commit == "" || resp.Header.Get("ETag") != `"`+g.Commit+`"` || len(g.Departures) != 3 {
		t.Fatalf("edited gates %+v", g)
	}
	expectProblem(t, e.do("PATCH", "/api/projects/evalp/gates", good, "Idempotency-Key", e.key(), "If-Match", `"defaults"`), 412, "precondition-failed")
	// An agent's gate edit waits for a person.
	var approval struct{ ApprovalID string }
	e.ok(e.agent("PATCH", "/api/projects/evalp/gates", good, "Idempotency-Key", e.key(), "If-Match", `"`+g.Commit+`"`), 202, &approval)
	if approval.ApprovalID == "" {
		t.Fatal("no approval for an agent's gates.edit")
	}
	e.ok(e.do("POST", "/api/evals/"+ev.ID+":gate", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, gated.Rev)), 200, &gated)
	if gated.Gate.Verdict != "passed" || gated.Gate.GatesSha != g.Commit {
		t.Fatalf("verdict with gates.yaml %+v", gated.Gate)
	}

	// models.register publishes the checkpoint with its card; an agent's registration waits for a person.
	var reg struct {
		Name  string
		Model struct {
			EvalID, WeightsHash, Card string
			Gate                      struct{ Verdict, GatesSha string }
			Lineage                   struct {
				RunID, MixSha     string
				DatasetVersionIds []string
			}
		}
	}
	e.ok(e.do("POST", "/api/projects/evalp/models:register?dryRun=true", `{"checkpointId":"`+ckp+`"}`, "Idempotency-Key", e.key()), 200, &reg)
	if reg.Name != "model/evalp" || reg.Model.EvalID != ev.ID || reg.Model.Gate.Verdict != "passed" || reg.Model.Lineage.RunID == "" ||
		len(reg.Model.Lineage.DatasetVersionIds) != 1 || !strings.Contains(reg.Model.Card, "Verdict **passed**") {
		t.Fatalf("registration %+v", reg)
	}
	e.ok(e.agent("POST", "/api/projects/evalp/models:register", `{"checkpointId":"`+ckp+`"}`, "Idempotency-Key", e.key()), 202, &approval)
	var mv struct {
		ID, Name, Kind, State string
		Model                 struct{ CheckpointID, WeightsHash string }
		UsedBy                []struct{ ProjectSlug string }
	}
	e.ok(e.do("POST", "/api/projects/evalp/models:register", `{"checkpointId":"`+ckp+`"}`, "Idempotency-Key", e.key()), 201, &mv)
	if mv.Kind != "model" || mv.State != "frozen" || mv.Model.CheckpointID != ckp || len(mv.UsedBy) != 1 || mv.UsedBy[0].ProjectSlug != "evalp" {
		t.Fatalf("model version %+v", mv)
	}
	var list struct{ Items []struct{ ID string } }
	e.ok(e.do("GET", "/api/registry/models", ""), 200, &list)
	e.ok(e.do("GET", "/api/registry/models/"+mv.ID, ""), 200, nil)
	if len(list.Items) != 1 || list.Items[0].ID != mv.ID {
		t.Fatalf("models %+v", list)
	}
	if n := e.count("SELECT count(*) FROM events WHERE topic = 'entity.model." + mv.ID + "'"); n != 1 {
		t.Fatalf("%d model events", n)
	}
	// The registered model has the checkpoint's weights: its eval is cached.
	status, raw = newEval("evalp", `{"subject":{"modelVersionId":"`+mv.ID+`"},"baseline":"`+pipelinestest.BaseModel+`"}`)
	var byModel evalView
	_ = json.Unmarshal(raw, &byModel)
	if status != 201 || byModel.Status != "done" || byModel.Progress.CellsCached != 4 || byModel.Subject.ModelKey != sc.ModelKey {
		t.Fatalf("model eval %d %s", status, raw)
	}

	// Boosting is a decoding axis: a list of the language pack at a commit, rendered into a boost_list artifact.
	if _, err := e.repos.Repos().Commit(ctx, "evalp", repos.Change{Message: "boost list", Author: repos.Signature{Name: "admin", Email: "usr_admin@cadence.local"},
		Files: map[string][]byte{"lang/he-IL/boost/names.txt": []byte("# weight: 2\nCadence\nshalom\n")}}); err != nil {
		t.Fatal(err)
	}
	head, err := e.repos.Repos().Head(ctx, "evalp")
	if err != nil {
		t.Fatal(err)
	}
	boosted := `{"subject":{"checkpointId":"` + ckp + `"},"baseline":"` + pipelinestest.BaseModel + `","goldenSets":["golden-set/fx-golden-he"],` +
		`"decoding":[{"boost":"none"},{"boost":"lang/he-IL/boost/names.txt@` + head + `"}]}`
	expectProblem(t, e.do("POST", "/api/projects/evalp/evals", strings.Replace(boosted, head, "0000000", 1), "Idempotency-Key", e.key()), 422, "validation-failed")
	// A failing transcription fails the eval with the step's error.
	e.leases.Script("transcribe-u1", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "decoder exploded"}})
	status, raw = newEval("evalp", boosted)
	var failing evalView
	_ = json.Unmarshal(raw, &failing)
	if status != 201 || failing.Progress.CellsCached != 2 {
		t.Fatalf("boosted eval %d %s", status, raw)
	}
	failed := e.waitEval(failing.ID, "failed")
	if !strings.Contains(failed.Error, "decoder exploded") {
		t.Fatalf("failed eval error %q", failed.Error)
	}
	e.leases.Script("transcribe-u1")
	status, raw = newEval("evalp", boosted)
	var ok evalView
	_ = json.Unmarshal(raw, &ok)
	if status != 201 {
		t.Fatalf("boosted eval %d %s", status, raw)
	}
	ok = e.waitEval(ok.ID, "done")
	if len(ok.Decoding) != 2 || ok.Decoding[1].Terms != 2 || !strings.HasPrefix(ok.Decoding[1].Artifact, "b3:") {
		t.Fatalf("decoding %+v", ok.Decoding)
	}
	for _, c := range ok.Cells {
		if c.DecodingIndex == 1 && c.Role == "subject" && (c.Summary == nil || c.Summary.Ins != 6 || c.DecodingHash == sc.DecodingHash) {
			t.Fatalf("boosted subject cell %+v", c)
		}
	}
	var evl struct{ Items []struct{ ID, Status string } }
	e.ok(e.do("GET", "/api/projects/evalp/evals?status=failed", ""), 200, &evl)
	if len(evl.Items) != 1 || evl.Items[0].ID != failing.ID {
		t.Fatalf("failed evals %+v", evl.Items)
	}
	_ = projectID
}
