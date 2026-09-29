package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey is the pg_advisory_lock key that serialises concurrent starts ("cadence-migrate").
const migrationLockKey int64 = 0x636164656e63_01

var migrationName = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.sql$`)

// Migration is one forward-only SQL file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// LoadMigrations reads NNNN_name.sql files from fsys in version order.
func LoadMigrations(fsys fs.FS) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	var out []Migration
	for _, e := range entries {
		m := migrationName.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		version, _ := strconv.Atoi(m[1])
		body, err := fs.ReadFile(fsys, e.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", e.Name(), err)
		}
		out = append(out, Migration{Version: version, Name: m[2], SQL: string(body)})
	}
	slices.SortFunc(out, func(a, b Migration) int { return a.Version - b.Version })
	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("duplicate migration version %04d", out[i].Version)
		}
	}
	return out, nil
}

// Step is a migration owned by a library (River's own tables). It runs after the SQL files on every start, while
// this process holds the migration lock (so concurrent starts still take turns), manages its own transactions on
// the pool, and must be idempotent.
type Step func(ctx context.Context, pool *pgxpool.Pool) error

// Migrate applies the migrations in fsys that the database has not seen, each in its own transaction, while
// holding a session advisory lock so that concurrent starts apply every migration exactly once; then it runs the
// steps under the same lock. It refuses to run when the database records a migration this binary does not know
// (a newer release migrated it). applied counts the SQL files only.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS, steps ...Step) (applied int, err error) {
	migrations, err := LoadMigrations(fsys)
	if err != nil {
		return 0, err
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return 0, fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// Use a fresh context: the caller's may be cancelled, and a leaked session lock blocks the next start.
		if _, uerr := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockKey); uerr != nil {
			err = errors.Join(err, fmt.Errorf("release migration lock: %w", uerr))
		}
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		name       text NOT NULL,
		applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return 0, fmt.Errorf("create schema_migrations: %w", err)
	}
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, fmt.Errorf("read schema_migrations: %w", err)
	}
	done, err := pgx.CollectRows(rows, pgx.RowTo[int])
	if err != nil {
		return 0, fmt.Errorf("read schema_migrations: %w", err)
	}
	known := make(map[int]bool, len(migrations))
	for _, m := range migrations {
		known[m.Version] = true
	}
	for _, v := range done {
		if !known[v] {
			return 0, fmt.Errorf("database has migration %04d which this binary does not know; run the release that applied it or a newer one", v)
		}
	}

	for _, m := range migrations {
		if slices.Contains(done, m.Version) {
			continue
		}
		if err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return fmt.Errorf("apply: %w", err)
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version, name) VALUES ($1, $2)", m.Version, m.Name)
			return err
		}); err != nil {
			return applied, fmt.Errorf("migration %04d_%s: %w", m.Version, m.Name, err)
		}
		applied++
	}
	for i, step := range steps {
		if err := step(ctx, pool); err != nil {
			return applied, fmt.Errorf("migration step %d: %w", i+1, err)
		}
	}
	return applied, nil
}
