package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// EventDigest is the in-app form of the daily digest, on Topic.
const EventDigest = "notification.digest"

// System is the actor of the digest.
var System = auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}

// SpendFunc reports the GPU-hours a project used in [since, until); metered is false until GPU use is metered
// (phase 2 · stream R plugs its meter in here).
type SpendFunc func(ctx context.Context, q storage.Querier, projectID string, since, until time.Time) (hours float64, metered bool, err error)

// JobCount counts one job kind's endings in the digest window.
type JobCount struct {
	Kind      string `json:"kind"`
	Done      int    `json:"done"`
	Failed    int    `json:"failed"`
	Cancelled int    `json:"cancelled"`
	Running   int    `json:"running"`
}

// ProjectSpend is one project's GPU-hours against its daily budget.
type ProjectSpend struct {
	Project        string   `json:"project"`
	BudgetGPUHours float64  `json:"budgetGpuHours"`
	UsedGPUHours   *float64 `json:"usedGpuHours,omitempty"` // nil: not metered yet
}

// Digest is the daily digest: runs and evals (their jobs), spend against budgets, open approvals, the events held
// for it, and the last backup.
type Digest struct {
	Since         time.Time      `json:"since"`
	Until         time.Time      `json:"until"`
	Jobs          []JobCount     `json:"jobs"`
	Spend         []ProjectSpend `json:"spend"`
	OpenApprovals int            `json:"openApprovals"`
	Approvals     []string       `json:"approvals"` // the oldest pending operations, at most 5
	Held          []string       `json:"held"`      // titles of events held for the digest
	LastBackup    string         `json:"lastBackup,omitempty"`
	// Branches are the branches waiting for a person (project: branch), when the digester knows them.
	Branches []string `json:"branches,omitempty"`
}

// BranchesFunc lists the branches waiting for a person as "project: branch" (bootstrap.Service.WaitingBranches).
type BranchesFunc func(ctx context.Context) ([]string, error)

