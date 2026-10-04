//go:build integration

package eviction

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/jobs"
)

// The view retention of control-plane tile pyramids (phase 4 tail): a pyramid last viewed more than
// media.tiles_retention_days ago goes for good; one viewed since, a pipeline step's pyramid and a referenced one stay.
func TestTilesRetention(t *testing.T) {
	e := newEnv(t)
	pyramid := func(audio, source string, files map[string]string) string {
		t.Helper()
		ref, _ := e.dir(TypeTiles, files)
		meta, _ := json.Marshal(map[string]any{"audio": audio, "source": source})
		ref.Meta = meta
		e.mustRecord(ref)
		return ref.Hash
	}
	old := pyramid("b3:old-audio", TilesSource, map[string]string{"manifest.json": `{"old":1}`, "c0/l0/0.u8": "old tile"})
	viewed := pyramid("b3:viewed-audio", TilesSource, map[string]string{"manifest.json": `{"viewed":1}`, "c0/l0/0.u8": "viewed tile"})
	never := pyramid("b3:never-viewed", TilesSource, map[string]string{"manifest.json": `{"never":1}`})
	step := pyramid("b3:step-audio", "", map[string]string{"manifest.json": `{"step":1}`})
	e.exec(`UPDATE artifacts SET created_at = now() - interval '40 days', last_used_at = now() - interval '20 days' WHERE hash = $1`, old)
	e.exec(`UPDATE artifacts SET created_at = now() - interval '40 days', last_used_at = now() - interval '2 days' WHERE hash = $1`, viewed)
	e.exec(`UPDATE artifacts SET created_at = now() - interval '3 days' WHERE hash = $1`, never)
	e.exec(`UPDATE artifacts SET created_at = now() - interval '90 days' WHERE hash = $1`, step)

	var p Plan
	if err := pgx.BeginFunc(e.ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		p, err = e.svc.PlanTilesRetention(e.ctx, tx, 14, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := p.Hashes(); !slices.Equal(got, []string{old}) || !p.Permanent || p.Blobs != 3 {
		t.Fatalf("plan %v (blobs %d, permanent %v), want [%s]", got, p.Blobs, p.Permanent, old)
	}
	if len(p.Kept) != 2 {
		t.Fatalf("kept %+v", p.Kept)
	}

	// The job, as the daily sweep queues it: permanent, as the system actor, even with a backup mirror.
	mirrored := &Service{Pool: e.pool, CAS: e.store, Log: e.svc.Log, MirrorDir: t.TempDir()}
	args, _ := json.Marshal(jobArgs{TilesRetentionDays: 14})
	out, err := mirrored.run(e.ctx, &jobs.Run{Job: jobs.Job{ID: "job_tiles", Actor: jobs.System}, Args: args})
	if err != nil {
		t.Fatal(err)
	}
	if done := out.(Plan); !slices.Equal(done.Hashes(), []string{old}) || !done.Permanent {
		t.Fatalf("job %+v", done)
	}
	if !e.evicted(old) || e.has(old) || e.evicted(viewed) || !e.has(viewed) || e.evicted(step) {
		t.Fatal("the retention evicted the wrong pyramids")
	}
	var detail struct {
		TilesRetentionDays int `json:"tilesRetentionDays"`
	}
	if err := e.pool.QueryRow(e.ctx, `SELECT detail FROM audit_log WHERE operation = $1`, Operation).Scan(&detail); err != nil || detail.TilesRetentionDays != 14 {
		t.Fatalf("audit %+v: %v", detail, err)
	}
}
