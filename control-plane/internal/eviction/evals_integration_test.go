//go:build integration

package eviction

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
)

// evalFixture seeds the rows the age retention reads: golden set and normalizer versions, evals with cells linking
// records and metrics, and a registered model.
type evalFixture struct{ *env }

func (f evalFixture) typed(content, typ string) string {
	f.t.Helper()
	r := f.blob(content)
	r.Type = typ
	f.mustRecord(r)
	return r.Hash
}

// eval inserts an eval created ageDays ago with one cell per record (record_id) and per metric key.
func (f evalFixture) eval(id, status string, ageDays int, records ...string) {
	f.t.Helper()
	f.exec(`INSERT INTO evals (id, project_id, status, subject, subject_id, baseline, golden_sets, profiles, decoding, significance,
			estimate, actor, created_at)
		VALUES ($1, $2, $3, '{}', 'ckp_x', '{}', '[]', '[]', '[]', '{}', '{}', '{}', now() - make_interval(days => $4))`,
		id, project, status, ageDays)
	for i, rec := range records {
		f.exec(`INSERT INTO eval_cells (id, eval_id, position, role, golden_set_version_id, normalizer_version_id, profile,
				decoding_index, decoding_hash, model_key, scorer, state, record_id)
			SELECT $1, $2, $3, 'subject', golden_set_version_id, normalizer_version_id, profile, 0, decoding_hash, model_key, scorer,
				'done', id FROM eval_records WHERE id = $4`, id+"_c"+string(rune('0'+i)), id, i, rec)
	}
}

func (f evalFixture) record(id, model, scores, hyps string, ageDays int) {
	f.t.Helper()
	f.exec(`INSERT INTO eval_records (id, model_key, golden_set_version_id, normalizer_version_id, decoding_hash, scorer, profile,
			scores_hash, hypotheses_hash, summary, created_at)
		VALUES ($1, $2, 'ver_gs', 'ver_gs', 'sha256:d', 'wer_score@2', '160ms', $3, NULLIF($4, ''), '{"wer":0.1}', now() - make_interval(days => $5))`,
		id, model, scores, hyps, ageDays)
}

func newEvalFixture(t *testing.T) evalFixture {
	e := newEnv(t)
	e.exec(`INSERT INTO registry_collections (id, kind, name, created_by) VALUES ('reg_gs', 'golden_set', 'golden-set/x', '{}'),
		('reg_model', 'model', 'model/x', '{}')`)
	e.exec(`INSERT INTO registry_versions (id, collection_id, version, fingerprint, payload, created_by)
		VALUES ('ver_gs', 'reg_gs', '2026-08-01.0123456789ab', repeat('a', 64), '{}', '{}')`)
	return evalFixture{e}
}