// BuildDigest gathers the digest of [since, until) in q.
func BuildDigest(ctx context.Context, q storage.Querier, since, until time.Time, spend SpendFunc) (Digest, error) {
	d := Digest{Since: since, Until: until, Jobs: []JobCount{}, Spend: []ProjectSpend{}, Approvals: []string{}, Held: []string{}}
	rows, err := q.Query(ctx, `SELECT kind,
			count(*) FILTER (WHERE state = 'done' AND finished_at >= $1 AND finished_at < $2),
			count(*) FILTER (WHERE state = 'failed' AND finished_at >= $1 AND finished_at < $2),
			count(*) FILTER (WHERE state = 'cancelled' AND finished_at >= $1 AND finished_at < $2),
			count(*) FILTER (WHERE state IN ('queued', 'running'))
		FROM jobs WHERE (finished_at >= $1 AND finished_at < $2) OR state IN ('queued', 'running')
		GROUP BY kind ORDER BY kind`, since, until)
	if err != nil {
		return d, fmt.Errorf("digest jobs: %w", err)
	}
	d.Jobs, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (JobCount, error) {
		var j JobCount
		err := row.Scan(&j.Kind, &j.Done, &j.Failed, &j.Cancelled, &j.Running)
		return j, err
	})
	if err != nil {
		return d, fmt.Errorf("digest jobs: %w", err)
	}
	ps, err := projects.List(ctx, q, false)
	if err != nil {
		return d, err
	}
	for _, p := range ps {
		s := ProjectSpend{Project: p.Slug, BudgetGPUHours: p.Budgets.GPUHoursPerDay}
		if spend != nil {
			h, metered, err := spend(ctx, q, p.ID, since, until)
			if err != nil {
				return d, err
			}
			if metered {
				s.UsedGPUHours = &h
			}
		}
		d.Spend = append(d.Spend, s)
	}
	if err := q.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE state = 'pending'`).Scan(&d.OpenApprovals); err != nil {
		return d, fmt.Errorf("digest approvals: %w", err)
	}
	rows, err = q.Query(ctx, `SELECT operation FROM approvals WHERE state = 'pending' ORDER BY created_at LIMIT 5`)
	if err != nil {
		return d, fmt.Errorf("digest approvals: %w", err)
	}
	if d.Approvals, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return d, fmt.Errorf("digest approvals: %w", err)
	}
	rows, err = q.Query(ctx, `SELECT title FROM notification_deliveries WHERE state = 'digest' AND created_at < $1
		ORDER BY created_at LIMIT 50`, until)
	if err != nil {
		return d, fmt.Errorf("digest held events: %w", err)
	}
	if d.Held, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return d, fmt.Errorf("digest held events: %w", err)
	}
	var (
		state string
		at    time.Time
	)
	err = q.QueryRow(ctx, `SELECT state, created_at FROM backups ORDER BY created_at DESC LIMIT 1`).Scan(&state, &at)
	if err == nil {
		d.LastBackup = fmt.Sprintf("%s (%s)", state, at.UTC().Format("2006-01-02 15:04 UTC"))
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return d, fmt.Errorf("digest backups: %w", err)
	}
	return d, nil
}

// Title is the digest's one-line summary.
func (d Digest) Title() string {
	return "Cadence daily digest · " + d.Until.Format("Mon 2 Jan")
}

// Text renders the digest as a plain-text message.
func (d Digest) Text() string {
	var b strings.Builder
	b.WriteString("Jobs (last 24 h):")
	if len(d.Jobs) == 0 {
		b.WriteString(" none")
	}
	for _, j := range d.Jobs {
		fmt.Fprintf(&b, "\n• %s: %d done, %d failed", j.Kind, j.Done, j.Failed)
		if j.Cancelled > 0 {
			fmt.Fprintf(&b, ", %d cancelled", j.Cancelled)
		}
		if j.Running > 0 {
			fmt.Fprintf(&b, ", %d queued or running", j.Running)
		}
	}
	b.WriteString("\n\nGPU spend against budgets:")
	if len(d.Spend) == 0 {
		b.WriteString(" no projects")
	}
	for _, s := range d.Spend {
		if s.UsedGPUHours == nil {
			fmt.Fprintf(&b, "\n• %s: budget %.1f GPU-h/day (use not metered yet)", s.Project, s.BudgetGPUHours)
			continue
		}
		fmt.Fprintf(&b, "\n• %s: %.1f of %.1f GPU-h", s.Project, *s.UsedGPUHours, s.BudgetGPUHours)
	}
	fmt.Fprintf(&b, "\n\nOpen approvals: %d", d.OpenApprovals)
	for _, a := range d.Approvals {
		b.WriteString("\n• " + a)
	}
	if len(d.Held) > 0 {
		fmt.Fprintf(&b, "\n\nHeld for the digest (%d):", len(d.Held))
		for _, h := range d.Held {
			b.WriteString("\n• " + h)
		}
	}
	if len(d.Branches) > 0 {
		fmt.Fprintf(&b, "\n\nBranches waiting for review (%d):", len(d.Branches))
		for _, br := range d.Branches {
			b.WriteString("\n• " + br)
		}
	}
	if d.LastBackup != "" {
		b.WriteString("\n\nLast backup: " + d.LastBackup)
	}
	return b.String()
}

// Digester sends the daily digest at the digest time (local to the policies timezone): a periodic job calls Tick,
// which does nothing until the time has come and the day's digest is not out yet.
type Digester struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Defaults func() *defaults.Defaults
	Now      func() time.Time
	Spend    SpendFunc
	Branches BranchesFunc // nil: the digest lists no branches
	// Wake is signalled after the digest was queued for Telegram.
	Wake chan<- struct{}
}

// Tick sends today's digest if it is due; it reports whether it sent one.
func (dg *Digester) Tick(ctx context.Context) (bool, error) {
	now := time.Now()
	if dg.Now != nil {
		now = dg.Now()
	}
	sent := false
	queued := false
	err := pgx.BeginFunc(ctx, dg.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM notification_settings WHERE id = $1 FOR UPDATE`, SettingsID); err != nil {
			return fmt.Errorf("lock notification settings: %w", err)
		}
		env, err := LoadEnv(ctx, tx, dg.Defaults())
		if err != nil {
			return err
		}
		rule := env.Rules[ClassDigest]
		if rule.Timing != TimingDaily || (!rule.InApp && !rule.Telegram) {
			return nil
		}
		at := Today(now, MustClock(env.Settings.DigestTime), env.Location)
		if now.Before(at) {
			return nil
		}
		day := now.In(env.Location).Format(time.DateOnly)
		if s := env.Settings.DigestSentOn; s != nil && s.Format(time.DateOnly) >= day {
			return nil
		}
		d, err := BuildDigest(ctx, tx, at.Add(-24*time.Hour), now, dg.Spend)
		if err == nil && dg.Branches != nil {
			if d.Branches, err = dg.Branches(ctx); err != nil {
				err = fmt.Errorf("digest branches: %w", err)
			}
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE notification_settings SET digest_sent_on = $2::date WHERE id = $1`, SettingsID, day); err != nil {
			return fmt.Errorf("record the digest: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE notification_deliveries SET state = 'digested' WHERE state = 'digest' AND created_at < $1`, now); err != nil {
			return fmt.Errorf("mark digested events: %w", err)
		}
		if decision := Decide(rule, env.Settings, env.BotReady, now, env.Location); decision.State != "" {
			state := decision.State
			if state == StateDigest {
				state = StateQueued
			}
			if err := InsertDelivery(ctx, tx, nil, Notice{Class: ClassDigest, Title: d.Title(), Body: d.Text()}, state); err != nil {
				return err
			}
			queued = state == StateQueued
		}
		sent = true
		if !rule.InApp {
			return nil
		}
		return events.Append(ctx, tx, System, nil, []events.Draft{{
			Topic: Topic, Type: EventDigest, Payload: map[string]any{"digest": d, "title": d.Title(), "text": d.Text()},
		}})
	})
	if err != nil {
		return false, fmt.Errorf("daily digest: %w", err)
	}
	if queued && dg.Wake != nil {
		select {
		case dg.Wake <- struct{}{}:
		default:
		}
	}
	if sent && dg.Log != nil {
		dg.Log.InfoContext(ctx, "daily digest sent")
	}
	return sent, nil
}
