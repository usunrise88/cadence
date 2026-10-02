//go:build integration

package eviction

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

func TestMain(m *testing.M) { testdb.Main(m) }

const project = "prj_evict"

type env struct {
	t     *testing.T
	ctx   context.Context
	pool  *pgxpool.Pool
	store *cas.Store
	svc   *Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	pool, err := storage.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatal(err)
	}
	store, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: ctx, pool: pool, store: store,
		svc: &Service{Pool: pool, CAS: store, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	e.exec(`INSERT INTO projects (id, slug, name) VALUES ($1, 'evict', 'Evict')`, project)
	e.exec(`INSERT INTO pipeline_runs (id, project_id, pipeline, source, definition, actor, state)
		VALUES ('plr_done', $1, 'train-stage', 'template', '{}', '{"kind":"user","id":"usr_admin"}', 'done')`, project)
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) blob(content string) steps.ArtifactRef {
	e.t.Helper()
	h, err := e.store.PutBytes([]byte(content))
	if err != nil {
		e.t.Fatal(err)
	}
	return steps.ArtifactRef{Hash: h, Type: TypeTrainingState, Size: int64(len(content))}
}

// dir puts a directory of files (path → content) into the store and answers its reference and its files' hashes.
func (e *env) dir(typ string, files map[string]string) (steps.ArtifactRef, map[string]string) {
	e.t.Helper()
	var m cas.Manifest
	var total int64
	hashes := map[string]string{}
	for p, c := range files {
		b := e.blob(c)
		m.Files = append(m.Files, cas.File{Path: p, Hash: b.Hash, Size: b.Size})
		hashes[p] = b.Hash
		total += b.Size
	}
	h, err := e.store.PutManifest(m)
	if err != nil {
		e.t.Fatal(err)
	}
	return steps.ArtifactRef{Hash: h, Type: typ, Size: total}, hashes
}

// record indexes ref in its own transaction, as an output of plr_done's train step when it is a training state.
func (e *env) record(ref steps.ArtifactRef) error {
	return pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error {
		_, err := recordIn(e.ctx, tx, e.store, ref)
		return err
	})
}

func recordIn(ctx context.Context, tx pgx.Tx, store *cas.Store, ref steps.ArtifactRef) (artifacts.Artifact, error) {
	var prod *artifacts.Producer
	if ref.Type == TypeTrainingState {
		prod = &artifacts.Producer{PipelineRunID: "plr_done", StepID: "pls_train", Step: "train", Output: "state"}
	}
	return artifacts.Record(ctx, tx, store, ref, project, prod)
}

func (e *env) mustRecord(ref steps.ArtifactRef) {
	e.t.Helper()
	if err := e.record(ref); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) has(hash string) bool {
	ok, _, _ := e.store.Has(hash)
	return ok
}

func (e *env) evicted(hash string) bool {
	e.t.Helper()
	var ev bool
	if err := e.pool.QueryRow(e.ctx, "SELECT evicted_at IS NOT NULL FROM artifacts WHERE hash = $1", hash).Scan(&ev); err != nil {
		e.t.Fatal(err)
	}
	return ev
}

// evict runs the eviction job for hashes, as the approved replay's job would.
func (e *env) evict(jobID string, hashes ...string) Plan {
	e.t.Helper()
	args, _ := json.Marshal(jobArgs{Hashes: hashes})
	out, err := e.svc.run(e.ctx, &jobs.Run{Job: jobs.Job{ID: jobID, Actor: auth.Actor{Kind: auth.KindUser, ID: "usr_admin"}}, Args: args})
	if err != nil {
		e.t.Fatalf("eviction job: %v", err)
	}
	return out.(Plan)
}

