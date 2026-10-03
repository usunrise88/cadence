//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/cache"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

type mountView struct {
	ID, Name, Kind, Root, URI string
	ReadOnly                  bool
	Rev                       int
	Utterances, Copies        int64
	Health                    struct {
		State, Host, Detail, JobID string
		FreeBytes                  *int64
		ThroughputMBps             *float64
	}
	Inventory *struct {
		Files, Bytes, Blobs int64
		Entries             []struct {
			Path  string
			Files int64
		}
	}
}

func (e *env) mount(ref string) mountView {
	e.t.Helper()
	var m mountView
	e.ok(e.do("GET", "/api/mounts/"+ref, ""), 200, &m)
	return m
}

// newMount asks for a mount and approves it; it answers the registered mount.
func (e *env) newMount(body string) mountView {
	e.t.Helper()
	var acc accepted
	e.ok(e.do("POST", "/api/mounts", body, "Idempotency-Key", e.key()), 202, &acc)
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+acc.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	if decided.Result == nil || decided.Result.Status != 201 {
		e.t.Fatalf("decided %+v", decided)
	}
	var m mountView
	if err := json.Unmarshal(decided.Result.Body, &m); err != nil {
		e.t.Fatal(err)
	}
	return m
}

// mountWorker registers a worker whose runtime has mount_check@1, as a worker publishes it.
func (e *env) mountWorker() string {
	e.t.Helper()
	if _, err := compute.Seed(context.Background(), e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		e.t.Fatal(err)
	}
	xc := func(def any, desc string, rng any) map[string]any {
		return map[string]any{"default": def, "description": desc, "source": "Cadence recommendation", "range": rng}
	}
	kind := map[string]any{"version": mounts.CheckKindVersion, "consumes": map[string]string{}, "produces": map[string]string{},
		"resources": map[string]any{"gpu": false, "gpus": 0, "jobKind": "data"}, "neutral": true, "help": "steps.mount-check",
		"params": map[string]any{"type": "object", "properties": map[string]any{
			"mount":       map[string]any{"type": "string", "default": "", "x-cadence": xc("", "Mount", map[string]any{"maxLength": 40})},
			"uri":         map[string]any{"type": "string", "default": "", "x-cadence": xc("", "URI", map[string]any{"maxLength": 100})},
			"sample_mb":   map[string]any{"type": "integer", "default": 64, "x-cadence": xc(64, "Sample", map[string]any{"min": 1, "max": 4096})},
			"probe_write": map[string]any{"type": "boolean", "default": false, "x-cadence": xc(false, "Probe", "any")},
		}}}
	reg, _ := json.Marshal(map[string]any{"host": "staging", "instance": "boot-m",
		"runtime":   map[string]any{"name": "core", "version": "1", "digest": "sha256:" + strings.Repeat("d", 64)},
		"stepKinds": map[string]any{mounts.CheckKind: kind}, "modelFamilies": []any{}})
	var w struct{ ID string }
	e.ok(e.do("POST", "/api/worker-registrations", string(reg)), 200, &w)
	return w.ID
}

