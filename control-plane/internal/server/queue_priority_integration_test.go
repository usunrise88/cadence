//go:build integration

package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// TestProjectQueuePriority: waiting step jobs start by the project's queue priority, then the job's priority, then
// first come (spec 02 "Budgets"); a projects.edit of budgets.queuePriority reorders jobs already waiting.
func TestProjectQueuePriority(t *testing.T) {
	w := startWorkers(t)
	ids := map[string]string{}
	ctx := auth.WithActor(context.Background(), auth.DevActor())
	for _, slug := range []string{"quiet", "urgent"} {
		if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
			p, drafts, err := projects.Create(ctx, tx, projects.NewInput{Slug: slug, Name: slug, Locales: []string{"he-IL"},
				Domain: "general", Budgets: projects.DefaultBudgets()})
			ids[slug] = p.ID
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, auth.DevActor(), nil, drafts)
		}); err != nil {
			t.Fatal(err)
		}
	}
	var p struct {
		Rev     int
		Budgets struct{ QueuePriority *int }
	}
	w.ok(w.do(http.MethodGet, "/api/projects/quiet", ""), http.StatusOK, &p)
	if p.Budgets.QueuePriority == nil || *p.Budgets.QueuePriority != 0 {
		t.Fatalf("default queue priority = %v, want 0 (defaults.yaml budgets.queue_priority_per_project)", p.Budgets.QueuePriority)
	}
	expectProblem(t, w.do(http.MethodPatch, "/api/projects/quiet", `{"budgets": {"queuePriority": 101}}`,
		"Idempotency-Key", w.key(), "If-Match", fmt.Sprint(p.Rev)), 422, "validation-failed")

	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})
	spec := func(project string, priority int) steps.Spec {
		s := gpuSpec("train_toy")
		s.ProjectID, s.Priority = ids[project], priority
		return s
	}
	// The quiet project's job comes first and has the higher job priority; the unscoped job ties with quiet at the
	// default project priority and loses on job priority.
	quiet := w.enqueue(spec("quiet", 50))
	urgent := w.enqueue(spec("urgent", 0))
	loose := w.enqueue(steps.Spec{Kind: "train_toy", KindVersion: "1", Params: gpuSpec("").Params, Priority: 10,
		Resources: gpuSpec("").Resources})

	// With equal project priorities the job's priority decides.
	var q struct {
		Items []struct {
			JobID           string
			ProjectPriority int
		} `json:"items"`
	}
	w.ok(w.do(http.MethodGet, "/api/queue-entries", ""), http.StatusOK, &q)
	if len(q.Items) != 3 || q.Items[0].JobID != quiet || q.Items[1].JobID != loose || q.Items[2].JobID != urgent {
		t.Fatalf("queue before the edit = %+v", q.Items)
	}

	// Raising the urgent project's priority puts its job first although it came later with a lower job priority.
	w.ok(w.do(http.MethodGet, "/api/projects/urgent", ""), http.StatusOK, &p)
	w.ok(w.do(http.MethodPatch, "/api/projects/urgent", `{"budgets": {"queuePriority": 10}}`,
		"Idempotency-Key", w.key(), "If-Match", fmt.Sprint(p.Rev)), http.StatusOK, &p)
	if p.Budgets.QueuePriority == nil || *p.Budgets.QueuePriority != 10 {
		t.Fatalf("edited queue priority = %v", p.Budgets.QueuePriority)
	}
	w.ok(w.do(http.MethodGet, "/api/queue-entries", ""), http.StatusOK, &q)
	if len(q.Items) != 3 || q.Items[0].JobID != urgent || q.Items[0].ProjectPriority != 10 {
		t.Fatalf("queue after the edit = %+v", q.Items)
	}

	// One training slot per card still holds: claims run the jobs one at a time in that order.
	for _, want := range []string{urgent, quiet, loose} {
		l := f.claim(2)
		if l == nil || l.JobID != want {
			t.Fatalf("claimed %+v, want %s", l, want)
		}
		if again := f.claim(0); again != nil {
			t.Fatalf("a second training job took the card: %+v", again)
		}
		w.ok(f.release(l.ID, steps.Outcome{State: steps.StateDone}), http.StatusNoContent, nil)
		if o := w.outcome(want); o.State != steps.StateDone {
			t.Fatalf("outcome of %s = %+v", want, o)
		}
	}
}

// TestWindowFollowsInstanceZone: an availability window naming no time zone follows policies.timezone.
func TestWindowFollowsInstanceZone(t *testing.T) {
	w := startWorkers(t)
	var pol struct{ Rev int }
	w.ok(w.do(http.MethodGet, "/api/policies", ""), http.StatusOK, &pol)
	w.ok(w.do(http.MethodPatch, "/api/policies", `{"timezone": "Europe/Berlin"}`, "Idempotency-Key", w.key(),
		"If-Match", fmt.Sprint(pol.Rev)), http.StatusOK, nil)
	var host struct{ Rev int }
	w.ok(w.do(http.MethodGet, "/api/compute/staging", ""), http.StatusOK, &host)
	body := `{"cards": [{"index": 0, "windows": {"training": [{"days": ["wed"], "start": "22:00", "end": "08:00"}]}}]}`
	w.ok(w.do(http.MethodPatch, "/api/compute/staging", body, "Idempotency-Key", w.key(), "If-Match", fmt.Sprint(host.Rev)), http.StatusOK, nil)

	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})
	w.clock.Set(time.Date(2026, 9, 30, 19, 30, 0, 0, time.UTC)) // Wednesday 21:30 Berlin: closed
	job := w.enqueue(gpuSpec("train_toy"))
	if l := f.claim(0); l != nil {
		t.Fatalf("leased before the window opened in the instance zone: %+v", l)
	}
	w.clock.Set(time.Date(2026, 9, 30, 20, 30, 0, 0, time.UTC)) // 22:30 Berlin, still before 22:00 UTC: open
	l := f.claim(2)
	if l == nil || l.JobID != job {
		t.Fatalf("lease in the instance-zone window = %+v", l)
	}
	w.clock.Set(time.Date(2026, 10, 1, 6, 30, 0, 0, time.UTC)) // 08:30 Berlin: closed (read as UTC it would be open)
	if stop, reason := f.report(l.ID, map[string]any{}); !stop || reason != "window-closed" {
		t.Fatalf("report after the close in the instance zone = %v %q", stop, reason)
	}
	w.ok(f.release(l.ID, steps.Outcome{State: steps.StateCancelled}), http.StatusNoContent, nil)
	w.jobCmd(job, "cancel")
}
