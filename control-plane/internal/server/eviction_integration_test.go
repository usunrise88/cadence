//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/eviction"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

type evictionPlan struct {
	Artifacts []struct {
		Hash, Type, RunID, PipelineRunID, Reason string
		Size                                     int64
	}
	Kept []struct {
		Hash, Reason string
	}
	BytesFreed int64
	Blobs      int
	Permanent  bool
}

func (p evictionPlan) hashes() []string {
	out := []string{}
	for _, a := range p.Artifacts {
		out = append(out, a.Hash)
	}
	slices.Sort(out)
	return out
}

func (p evictionPlan) keptReason(hash string) string {
	for _, k := range p.Kept {
		if k.Hash == hash {
			return k.Reason
		}
	}
	return ""
}

// evictionFixture builds training states in the content store and the index the way the pipeline engine records
// step outputs (artifacts.Record with the producing step).
type evictionFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	store   *cas.Store
	project string
}

func (f evictionFixture) blob(content string) (string, int64) {
	f.t.Helper()
	h, err := f.store.PutBytes([]byte(content))
	if err != nil {
		f.t.Fatal(err)
	}
	return h, int64(len(content))
}

func (f evictionFixture) size(hash string) int64 {
	f.t.Helper()
	_, n, err := f.store.Has(hash)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f evictionFixture) has(hash string) bool {
	ok, _, _ := f.store.Has(hash)
	return ok
}

