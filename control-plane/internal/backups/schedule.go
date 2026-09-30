package backups

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/notify"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Schedule is when sets are taken and tested, and how many are kept (defaults.yaml backups.*, local to the
// policies timezone). Its JSON form is the contract's BackupSchedule.
type Schedule struct {
	Directory          string     `json:"directory"`
	NightlyAt          string     `json:"nightlyAt"`
	RestoreTestWeekday string     `json:"restoreTestWeekday"`
	RestoreTestAt      string     `json:"restoreTestAt"`
	KeepNightly        int        `json:"keepNightly"`
	KeepWeekly         int        `json:"keepWeekly"`
	Timezone           string     `json:"timezone"`
	NextBackupAt       *time.Time `json:"nextBackupAt,omitempty"`
	NextRestoreTestAt  *time.Time `json:"nextRestoreTestAt,omitempty"`

	loc *time.Location
}

// ScheduleOf reads the schedule at now: defaults d, the instance timezone from the policies.
func (s *Service) ScheduleOf(ctx context.Context, q storage.Querier, d *defaults.Defaults, now time.Time) (Schedule, error) {
	p, err := policies.Get(ctx, q, d)
	if err != nil {
		return Schedule{}, err
	}
	sc := Schedule{
		Directory: s.Config.Dir, NightlyAt: d.Backups.NightlyAt.Value, RestoreTestWeekday: d.Backups.RestoreTestWeekday.Value,
		RestoreTestAt: d.Backups.RestoreTestAt.Value, KeepNightly: d.Backups.KeepNightly.Value,
		KeepWeekly: d.Backups.KeepWeekly.Value, Timezone: p.Timezone, loc: p.Location(),
	}
	next := notify.Next(now, notify.MustClock(sc.NightlyAt), sc.loc)
	sc.NextBackupAt = &next
	if wd, ok := notify.ParseWeekday(sc.RestoreTestWeekday); ok {
		nr := notify.NextWeekday(now, wd, notify.MustClock(sc.RestoreTestAt), sc.loc)
		sc.NextRestoreTestAt = &nr
	}
	return sc, nil
}

// Tick is the periodic job: it enqueues the nightly set once its local time has passed today and none was taken
// since (the set taken on the restore-test weekday is the weekly one), and the weekly restore test of the newest
// succeeded set once its time has passed. A start after a missed time catches up at once.
func (s *Service) Tick(ctx context.Context) error {
	now := s.now()
	d := s.Defaults()
	sc, err := s.ScheduleOf(ctx, s.Pool, d, now)
	if err != nil {
		return err
	}
	wd, _ := notify.ParseWeekday(sc.RestoreTestWeekday)
	ctx = auth.WithActor(ctx, System)
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// One scheduler at a time (two control planes during a rolling restart).
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('cadence.backups.schedule'))`); err != nil {
			return fmt.Errorf("lock the backup schedule: %w", err)
		}
		var drafts []events.Draft
		at := notify.Today(now, notify.MustClock(sc.NightlyAt), sc.loc)
		if !now.Before(at) {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backups WHERE trigger IN ('nightly', 'weekly')
				AND created_at >= $1)`, at).Scan(&taken); err != nil {
				return fmt.Errorf("look up tonight's set: %w", err)
			}
			if !taken {
				trigger := TriggerNightly
				if now.In(sc.loc).Weekday() == wd {
					trigger = TriggerWeekly
				}
				_, _, d, err := s.Enqueue(ctx, tx, trigger, System, false)
				if err != nil && !isConflict(err) {
					return err
				}
				drafts = append(drafts, d...)
			}
		}
		rat := notify.Today(now, notify.MustClock(sc.RestoreTestAt), sc.loc)
		if now.In(sc.loc).Weekday() == wd && !now.Before(rat) {
			var id string
			var started *time.Time
			err := tx.QueryRow(ctx, `SELECT id, (restore_test->>'startedAt')::timestamptz FROM backups
				WHERE state = 'succeeded' AND pruned_at IS NULL ORDER BY created_at DESC LIMIT 1`).Scan(&id, &started)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
			case err != nil:
				return fmt.Errorf("find the set to restore: %w", err)
			default:
				var tested bool
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM backups WHERE restore_test IS NOT NULL
					AND (restore_test->>'startedAt')::timestamptz >= $1)`, rat).Scan(&tested); err != nil {
					return fmt.Errorf("look up this week's restore test: %w", err)
				}
				if !tested {
					_, _, d, err := s.EnqueueRestoreTest(ctx, tx, id, -1, System, false)
					if err != nil && !isConflict(err) {
						return err
					}
					drafts = append(drafts, d...)
				}
			}
		}
		if len(drafts) == 0 {
			return nil
		}
		return events.Append(ctx, tx, System, nil, drafts)
	})
}

func isConflict(err error) bool {
	var pe *problems.Error
	return errors.As(err, &pe) && pe.Type == problems.Conflict
}

// Prune removes the files of sets beyond retention: the newest keep_nightly nightly and manual sets and the newest
// keep_weekly weekly sets stay; failed sets keep no files. The rows stay with pruned_at set. The content-store
// mirror is never pruned (blobs are immutable and shared by every set).
func (s *Service) Prune(ctx context.Context) (int, error) {
	d := s.Defaults()
	rows, err := s.Pool.Query(ctx, `SELECT id, coalesce(path, '') FROM (
			SELECT id, path, row_number() OVER (PARTITION BY trigger = 'weekly' ORDER BY created_at DESC) AS n,
				trigger = 'weekly' AS weekly
			FROM backups WHERE state = 'succeeded' AND pruned_at IS NULL) ranked
		WHERE (weekly AND n > $1) OR (NOT weekly AND n > $2)`, d.Backups.KeepWeekly.Value, d.Backups.KeepNightly.Value)
	if err != nil {
		return 0, fmt.Errorf("find sets beyond retention: %w", err)
	}
	type set struct{ id, path string }
	old, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (set, error) {
		var x set
		err := row.Scan(&x.id, &x.path)
		return x, err
	})
	if err != nil {
		return 0, fmt.Errorf("find sets beyond retention: %w", err)
	}
	n := 0
	for _, x := range old {
		if x.path != "" {
			if err := os.RemoveAll(x.path); err != nil {
				return n, fmt.Errorf("remove set %s: %w", x.id, err)
			}
		}
		if _, err := s.update(ctx, x.id, EventPruned, `UPDATE backups SET pruned_at = $2, rev = rev + 1 WHERE id = $1`, s.now()); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
