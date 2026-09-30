// Package backups takes backup sets of a Cadence instance and proves they restore (docs/spec/06-platform.md
// "Operations": nightly pg_dump and content-store sync, a weekly automated restore into a scratch database with a
// report; 24 h RPO, 1 h RTO).
//
// A set is a directory under CADENCE_BACKUP_DIR/sets: cadence.dump (pg_dump custom format, taken on an exported
// snapshot so the row counts recorded beside it are exact), the sealed secret values (still encrypted: the master
// key is backed up separately, never here) and manifest.json. Content-store blobs are immutable, so they are
// mirrored once into CADENCE_BACKUP_DIR/cas and each set copies only the blobs that are new. The restore test
// restores a set into a scratch database on the same server, compares row counts and the migration version with
// the set's manifest, re-hashes a sample of mirrored blobs, and drops the scratch database.
//
// Sets are rows of the backups table (bkp_), taken by the `backup` job and tested by the `backup.restore_test` job;
// the Scheduler enqueues both at their local times, and retention (keep_nightly / keep_weekly in defaults.yaml)
// removes old sets' files while the rows stay.
package backups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind; Topic carries every backup event (the entity topic repeats them).
const (
	Kind  = "backup"
	Topic = "backups"
)

// Job kinds.
const (
	JobBackup      = "backup"
	JobRestoreTest = "backup.restore_test"
)

// States and triggers.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"

	TriggerNightly = "nightly"
	TriggerWeekly  = "weekly"
	TriggerManual  = "manual"
)

// Event types.
const (
	EventQueued        = "backup.queued"
	EventStarted       = "backup.started"
	EventSucceeded     = "backup.succeeded"
	EventFailed        = "backup.failed"
	EventRestoreQueued = "backup.restore_queued"
	EventRestorePassed = "backup.restore_passed"
	EventRestoreFailed = "backup.restore_failed"
	EventPruned        = "backup.pruned"
)

// System is the actor of scheduled backups.
var System = auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}

// TableCount is a row count of one key table.
type TableCount struct {
	Name string `json:"name"`
	Rows int64  `json:"rows"`
}

// RestoreTable compares one table's rows at backup time and after the restore.
type RestoreTable struct {
	Name     string `json:"name"`
	BackedUp int64  `json:"backedUp"`
	Restored int64  `json:"restored"`
}

// RestoreTest is the report of a restore test (the contract's RestoreTest).
type RestoreTest struct {
	State            string         `json:"state"` // running | passed | failed
	StartedAt        time.Time      `json:"startedAt"`
	FinishedAt       *time.Time     `json:"finishedAt,omitempty"`
	DurationMS       int64          `json:"durationMs,omitempty"`
	Database         string         `json:"database,omitempty"`
	MigrationVersion int64          `json:"migrationVersion,omitempty"`
	Tables           []RestoreTable `json:"tables,omitempty"`
	CASChecked       int            `json:"casChecked,omitempty"`
	Error            string         `json:"error,omitempty"`
}

// Backup is one set. Its JSON form is the contract's Backup.
type Backup struct {
	ID               string       `json:"id"`
	State            string       `json:"state"`
	Trigger          string       `json:"trigger"`
	Rev              int          `json:"rev"`
	JobID            string       `json:"jobId,omitempty"`
	CreatedAt        time.Time    `json:"createdAt"`
	StartedAt        *time.Time   `json:"startedAt,omitempty"`
	FinishedAt       *time.Time   `json:"finishedAt,omitempty"`
	Path             string       `json:"path,omitempty"`
	DumpBytes        *int64       `json:"dumpBytes,omitempty"`
	DumpSHA256       string       `json:"dumpSha256,omitempty"`
	PgDumpVersion    string       `json:"pgDumpVersion,omitempty"`
	ServerVersion    string       `json:"serverVersion,omitempty"`
	MigrationVersion *int64       `json:"migrationVersion,omitempty"`
	Tables           []TableCount `json:"tables,omitempty"`
	CASBlobs         *int64       `json:"casBlobs,omitempty"`
	CASCopied        *int64       `json:"casCopied,omitempty"`
	CASBytesCopied   *int64       `json:"casBytesCopied,omitempty"`
	Error            string       `json:"error,omitempty"`
	RestoreTest      *RestoreTest `json:"restoreTest,omitempty"`
	PrunedAt         *time.Time   `json:"prunedAt,omitempty"`
	Actor            auth.Actor   `json:"-"`
}

