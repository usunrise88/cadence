//go:build integration

package goldensets

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
	"github.com/usunrise88/cadence/control-plane/templates"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func openDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := storage.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Seed(ctx, pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// dataset registers a frozen dataset version holding utterances lo..hi (language lang), creating them when absent.
func dataset(t *testing.T, pool *pgxpool.Pool, name, lang string, evalOnly bool, lo, hi int) string {
	t.Helper()
	ctx := context.Background()
	exec(t, pool, `INSERT INTO utterances (id, content_hash, source_id, duration_s, language, speaker, sample_rate)
		SELECT 'utt_' || i, 'b3:' || lpad(to_hex(i), 64, '0'), 'src_bench', 3.0, $3, 'spk' || (i % 40), 16000
		FROM generate_series($1::int, $2::int) i ON CONFLICT DO NOTHING`, lo, hi, lang)
	exec(t, pool, `INSERT INTO transcripts (id, utterance_id, text, origin)
		SELECT 'trn_' || i, 'utt_' || i, 'text ' || i, 'human' FROM generate_series($1::int, $2::int) i ON CONFLICT DO NOTHING`, lo, hi)
	exec(t, pool, `INSERT INTO utterance_fingerprints (utterance_id, kind, value)
		SELECT 'utt_' || i, k.kind, CASE k.kind WHEN 'audio-b3' THEN 'b3:' || lpad(to_hex(i), 64, '0') ELSE 'ac' || i END
		FROM generate_series($1::int, $2::int) i, (VALUES ('audio-b3'), ('acoustic')) k(kind) ON CONFLICT DO NOTHING`, lo, hi)
	payload, _ := json.Marshal(map[string]any{
		"evalOnly": evalOnly, "locales": []string{lang}, "artifact": map[string]string{"hash": "b3:" + strings.Repeat("a", 60) + fmt.Sprintf("%04x", lo%65536), "type": "dataset"},
		"hours": float64(hi-lo+1) * 3 / 3600, "utterances": hi - lo + 1, "splits": []any{}, "source": "bench",
	})
	var id string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindDataset, Name: name,
			Licence: "CC-BY-4.0", Payload: payload, Actor: registry.Bundled(), Freeze: true}, time.Now())
		id = v.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `INSERT INTO dataset_utterances (version_id, utterance_id, split, transcript_id)
		SELECT $1, 'utt_' || i, 'test', 'trn_' || i FROM generate_series($2::int, $3::int) i`, id, lo, hi)
	return id
}

func timed(t *testing.T, what string, limit time.Duration, fn func() error) {
	t.Helper()
	start := time.Now()
	if err := fn(); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	took := time.Since(start)
	t.Logf("%s: %v", what, took)
	if took > limit {
		t.Errorf("%s took %v, more than %v", what, took, limit)
	}
}

