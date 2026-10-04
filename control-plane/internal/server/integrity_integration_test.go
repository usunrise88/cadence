//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

// Fix stream F2 after the phase-4 audit (2026-10-04): eviction relies only on copies it read back (M1), training
// refuses non-commercial and no-derivatives licences whatever was cleared (M2), registry references resolve through
// data.lock (C5), training refuses an evicted dataset before the lease (C6) and datasets.get shows the cache now (C7).

// writeCopy writes content on a mount under root at the path of blob hash, laid out like the store.
func writeCopy(t *testing.T, root, hash, content string) {
	t.Helper()
	dir := filepath.Join(root, "cas", "b3", hash[3:5])
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, hash[3:]), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestEvictionVerifiesCopies: a scan records a copy only at the blob's size, and an eviction deletes a blob only once
// a copy on a mount hashes to it; a bad copy is dropped and the version stays cached (M1). datasets.get reports the
// shards' location and pin from the cache, not from the payload written at freeze (C7).
func TestEvictionVerifiesCopies(t *testing.T) {
	e := startStorage(t)
	ctx := context.Background()
	p := e.newProject("verify")
	f := evictionFixture{t: t, pool: e.pool, store: e.admin.CAS, project: p.ID}
	zero, one := "shard zero", "shard one"
	hz, ho := cas.Hash([]byte(zero)), cas.Hash([]byte(one))
	ds := f.dir("dataset", "", 5, map[string]string{"shard-0.tar": zero, "shard-1.tar": one})
	var vid string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		payload := fmt.Sprintf(`{"artifact":{"hash":%q,"type":"dataset"},"hours":0.1,"shards":[
			{"index":0,"hash":%q,"path":"shard-0.tar","utterances":1,"bytes":10,"seconds":1,"location":"cas","pinned":false},
			{"index":1,"hash":%q,"path":"shard-1.tar","utterances":1,"bytes":9,"seconds":1,"location":"cas","pinned":false}]}`, ds, hz, ho)
		v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindDataset, Name: "dataset/verify-a",
			Actor: registry.Bundled(), Freeze: true, Payload: []byte(payload)}, time.Now())
		vid = v.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	type shardView struct {
		Hash, Location string
		Pinned         bool
	}
	shardsOf := func() []shardView {
		var v struct {
			Dataset struct{ Shards []shardView }
		}
		e.ok(e.do("GET", "/api/registry/datasets/"+vid, ""), 200, &v)
		return v.Dataset.Shards
	}

	root := t.TempDir()
	writeCopy(t, root, hz, zero)
	writeCopy(t, root, ho, "shard ONE, rewritten") // named like the blob, another size
	e.newMount(fmt.Sprintf(`{"name":"backup","kind":"local","root":%q,"readOnly":false}`, root))
	scan := func(rev int) {
		t.Helper()
		var job struct{ JobID string }
		e.ok(e.do("POST", "/api/mounts/backup:scan", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, rev)), 202, &job)
		e.waitJob(job.JobID, "done")
	}
	scan(1)

	// 1. The scan compares sizes: the rewritten file is no copy, so the shard exists on no mount and nothing goes.
	if n := e.count(`SELECT count(*) FROM blob_copies WHERE hash = '` + ho + `'`); n != 0 {
		t.Fatalf("a file of another size was recorded as a copy (%d rows)", n)
	}
	if n := e.count(`SELECT (inventory->>'blobsMismatched')::int FROM mounts WHERE name = 'backup'`); n != 1 {
		t.Errorf("inventory blobsMismatched = %d, want 1", n)
	}
	expectProblem(t, e.do("POST", "/api/registry/datasets:evict", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()),
		409, "artifact-not-evictable")

	// 2. Same size, other bytes: the scan records it, the eviction reads it back, drops it and keeps the version.
	writeCopy(t, root, ho, "shard ONE")
	scan(e.mount("backup").Rev)
	if n := e.count(`SELECT count(*) FROM blob_copies WHERE hash = '` + ho + `'`); n != 1 {
		t.Fatalf("the same-size copy was not recorded (%d rows)", n)
	}
	var job struct{ JobID string }
	e.ok(e.do("POST", "/api/registry/datasets:evict", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()), 202, &job)
	j := e.waitJob(job.JobID, "done")
	if !f.has(ho) || !f.has(hz) {
		t.Fatal("an eviction deleted a shard whose copy does not hold it")
	}
	if e.count(`SELECT count(*) FROM artifacts WHERE evicted_at IS NOT NULL`) != 0 {
		t.Fatal("the version was marked evicted with a bad copy")
	}
	if n := e.count(`SELECT count(*) FROM blob_copies WHERE hash = '` + ho + `'`); n != 0 {
		t.Errorf("the bad copy is still recorded (%d rows)", n)
	}
	if b, _ := json.Marshal(j); !strings.Contains(string(b), "copy on a mount") && !strings.Contains(string(b), "exist on no mount") {
		t.Errorf("the job does not say why it kept the version: %s", b)
	}
	for _, s := range shardsOf() {
		if s.Location != "cas" || s.Pinned {
			t.Errorf("cached shard %+v", s)
		}
	}

	// 3. A waiting job naming the version pins it, and datasets.get says so.
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (id, river_id, kind, actor) VALUES ('job_vpin', 987656, 'step', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO step_jobs (job_id, spec, kind_ref, job_kind, gpu)
		VALUES ('job_vpin', jsonb_build_object('params', jsonb_build_object('dataset', $1::text)), 'k@1', 'training', true)`, vid); err != nil {
		t.Fatal(err)
	}
	for _, s := range shardsOf() {
		if !s.Pinned {
			t.Errorf("shard of a pinned version %+v", s)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE step_jobs SET state = 'ended' WHERE job_id = 'job_vpin'`); err != nil {
		t.Fatal(err)
	}

	// 4. A good copy: the eviction verifies it and deletes; datasets.get reports the shards on the mount.
	writeCopy(t, root, ho, one)
	scan(e.mount("backup").Rev)
	e.ok(e.do("POST", "/api/registry/datasets:evict", `{"versionId":"`+vid+`"}`, "Idempotency-Key", e.key()), 202, &job)
	e.waitJob(job.JobID, "done")
	if f.has(ho) || f.has(hz) {
		t.Fatal("verified shards were not deleted")
	}
	for _, s := range shardsOf() {
		if s.Location != "mount" || s.Pinned {
			t.Errorf("evicted shard %+v", s)
		}
	}
}