const cols = `id, state, trigger, rev, coalesce(job_id, ''), created_at, started_at, finished_at, coalesce(path, ''),
	dump_bytes, coalesce(dump_sha256, ''), coalesce(pg_dump_version, ''), coalesce(server_version, ''),
	migration_version, tables, cas_blobs, cas_copied, cas_bytes_copied, coalesce(error, ''), restore_test, pruned_at,
	actor`

func scan(row pgx.CollectableRow) (Backup, error) {
	var (
		b             Backup
		tables, rtest []byte
	)
	err := row.Scan(&b.ID, &b.State, &b.Trigger, &b.Rev, &b.JobID, &b.CreatedAt, &b.StartedAt, &b.FinishedAt, &b.Path,
		&b.DumpBytes, &b.DumpSHA256, &b.PgDumpVersion, &b.ServerVersion, &b.MigrationVersion, &tables, &b.CASBlobs,
		&b.CASCopied, &b.CASBytesCopied, &b.Error, &rtest, &b.PrunedAt, &b.Actor)
	if err != nil {
		return b, err
	}
	if len(tables) > 0 {
		if err := json.Unmarshal(tables, &b.Tables); err != nil {
			return b, fmt.Errorf("decode backup tables: %w", err)
		}
	}
	if len(rtest) > 0 {
		b.RestoreTest = &RestoreTest{}
		if err := json.Unmarshal(rtest, b.RestoreTest); err != nil {
			return b, fmt.Errorf("decode restore test: %w", err)
		}
	}
	return b, nil
}

func one(rows pgx.Rows, err error, id string) (Backup, error) {
	if err != nil {
		return Backup{}, fmt.Errorf("query backup: %w", err)
	}
	b, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Backup{}, problems.NotFound.New("no backup set %q", id)
	}
	if err != nil {
		return Backup{}, fmt.Errorf("read backup %s: %w", id, err)
	}
	return b, nil
}

// Get returns the set id.
func Get(ctx context.Context, q storage.Querier, id string) (Backup, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM backups WHERE id = $1", id)
	return one(rows, err, id)
}

// List returns sets newest first.
func List(ctx context.Context, q storage.Querier, limit int) ([]Backup, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM backups ORDER BY created_at DESC, id DESC LIMIT $1", limit)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("read backups: %w", err)
	}
	return out, nil
}

// LastRestoreTest returns the newest set with a finished restore test, if any.
func LastRestoreTest(ctx context.Context, q storage.Querier) (Backup, bool, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+` FROM backups WHERE restore_test IS NOT NULL
		ORDER BY (restore_test->>'startedAt')::timestamptz DESC LIMIT 1`)
	if err != nil {
		return Backup{}, false, fmt.Errorf("last restore test: %w", err)
	}
	b, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Backup{}, false, nil
	}
	if err != nil {
		return Backup{}, false, fmt.Errorf("last restore test: %w", err)
	}
	return b, true, nil
}

// Drafts are the events of a change: on the backups topic and the set's entity topic.
func Drafts(b Backup, typ string) []events.Draft {
	ref := &events.EntityRef{Kind: Kind, ID: b.ID, Rev: b.Rev}
	payload := map[string]any{"backup": b}
	if b.Error != "" {
		payload["error"] = b.Error
	}
	if typ == EventRestoreFailed && b.RestoreTest != nil {
		payload["error"] = b.RestoreTest.Error
	}
	return []events.Draft{
		{Topic: Topic, Type: typ, Entity: ref, Payload: payload},
		{Topic: events.EntityTopic(Kind, b.ID), Type: typ, Entity: ref, Payload: payload},
	}
}

// Args are the arguments of both job kinds.
type Args struct {
	BackupID string `json:"backupId"`
}

// Service takes, tests and prunes backup sets.
type Service struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Config   Config
	Jobs     *jobs.Service
	Defaults func() *defaults.Defaults
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Register adds the two job kinds.
func (s *Service) Register(j *jobs.Service) {
	j.Register(JobBackup, func(ctx context.Context, run *jobs.Run) (any, error) {
		var a Args
		if err := json.Unmarshal(run.Args, &a); err != nil {
			return nil, fmt.Errorf("backup args: %w", err)
		}
		b, err := s.Run(ctx, a.BackupID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"backupId": b.ID, "path": b.Path}, nil
	}, jobs.KindOptions{Timeout: 3 * time.Hour})
	j.Register(JobRestoreTest, func(ctx context.Context, run *jobs.Run) (any, error) {
		var a Args
		if err := json.Unmarshal(run.Args, &a); err != nil {
			return nil, fmt.Errorf("restore test args: %w", err)
		}
		b, err := s.RestoreTest(ctx, a.BackupID)
		if err != nil {
			return nil, err
		}
		return map[string]any{"backupId": b.ID, "restoreTest": b.RestoreTest}, nil
	}, jobs.KindOptions{Timeout: 3 * time.Hour})
}

