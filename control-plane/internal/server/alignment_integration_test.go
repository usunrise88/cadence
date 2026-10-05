//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/goldensets"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

type goldenSetAlignmentView struct {
	ID        string
	GoldenSet struct{ DatasetHash string }
	Alignment *struct {
		ID, Artifact, Aligner, AlignerVersionID, Method, PipelineRunID string
		Utterances, Aligned, Words                                     int
		Reasons                                                        []string
	}
}

type emissionEvalView struct {
	ID, Status string
	Cells      []struct {
		Role, GoldenSetVersionID string
		AugmentationIndex        int
		Metrics                  *struct {
			Latency *struct {
				Scorer   string
				Emission *struct {
					Available      bool
					Reason         string
					PR50Ms, PR90Ms *float64
					MatchedWords   int
				}
			}
		}
	}
}

// Golden sets carry word timings and evals report emission delay (phase 4 stream L) on the in-process fake worker:
// an aligning step's alignment output is recorded against the dataset artifact it aligned and shown on every golden
// set built on it (goldenSets.get, goldenSets.list; event golden_set.aligned), a reused step records nothing twice,
// and evals.new feeds the alignment and the normalizer to latency_score@3 for the aligned golden set's unaugmented
// cells only: the unaligned golden set reports emission delay unavailable instead of a number.
func TestGoldenSetAlignmentAndEmissionDelay(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	for _, reg := range []func(context.Context) error{
		func(ctx context.Context) error { return pipelinestest.RegisterTraining(ctx, e.pool) },
		func(ctx context.Context) error { return pipelinestest.RegisterEvaluation(ctx, e.pool) },
		func(ctx context.Context) error { return pipelinestest.RegisterRobustness(ctx, e.pool) },
		func(ctx context.Context) error { return pipelinestest.RegisterAlignment(ctx, e.pool) },
	} {
		if err := reg(ctx); err != nil {
			t.Fatal(err)
		}
	}
	_, ckp, golden := evalProject(t, e, "alnp")
	gs := func(id string) goldenSetAlignmentView {
		t.Helper()
		var v goldenSetAlignmentView
		e.ok(e.do("GET", "/api/registry/golden-sets/"+id, ""), 200, &v)
		return v
	}
	he := gs(golden["fx-golden-he"])
	if he.Alignment != nil || !steps.ValidHash(he.GoldenSet.DatasetHash) {
		t.Fatalf("golden set before alignment: %+v", he)
	}

	// Align the he golden set's dataset with the fixture aligner.
	e.commitPipeline("alnp", "align", pipelinestest.AlignmentPipeline)
	f, err := e.admin.CAS.Open(he.GoldenSet.DatasetHash) // the fixture's golden dataset is a blob without an artifacts row
	if err != nil {
		t.Fatal(err)
	}
	blob, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	var run struct{ ID string }
	e.ok(e.do("POST", "/api/projects/alnp/pipelines/align:run",
		fmt.Sprintf(`{"inputs":{"data":{"hash":%q,"type":"dataset","size":%d}}}`, he.GoldenSet.DatasetHash, len(blob)),
		"Idempotency-Key", e.key(), "If-Match", "*"), 201, &run)
	r := e.waitPipelineRun(run.ID, "done")
	out := r.Steps[0].Outputs["alignment"]
	he = gs(golden["fx-golden-he"])
	a := he.Alignment
	if a == nil || a.Artifact != out.Hash || a.Aligner != "auxiliary/fx-aligner" || a.AlignerVersionID != "ver_fx_aligner" ||
		a.Method != "ctc-viterbi" || a.Utterances != 6 || a.Aligned != 6 || a.Words == 0 || a.PipelineRunID != run.ID ||
		!strings.HasPrefix(a.ID, "aln_") {
		t.Fatalf("alignment %+v (output %s)", a, out.Hash)
	}
	if ru := gs(golden["fx-golden-ru"]); ru.Alignment != nil {
		t.Fatalf("the ru golden set has its own dataset and no alignment: %+v", ru.Alignment)
	}
	var list struct{ Items []goldenSetAlignmentView }
	e.ok(e.do("GET", "/api/registry/golden-sets", ""), 200, &list)
	for _, v := range list.Items {
		if (v.ID == golden["fx-golden-he"]) != (v.Alignment != nil) {
			t.Fatalf("goldenSets.list alignment of %s: %+v", v.ID, v.Alignment)
		}
	}
	if n := e.count("SELECT count(*) FROM events WHERE type = 'golden_set.aligned' AND topic = 'entity.golden_set." + golden["fx-golden-he"] + "'"); n != 1 {
		t.Fatalf("%d golden_set.aligned events", n)
	}

	// Recording the same artifact again (a reused step) adds nothing; an output without the format is refused.
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		drafts, err := goldensets.AlignmentHook(ctx, tx, steps.Output{PipelineRunID: run.ID, StepID: "pls_again", Artifact: out,
			Spec: steps.Spec{Inputs: map[string]steps.ArtifactRef{"data": {Hash: he.GoldenSet.DatasetHash, Type: "dataset"}}}})
		if err != nil || len(drafts) != 0 {
			t.Fatalf("again: %v, %d events", err, len(drafts))
		}
		bad := out
		bad.Meta = json.RawMessage(`{"utterances":1}`)
		bad.Hash = "b3:" + strings.Repeat("0", 64)
		if _, err := goldensets.AlignmentHook(ctx, tx, steps.Output{StepID: "pls_bad", Artifact: bad,
			Spec: steps.Spec{Inputs: map[string]steps.ArtifactRef{"data": {Hash: he.GoldenSet.DatasetHash, Type: "dataset"}}}}); err == nil {
			t.Fatal("an alignment output without its format was recorded")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n := e.count("SELECT count(*) FROM reference_alignments"); n != 1 {
		t.Fatalf("%d reference alignments", n)
	}

	// The audio view's reference track (words.get, phase 4 tail): a golden utterance's reference words at their
	// aligned times, by golden set or by alignment artifact; a golden set never aligned has no track.
	if _, err := e.pool.Exec(ctx, `INSERT INTO sources (id, name, licence, kind, created_by)
		VALUES ('src_ref', 'ref-fixtures', 'CC-BY-4.0', 'public', '{"kind":"user","id":"usr_admin"}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO utterances (id, content_hash, source_id, duration_s, language, sample_rate, channels, bytes)
		VALUES ('utt_ref2', $1, 'src_ref', 5, 'he-IL', 16000, 1, 160044)`, fmt.Sprintf("b3:%064d", 2)); err != nil {
		t.Fatal(err)
	}
	type refTrack struct {
		Text      string
		Words     []map[string]any
		Reference *struct {
			Artifact, Text, Aligner string
			Aligned                 bool
			Words                   []struct {
				Index      int
				Word       string
				Start, End float64
			}
		}
	}
	var rt refTrack
	e.ok(e.do("GET", "/api/registry/utterances/utt_ref2/words?goldenSet="+golden["fx-golden-he"], ""), 200, &rt)
	r2 := rt.Reference
	if r2 == nil || r2.Artifact != a.Artifact || !r2.Aligned || r2.Aligner != "auxiliary/fx-aligner" || len(r2.Words) != 10 ||
		r2.Words[5].Word != "2" || r2.Words[5].Index != 5 || math.Abs(r2.Words[5].Start-1.5) > 1e-9 || rt.Text != "" || len(rt.Words) != 0 {
		t.Fatalf("reference track %+v", rt)
	}
	var byArtifact refTrack
	e.ok(e.do("GET", "/api/registry/utterances/utt_ref2/words?alignment="+a.Artifact, ""), 200, &byArtifact)
	if byArtifact.Reference == nil || len(byArtifact.Reference.Words) != 10 {
		t.Fatalf("by artifact %+v", byArtifact)
	}
	var unaligned refTrack
	e.ok(e.do("GET", "/api/registry/utterances/utt_ref2/words?goldenSet="+golden["fx-golden-ru"], ""), 200, &unaligned)
	if unaligned.Reference != nil {
		t.Fatalf("a golden set never aligned has a reference track: %+v", unaligned.Reference)
	}
	expectProblem(t, e.do("GET", "/api/registry/utterances/utt_ref2/words", ""), 400, "bad-request")
	expectProblem(t, e.agent("GET", "/api/registry/utterances/utt_ref2/words?goldenSet="+golden["fx-golden-he"], ""), 403, "forbidden")

	// The eval: latency_score@3 gets the alignment for the he cells, none for the ru cells.
	var ev struct{ ID string }
	e.ok(e.do("POST", "/api/projects/alnp/evals", `{"subject":{"checkpointId":"`+ckp+`"},"baseline":"`+pipelinestest.BaseModel+`"}`,
		"Idempotency-Key", e.key()), 201, &ev)
	e.waitEval(ev.ID, "done")
	var v emissionEvalView
	e.ok(e.do("GET", "/api/evals/"+ev.ID, ""), 200, &v)
	seen := map[string]int{}
	for _, c := range v.Cells {
		if c.Metrics == nil || c.Metrics.Latency == nil || c.Metrics.Latency.Emission == nil {
			t.Fatalf("cell %s/%s has no latency metrics: %+v", c.Role, c.GoldenSetVersionID, c.Metrics)
		}
		l := c.Metrics.Latency
		if l.Scorer != "latency_score@3" {
			t.Fatalf("scorer %s", l.Scorer)
		}
		em := l.Emission
		switch c.GoldenSetVersionID {
		case golden["fx-golden-he"]:
			if !em.Available || em.PR50Ms == nil || *em.PR50Ms != pipelinestest.EmissionPR50 || *em.PR90Ms != pipelinestest.EmissionPR90 {
				t.Fatalf("he emission %+v", em)
			}
		default:
			if em.Available || em.PR50Ms != nil || !strings.Contains(em.Reason, "no aligned references") {
				t.Fatalf("ru emission %+v", em)
			}
		}
		seen[c.GoldenSetVersionID]++
	}
	if seen[golden["fx-golden-he"]] == 0 || seen[golden["fx-golden-ru"]] == 0 {
		t.Fatalf("cells by golden set %v", seen)
	}
	// The latency step of an aligned golden set reads its alignment and its normalizer.
	var wired int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM pipeline_steps s JOIN evals ev ON ev.pipeline_run_id = s.pipeline_run_id
		WHERE ev.id = $1 AND s.step LIKE 'latency-%' AND s.inputs::text LIKE '%`+a.Artifact+`%' AND s.inputs::text LIKE '%"normalizer"%'`,
		ev.ID).Scan(&wired); err != nil {
		t.Fatal(err)
	}
	if wired == 0 {
		t.Fatal("no latency step reads the alignment and the normalizer")
	}
}