// TestLeakageAtStandSize runs the three leakage checks at the size of the stand: 34 replay golden sets of 300
// utterances, the replay-base training set (34 × 300), a 10 000-utterance training set, two fingerprints per
// utterance. Each check must stay well under a second and answer exactly the planted overlap.
func TestLeakageAtStandSize(t *testing.T) {
	pool := openDB(t)
	ctx := context.Background()
	exec(t, pool, `INSERT INTO sources (id, name, licence, kind, training_cleared, created_by) VALUES ('src_bench', 'bench', 'CC-BY-4.0', 'public', true, '{}')`)

	const sets, per = 34, 300
	golden := make([]string, sets)
	for g := range sets {
		golden[g] = dataset(t, pool, fmt.Sprintf("dataset/replay-golden-l%02d", g), fmt.Sprintf("l%02d", g), true, g*per+1, (g+1)*per)
	}
	replay := dataset(t, pool, "dataset/replay-base", "l00", false, 20001, 20000+sets*per)
	train := dataset(t, pool, "dataset/train-he", "he-IL", false, 40001, 50000)
	exec(t, pool, "ANALYZE")

	d := defaults.Get()
	frozen := make([]registry.Version, sets)
	timed(t, "freeze 34 golden sets (each checked against every trainable version and every trained one)", 10*time.Second, func() error {
		for g := range sets {
			err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
				v, created, drafts, err := Freeze(ctx, tx, FreezeInput{Dataset: golden[g], Actor: auth.DevActor()}, d, time.Now())
				if err != nil {
					return err
				}
				if !created || v.Kind != registry.KindGoldenSet {
					return fmt.Errorf("golden %d: created %v kind %s", g, created, v.Kind)
				}
				frozen[g] = v
				return events.Append(ctx, tx, auth.DevActor(), nil, drafts)
			})
			if err != nil {
				return err
			}
		}
		return nil
	})

	// The training set gains one utterance of golden set 7 (7*300+5) and one acoustic near-duplicate of golden set 12.
	exec(t, pool, `INSERT INTO dataset_utterances (version_id, utterance_id, split, transcript_id) VALUES ($1, 'utt_2105', 'train', 'trn_2105')`, train)
	exec(t, pool, `UPDATE utterance_fingerprints SET value = 'ac3610' WHERE utterance_id = 'utt_40010' AND kind = 'acoustic'`)
	exec(t, pool, "ANALYZE")

	var hits []data.GoldenOverlap
	timed(t, "training exclusion: 10k-utterance set against 34 golden sets", time.Second, func() (err error) {
		hits, err = data.GoldenOverlaps(ctx, pool, []string{train})
		return err
	})
	if len(hits) != 2 || hits[0].GoldenSetID == hits[1].GoldenSetID {
		t.Fatalf("overlaps %+v", hits)
	}
	want := map[string]data.Overlap{
		frozen[7].ID:  {VersionID: train, OtherID: golden[7], Utterances: 1},
		frozen[12].ID: {VersionID: train, OtherID: golden[12], Utterances: 1, ByFingerprint: 1},
	}
	for _, h := range hits {
		if w, ok := want[h.GoldenSetID]; !ok || h.Overlap != w {
			t.Errorf("overlap %+v, want %+v", h, w)
		}
	}
	timed(t, "training exclusion: replay-base (10 200) against 34 golden sets", time.Second, func() error {
		h, err := data.GoldenOverlaps(ctx, pool, []string{replay})
		if err == nil && len(h) != 0 {
			err = fmt.Errorf("replay-base overlaps %+v", h)
		}
		return err
	})
	timed(t, "freeze check of a golden set with the planted overlap", time.Second, func() error {
		_, err := Prepare(ctx, pool, FreezeInput{Dataset: golden[7], Name: "again-7", Actor: auth.DevActor()}, d)
		if pe, ok := problems.As(err); !ok || pe.Type != problems.GoldenSetLeakage || len(pe.Errors) != 1 ||
			!strings.Contains(pe.Errors[0].Message, train) {
			return fmt.Errorf("freeze of golden 7: %w", err)
		}
		return nil
	})

	// A project that trained on the 10k set may adopt golden set 0, not 7 or 12.
	var p projects.Project
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		p, _, err = projects.Create(ctx, tx, projects.NewInput{Slug: "hebrew", Name: "Hebrew"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var artifactHash string
	if err := pool.QueryRow(ctx, "SELECT payload->'artifact'->>'hash' FROM registry_versions WHERE id = $1", train).Scan(&artifactHash); err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `INSERT INTO pipeline_runs (id, project_id, pipeline, source, definition, actor) VALUES ('plr_t', $1, 'fit', 'inline', '{}', '{}')`, p.ID)
	exec(t, pool, `INSERT INTO pipeline_steps (id, pipeline_run_id, project_id, step, position, kind, kind_version, inputs, resources)
		VALUES ('pls_t', 'plr_t', $1, 'fit', 0, 'fit', '1', jsonb_build_object('data', jsonb_build_object('hash', $2::text, 'type', 'dataset')),
		'{"jobKind":"training"}')`, p.ID, artifactHash)
	for _, c := range []struct {
		g    int
		leak bool
	}{{0, false}, {7, true}, {12, true}} {
		timed(t, fmt.Sprintf("adoption check of golden set %d", c.g), time.Second, func() error {
			err := CheckAdoption(ctx, pool, p.ID, frozen[c.g])
			if pe, ok := problems.As(err); c.leak != (err != nil) || (err != nil && (!ok || pe.Type != problems.GoldenSetLeakage)) {
				return fmt.Errorf("adopt golden %d: %w", c.g, err)
			}
			return nil
		})
	}

	// The plans: every overlap query reaches dataset_utterances and utterance_fingerprints through their indexes.
	var trained []string
	timed(t, "datasets the project trained on", time.Second, func() (err error) {
		trained, err = TrainedDatasets(ctx, pool, p.ID)
		return err
	})
	if len(trained) != 1 || trained[0] != train {
		t.Errorf("trained on %v, want [%s]", trained, train)
	}

	// Index-backed: with sequential scans priced out, every overlap query reaches dataset_utterances and
	// utterance_fingerprints through an index, so a registry far larger than the stand's stays an index lookup per
	// utterance of the side asked about. (At the stand's size the planner may prefer hash joins; the timings above
	// are the budget.)
	for name, q := range map[string]struct {
		sql  string
		args []any
	}{
		"training exclusion": {data.GoldenOverlapsQuery(), []any{[]string{train}}},
		"freeze check":       {data.TrainableOverlapsQuery(), []any{[]string{golden[7]}}},
		"adoption check":     {data.OverlapsQuery(), []any{[]string{golden[7]}, []string{train}}},
	} {
		plan := explain(t, pool, q.sql, q.args...)
		for _, table := range []string{"dataset_utterances", "utterance_fingerprints"} {
			if strings.Contains(plan, "Seq Scan on "+table) {
				t.Errorf("the %s scans %s sequentially:\n%s", name, table, plan)
			}
		}
	}
}

// explain answers the plan of sql with sequential scans priced out.
func explain(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var lines []string
	err := pgx.BeginFunc(context.Background(), pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(context.Background(), "SET LOCAL enable_seqscan = off"); err != nil {
			return err
		}
		rows, err := tx.Query(context.Background(), "EXPLAIN "+sql, args...)
		if err != nil {
			return err
		}
		lines, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}