func (e *env) waitMountHealth(ref, state string) mountView {
	e.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		m := e.mount(ref)
		if m.Health.State == state {
			return m
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("mount %s health %+v, want %s", ref, m.Health, state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// startStorage starts a server whose job runner has the mount and cache job kinds (main registers them through
// RegisterJobs; tests build the server after the runner starts). The cache sees a nearly empty disk.
func startStorage(t *testing.T) *env {
	msvc := &mounts.Service{}
	csvc := &cache.Service{Disk: func(string) (int64, int64, error) { return 100, 90, nil }}
	return startWith(t, func(c *Config) {
		msvc.Leases, csvc.CAS = c.Workers, c.CAS
	}, func(pool *pgxpool.Pool, js *jobs.Service) {
		msvc.Pool, csvc.Pool = pool, pool
		msvc.Register(js)
		csvc.Register(js)
	})
}

func TestMountsLifecycle(t *testing.T) {
	e := startStorage(t)
	e.useWorkers()
	ctx := context.Background()
	root := t.TempDir()
	blob := cas.Hash([]byte("a shard"))
	for p, c := range map[string]string{
		"fleurs-sr/2024/a.wav": "RIFF a", "fleurs-sr/2024/b.wav": "RIFF bb", "calls/x.wav": "RIFF x",
		"cas/b3/" + blob[3:5] + "/" + blob[3:]: "a shard",
	} {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	body := fmt.Sprintf(`{"name":"corpora","kind":"local","root":%q,"licenceHint":"CC-BY-4.0"}`, root)

	// A bad request is refused before any approval; a dry run validates.
	expectProblem(t, e.do("POST", "/api/mounts", `{"name":"corpora","kind":"local","root":"relative"}`, "Idempotency-Key", e.key()),
		422, "validation-failed")
	var dry mountView
	e.ok(e.do("POST", "/api/mounts?dryRun=true", body, "Idempotency-Key", e.key()), 200, &dry)
	if dry.Name != "corpora" || !dry.ReadOnly || dry.URI != "mount://corpora/" || dry.Health.State != "unknown" {
		t.Fatalf("dry run %+v", dry)
	}
	// Registering is a registry-scope approval for everyone, agents included.
	var acc accepted
	e.ok(e.agent("POST", "/api/mounts", body, "Idempotency-Key", e.key()), 202, &acc)
	if a := e.approval(acc.ApprovalID); a.Rule != "mount-registration" || a.Scope != "registry" {
		t.Fatalf("agent approval %+v", a)
	}
	m := e.newMount(body)
	if m.ID == "" || m.Health.JobID == "" {
		t.Fatalf("registered %+v: the first health check is queued", m)
	}
	expectProblem(t, e.do("POST", "/api/mounts?dryRun=true", body, "Idempotency-Key", e.key()), 409, "conflict")

	// The worker runs the check: its lease carries the mounts; the outcome becomes the mount's health.
	wid := e.mountWorker()
	l := e.claimLease(wid)
	if l.Spec.Kind != mounts.CheckKind || len(l.Mounts) != 1 || l.Mounts[0].Name != "corpora" || l.Mounts[0].Root != root {
		t.Fatalf("lease %+v", l)
	}
	e.release(l.ID, steps.Outcome{State: steps.StateDone, Metrics: map[string]float64{"reachable": 1, "free_bytes": 1e9,
		"total_bytes": 2e9, "throughput_mbps": 120, "sampled_bytes": 20}})
	h := e.waitMountHealth("corpora", "healthy")
	if h.Health.Host != "staging" || h.Health.FreeBytes == nil || *h.Health.FreeBytes != 1e9 {
		t.Errorf("health %+v", h.Health)
	}

	// A scan records the inventory and the blob copy, then queues a check that fails: the mount is unhealthy.
	var job struct{ JobID string }
	e.ok(e.do("POST", "/api/mounts/corpora:scan", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 202, &job)
	e.waitJob(job.JobID, "done")
	m = e.mount("corpora")
	if m.Inventory == nil || m.Inventory.Files != 4 || m.Inventory.Blobs != 1 || m.Copies != 1 {
		t.Fatalf("after the scan: %+v", m)
	}
	l = e.claimLease(wid)
	e.release(l.ID, steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep,
		Message: "mount corpora: /mnt/corpora is not a directory on this worker"}})
	m = e.waitMountHealth("corpora", "unhealthy")
	if !strings.Contains(m.Health.Detail, "not a directory") {
		t.Errorf("unhealthy detail %q", m.Health.Detail)
	}
	spec := steps.Spec{Kind: "sdp_ingest", Params: json.RawMessage(`{"path":"mount://corpora/fleurs-sr/2024"}`)}
	if err := mounts.CheckJob(ctx, e.pool, spec); err == nil || !strings.Contains(err.Error(), "mount-unhealthy") {
		t.Errorf("a job naming an unhealthy mount: %v", err)
	}
	if pe, ok := problems.As(mounts.CheckJob(ctx, e.pool, spec)); !ok || pe.Type != problems.MountUnhealthy {
		t.Errorf("problem %v", pe)
	}

	// mounts.verify queues another check; the stale If-Match is refused.
	expectProblem(t, e.do("POST", "/api/mounts/corpora:verify", "", "Idempotency-Key", e.key(), "If-Match", `"2"`), 412, "precondition-failed")
	e.ok(e.do("POST", "/api/mounts/"+m.ID+":verify", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 202, &job)

	// Utterance URIs: recorded through mounts.RecordURIs, shown by utterances.get.
	var srcID, uttID string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO sources (id, name, licence, kind, created_by) VALUES ('src_m', 'fleurs-sr', 'CC-BY-4.0',
			'public', '{"kind":"user","id":"usr_admin"}') RETURNING id`).Scan(&srcID); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `INSERT INTO utterances (id, content_hash, source_id, duration_s, language, sample_rate)
			VALUES ('utt_m', $1, $2, 1.5, 'sr-RS', 16000) RETURNING id`, cas.Hash([]byte("pcm")), srcID).Scan(&uttID); err != nil {
			return err
		}
		return mounts.RecordURIs(ctx, tx, uttID, []string{"mount://corpora/fleurs-sr/2024/a.wav#t=0,1.5&ch=0",
			"mount://corpora/fleurs-sr/2024/a.wav#t=0,1.5&ch=0"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return mounts.RecordURIs(ctx, tx, uttID, []string{"mount://elsewhere/a.wav"})
	}); err == nil {
		t.Error("a URI on an unregistered mount was recorded")
	}
	var u struct{ Uris []string }
	e.ok(e.do("GET", "/api/registry/utterances/"+uttID, ""), 200, &u)
	if len(u.Uris) != 1 || u.Uris[0] != "mount://corpora/fleurs-sr/2024/a.wav#t=0,1.5&ch=0" {
		t.Errorf("uris %v", u.Uris)
	}
	var list struct{ Items []mountView }
	e.ok(e.do("GET", "/api/mounts", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Utterances != 1 {
		t.Errorf("mounts.list %+v", list.Items)
	}
}

// claimLease claims a lease and reads the mounts it carries too.
func (e *env) claimLease(workerID string) struct {
	workerLease
	Mounts []mounts.LeaseMount `json:"mounts"`
} {
	e.t.Helper()
	var c struct {
		Lease *struct {
			workerLease
			Mounts []mounts.LeaseMount `json:"mounts"`
		}
	}
	e.ok(e.do("POST", "/api/worker-leases:claim", `{"workerId":"`+workerID+`","wait":20,"cards":[]}`), 200, &c)
	if c.Lease == nil {
		e.t.Fatal("no lease")
	}
	return *c.Lease
}

type cachePlan struct {
	State                       string
	Shards, CopyShards, Missing int
	FreeShards                  int
	FreeBytes, CopyBytes        int64
	Pinned, Blocked             []string
	From                        []struct{ Mount string }
}

// A dataset version whose shards live on a mount is evicted and materialised back; one with a shard on no mount,
// or pinned, is refused.
func TestDatasetEvictAndMaterialize(t *testing.T) {
	e := startStorage(t)
	ctx := context.Background()
	p := e.newProject("cache")
	f := evictionFixture{t: t, pool: e.pool, store: e.admin.CAS, project: p.ID}
	ds := f.dir("dataset", "", 5, map[string]string{"shard-0.tar": "shard zero", "shard-1.tar": "shard one"})
	other := f.dir("dataset", "", 1, map[string]string{"shard-0.tar": "imported only"})
	register := func(name, hash string) string {
		var id string
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindDataset, Name: name,
				Actor: registry.Bundled(), Freeze: true,
				Payload: []byte(fmt.Sprintf(`{"artifact":{"hash":%q,"type":"dataset"},"hours":0.1}`, hash))}, time.Now())
			id = v.ID
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	vid, otherID := register("dataset/cache-a", ds), register("dataset/cache-b", other)

	// Copies of ds's shards on a writable mount, laid out like the store (a backup mirror or an export).
	root := t.TempDir()
	for _, c := range []string{"shard zero", "shard one"} {
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

	// Imported audio exists on no mount: never evicted.
	var plan cachePlan
	expectProblem(t, e.do("POST", "/api/registry/datasets:evict", `{"versionId":"`+otherID+`"}`, "Idempotency-Key", e.key()),
		409, "artifact-not-evictable")
	e.ok(e.do("POST", "/api/registry/datasets:evict?dryRun=true", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()), 200, &plan)
	if len(plan.Blocked) != 0 || plan.FreeShards != 2 || plan.State != "cached" {
		t.Fatalf("evict plan %+v", plan)
	}

	// Pinned while a waiting step job names the version.
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (id, river_id, kind, actor) VALUES ('job_pin', 987655, 'step', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO step_jobs (job_id, spec, kind_ref, job_kind, gpu)
		VALUES ('job_pin', jsonb_build_object('params', jsonb_build_object('dataset', $1::text)), 'k@1', 'training', true)`, vid); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("POST", "/api/registry/datasets:evict?dryRun=true", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()), 200, &plan)
	if len(plan.Pinned) != 1 || !strings.Contains(plan.Pinned[0], "job_pin") {
		t.Fatalf("pins %+v", plan)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE step_jobs SET state = 'ended' WHERE job_id = 'job_pin'`); err != nil {
		t.Fatal(err)
	}

	// storage.get accounts it.
	var use struct {
		HighWaterPct float64
		Datasets     []struct {
			VersionID, State string
			Evictable        bool
		}
		Projects []struct {
			Slug         string
			DatasetBytes int64
		}
	}
	e.ok(e.do("GET", "/api/storage", ""), 200, &use)
	if use.HighWaterPct != 85 || len(use.Datasets) != 2 || len(use.Projects) != 1 || use.Projects[0].Slug != "cache" {
		t.Fatalf("storage %+v", use)
	}

	// Evict: the shards go, the manifest stays, the artifact is marked.
	e.ok(e.do("POST", "/api/registry/datasets:evict", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()), 202, &job)
	e.waitJob(job.JobID, "done")
	for _, c := range []string{"shard zero", "shard one"} {
		if f.has(cas.Hash([]byte(c))) {
			t.Errorf("shard %q is still cached", c)
		}
	}
	if !f.has(ds) || e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL AND hash = '`+ds+`'`) != 1 {
		t.Fatal("the manifest went, or the artifact is not marked evicted")
	}
	if !f.has(other) {
		t.Fatal("another dataset went")
	}

	// Materialise: copied back from the mount, verified, the eviction cleared.
	e.ok(e.do("POST", "/api/registry/datasets:materialize?dryRun=true", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()), 200, &plan)
	if plan.State != "evicted" || plan.CopyShards != 2 || plan.Missing != 0 || len(plan.From) != 1 || plan.From[0].Mount != "exports" {
		t.Fatalf("materialize plan %+v", plan)
	}
	e.ok(e.do("POST", "/api/registry/datasets:materialize", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()), 202, &job)
	e.waitJob(job.JobID, "done")
	for _, c := range []string{"shard zero", "shard one"} {
		if !f.has(cas.Hash([]byte(c))) {
			t.Errorf("shard %q was not copied back", c)
		}
	}
	if e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL`) != 0 {
		t.Fatal("the eviction was not cleared")
	}

	// The sweep: above the high-water mark it evicts what it can, as the system.
	svc := &cache.Service{Pool: e.pool, CAS: e.admin.CAS, Jobs: e.jobs,
		Disk: func(string) (int64, int64, error) { return 100, 5, nil }}
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	var sweepJob string
	if err := e.pool.QueryRow(ctx, `SELECT job_id FROM cache_sweeps`).Scan(&sweepJob); err != nil {
		t.Fatal(err)
	}
	e.waitJob(sweepJob, "done")
	if e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL AND hash = '`+ds+`'`) != 1 ||
		e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL AND hash = '`+other+`'`) != 0 {
		t.Fatal("the sweep evicted the wrong versions")
	}

	// A freeze past the quota is refused.
	d := *e.admin.defaultsDoc()
	d.Storage.ProjectQuotaGB.Value = 1e-9
	if err := cache.CheckQuota(ctx, e.pool, &d, p.ID, 1000); err == nil || !strings.Contains(err.Error(), "storage-quota-exceeded") {
		t.Errorf("quota: %v", err)
	}
}
