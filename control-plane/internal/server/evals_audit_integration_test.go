//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The phase-3 audit's eval correctness fixes on the in-process fake worker: languages, boosting and augmentation in
// one eval whose reported-only metric steps fail (the eval and its gate go on, the metrics say why they are
// unavailable, the record names the decode language); a retried pipeline run reopens a failed eval; two evals that
// race on one record key share the record.
func TestEvalAuditFixes(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	for _, reg := range []func(context.Context) error{
		func(ctx context.Context) error { return pipelinestest.RegisterTraining(ctx, e.pool) },
		func(ctx context.Context) error { return pipelinestest.RegisterEvaluation(ctx, e.pool) },
		func(ctx context.Context) error { return pipelinestest.RegisterRobustness(ctx, e.pool) },
	} {
		if err := reg(ctx); err != nil {
			t.Fatal(err)
		}
	}
	projectID, ckp, _ := evalProject(t, e, "audp")
	c, err := e.repos.Repos().Commit(ctx, "audp", repos.Change{Message: "fixtures", Author: repos.Signature{Name: "admin", Email: "usr_admin@cadence.local"},
		Files: map[string][]byte{
			"augment/telephony.yaml":       []byte("name: telephony\nseed: 7\ntransforms:\n  band_limit: { probability: 1.0, cutoff_hz: 3400 }\n"),
			"lang/he-IL/itn.yaml":          []byte("version: 1\nlocale: he-IL\nclasses:\n  - name: number\n    description: digits\n    pattern: \"\\\\d+\"\n    examples:\n      - { spoken: \"three\", written: \"3\" }\n"),
			"lang/he-IL/boost/names.txt":   []byte("# weight: 2\nCadence\nshalom\n"),
			"lang/he-IL/boost/places.txt":  []byte("# weight: 1\nHaifa\n"),
			"lang/he-IL/boost/numbers.txt": []byte("# weight: 1\nthree\n"),
		}})
	if err != nil {
		t.Fatal(err)
	}
	post := func(body, query string) (int, []byte) {
		resp := e.do("POST", "/api/projects/audp/evals"+query, body, "Idempotency-Key", e.key())
		defer func() { _ = resp.Body.Close() }()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return resp.StatusCode, raw
	}
	subject := `{"subject":{"checkpointId":"` + ckp + `"},"baseline":"` + pipelinestest.BaseModel + `",`

	// 1. Every axis at once, with the first VAD and the first entity scorer failing.
	body := subject + `"languages":{"he-IL":"yi"},"decoding":[{"boost":"none"},{"boost":"lang/he-IL/boost/names.txt@` + c.SHA + `"}],` +
		`"augmentations":[{"profile":"none"},{"profile":"augment/telephony.yaml@` + c.SHA + `"}]}`
	status, raw := post(body, "?dryRun=true")
	if status != 200 {
		t.Fatalf("dry run %d: %s", status, raw)
	}
	var plan struct {
		Steps []struct {
			Step, Kind string
		}
		CellsToCompute int
	}
	_ = json.Unmarshal(raw, &plan)
	entity := ""
	for _, s := range plan.Steps {
		if strings.HasPrefix(s.Kind, pipelinestest.KindEntity+"@") && entity == "" {
			entity = s.Step
		}
	}
	// he: 2 decodings × 2 augmentations, ru (replay): 2 decodings × none; two models.
	if entity == "" || plan.CellsToCompute != 12 || !slices.ContainsFunc(plan.Steps, func(s struct{ Step, Kind string }) bool { return s.Step == "vad-g1a0" }) {
		t.Fatalf("plan %s", raw)
	}
	e.leases.Script("vad-g1a0", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "the VAD model is gone"}})
	e.leases.Script(entity, pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "itn exploded"}})
	status, raw = post(body, "")
	if status != 201 {
		t.Fatalf("evals.new %d: %s", status, raw)
	}
	var created robustnessView
	_ = json.Unmarshal(raw, &created)
	e.waitEval(created.ID, "done")
	var ev robustnessView
	e.ok(e.do("GET", "/api/evals/"+created.ID, ""), 200, &ev)
	skipped, failed := 0, 0
	for _, cell := range ev.Cells {
		if cell.Summary == nil || cell.State != "done" {
			t.Fatalf("cell %s has no WER: %+v", cell.ID, cell)
		}
		if cell.Metrics == nil {
			t.Fatalf("cell %s planned no metrics", cell.ID)
		}
		for _, u := range cell.Metrics.Unavailable {
			switch {
			case u.Metric == "latency" && strings.Contains(u.Reason, "skipped"):
				skipped++
			case u.Metric == "entities" && strings.Contains(u.Reason, "itn exploded"):
				failed++
			}
		}
	}
	if skipped == 0 || failed == 0 {
		t.Fatalf("unavailable metrics: %d skipped latency, %d failed entities; cells %+v", skipped, failed, ev.Cells)
	}
	if n := e.count("SELECT count(*) FROM eval_records WHERE decoding->>'language' = 'yi'"); n != 8 {
		t.Fatalf("%d records decoded in yi, want the 8 of the he set", n)
	}
	var gated evalView
	e.ok(e.do("POST", "/api/evals/"+created.ID+":gate", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, e.eval(created.ID, "").Rev)), 200, &gated)
	if gated.Gate == nil || len(gated.Gate.Checks) == 0 {
		t.Fatalf("no verdict: %+v", gated.Gate)
	}
	e.leases.Script("vad-g1a0")
	e.leases.Script(entity)

	// 2. A retried pipeline run reopens its failed eval, which then finishes.
	e.leases.Script("transcribe-u1", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "decoder exploded"}})
	status, raw = post(subject+`"goldenSets":["golden-set/fx-golden-he"],"decoding":[{"boost":"lang/he-IL/boost/places.txt@`+c.SHA+`"}]}`, "")
	if status != 201 {
		t.Fatalf("evals.new %d: %s", status, raw)
	}
	_ = json.Unmarshal(raw, &created)
	failedEval := e.waitEval(created.ID, "failed")
	e.leases.Script("transcribe-u1")
	pr := e.waitPipelineRun(failedEval.PipelineRunID, "failed")
	e.ok(e.do("POST", "/api/pipeline-runs/"+pr.ID+":retry", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, pr.Rev)), 200, nil)
	if reopened := e.waitEval(created.ID, "done"); reopened.Error != "" || reopened.Progress.CellsDone != reopened.Progress.CellsTotal {
		t.Fatalf("reopened eval %+v", reopened)
	}

	// 3. Two evals planned before either computed its cells: both compute, the second record insert finds the first's
	// key and links it, so both evals share the records.
	in := evals.NewInput{ProjectID: projectID, Actor: auth.DevActor(), Subject: evals.SubjectRef{CheckpointID: ckp},
		Baseline: pipelinestest.BaseModel, GoldenSets: []string{"golden-set/fx-golden-he"},
		Decoding: []evals.DecodingIn{{Boost: "lang/he-IL/boost/numbers.txt@" + c.SHA}}}
	var plans []evals.Plan
	for range 2 {
		pl, err := e.admin.evals.Prepare(ctx, e.pool, in)
		if err != nil {
			t.Fatal(err)
		}
		if pl.CellsToCompute != 2 {
			t.Fatalf("plan computes %d cells", pl.CellsToCompute)
		}
		plans = append(plans, pl)
	}
	var ids []string
	for _, pl := range plans {
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			ev, drafts, err := e.admin.evals.Create(ctx, tx, pl)
			ids = append(ids, ev.ID)
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, auth.DevActor(), nil, drafts)
		}); err != nil {
			t.Fatal(err)
		}
	}
	a, b := e.waitEval(ids[0], "done"), e.waitEval(ids[1], "done")
	recs := func(v evalView) []string {
		var out []string
		for _, cell := range v.Cells {
			out = append(out, cell.RecordID)
		}
		slices.Sort(out)
		return out
	}
	if ra, rb := recs(a), recs(b); len(ra) != 2 || !slices.Equal(ra, rb) || ra[0] == "" {
		t.Fatalf("racing evals link %v and %v", ra, rb)
	}
	if n := e.count("SELECT count(*) FROM eval_records WHERE decoding->'list' IS NOT NULL AND decoding->>'boost' LIKE '%numbers.txt%'"); n != 2 {
		t.Fatalf("%d records of the raced key", n)
	}
}