// TestTrainingRefusesEvictedAndUnlicensedData: a run's dry run and the queue refuse a dataset the cache evicted,
// naming datasets.materialize (C6); training refuses a source whose licence forbids commercial use or derivatives
// even when it was cleared, and sources.edit refuses to clear one (M2).
func TestTrainingRefusesEvictedAndUnlicensedData(t *testing.T) {
	e, store := startData(t)
	ctx := context.Background()
	if err := pipelinestest.Register(ctx, e.pool,
		evalOnlyKind("fit", "dataset", "training"), evalOnlyKind("fitmix", "mix", "training")); err != nil {
		t.Fatal(err)
	}
	e.newProject("hebrew")
	e.commitPipeline("hebrew", "fit", "name: fit\ninputs: {data: dataset}\nsteps:\n  - {id: fit, kind: fit@1, in: {data: $inputs.data}}\n")
	e.commitPipeline("hebrew", "fitmix", "name: fitmix\ninputs: {mix: mix}\nsteps:\n  - {id: fit, kind: fitmix@1, in: {data: $inputs.mix}}\n")
	train := artifact(t, store, fleursHeader("fleurs-he"), heUtts)
	if err := e.runHook(train, "plr_1", `{}`); err != nil {
		t.Fatal(err)
	}
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, nil)
	trainID := e.datasetIn("dataset/fleurs-he").ID
	e.recordArtifact(store, train)

	post := func(name, input, artifact string, dryRun bool) (int, string, string) {
		resp := e.do("POST", fmt.Sprintf("/api/projects/hebrew/pipelines/%s:run?dryRun=%t", name, dryRun),
			`{"inputs":{"`+input+`":`+artifact+`}}`, "Idempotency-Key", e.key(), "If-Match", "*")
		defer func() { _ = resp.Body.Close() }()
		var p struct{ Type, Detail, ID string }
		_ = json.NewDecoder(resp.Body).Decode(&p)
		return resp.StatusCode, strings.TrimPrefix(p.Type, "https://cadence.local/help/errors/") + " " + p.Detail, p.ID
	}
	ref := func(hash, typ string) string { return fmt.Sprintf(`{"hash":%q,"type":%q}`, hash, typ) }
	mix := ref(renderedMix(t, store, mixEntry{trainID, train.Hash}), "mix")
	if code, d, _ := post("fitmix", "mix", mix, true); code != 200 {
		t.Fatalf("fitmix on the cached corpus: %d %s", code, d)
	}

	// C6. The cache evicted the corpus: a run on it or on a mix of it is refused at the dry run, naming
	// datasets.materialize (the queue asks the same question of a training step's inputs).
	if _, err := e.pool.Exec(ctx, `UPDATE artifacts SET evicted_at = now() WHERE hash = $1`, train.Hash); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, input, body string }{{"fit", "data", ref(train.Hash, "dataset")}, {"fitmix", "mix", mix}} {
		code, d, _ := post(c.name, c.input, c.body, true)
		if code != 409 || !strings.Contains(d, "artifact-missing") || !strings.Contains(d, "datasets.materialize") || !strings.Contains(d, trainID) {
			t.Errorf("%s on an evicted dataset: %d %s", c.name, code, d)
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE artifacts SET evicted_at = NULL WHERE hash = $1`, train.Hash); err != nil {
		t.Fatal(err)
	}

	// M2. sources.edit never clears a licence that forbids commercial use or derivatives, nor moves a cleared source to one.
	p := expectProblem(t, e.do("PATCH", "/api/registry/sources/fleurs", `{"licence":"CC-BY-NC-4.0"}`, "Idempotency-Key", e.key(), "If-Match", `"2"`),
		422, "validation-failed")
	if len(p.Errors) != 1 || p.Errors[0].Path != "/trainingCleared" || !strings.Contains(p.Errors[0].Message, "commercial") {
		t.Errorf("licence change on a cleared source: %+v", p.Errors)
	}
	e.ok(e.do("PATCH", "/api/registry/sources/fleurs", `{"licence":"CC-BY-ND-4.0","trainingCleared":false}`, "Idempotency-Key", e.key(), "If-Match", `"2"`), 200, nil)
	expectProblem(t, e.do("PATCH", "/api/registry/sources/fleurs", `{"trainingCleared":true}`, "Idempotency-Key", e.key(), "If-Match", `"3"`),
		422, "validation-failed")
	// A source cleared before the rule (or by hand): training refuses it all the same, directly and in a mix.
	if _, err := e.pool.Exec(ctx, `UPDATE sources SET training_cleared = true, licence = 'CC-BY-NC-SA-4.0' WHERE name = 'fleurs'`); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, input, body string }{{"fit", "data", ref(train.Hash, "dataset")}, {"fitmix", "mix", mix}} {
		code, d, _ := post(c.name, c.input, c.body, true)
		if code != 422 || !strings.Contains(d, "eval-only-dataset") || !strings.Contains(d, "forbids commercial use") {
			t.Errorf("%s on a non-commercial source: %d %s", c.name, code, d)
		}
	}
	// Mix validation reads the same rule.
	resp := e.do("POST", "/api/projects/hebrew/mixes?dryRun=true", jsonBody(map[string]any{"name": "nc",
		"groups": []any{map[string]any{"name": "target", "datasets": []string{trainID}}}}), "Idempotency-Key", e.key())
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 422 || !strings.Contains(string(b), "forbids commercial use") {
		t.Errorf("mix of a non-commercial source: %d %s", resp.StatusCode, b)
	}
}

// TestRegistryRefsResolveThroughDataLock: a step's registry reference (x-cadence.registryRef) resolves to the version
// data.lock lists at the commit the pipeline is read at, not to the newest adoption; the newest locked version is the
// last created, not the greatest name; an unadopted alias target is not-adopted (C5).
func TestRegistryRefsResolveThroughDataLock(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.Register(ctx, e.pool, append(append([]map[string]any{}, pipelinestest.Fixtures...), memberFixture)...); err != nil {
		t.Fatal(err)
	}
	e.newProject("demo")
	// Two versions of one collection made the same day; the later one has the smaller name.
	register := func(payload, version string, created time.Time) string {
		var id string
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindAuxiliary, Name: "auxiliary/fx-member",
				Description: "a member", Licence: "Apache-2.0", Actor: registry.Bundled(), Payload: json.RawMessage(payload)}, created)
			if err != nil {
				return err
			}
			id = v.ID
			_, err = tx.Exec(ctx, "UPDATE registry_versions SET version = $2, created_at = $3, state = 'frozen', frozen_at = $3 WHERE id = $1", v.ID, version, created)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	day := time.Date(2026, 10, 4, 9, 0, 0, 0, time.UTC)
	older := register(`{"roles":["pseudolabel"],"licence":"Apache-2.0","outputsCommercialUse":true,"languages":["*"],"hfRepo":"org/a","revision":"a1"}`,
		"2026-10-04.ffffffffffff", day)
	newer := register(`{"roles":["pseudolabel"],"licence":"Apache-2.0","outputsCommercialUse":true,"languages":["*"],"hfRepo":"org/a","revision":"b2"}`,
		"2026-10-04.000000000000", day.Add(time.Hour))

	e.commitPipeline("demo", "member", "name: member\ninputs: {text: text}\nsteps:\n  - {id: m, kind: fx_member@1, in: {text: $inputs.text}, params: {auxiliary: auxiliary/fx-member}}\n")
	e.adoptAuxiliary("demo", older, e.do)
	atOlder := e.recipe("demo", "data.lock", "").History[0].Sha
	e.adoptAuxiliary("demo", newer, e.do)
	if lock := e.recipe("demo", "data.lock", "").Content; !strings.Contains(lock, older) || !strings.Contains(lock, newer) {
		t.Fatalf("data.lock lacks the adoptions:\n%s", lock)
	}

	runAt := func(ref, text string) string {
		t.Helper()
		body := `{"inputs":{"text":` + e.putText(text) + `}`
		if ref != "" {
			body += `,"ref":"` + ref + `"`
		}
		var started pipelineRunView
		e.ok(e.do("POST", "/api/projects/demo/pipelines/member:run", body+"}", "Idempotency-Key", e.key(), "If-Match", "*"), 201, &started)
		e.waitPipelineRun(started.ID, "failed") // the in-process fake runs no fx_member; the spec is what matters
		calls := e.leases.CallsOf("m")
		return calls[len(calls)-1].Spec.Auxiliaries["auxiliary"].VersionID
	}
	// At main the lock lists both: the newest is the one created last, whatever its name.
	if got := runAt("", "main"); got != newer {
		t.Errorf("at main the step resolved %s, want the newer %s", got, newer)
	}
	// At the commit that locked only the older version, the step runs it, though the project adopted a newer one since.
	if got := runAt(atOlder, "old"); got != older {
		t.Errorf("at %s the step resolved %s, want the locked %s", atOlder[:12], got, older)
	}

	// aliases.set on a version the project has not adopted answers not-adopted.
	var oasis string
	if err := e.pool.QueryRow(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.name = 'auxiliary/oasis' LIMIT 1`).Scan(&oasis); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do("PUT", "/api/projects/demo/aliases/teacher", `{"version":"`+oasis+`"}`, "Idempotency-Key", e.key()), 422, "not-adopted")
}
