//go:build integration

package backups

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

func TestMain(m *testing.M) { testdb.Main(m) }

// fixture is a migrated database, a content store with two blobs, a secret store with one sealed file and a
// backup service whose pg_dump and pg_restore run inside the Postgres container (the test host may lack a client
// of the server's major version; the container has exactly the right one).
type fixture struct {
	t    *testing.T
	pool *pgxpool.Pool
	svc  *Service
	cas  *cas.Store
	d    defaults.Defaults
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is needed to run pg_dump inside the Postgres container:", err)
	}
	ctx := context.Background()
	dsn := testdb.New(t)
	pool, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	blobs, err := cas.New(filepath.Join(dir, "cas"))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"first blob", "second blob"} {
		if _, err := blobs.PutBytes([]byte(b)); err != nil {
			t.Fatal(err)
		}
	}
	secretsDir := filepath.Join(dir, "secrets")
	if err := os.MkdirAll(filepath.Join(secretsDir, "transit"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "sec_x"), []byte("sealed"), 0o600); err != nil {
		t.Fatal(err)
	}
	docker := []string{"docker", "exec", "-i", "-e", "PGPASSWORD", testdb.ContainerID()}
	f := &fixture{t: t, pool: pool, cas: blobs, d: *defaults.Get()}
	f.svc = &Service{
		Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Defaults: func() *defaults.Defaults { return &f.d },
		Config: Config{
			Dir: filepath.Join(dir, "backups"), CASDir: blobs.Root(), SecretsDir: secretsDir,
			DSN: testdb.InContainer(dsn), PoolDSN: dsn,
			PgDump: append(append([]string{}, docker...), "pg_dump"), PgRestore: append(append([]string{}, docker...), "pg_restore"),
		},
	}
	// Some data to count.
	if _, err := pool.Exec(ctx, `INSERT INTO projects (id, slug, name) VALUES ('prj_a', 'alpha', 'Alpha'), ('prj_b', 'beta', 'Beta')`); err != nil {
		t.Fatal(err)
	}
	return f
}

