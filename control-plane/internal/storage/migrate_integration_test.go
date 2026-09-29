//go:build integration

package storage_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

func TestMain(m *testing.M) { testdb.Main(m) }

func TestMigrateConcurrentRunners(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.New(t)
	known, err := storage.LoadMigrations(migrations.FS)
	if err != nil || len(known) == 0 {
		t.Fatalf("load migrations: %v (%d)", err, len(known))
	}

	const runners = 4
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		applied int
	)
	for range runners {
		wg.Go(func() {
			pool, err := storage.Open(ctx, dsn) // separate pools: separate sessions, as separate processes would be
			if err != nil {
				t.Error(err)
				return
			}
			defer pool.Close()
			n, err := storage.Migrate(ctx, pool, migrations.FS)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			applied += n
			mu.Unlock()
		})
	}
	wg.Wait()
	if applied != len(known) {
		t.Fatalf("runners applied %d migrations in total, want exactly %d", applied, len(known))
	}

	pool, err := storage.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var rows int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&rows); err != nil || rows != len(known) {
		t.Fatalf("schema_migrations has %d rows (%v), want %d", rows, err, len(known))
	}
	var admin string
	if err := pool.QueryRow(ctx, "SELECT name FROM users WHERE id = 'usr_admin'").Scan(&admin); err != nil || admin != "admin" {
		t.Fatalf("seeded admin = %q, %v", admin, err)
	}
	if n, err := storage.Migrate(ctx, pool, migrations.FS); err != nil || n != 0 {
		t.Fatalf("second run applied %d, %v", n, err)
	}

	// A database migrated by a newer release is refused.
	if _, err := pool.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES (9999, 'from_the_future')"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Migrate(ctx, pool, migrations.FS); err == nil || !strings.Contains(err.Error(), "9999") {
		t.Fatalf("unknown migration: err = %v", err)
	}
}