// Plan is the set a backups.new would take (a dry run answers it).
func Plan(trigger string, actor auth.Actor, now time.Time) Backup {
	return Backup{ID: "bkp_" + uuid.Must(uuid.NewV7()).String(), State: StateQueued, Trigger: trigger, Rev: 1,
		CreatedAt: now, Actor: actor}
}

// Enqueue records a queued set and its job in tx (unless dryRun) and returns them with their events. A set
// already queued or running is a conflict: one backup at a time.
func (s *Service) Enqueue(ctx context.Context, tx pgx.Tx, trigger string, actor auth.Actor, dryRun bool) (Backup, string, []events.Draft, error) {
	var busy string
	err := tx.QueryRow(ctx, `SELECT id FROM backups WHERE state IN ('queued', 'running') LIMIT 1`).Scan(&busy)
	if err == nil {
		return Backup{}, "", nil, problems.Conflict.New("backup set %s is still being taken; wait for its job", busy)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Backup{}, "", nil, fmt.Errorf("check running backups: %w", err)
	}
	b := Plan(trigger, actor, s.now())
	if dryRun {
		return b, "", nil, nil
	}
	if s.Jobs == nil {
		return Backup{}, "", nil, errors.New("the job service is not configured")
	}
	job, jd, err := s.Jobs.Enqueue(auth.WithActor(ctx, actor), tx, jobs.Spec{Kind: JobBackup, Args: Args{BackupID: b.ID}})
	if err != nil {
		return Backup{}, "", nil, err
	}
	rows, err := tx.Query(ctx, `INSERT INTO backups (id, state, trigger, job_id, actor, created_at)
		VALUES ($1, 'queued', $2, $3, $4, $5) RETURNING `+cols, b.ID, trigger, job.ID, actor, b.CreatedAt)
	b, err = one(rows, err, b.ID)
	if err != nil {
		return Backup{}, "", nil, err
	}
	return b, job.ID, append(Drafts(b, EventQueued), jd...), nil
}

// EnqueueRestoreTest marks the set's restore test as running and enqueues it in tx (unless dryRun). The set must
// have succeeded and still have its files; rev is its revision (If-Match).
func (s *Service) EnqueueRestoreTest(ctx context.Context, tx pgx.Tx, id string, rev int, actor auth.Actor, dryRun bool) (Backup, string, []events.Draft, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+" FROM backups WHERE id = $1 FOR UPDATE", id)
	b, err := one(rows, err, id)
	if err != nil {
		return Backup{}, "", nil, err
	}
	if rev >= 0 {
		if err := commands.CheckRev(Kind, rev, b.Rev); err != nil {
			return Backup{}, "", nil, err
		}
	}
	switch {
	case b.State != StateSucceeded:
		return Backup{}, "", nil, problems.Conflict.New("backup set %s is %s; only a succeeded set can be restored", id, b.State)
	case b.PrunedAt != nil:
		return Backup{}, "", nil, problems.Conflict.New("backup set %s was removed by retention", id)
	case b.RestoreTest != nil && b.RestoreTest.State == "running":
		return Backup{}, "", nil, problems.Conflict.New("backup set %s is being restored already", id)
	}
	if dryRun {
		return b, "", nil, nil
	}
	if s.Jobs == nil {
		return Backup{}, "", nil, errors.New("the job service is not configured")
	}
	job, jd, err := s.Jobs.Enqueue(auth.WithActor(ctx, actor), tx, jobs.Spec{Kind: JobRestoreTest, Args: Args{BackupID: b.ID}})
	if err != nil {
		return Backup{}, "", nil, err
	}
	rt, _ := json.Marshal(RestoreTest{State: "running", StartedAt: s.now()})
	rows, err = tx.Query(ctx, `UPDATE backups SET restore_test = $2, rev = rev + 1 WHERE id = $1 RETURNING `+cols, id, rt)
	if b, err = one(rows, err, id); err != nil {
		return Backup{}, "", nil, err
	}
	return b, job.ID, append(Drafts(b, EventRestoreQueued), jd...), nil
}

// update applies one change to a set in its own transaction and emits typ.
func (s *Service) update(ctx context.Context, id, typ, sql string, args ...any) (Backup, error) {
	var b Backup
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, sql+" RETURNING "+cols, append([]any{id}, args...)...)
		if b, err = one(rows, err, id); err != nil {
			return err
		}
		return events.Append(ctx, tx, System, nil, Drafts(b, typ))
	})
	return b, err
}