// queue inserts a queued set directly (the job service is not needed to take one).
func (f *fixture) queue(trigger string, at time.Time) string {
	f.t.Helper()
	b := Plan(trigger, System, at)
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO backups (id, state, trigger, actor, created_at)
		VALUES ($1, 'queued', $2, $3, $4)`, b.ID, trigger, System, at); err != nil {
		f.t.Fatal(err)
	}
	return b.ID
}

func count(t *testing.T, b Backup, table string) int64 {
	t.Helper()
	for _, c := range b.Tables {
		if c.Name == table {
			return c.Rows
		}
	}
	t.Fatalf("no count for %s in %+v", table, b.Tables)
	return 0
}

func TestBackupAndRestoreTest(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	id := f.queue(TriggerManual, time.Now())
	b, err := f.svc.Run(ctx, id)
	if err != nil {
		t.Fatalf("backup: %v", err)
	}
	if b.State != StateSucceeded || b.DumpBytes == nil || *b.DumpBytes == 0 || len(b.DumpSHA256) != 64 ||
		!strings.HasPrefix(b.PgDumpVersion, "17") || !strings.HasPrefix(b.ServerVersion, "17") {
		t.Fatalf("set %+v", b)
	}
	if b.MigrationVersion == nil || *b.MigrationVersion < 15 {
		t.Fatalf("migration version %v", b.MigrationVersion)
	}
	if count(t, b, "projects") != 2 || count(t, b, "notification_rules") != 5 {
		t.Fatalf("tables %+v", b.Tables)
	}
	if *b.CASBlobs != 2 || *b.CASCopied != 2 {
		t.Fatalf("cas blobs %d copied %d", *b.CASBlobs, *b.CASCopied)
	}
	for _, p := range []string{DumpFile, "manifest.json", "secrets/sec_x"} {
		if _, err := os.Stat(filepath.Join(b.Path, p)); err != nil {
			t.Errorf("set lacks %s: %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(b.Path, "secrets", "transit")); err == nil {
		t.Error("the transit area is not a stored secret")
	}
	var m manifest
	raw, _ := os.ReadFile(filepath.Join(b.Path, "manifest.json"))
	if err := json.Unmarshal(raw, &m); err != nil || m.DumpSHA256 != b.DumpSHA256 || m.SecretsCopied != 1 {
		t.Fatalf("manifest %+v %v", m, err)
	}

	// A second set copies only new blobs.
	if _, err := f.cas.PutBytes([]byte("third blob")); err != nil {
		t.Fatal(err)
	}
	b2, err := f.svc.Run(ctx, f.queue(TriggerNightly, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if *b2.CASBlobs != 3 || *b2.CASCopied != 1 {
		t.Fatalf("second set: blobs %d copied %d", *b2.CASBlobs, *b2.CASCopied)
	}

	// The restore test restores the first set into a scratch database and finds what the set recorded — the
	// rows added afterwards are not in it.
	if _, err := f.pool.Exec(ctx, `INSERT INTO projects (id, slug, name) VALUES ('prj_c', 'gamma', 'Gamma')`); err != nil {
		t.Fatal(err)
	}
	r, err := f.svc.RestoreTest(ctx, id)
	if err != nil {
		t.Fatalf("restore test: %v", err)
	}
	rt := r.RestoreTest
	if rt == nil || rt.State != "passed" || rt.MigrationVersion != *b.MigrationVersion || rt.CASChecked != 3 || rt.FinishedAt == nil {
		t.Fatalf("report %+v", rt)
	}
	for _, tbl := range rt.Tables {
		if tbl.Name == "projects" && (tbl.BackedUp != 2 || tbl.Restored != 2) {
			t.Fatalf("projects %+v", tbl)
		}
	}
	var scratch int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM pg_database WHERE datname = $1`, rt.Database).Scan(&scratch); err != nil || scratch != 0 {
		t.Fatalf("scratch database %s left behind (%d, %v)", rt.Database, scratch, err)
	}
	last, ok, err := LastRestoreTest(ctx, f.pool)
	if err != nil || !ok || last.ID != id {
		t.Fatalf("last restore test %v %v %v", last.ID, ok, err)
	}

	// A damaged mirror fails the test (and the failure is an event that notifies).
	var victim string
	_ = filepath.WalkDir(filepath.Join(f.svc.Config.Dir, "cas", "b3"), func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && victim == "" {
			victim = p
		}
		return nil
	})
	if err := os.WriteFile(victim, []byte("bit rot"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.RestoreTest(ctx, id)
	if err == nil || r.RestoreTest.State != "failed" || !strings.Contains(r.RestoreTest.Error, "does not match its hash") {
		t.Fatalf("damaged mirror: %v %+v", err, r.RestoreTest)
	}
	var failed int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE topic = 'backups' AND type = $1`, EventRestoreFailed).Scan(&failed); err != nil || failed != 1 {
		t.Fatalf("restore_failed events %d %v", failed, err)
	}
}

func TestBackupFailureIsRecorded(t *testing.T) {
	f := newFixture(t)
	f.svc.Config.PgDump = []string{"false"} // a client that cannot even say its version
	id := f.queue(TriggerNightly, time.Now())
	b, err := f.svc.Run(context.Background(), id)
	if err == nil || b.State != StateFailed || !strings.Contains(b.Error, "--version") {
		t.Fatalf("failed set %+v, %v", b, err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.svc.Config.Dir, "sets"))
	if len(entries) != 0 {
		t.Fatalf("a failed set keeps files: %v", entries)
	}
	var n int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM events WHERE topic = 'backups' AND type = 'backup.failed'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("backup.failed events %d %v", n, err)
	}
}

func TestRetention(t *testing.T) {
	f := newFixture(t)
	f.d.Backups.KeepNightly.Value, f.d.Backups.KeepWeekly.Value = 2, 1
	ctx := context.Background()
	base := time.Now().Add(-10 * 24 * time.Hour)
	var ids []string
	for i, trig := range []string{TriggerNightly, TriggerWeekly, TriggerNightly, TriggerManual, TriggerWeekly, TriggerNightly} {
		id := f.queue(trig, base.Add(time.Duration(i)*24*time.Hour))
		if _, err := f.svc.Run(ctx, id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Run prunes after each set; the survivors are the newest two nightly/manual sets and the newest weekly one.
	kept := map[string]bool{}
	for _, id := range ids {
		b, err := Get(ctx, f.pool, id)
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(b.Path)
		if b.PrunedAt == nil {
			kept[b.Trigger+":"+id] = true
			if statErr != nil {
				t.Errorf("kept set %s lost its files", id)
			}
		} else if statErr == nil {
			t.Errorf("pruned set %s kept its files", id)
		}
	}
	want := map[string]bool{"manual:" + ids[3]: true, "weekly:" + ids[4]: true, "nightly:" + ids[5]: true}
	if len(kept) != len(want) {
		t.Fatalf("kept %v, want %v", kept, want)
	}
	for k := range want {
		if !kept[k] {
			t.Fatalf("kept %v, want %v", kept, want)
		}
	}
}

func TestSchedule(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	js := jobs.New(f.pool, f.svc.Log)
	js.FetchPollInterval = 100 * time.Millisecond
	f.svc.Jobs = js
	f.svc.Register(js)
	if err := js.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = js.Stop(context.Background()) }()
	// Sunday 2026-10-04 in UTC (the default timezone): before 03:00 nothing happens.
	now := time.Date(2026, 10, 4, 2, 30, 0, 0, time.UTC)
	f.svc.Now = func() time.Time { return now }
	if err := f.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, f.pool, `SELECT count(*) FROM backups`); n != 0 {
		t.Fatalf("%d sets before the nightly time", n)
	}
	// 03:10 on the restore-test weekday: the weekly set is taken, once.
	now = time.Date(2026, 10, 4, 3, 10, 0, 0, time.UTC)
	for range 2 {
		if err := f.svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	id := waitState(t, f.pool, StateSucceeded)
	if b, _ := Get(ctx, f.pool, id); b.Trigger != TriggerWeekly || b.Actor.ID != System.ID {
		t.Fatalf("scheduled set %+v", b)
	}
	if n := countRows(t, f.pool, `SELECT count(*) FROM backups`); n != 1 {
		t.Fatalf("%d sets after two ticks", n)
	}
	// 04:05: the restore test of the newest set runs, once.
	now = time.Date(2026, 10, 4, 4, 5, 0, 0, time.UTC)
	for range 2 {
		if err := f.svc.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		b, _ := Get(ctx, f.pool, id)
		if b.RestoreTest != nil && b.RestoreTest.State == "passed" {
			break
		}
		if b.RestoreTest != nil && b.RestoreTest.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("restore test %+v", b.RestoreTest)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := countRows(t, f.pool, `SELECT count(*) FROM jobs WHERE kind = 'backup.restore_test'`); n != 1 {
		t.Fatalf("%d restore test jobs", n)
	}
	sc, err := f.svc.ScheduleOf(ctx, f.pool, &f.d, now)
	if err != nil || sc.NextBackupAt == nil || !sc.NextBackupAt.Equal(time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)) ||
		!sc.NextRestoreTestAt.Equal(time.Date(2026, 10, 11, 4, 0, 0, 0, time.UTC)) || sc.Timezone != "UTC" {
		t.Fatalf("schedule %+v %v", sc, err)
	}
	// A manual set while one runs is refused.
	tx, _ := f.pool.Begin(ctx)
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `UPDATE backups SET state = 'running' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.svc.Enqueue(ctx, tx, TriggerManual, auth.DevActor(), false); !isConflict(err) {
		t.Fatalf("second concurrent set: %v", err)
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, sql string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitState(t *testing.T, pool *pgxpool.Pool, state string) string {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		var id, st, errText string
		err := pool.QueryRow(context.Background(), `SELECT id, state, coalesce(error, '') FROM backups ORDER BY created_at DESC LIMIT 1`).Scan(&id, &st, &errText)
		if err == nil && st == state {
			return id
		}
		if st == StateFailed {
			t.Fatalf("set failed: %s", errText)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no set reached %s", state)
	return ""
}

// Training states are not mirrored: a blob only a training state holds is skipped (it is read only to resume, and its
// eviction is permanent by design); a file a state shares with a checkpoint is mirrored for the checkpoint.
func TestMirrorSkipsTrainingStates(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	put := func(s string) string {
		t.Helper()
		h, err := f.cas.PutBytes([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	stateOnly, shared, ckp := put("optimizer moments"), put("shared weights"), put("checkpoint manifest")
	state := put("state manifest")
	if _, err := f.pool.Exec(ctx, `INSERT INTO artifacts (hash, type, size, directory) VALUES
			($1, 'training-state', 1, true), ($2, 'checkpoint', 1, true)`, state, ckp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO artifact_files (hash, path, file_hash, size) VALUES
			($1, 'last.ckpt', $2, 1), ($1, 'weights', $3, 1), ($4, 'weights', $3, 1)`,
		state, stateOnly, shared, ckp); err != nil {
		t.Fatal(err)
	}
	b, err := f.svc.Run(ctx, f.queue(TriggerManual, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	mirrored := func(h string) bool {
		hx := strings.TrimPrefix(h, cas.Prefix)
		_, err := os.Stat(filepath.Join(f.svc.Config.Dir, "cas", "b3", hx[:2], hx))
		return err == nil
	}
	for h, want := range map[string]bool{state: false, stateOnly: false, shared: true, ckp: true} {
		if mirrored(h) != want {
			t.Errorf("blob %s mirrored = %v, want %v", h, !want, want)
		}
	}
	// The fixture's two loose blobs, the shared file and the checkpoint manifest.
	if *b.CASBlobs != 4 || *b.CASCopied != 4 {
		t.Fatalf("cas blobs %d copied %d", *b.CASBlobs, *b.CASCopied)
	}
}