func (f evictionFixture) pipelineRun(id, state string) {
	f.t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO pipeline_runs (id, project_id, pipeline, source, definition, actor, state)
		VALUES ($1, $2, 'train-stage', 'template', '{}', '{"kind":"user","id":"usr_admin"}', $3)`, id, f.project, state); err != nil {
		f.t.Fatal(err)
	}
}

// record indexes ref as an output of plr's train step, ageHours ago.
func (f evictionFixture) record(ref steps.ArtifactRef, plr string, ageHours int) {
	f.t.Helper()
	ctx := context.Background()
	err := pgx.BeginFunc(ctx, f.pool, func(tx pgx.Tx) error {
		var prod *artifacts.Producer
		if plr != "" {
			prod = &artifacts.Producer{PipelineRunID: plr, StepID: "pls_" + plr, Step: "train", Output: "state"}
		}
		if _, err := artifacts.Record(ctx, tx, f.store, ref, f.project, prod); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "UPDATE artifacts SET created_at = now() - make_interval(hours => $2) WHERE hash = $1", ref.Hash, ageHours)
		return err
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// fileState records a file training state.
func (f evictionFixture) fileState(content, plr string, ageHours int) string {
	h, n := f.blob(content)
	f.record(steps.ArtifactRef{Hash: h, Type: "training-state", Size: n}, plr, ageHours)
	return h
}

// dir records a directory artifact of typ from files (path → content) and returns its hash.
func (f evictionFixture) dir(typ, plr string, ageHours int, files map[string]string) string {
	f.t.Helper()
	var m cas.Manifest
	var total int64
	for p, c := range files {
		h, n := f.blob(c)
		m.Files = append(m.Files, cas.File{Path: p, Hash: h, Size: n})
		total += n
	}
	h, err := f.store.PutManifest(m)
	if err != nil {
		f.t.Fatal(err)
	}
	f.record(steps.ArtifactRef{Hash: h, Type: typ, Size: total}, plr, ageHours)
	return h
}

// mirror copies every blob of the store into the backup mirror, as a backup set does.
func mirror(t *testing.T, store *cas.Store, dst string) {
	t.Helper()
	root := store.Root()
	err := filepath.WalkDir(filepath.Join(root, "b3"), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		return copyBlob(p, filepath.Join(dst, rel))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func copyBlob(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	in, err := os.Open(src) //nolint:gosec // test paths
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.Create(dst) //nolint:gosec // test paths
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func TestArtifactsEvict(t *testing.T) {
	mirrorDir := t.TempDir()
	svc := &eviction.Service{MirrorDir: mirrorDir}
	e := startWith(t, func(c *Config) {
		svc.CAS = c.CAS
		c.Eviction = svc
	}, func(pool *pgxpool.Pool, js *jobs.Service) {
		svc.Pool = pool
		svc.Register(js)
	})
	ctx := context.Background()
	p := e.newProject("evict")
	f := evictionFixture{t: t, pool: e.pool, store: e.admin.CAS, project: p.ID}

	// A finished run: a directory state sharing a file with the run's checkpoint, and a file state.
	f.pipelineRun("plr_done", "done")
	d1 := f.dir("training-state", "plr_done", 30, map[string]string{"optim.bin": "optimizer state of d1", "weights.bin": "shared weights"})
	ckp := f.dir("checkpoint", "plr_done", 29, map[string]string{"model.bin": "shared weights"})
	d2 := f.fileState("state d2", "plr_done", 20)
	// A failed run: the newest state stays for runs.resume.
	f.pipelineRun("plr_failed", "failed")
	f1 := f.fileState("state f1 (older)", "plr_failed", 10)
	f2 := f.fileState("state f2 (newest)", "plr_failed", 5)
	// A running run keeps its state; a finished run's state a waiting step job resumes from is kept too.
	f.pipelineRun("plr_running", "running")
	r1 := f.fileState("state r1", "plr_running", 2)
	f.pipelineRun("plr_named", "done")
	x1 := f.fileState("state x1", "plr_named", 2)
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (id, river_id, kind, actor) VALUES ('job_step', 987654, 'step', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO step_jobs (job_id, spec, kind_ref, job_kind, gpu)
		VALUES ('job_step', jsonb_build_object('overrides', jsonb_build_object('resumeFrom', $1::text)), 'k@1', 'training', true)`,
		x1); err != nil {
		t.Fatal(err)
	}
	mirror(t, f.store, mirrorDir)
	// Recorded after the last backup: the mirror lacks it, so it waits for the next one.
	u1 := f.fileState("state u1 (not backed up)", "plr_done", 1)

	sharedHash := cas.Hash([]byte("shared weights"))
	optimHash := cas.Hash([]byte("optimizer state of d1"))
	wantBytes := f.size(d1) + f.size(optimHash) + f.size(d2) + f.size(f1)

	// Dry run: the plan, with the shared file kept and not counted.
	var plan evictionPlan
	e.ok(e.do("POST", "/api/artifacts:evict?dryRun=true", "", "Idempotency-Key", e.key()), 200, &plan)
	want := []string{d1, d2, f1}
	slices.Sort(want)
	if !slices.Equal(plan.hashes(), want) || plan.BytesFreed != wantBytes || plan.Blobs != 4 || plan.Permanent {
		t.Fatalf("plan %+v, want %v and %d bytes", plan, want, wantBytes)
	}
	for hash, reason := range map[string]string{f2: "newest state of a failed run", r1: "still running",
		x1: "step job names it", u1: "backup mirror does not hold"} {
		if got := plan.keptReason(hash); !strings.Contains(got, reason) {
			t.Errorf("kept %s: %q, want %q", hash, got, reason)
		}
	}
	if plan.keptReason(ckp) != "" {
		t.Error("a checkpoint is not a training state; it is not even considered")
	}

	// Named artifacts something needs are refused, before and without an approval.
	expectProblem(t, e.do("POST", "/api/artifacts:evict?dryRun=true", `{"hashes":["`+r1+`"]}`, "Idempotency-Key", e.key()),
		409, "artifact-not-evictable")
	expectProblem(t, e.do("POST", "/api/artifacts:evict", `{"hashes":["`+d2+`","`+x1+`"]}`, "Idempotency-Key", e.key()),
		409, "artifact-not-evictable")
	var narrowed evictionPlan
	e.ok(e.do("POST", "/api/artifacts:evict?dryRun=true", `{"runId":"run_none"}`, "Idempotency-Key", e.key()), 200, &narrowed)
	if len(narrowed.Artifacts) != 0 {
		t.Fatalf("a filter by an unknown run selected %v", narrowed.hashes())
	}

	// Agents never evict.
	expectProblem(t, e.agent("POST", "/api/artifacts:evict", "", "Idempotency-Key", e.key()), 403, "policy-denied")

	// The admin's real call waits for an approval; nothing is deleted yet.
	var acc accepted
	e.ok(e.do("POST", "/api/artifacts:evict", "", "Idempotency-Key", e.key()), 202, &acc)
	if a := e.approval(acc.ApprovalID); a.State != "pending" || a.Rule != "store-eviction" || a.Scope != "registry" {
		t.Fatalf("approval %+v", a)
	}
	if !f.has(d1) || e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL`) != 0 {
		t.Fatal("something was evicted before the approval")
	}

	// Approved: the replay queues the job, the job deletes the blobs and marks the rows.
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+acc.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	if decided.Result == nil || decided.Result.Status != 202 {
		t.Fatalf("decided %+v", decided)
	}
	var job struct{ JobID string }
	if err := json.Unmarshal(decided.Result.Body, &job); err != nil || job.JobID == "" {
		t.Fatalf("replay body %s", decided.Result.Body)
	}
	j := e.waitJob(job.JobID, "done")
	var done evictionPlan
	if err := json.Unmarshal(j.Result, &done); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(done.hashes(), want) || done.BytesFreed != wantBytes || done.Blobs != 4 {
		t.Fatalf("job result %+v", done)
	}
	for _, h := range []string{d1, optimHash, d2, f1} {
		if f.has(h) {
			t.Errorf("blob %s is still in the store", h)
		}
	}
	for _, h := range []string{sharedHash, ckp, f2, r1, x1, u1} {
		if !f.has(h) {
			t.Errorf("blob %s was deleted", h)
		}
	}
	if n := e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL AND eviction_job_id = '` + job.JobID + `'`); n != 3 {
		t.Fatalf("%d rows marked evicted", n)
	}
	var got struct {
		Hash    string
		Evicted *struct {
			JobID string
			By    struct{ ID string }
		}
		Files []struct{ Path, Hash string }
	}
	e.ok(e.do("GET", "/api/artifacts/"+d1+"?content=true&path=optim.bin", ""), 200, &got)
	if got.Evicted == nil || got.Evicted.JobID != job.JobID || got.Evicted.By.ID != "usr_admin" || len(got.Files) != 2 {
		t.Fatalf("evicted artifact %+v", got)
	}
	if evs := e.events("entity.artifact." + d1); len(evs) != 1 || evs[0].Type != eviction.EventEvicted {
		t.Fatalf("events %+v", evs)
	}
	var detail map[string]any
	if err := e.pool.QueryRow(ctx, `SELECT detail FROM audit_log WHERE operation = 'artifacts.evict' AND detail IS NOT NULL`).
		Scan(&detail); err != nil || detail["bytesFreed"] != float64(wantBytes) || detail["jobId"] != job.JobID {
		t.Fatalf("audit detail %v (%v)", detail, err)
	}

	// A pipeline input naming an evicted artifact fails as missing.
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := artifacts.Record(ctx, tx, f.store, steps.ArtifactRef{Hash: d2, Type: "training-state", Size: 8}, p.ID, nil)
		return err
	})
	if !errors.Is(err, artifacts.ErrEvicted) {
		t.Fatalf("recording an evicted input: %v", err)
	}

	// Again: nothing left to evict, and a second approved eviction frees nothing.
	var again evictionPlan
	e.ok(e.do("POST", "/api/artifacts:evict?dryRun=true", "", "Idempotency-Key", e.key()), 200, &again)
	if len(again.Artifacts) != 0 || again.BytesFreed != 0 {
		t.Fatalf("second plan %+v", again)
	}
	e.ok(e.do("POST", "/api/artifacts:evict", "", "Idempotency-Key", e.key()), 202, &acc)
	e.ok(e.do("POST", "/api/approvals/"+acc.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	_ = json.Unmarshal(decided.Result.Body, &job)
	j = e.waitJob(job.JobID, "done")
	if err := json.Unmarshal(j.Result, &done); err != nil || len(done.Artifacts) != 0 || done.BytesFreed != 0 {
		t.Fatalf("second job %s (%v)", j.Result, err)
	}

	// Restore from the backup mirror: copy the blob back; the start-time pass clears the eviction.
	hx := strings.TrimPrefix(d2, cas.Prefix)
	if err := copyBlob(filepath.Join(mirrorDir, "b3", hx[:2], hx), filepath.Join(f.store.Root(), "b3", hx[:2], hx)); err != nil {
		t.Fatal(err)
	}
	if _, restored, err := eviction.Backfill(ctx, e.pool, f.store); err != nil || restored != 1 {
		t.Fatalf("restored %d (%v)", restored, err)
	}
	if e.count(`SELECT count(*) FROM artifacts WHERE hash = '`+d2+`' AND evicted_at IS NULL`) != 1 {
		t.Fatal("the restored artifact is still marked evicted")
	}
}

func TestEvictionBackfillsTheFileIndex(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	p := e.newProject("backfill")
	f := evictionFixture{t: t, pool: e.pool, store: e.admin.CAS, project: p.ID}
	d := f.dir("checkpoint", "", 1, map[string]string{"a.bin": "a", "b/c.bin": "c"})
	// A directory recorded before migration 0020 has no file rows.
	if _, err := e.pool.Exec(ctx, "DELETE FROM artifact_files WHERE hash = $1", d); err != nil {
		t.Fatal(err)
	}
	indexed, restored, err := eviction.Backfill(ctx, e.pool, f.store)
	if err != nil || indexed != 1 || restored != 0 {
		t.Fatalf("backfill %d %d %v", indexed, restored, err)
	}
	if n := e.count(`SELECT count(*) FROM artifact_files WHERE hash = '` + d + `'`); n != 2 {
		t.Fatalf("%d file rows", n)
	}
	if indexed, _, _ = eviction.Backfill(ctx, e.pool, f.store); indexed != 0 {
		t.Fatal("a second backfill indexed again")
	}
}
