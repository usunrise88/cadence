package approvals

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/events"
)

// Sweep denies every pending approval older than TTL at now (R5): one transaction with the decisions, their
// approval.decided events and an audit row each. It returns how many expired.
func Sweep(ctx context.Context, pool *pgxpool.Pool, now time.Time) (int, error) {
	var n int
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		expired, err := Expire(ctx, tx, now)
		if err != nil {
			return err
		}
		n = len(expired)
		var drafts []events.Draft
		for _, e := range expired {
			drafts = append(drafts, e.Drafts...)
			if err := audit.Write(ctx, tx, audit.Entry{
				Operation: "approvals.deny", Actor: System, ProjectID: e.Approval.ProjectID,
				Outcome: audit.OutcomeExpired, Status: 200, Rule: e.Approval.Rule, ApprovalID: e.Approval.ID, At: now,
			}); err != nil {
				return err
			}
		}
		return events.Append(ctx, tx, System, nil, drafts)
	})
	if err != nil {
		return 0, fmt.Errorf("expire approvals: %w", err)
	}
	return n, nil
}
