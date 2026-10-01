//go:build integration

package server

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
)

// TestJobCancelledBeforeItsHandlerStarts: a jobs.cancel that lands after River fetched the job but before the
// handler marked it running (River says running, the mirror still queued) ends the job as cancelled once the
// handler sees the request, instead of leaving it queued (the race behind a flaky pipelineRuns.cancel).
func TestJobCancelledBeforeItsHandlerStarts(t *testing.T) {
	e := start(t)
	p := e.newProject("demo")
	ctx := auth.WithActor(context.Background(), testAgent)
	var j jobs.Job
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var drafts []events.Draft
		var err error
		if j, drafts, err = e.jobs.Enqueue(ctx, tx, jobs.Spec{Kind: jobs.KindNoop, ProjectID: p.ID}); err != nil {
			return err
		}
		// What a River fetch does, before the handler runs.
		if _, err := tx.Exec(ctx, `UPDATE river_job SET state = 'running', attempt = 1, attempted_at = now()
			WHERE id = (SELECT river_id FROM jobs WHERE id = $1)`, j.ID); err != nil {
			return err
		}
		return events.Append(ctx, tx, testAgent, nil, drafts)
	}); err != nil {
		t.Fatal(err)
	}
	var v jobView
	e.ok(e.agent("POST", "/api/jobs/"+j.ID+":cancel", "", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &v)
	if v.State != jobs.StateQueued || v.CancelRequestedAt == "" {
		t.Fatalf("cancel of a fetched job answered %+v", v)
	}
	// The handler now runs (River hands the job over).
	if _, err := e.pool.Exec(ctx, `UPDATE river_job SET state = 'available', scheduled_at = now()
		WHERE id = (SELECT river_id FROM jobs WHERE id = $1)`, j.ID); err != nil {
		t.Fatal(err)
	}
	e.ok(e.agent("GET", "/api/jobs/"+j.ID+":wait?timeout=20", ""), 200, &v)
	if v.State != jobs.StateCancelled {
		t.Fatalf("a job cancelled before its handler started ended %+v", v)
	}
}
