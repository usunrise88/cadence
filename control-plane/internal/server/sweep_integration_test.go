//go:build integration

package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cache"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// TestSweepSkipsAPinnedVersion (a test gap of the phase-4 audit, C12): above the high-water mark the sweep evicts only
// what nothing pins. A dataset version a waiting step job names stays cached, and the sweep queues nothing while it is
// the only candidate; once the job ends, the next sweep evicts it.
func TestSweepSkipsAPinnedVersion(t *testing.T) {
	e := startStorage(t)
	ctx := context.Background()
	p := e.newProject("sweep-pin")
	f := evictionFixture{t: t, pool: e.pool, store: e.admin.CAS, project: p.ID}
	ds := f.dir("dataset", "", 5, map[string]string{"shard-0.tar": "pinned zero", "shard-1.tar": "pinned one"})
	var vid string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindDataset, Name: "dataset/sweep-pin",
			Actor: registry.Bundled(), Freeze: true,
			Payload: []byte(fmt.Sprintf(`{"artifact":{"hash":%q,"type":"dataset"},"hours":0.1}`, ds))}, time.Now())
		vid = v.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Copies of its shards on a mount, so it is evictable but for the pin.
	root := t.TempDir()
	for _, c := range []string{"pinned zero", "pinned one"} {
		h := cas.Hash([]byte(c))
		dir := filepath.Join(root, "cas", "b3", h[3:5])
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, h[3:]), []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.newMount(fmt.Sprintf(`{"name":"exports","kind":"local","root":%q,"readOnly":false}`, root))
	var job struct{ JobID string }
	e.ok(e.do("POST", "/api/mounts/exports:scan", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 202, &job)
	e.waitJob(job.JobID, "done")

	// Pinned: a waiting step job names the version.
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (id, river_id, kind, actor) VALUES ('job_sweep_pin', 987656, 'step', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO step_jobs (job_id, spec, kind_ref, job_kind, gpu)
		VALUES ('job_sweep_pin', jsonb_build_object('params', jsonb_build_object('dataset', $1::text)), 'k@1', 'training', true)`, vid); err != nil {
		t.Fatal(err)
	}
	svc := &cache.Service{Pool: e.pool, CAS: e.admin.CAS, Jobs: e.jobs,
		Disk: func(string) (int64, int64, error) { return 100, 5, nil }}
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM cache_sweeps WHERE job_id IS NOT NULL`); n != 0 {
		t.Fatalf("the sweep queued an eviction of a pinned version (%d sweeps with a job)", n)
	}
	if !f.has(cas.Hash([]byte("pinned zero"))) || e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL`) != 0 {
		t.Fatal("a pinned version was evicted")
	}

	// Unpinned, the next sweep evicts it.
	if _, err := e.pool.Exec(ctx, `UPDATE step_jobs SET state = 'ended' WHERE job_id = 'job_sweep_pin'`); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var sweepJob string
	if err := e.pool.QueryRow(ctx, `SELECT job_id FROM cache_sweeps WHERE job_id IS NOT NULL`).Scan(&sweepJob); err != nil {
		t.Fatal(err)
	}
	e.waitJob(sweepJob, "done")
	if e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL AND hash = '`+ds+`'`) != 1 {
		t.Fatal("the unpinned version was not evicted")
	}
}
