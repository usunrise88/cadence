//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/eviction"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Eval artifacts are kept by age (owner decision 2026-10-03): the daily sweep evicts the scores and hypotheses of
// records last used more than eval.artifact_retention_days ago; the records, deltas and verdicts stay. evals.get shows
// the evicted cell instead of rows, evals.gate reuses the stored deltas and turns inconclusive only when it must
// recompute, and evals.new computes the cells again and brings the rows back.
func TestEvalArtifactRetention(t *testing.T) {
	svc := &eviction.Service{} // no backup mirror: the eviction is permanent
	e := startWith(t, func(c *Config) {
		svc.CAS = c.CAS
		c.Eviction = svc
	}, func(pool *pgxpool.Pool, js *jobs.Service) {
		svc.Pool = pool
		svc.Register(js)
	})
	ctx := context.Background()
	if err := pipelinestest.RegisterTraining(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	if err := pipelinestest.RegisterEvaluation(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	_, ckp, golden := evalProject(t, e, "evret")
	body := `{"subject":{"checkpointId":"` + ckp + `"},"baseline":"` + pipelinestest.BaseModel + `"}`
	newEval := func() evalView {
		t.Helper()
		var v evalView
		e.ok(e.do("POST", "/api/projects/evret/evals", body, "Idempotency-Key", e.key()), 201, &v)
		return v
	}
	gate := func(id string) evalView {
		t.Helper()
		cur := e.eval(id, "")
		var v evalView
		e.ok(e.do("POST", "/api/evals/"+id+":gate", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, cur.Rev)), 200, &v)
		return v
	}
	sweep := func() string {
		t.Helper()
		if err := svc.SweepEvalArtifacts(ctx, defaults.Get()); err != nil {
			t.Fatal(err)
		}
		var id string
		if err := e.pool.QueryRow(ctx, `SELECT coalesce(max(id), '') FROM jobs WHERE kind = $1 AND state IN ('queued', 'running')`,
			eviction.JobKind).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	ev := e.waitEval(newEval().ID, "done")
	if g := gate(ev.ID); g.Gate == nil || g.Gate.Verdict != "passed" {
		t.Fatalf("verdict before the retention %+v", g.Gate)
	}
	var subject string
	for _, c := range ev.Cells {
		if c.Role == "subject" && c.GoldenSetVersionID == golden["fx-golden-he"] {
			subject = c.ID
		}
	}
	live := e.count(`SELECT count(*) FROM artifacts WHERE type IN ('scores', 'hypotheses') AND evicted_at IS NULL`)
	if live < 8 {
		t.Fatalf("%d live eval artifacts, want the scores and hypotheses of 4 records", live)
	}

	// Within the retention the sweep queues nothing.
	if id := sweep(); id != "" {
		t.Fatalf("a sweep within the retention queued %s", id)
	}
	// 40 days later (the records, their metrics and the eval that used them): the sweep evicts them.
	for _, table := range []string{"eval_records", "eval_metrics", "evals"} {
		if _, err := e.pool.Exec(ctx, `UPDATE `+table+` SET created_at = created_at - interval '40 days'`); err != nil {
			t.Fatal(err)
		}
	}
	id := sweep()
	if id == "" {
		t.Fatal("the sweep queued no eviction")
	}
	j := e.waitJob(id, "done")
	var res struct {
		Artifacts  []struct{ Hash, Type, Reason string }
		BytesFreed int64
		Permanent  bool
	}
	_ = json.Unmarshal(j.Result, &res)
	if len(res.Artifacts) != live || res.BytesFreed == 0 || !res.Permanent || !strings.Contains(res.Artifacts[0].Reason, "more than 30 days ago") {
		t.Fatalf("eviction result %s", j.Result)
	}
	if n := e.count(`SELECT count(*) FROM artifacts WHERE type IN ('scores', 'hypotheses') AND evicted_at IS NULL`); n != 0 {
		t.Fatalf("%d eval artifacts left", n)
	}
	if n := e.count(`SELECT count(*) FROM eval_records`); n != 4 {
		t.Fatalf("%d eval records, want the 4 kept", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_log WHERE operation = 'artifacts.evict' AND (detail->>'retentionDays')::int = 30`); n != 1 {
		t.Fatalf("%d audit entries of the retention", n)
	}
	if ok, _, _ := e.admin.CAS.Has(res.Artifacts[0].Hash); ok {
		t.Fatal("an evicted blob is still in the store")
	}

	// evals.get: the cell keeps its summary and delta and says its rows are gone; no 500.
	got := e.eval(ev.ID, "?cell="+subject+"&worst=5")
	var raw struct {
		Cells []struct {
			Summary json.RawMessage
			Delta   *struct{ Error string }
			Worst   []json.RawMessage
			Evicted *struct {
				Note          string
				RetentionDays int
			}
		}
	}
	e.ok(e.do("GET", "/api/evals/"+ev.ID+"?cell="+subject+"&worst=5", ""), 200, &raw)
	c := raw.Cells[0]
	if len(got.Cells) != 1 || c.Evicted == nil || c.Evicted.RetentionDays != 30 || !strings.Contains(c.Evicted.Note, "run the eval again") ||
		len(c.Worst) != 0 || len(c.Summary) == 0 || c.Delta == nil || c.Delta.Error != "" {
		t.Fatalf("evicted cell %+v", c)
	}

	// evals.gate at the eval's significance reuses the stored deltas: still passed.
	if g := gate(ev.ID); g.Gate.Verdict != "passed" {
		t.Fatalf("verdict with stored deltas %+v", g.Gate)
	}
	// Another significance must recompute: the target check turns inconclusive and says why.
	cfg := `{"config":{"significance":{"samples":500}}}`
	e.ok(e.do("PATCH", "/api/projects/evret/gates", cfg, "Idempotency-Key", e.key(), "If-Match", `"defaults"`), 200, nil)
	g := gate(ev.ID)
	var target string
	for _, ch := range g.Gate.Checks {
		if ch.Kind == "target" {
			target = ch.State + ": " + ch.Message
		}
	}
	if g.Gate.Verdict != "failed" || !strings.Contains(target, "inconclusive: ") ||
		!strings.Contains(target, "per-utterance scores evicted (older than 30 days); re-run the eval") {
		t.Fatalf("verdict after the eviction %q %+v", target, g.Gate)
	}

	// Running the eval again computes the cells (nothing cached from the evicted records) and brings the rows back,
	// for the old eval too: it links the same records.
	// The subject's record names scores the new computation will not reproduce byte for byte (a GPU decodes a word
	// differently): the scores hook refreshes the record with the new artifacts and keeps its summary.
	stale, err := e.admin.CAS.PutBytes([]byte("scores of another decode"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if _, err := artifacts.Record(ctx, tx, e.admin.CAS, steps.ArtifactRef{Hash: stale, Type: "scores", Size: 24}, "", nil); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE artifacts SET evicted_at = now() - interval '1 day' WHERE hash = $1`, stale); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE eval_records SET scores_hash = $2 WHERE id = (SELECT record_id FROM eval_cells WHERE id = $1)`, subject, stale)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var summaryBefore string
	if err := e.pool.QueryRow(ctx, `SELECT r.summary::text FROM eval_records r JOIN eval_cells c ON c.record_id = r.id WHERE c.id = $1`,
		subject).Scan(&summaryBefore); err != nil {
		t.Fatal(err)
	}
	again := newEval()
	if again.Progress.CellsCached != 0 {
		t.Fatalf("the new eval linked %d evicted records", again.Progress.CellsCached)
	}
	e.waitEval(again.ID, "done")
	if n := e.count(`SELECT count(*) FROM eval_records`); n != 4 {
		t.Fatalf("%d eval records after the recomputation, want the same 4", n)
	}
	var scoresAfter, summaryAfter string
	if err := e.pool.QueryRow(ctx, `SELECT r.scores_hash, r.summary::text FROM eval_records r JOIN eval_cells c ON c.record_id = r.id
		WHERE c.id = $1`, subject).Scan(&scoresAfter, &summaryAfter); err != nil {
		t.Fatal(err)
	}
	if scoresAfter == stale || summaryAfter != summaryBefore {
		t.Fatalf("the record was not refreshed (%s) or its summary changed:\n%s\n%s", scoresAfter, summaryBefore, summaryAfter)
	}
	raw.Cells = nil // a fresh decode: absent fields would keep the old values
	e.ok(e.do("GET", "/api/evals/"+ev.ID+"?cell="+subject+"&worst=5", ""), 200, &raw)
	if c := raw.Cells[0]; c.Evicted != nil || len(c.Worst) == 0 {
		t.Fatalf("the old eval's cell after the recomputation %+v", c)
	}
	if g := gate(ev.ID); g.Gate.Verdict != "passed" {
		t.Fatalf("verdict after the recomputation %+v", g.Gate)
	}
	// The recomputation used the records again today: the next sweep keeps them.
	if id := sweep(); id != "" {
		t.Fatalf("a sweep after the recomputation queued %s", id)
	}
}