// waitForLockWaiter returns once a session waits for an advisory lock (the store lock).
func (e *env) waitForLockWaiter() {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var n int
		if err := e.pool.QueryRow(e.ctx, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND NOT granted`).Scan(&n); err != nil {
			e.t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatal("nobody waits for the store lock")
}

func TestEvictionInterleavedWithRecord(t *testing.T) {
	t.Run("a Record between marking and deleting keeps the bytes", func(t *testing.T) {
		e := newEnv(t)
		state := e.blob("state recorded again")
		e.mustRecord(state)
		d, files := e.dir(TypeTrainingState, map[string]string{"optim.bin": "optimizer", "weights.bin": "weights of d"})
		e.mustRecord(d)
		shared := files["weights.bin"]
		ckp, _ := e.dir("checkpoint", map[string]string{"model.bin": "weights of d"}) // in the store, not indexed yet

		e.svc.testHook = func(phase string) {
			if phase != "marked" {
				return
			}
			if !e.evicted(state.Hash) || !e.evicted(d.Hash) {
				t.Error("the job did not mark the rows before deleting")
			}
			// A reused step records the state again, and a step records a checkpoint that shares d's weights.
			if err := e.record(state); err != nil {
				t.Errorf("recording a marked state whose bytes are still there: %v", err)
			}
			if err := e.record(ckp); err != nil {
				t.Errorf("recording a checkpoint sharing a file: %v", err)
			}
		}
		done := e.evict("job_1", state.Hash, d.Hash)

		if got := done.Hashes(); !slices.Equal(got, []string{d.Hash}) {
			t.Fatalf("evicted %v, want only the directory", got)
		}
		if e.evicted(state.Hash) || !e.has(state.Hash) {
			t.Fatal("the state recorded again lost its bytes or stayed evicted")
		}
		if !e.has(shared) {
			t.Fatal("the file the new checkpoint lists was deleted")
		}
		if e.has(d.Hash) || e.has(files["optim.bin"]) {
			t.Fatal("the directory's own blobs are still there")
		}
		for _, ref := range []steps.ArtifactRef{state, ckp} {
			if _, err := artifacts.Verify(e.store, ref); err != nil {
				t.Fatalf("a live artifact does not verify: %v", err)
			}
		}
	})

	t.Run("a Record during the deletion waits and finds the bytes gone", func(t *testing.T) {
		e := newEnv(t)
		state := e.blob("state deleted")
		e.mustRecord(state)
		recorded := make(chan error, 1)
		e.svc.testHook = func(phase string) {
			if phase != "deleting" {
				return
			}
			go func() { recorded <- e.record(state) }()
			e.waitForLockWaiter() // the Record waits for the deletion
		}
		done := e.evict("job_2", state.Hash)

		if got := done.Hashes(); !slices.Equal(got, []string{state.Hash}) || done.Blobs != 1 {
			t.Fatalf("evicted %+v", done)
		}
		if err := <-recorded; !errors.Is(err, artifacts.ErrEvicted) {
			t.Fatalf("the waiting Record answered %v, want ErrEvicted", err)
		}
		if !e.evicted(state.Hash) || e.has(state.Hash) {
			t.Fatal("the state is live or its bytes are back")
		}
	})

	t.Run("a Record in progress holds the plan off until it commits", func(t *testing.T) {
		e := newEnv(t)
		d, files := e.dir(TypeTrainingState, map[string]string{"weights.bin": "weights shared later"})
		e.mustRecord(d)
		ckp, _ := e.dir("checkpoint", map[string]string{"model.bin": "weights shared later"})

		tx, err := e.pool.Begin(e.ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(e.ctx) }()
		if _, err := recordIn(e.ctx, tx, e.store, ckp); err != nil {
			t.Fatal(err)
		}
		planned := make(chan Plan, 1)
		go func() { planned <- e.evict("job_3", d.Hash) }()
		e.waitForLockWaiter() // the job waits for the open Record
		if err := tx.Commit(e.ctx); err != nil {
			t.Fatal(err)
		}
		done := <-planned

		if got := done.Hashes(); !slices.Equal(got, []string{d.Hash}) {
			t.Fatalf("evicted %v", got)
		}
		if !e.has(files["weights.bin"]) {
			t.Fatal("the file the committed checkpoint lists was deleted")
		}
		if _, err := artifacts.Verify(e.store, ckp); err != nil {
			t.Fatalf("the checkpoint does not verify: %v", err)
		}
	})
}

func TestReferencesAreIndexLookups(t *testing.T) {
	e := newEnv(t)
	ref := func(c string) string { return e.blob(c).Hash }
	inJob, inEndedJob, inRun, inStep, inDoneStep, inRegistry, unnamed :=
		ref("job"), ref("ended job"), ref("run"), ref("step"), ref("done step"), ref("registry"), ref("unnamed")

	e.exec(`INSERT INTO jobs (id, river_id, kind, actor) VALUES ('job_a', 1, 'step', '{}'), ('job_b', 2, 'step', '{}')`)
	// Named deep inside the spec, and inside a longer string: strpos found both, so must the index.
	e.exec(`INSERT INTO step_jobs (job_id, spec, kind_ref, job_kind, gpu, state) VALUES
		('job_a', jsonb_build_object('inputs', jsonb_build_object('x', jsonb_build_object('note', 'from cas://' || $1::text || '/weights'))), 'k@1', 'training', true, 'leased'),
		('job_b', jsonb_build_object('overrides', jsonb_build_object('resumeFrom', $2::text)), 'k@1', 'training', true, 'ended')`,
		inJob, inEndedJob)
	e.exec(`INSERT INTO pipeline_runs (id, project_id, pipeline, source, definition, actor, inputs)
		VALUES ('plr_running', $1, 'p', 'inline', '{}', '{}', jsonb_build_object('base', jsonb_build_object('hash', $2::text)))`,
		project, inRun)
	e.exec(`INSERT INTO pipeline_steps (id, pipeline_run_id, project_id, step, position, kind, kind_version, state, inputs) VALUES
		('pls_a', 'plr_running', $1, 'a', 0, 'k', '1', 'queued', jsonb_build_object('in', jsonb_build_object('hash', $2::text))),
		('pls_b', 'plr_running', $1, 'b', 1, 'k', '1', 'done', jsonb_build_object('in', jsonb_build_object('hash', $3::text))),
		('pls_c', 'plr_running', $1, 'c', 2, 'k', '1', 'waiting', NULL)`, project, inStep, inDoneStep)
	e.exec(`INSERT INTO registry_collections (id, kind, name, created_by) VALUES ('reg_a', 'dataset_version', 'd', '{}')`)
	e.exec(`INSERT INTO registry_versions (id, collection_id, version, fingerprint, payload, created_by)
		VALUES ('ver_a', 'reg_a', '2026-10-01.0123456789ab', repeat('a', 64), jsonb_build_object('files', jsonb_build_array($1::text)), '{}')`,
		inRegistry)
	// Eval records and metrics read their scores and hypotheses for good (evals and gates).
	typed := func(c, typ string) string {
		r := e.blob(c)
		r.Type = typ
		e.mustRecord(r)
		return r.Hash
	}
	scores, hyps, metric := typed("scores", "scores"), typed("hypotheses", "hypotheses"), typed("metric", "metric_scores")
	e.exec(`INSERT INTO eval_records (id, model_key, golden_set_version_id, normalizer_version_id, decoding_hash, scorer, profile,
		scores_hash, hypotheses_hash, summary) VALUES ('erc_a', 'm', 'ver_a', 'ver_a', 'sha256:x', 'wer_score@1', '160ms', $1, $2, '{}')`,
		scores, hyps)
	e.exec(`INSERT INTO eval_metrics (id, model_key, golden_set_version_id, decoding_hash, scorer, metric, scores_hash, summary)
		VALUES ('erm_a', 'm', 'ver_a', 'sha256:x', 'entity_score@1', 'entities', $1, '{}')`, metric)

	cases := []struct {
		hash, want string
	}{
		{inJob, "step job names it"},
		{inEndedJob, ""},
		{inRun, "input of a running pipeline run"},
		{inStep, "pipeline step that has not finished"},
		{inDoneStep, ""},
		{inRegistry, "registry version"},
		{scores, "eval record"},
		{hyps, "eval record"},
		{metric, "eval record"},
		{unnamed, ""},
	}
	err := pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error {
		for _, c := range cases {
			got, err := referenced(e.ctx, tx, c.hash)
			if err != nil {
				return err
			}
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Errorf("referenced(%s) = %q, want %q", c.hash, got, c.want)
			}
		}
		// The planner uses the indexes rather than scanning (with sequential scans discouraged, as on a large table).
		if _, err := tx.Exec(e.ctx, "SET LOCAL enable_seqscan = off"); err != nil {
			return err
		}
		rows, err := tx.Query(e.ctx, "EXPLAIN "+referencesQuery, unnamed)
		if err != nil {
			return err
		}
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		plan := strings.Join(lines, "\n")
		for _, idx := range []string{"step_jobs_artifact_hashes_idx", "pipeline_runs_artifact_hashes_idx",
			"pipeline_steps_artifact_hashes_idx", "registry_versions_artifact_hashes_idx", "checkpoints_artifact_idx",
			"eval_records_scores_idx", "eval_records_hypotheses_idx", "eval_metrics_scores_idx"} {
			if !strings.Contains(plan, idx) {
				t.Errorf("the plan does not use %s:\n%s", idx, plan)
			}
		}
		if strings.Contains(plan, "Seq Scan") {
			t.Errorf("the plan scans:\n%s", plan)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
