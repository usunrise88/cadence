//go:build integration

package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// TestLeaseContinuesTheJobTrace: the request that queues a step job starts a trace; the job's span continues it and
// the lease hands the job span's context to the worker (traceparent), so the worker's step spans join the same trace
// as children of the control plane's job span.
func TestLeaseContinuesTheJobTrace(t *testing.T) {
	w := startWorkers(t)
	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})

	ctx, request := w.tracer.Tracer("test").Start(auth.WithActor(context.Background(), auth.DevActor()), "POST /api/runs")
	spec := gpuSpec("train_toy")
	spec.StepID, spec.PipelineRunID, spec.Attempt = "pls_trace", "plr_trace", 1
	var jobID string
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		j, drafts, err := w.jobs.Enqueue(ctx, tx, jobs.Spec{Kind: steps.JobKind, Args: spec})
		jobID = j.ID
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, auth.DevActor(), nil, drafts)
	}); err != nil {
		t.Fatal(err)
	}
	request.End()

	l := f.claim(5)
	if l == nil || l.JobID != jobID {
		t.Fatalf("lease = %+v", l)
	}
	traceID := request.SpanContext().TraceID().String()
	parts := strings.Split(l.Traceparent, "-")
	if len(parts) != 4 || parts[1] != traceID || parts[2] == request.SpanContext().SpanID().String() {
		t.Fatalf("lease traceparent %q does not continue trace %s under the job span", l.Traceparent, traceID)
	}
	w.ok(f.release(l.ID, steps.Outcome{State: steps.StateDone}), http.StatusNoContent, nil)
	w.outcome(jobID)

	deadline := time.Now().Add(10 * time.Second)
	for {
		for _, s := range w.spans.Ended() {
			if s.Name() != "job "+steps.JobKind {
				continue
			}
			if s.SpanContext().TraceID().String() != traceID || s.Parent().SpanID() != request.SpanContext().SpanID() ||
				s.SpanContext().SpanID().String() != parts[2] {
				t.Fatalf("job span %s (parent %s) is not the lease's parent under the request", s.SpanContext().SpanID(), s.Parent().SpanID())
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no job span ended")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
