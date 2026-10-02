//go:build integration

package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/lineage"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

type robustnessView struct {
	ID, Status, Error, PipelineRunID string
	Augmentations                    []struct {
		Index                         int
		Profile, Name, Hash, Artifact string
		Seed                          *int
	}
	Progress struct{ CellsTotal, CellsDone, CellsCached int }
	Cells    []struct {
		ID, Role, GoldenSetVersionID, Profile, State, DecodingHash string
		AugmentationIndex                                          int
		Summary                                                    *struct{ Wer float64 }
		Delta                                                      *struct{ BaselineCellID string }
		Metrics                                                    *struct {
			Entities *struct {
				Scorer, Scores       string
				Available            bool
				RefEntities, Correct int
				Accuracy             *float64
			}
			Latency *struct {
				Scorer, Pace string
				Available    bool
				P50Ms, P95Ms float64
			}
			Unavailable []struct{ Metric, Reason string }
		}
	}
	Robustness []struct {
		Role, GoldenSetVersionID, Profile, CellID string
		AugmentationIndex                         int
		Wer, WerNone, Degradation                 *float64
	}
}

// The robustness axis and the metrics beside WER (phase 3 stream R) on the in-process fake worker: evals.new takes
// augmentation profiles from the project repository (validated against defaults.yaml augment.*), plans an augment
// step per golden set and profile before transcription, keys augmented records apart (the decoding hash) while the
// unaugmented ones keep their key, reports the robustness matrix, never registers the augmented dataset, and plans
// entity accuracy (the pack's itn.yaml) and latency to final (a VAD found by what it produces) per record; a second
// eval reuses all of it; lineage shows the eval downstream of the checkpoint and the golden set.
func TestEvalRobustnessAndMetrics(t *testing.T) {
	e := startWith(t, func(c *Config) { c.Lineage = lineage.New(evals.LineageSource{}) })
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
	_, ckp, golden := evalProject(t, e, "robp")
	commit := func(files map[string]string) string {
		t.Helper()
		ch := repos.Change{Message: "robustness fixtures", Author: repos.Signature{Name: "admin", Email: "usr_admin@cadence.local"},
			Files: map[string][]byte{}}
		for p, c := range files {
			ch.Files[p] = []byte(c)
		}
		c, err := e.repos.Repos().Commit(ctx, "robp", ch)
		if err != nil {
			t.Fatal(err)
		}
		return c.SHA
	}
	sha := commit(map[string]string{
		"augment/telephony.yaml": "name: telephony\nseed: 7\ntransforms:\n  codec: { probability: 0.5 }\n  band_limit: { probability: 1.0, cutoff_hz: 3400 }\n",
		"augment/broken.yaml":    "name: broken\ntransforms:\n  reverb: { probability: 1 }\n  level: { gain_db: [6, -6] }\n",
		"lang/he-IL/itn.yaml": "version: 1\nlocale: he-IL\nclasses:\n  - name: number\n    description: digits\n    pattern: \"\\\\d+\"\n" +
			"    examples:\n      - { spoken: \"three\", written: \"3\" }\n",
	})
	datasets := e.count("SELECT count(*) FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id WHERE c.kind = 'dataset'")
	post := func(body, query string) (int, []byte) {
		resp := e.do("POST", "/api/projects/robp/evals"+query, body, "Idempotency-Key", e.key())
		defer func() { _ = resp.Body.Close() }()
		var raw json.RawMessage
		_ = json.NewDecoder(resp.Body).Decode(&raw)
		return resp.StatusCode, raw
	}
	subject := `{"subject":{"checkpointId":"` + ckp + `"},"baseline":"` + pipelinestest.BaseModel + `",`

	// A profile that names an unknown transform or an inverted range is refused before anything runs.
	expectProblem(t, e.do("POST", "/api/projects/robp/evals?dryRun=true", subject+`"augmentations":[{"profile":"augment/broken.yaml@`+sha+`"}]}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")
	expectProblem(t, e.do("POST", "/api/projects/robp/evals?dryRun=true", subject+`"augmentations":[{"profile":"augment/telephony.yaml"}]}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")

	body := subject + `"augmentations":[{"profile":"none"},{"profile":"augment/telephony.yaml@` + sha + `"}]}`
	status, raw := post(body, "?dryRun=true")
	var plan struct {
		Augmentations []struct {
			Index         int
			Profile, Hash string
			Seed          *int
			Transforms    map[string]map[string]any
		}
		Cells []struct {
			Role              string
			AugmentationIndex int
		}
		CellsToCompute int
		Steps          []struct{ Step, Kind string }
	}
	if status != 200 {
		t.Fatalf("dry run %d: %s", status, raw)
	}
	_ = json.Unmarshal(raw, &plan)
	// none + telephony; the he target set gets both, the ru replay set none only: 2×2 + 1×2 cells, all to compute.
	if len(plan.Augmentations) != 2 || plan.Augmentations[1].Seed == nil || *plan.Augmentations[1].Seed != 7 ||
		!strings.HasPrefix(plan.Augmentations[1].Hash, "sha256:") || plan.Augmentations[1].Transforms["codec"]["codecs"] == nil ||
		plan.Augmentations[1].Transforms["band_limit"]["cutoff_hz"] != float64(3400) || len(plan.Cells) != 6 || plan.CellsToCompute != 6 {
		t.Fatalf("plan %s", raw)
	}
	kinds := map[string]int{}
	for _, s := range plan.Steps {
		kinds[strings.SplitN(s.Kind, "@", 2)[0]]++
	}
	// one augment step (he × telephony); a VAD per dataset (he, he-telephony, ru); entity and latency per record
	// (the ru set has no pack: no entity step there).
	if kinds[pipelinestest.KindAugment] != 1 || kinds[pipelinestest.KindVAD] != 3 || kinds[pipelinestest.KindLatency] != 6 ||
		kinds[pipelinestest.KindEntity] != 4 {
		t.Fatalf("steps %v", kinds)
	}

	status, raw = post(body, "")
	if status != 201 {
		t.Fatalf("evals.new %d: %s", status, raw)
	}
	var created robustnessView
	_ = json.Unmarshal(raw, &created)
	e.waitEval(created.ID, "done")
	var ev robustnessView
	e.ok(e.do("GET", "/api/evals/"+created.ID, ""), 200, &ev)
	if len(ev.Augmentations) != 2 || ev.Augmentations[0].Profile != "none" || ev.Augmentations[1].Name != "telephony" {
		t.Fatalf("augmentations %+v", ev.Augmentations)
	}
	var none, aug *float64
	for _, c := range ev.Cells {
		if c.GoldenSetVersionID != golden["fx-golden-he"] || c.Role != "subject" {
			continue
		}
		w := c.Summary.Wer
		if c.AugmentationIndex == 0 {
			none = &w
		} else {
			aug = &w
			if c.Delta == nil {
				t.Fatalf("the augmented subject cell has no delta against the augmented baseline cell")
			}
		}
		m := c.Metrics
		if m == nil || m.Entities == nil || !m.Entities.Available || m.Entities.RefEntities != 6 || m.Entities.Correct != 6 ||
			m.Latency == nil || m.Latency.P50Ms != pipelinestest.LatencyP50 || m.Latency.Pace != "simulated" {
			t.Fatalf("metrics of %s: %+v", c.ID, m)
		}
	}
	if none == nil || aug == nil || *aug <= *none {
		t.Fatalf("augmented WER %v vs none %v", aug, none)
	}
	for _, c := range ev.Cells {
		if c.GoldenSetVersionID == golden["fx-golden-ru"] {
			if c.Metrics == nil || len(c.Metrics.Unavailable) != 1 || c.Metrics.Unavailable[0].Metric != "entities" ||
				!strings.Contains(c.Metrics.Unavailable[0].Reason, "ru-RU") || c.Metrics.Latency == nil {
				t.Fatalf("ru metrics %+v", c.Metrics)
			}
		}
	}
	if len(ev.Robustness) != 2 || ev.Robustness[0].Degradation == nil || *ev.Robustness[0].Degradation <= 0 {
		t.Fatalf("robustness %+v", ev.Robustness)
	}
	// Augmented records are keyed apart and say so; the augmented dataset was never registered.
	if n := e.count("SELECT count(*) FROM eval_records WHERE augmentation IS NOT NULL"); n != 2 {
		t.Fatalf("%d augmented records", n)
	}
	if n := e.count("SELECT count(*) FROM eval_metrics"); n != 10 {
		t.Fatalf("%d eval metrics", n)
	}
	if n := e.count("SELECT count(*) FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id WHERE c.kind = 'dataset'"); n != datasets {
		t.Fatalf("the augmented dataset was registered: %d dataset versions, %d before", n, datasets)
	}

	// The same eval again is fully cached, metrics included: done at once.
	status, raw = post(body, "")
	var again robustnessView
	_ = json.Unmarshal(raw, &again)
	if status != 201 || again.Status != "done" || again.PipelineRunID != "" || again.Progress.CellsCached != 6 {
		t.Fatalf("cached eval %d %s", status, raw)
	}
	// Without the axis the unaugmented records keep their keys: the default eval is cached as well.
	status, raw = post(subject+`"goldenSets":["golden-set/fx-golden-he"]}`, "")
	_ = json.Unmarshal(raw, &again)
	if status != 201 || again.Status != "done" || again.Progress.CellsCached != 2 {
		t.Fatalf("eval without augmentation %d %s", status, raw)
	}

	// Lineage: the checkpoint and the golden set show the eval and its records downstream.
	var g struct {
		Nodes []struct{ ID, Kind string }
		Edges []struct{ From, To, Relation string }
	}
	e.ok(e.do("GET", "/api/registry/"+ckp+":lineage?direction=downstream&depth=1", ""), 200, &g)
	if !hasEdge(g.Edges, ckp, created.ID, "subject") {
		t.Fatalf("checkpoint lineage %+v", g)
	}
	e.ok(e.do("GET", "/api/registry/"+golden["fx-golden-he"]+":lineage?direction=downstream&depth=1", ""), 200, &g)
	if !hasEdge(g.Edges, golden["fx-golden-he"], created.ID, "golden set") {
		t.Fatalf("golden set lineage %+v", g)
	}
	e.ok(e.do("GET", "/api/registry/"+created.ID+":lineage?direction=upstream&depth=1", ""), 200, &g)
	kindsOf := map[string]bool{}
	for _, n := range g.Nodes {
		kindsOf[n.Kind] = true
	}
	if !kindsOf["eval"] || !kindsOf["eval_record"] || !kindsOf["checkpoint"] {
		t.Fatalf("eval lineage %+v", g)
	}
}

func hasEdge(edges []struct{ From, To, Relation string }, from, to, rel string) bool {
	for _, e := range edges {
		if e.From == from && e.To == to && e.Relation == rel {
			return true
		}
	}
	return false
}