// mirrorBlobs copies the blobs of hash (a file, or a directory's manifest and files) into the backup mirror.
func (f evalFixture) mirrorBlobs(dir, hash string, files ...string) {
	f.t.Helper()
	for _, b := range append([]string{hash}, files...) {
		src, err := f.store.Path(b)
		if err != nil {
			f.t.Fatal(err)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			f.t.Fatal(err)
		}
		hx := strings.TrimPrefix(b, cas.Prefix)
		dst := filepath.Join(dir, "b3", hx[:2], hx)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

func TestEvalRetention(t *testing.T) {
	f := newEvalFixture(t)
	// An old record whose scores (a directory) go; its hypotheses are shared with a record a recent eval linked.
	oldScores, oldFiles := f.dir("scores", map[string]string{"summary.json": `{"wer":0.1}`, "utterances.jsonl": "old rows"})
	f.mustRecord(oldScores)
	shared := f.typed("shared hypotheses", "hypotheses")
	f.record("erc_old", "m1", oldScores.Hash, shared, 40)
	f.eval("evl_old", "done", 40, "erc_old")
	newScores := f.typed("scores of the newer record", "scores")
	f.record("erc_newer", "m1b", newScores, shared, 40)
	f.eval("evl_recent", "done", 2, "erc_newer") // linked as cached two days ago
	// The metric scores beside the old record (same model, golden set and decoding as evl_old's cell) go too.
	metric := f.typed("old metric", "metric_scores")
	f.exec(`INSERT INTO eval_metrics (id, model_key, golden_set_version_id, decoding_hash, scorer, metric, scores_hash, summary, created_at)
		VALUES ('erm_old', 'm1', 'ver_gs', 'sha256:d', 'entity_score@1', 'entities', $1, '{}', now() - interval '40 days')`, metric)
	// A registered model's eval keeps its records, however old.
	modelScores, modelHyps := f.typed("model scores", "scores"), f.typed("model hypotheses", "hypotheses")
	f.record("erc_model", "m2", modelScores, modelHyps, 90)
	f.eval("evl_model", "done", 90, "erc_model")
	f.exec(`INSERT INTO registry_versions (id, collection_id, version, fingerprint, payload, created_by)
		VALUES ('ver_model', 'reg_model', '2026-07-01.0123456789ab', repeat('b', 64), '{"evalId":"evl_model"}', '{}')`)
	// An unfinished eval keeps what it links.
	runScores := f.typed("running scores", "scores")
	f.record("erc_running", "m3", runScores, "", 50)
	f.eval("evl_running", "running", 50, "erc_running")
	// A recent record stays.
	freshScores := f.typed("fresh scores", "scores")
	f.record("erc_fresh", "m4", freshScores, "", 3)

	plan := func(s *Service) Plan {
		t.Helper()
		var p Plan
		if err := pgx.BeginFunc(f.ctx, f.pool, func(tx pgx.Tx) error {
			var err error
			p, err = s.PlanEvalRetention(f.ctx, tx, 30, time.Now())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	reasons := func(p Plan) map[string]string {
		out := map[string]string{}
		for _, k := range p.Kept {
			out[k.Hash] = k.Reason
		}
		return out
	}

	p := plan(f.svc)
	want := []string{oldScores.Hash, metric}
	slices.Sort(want)
	got := p.Hashes()
	slices.Sort(got)
	if !slices.Equal(got, want) || !p.Permanent || p.Blobs != 4 {
		t.Fatalf("evictable %v (blobs %d), want %v", got, p.Blobs, want)
	}
	kept := reasons(p)
	for h, why := range map[string]string{
		shared: "erc_newer was last used", newScores: "less than 30 days ago", modelScores: "ver_model", modelHyps: "ver_model",
		runScores: "evl_running has not finished", freshScores: "less than 30 days ago",
	} {
		if !strings.Contains(kept[h], why) {
			t.Errorf("kept %s: %q, want %q", h, kept[h], why)
		}
	}

	// With a backup mirror an artifact goes only once the mirror holds every blob of it.
	mirrorDir := t.TempDir()
	mirrored := &Service{Pool: f.pool, CAS: f.store, Log: f.svc.Log, MirrorDir: mirrorDir}
	f.mirrorBlobs(mirrorDir, oldScores.Hash, oldFiles["summary.json"], oldFiles["utterances.jsonl"])
	p = plan(mirrored)
	if got := p.Hashes(); !slices.Equal(got, []string{oldScores.Hash}) || p.Permanent ||
		!strings.Contains(reasons(p)[metric], "backup mirror does not hold") {
		t.Fatalf("mirrored plan %+v", p)
	}

	// The job (as the sweep queues it) marks, emits and deletes.
	args, _ := json.Marshal(jobArgs{RetentionDays: 30})
	out, err := mirrored.run(f.ctx, &jobs.Run{Job: jobs.Job{ID: "job_ret", Actor: jobs.System}, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	done := out.(Plan)
	if !slices.Equal(done.Hashes(), []string{oldScores.Hash}) || done.Blobs != 3 || done.Permanent {
		t.Fatalf("job %+v", done)
	}
	if !f.evicted(oldScores.Hash) || f.has(oldScores.Hash) || f.has(oldFiles["utterances.jsonl"]) || f.evicted(shared) || !f.has(shared) {
		t.Fatal("the job evicted the wrong artifacts")
	}
	var payload struct {
		Permanent bool
		By        auth.Actor
	}
	if err := f.pool.QueryRow(f.ctx, `SELECT payload FROM events WHERE topic = $1 AND type = $2`,
		"entity.artifact."+oldScores.Hash, EventEvicted).Scan(&payload); err != nil || payload.Permanent || payload.By.ID != jobs.System.ID {
		t.Fatalf("event %+v: %v", payload, err)
	}
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT count(*) FROM eval_records`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("%d eval records after the eviction (%v), want all 5", n, err)
	}
	// The second run finds nothing left: the metric is not mirrored yet.
	if p := plan(mirrored); len(p.Artifacts) != 0 {
		t.Fatalf("second plan %+v", p.Artifacts)
	}
}
