//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// workerLease is a claimed lease as the worker sees it.
type workerLease struct {
	ID     string            `json:"id"`
	JobID  string            `json:"jobId"`
	Spec   steps.Spec        `json:"spec"`
	Inputs map[string]string `json:"inputs"`
}

func (e *env) claim(workerID string) workerLease {
	e.t.Helper()
	var c struct{ Lease *workerLease }
	cards := `[{"index":0,"name":"card","memoryTotalMb":49152,"memoryUsedMb":24000,"utilization":0.1}]`
	e.ok(e.do("POST", "/api/worker-leases:claim", `{"workerId":"`+workerID+`","wait":20,"cards":`+cards+`}`), 200, &c)
	if c.Lease == nil {
		e.t.Fatal("no lease")
	}
	return *c.Lease
}

func (e *env) release(leaseID string, o steps.Outcome) {
	e.t.Helper()
	b, _ := json.Marshal(o)
	resp := e.do("POST", "/api/worker-leases/"+leaseID+":release", string(b))
	if resp.StatusCode != http.StatusNoContent {
		e.t.Fatalf("release %s: %d", leaseID, resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func (e *env) putJSON(v any, typ string, meta map[string]any) steps.ArtifactRef {
	e.t.Helper()
	b, _ := json.Marshal(v)
	h, err := e.admin.CAS.PutBytes(b)
	if err != nil {
		e.t.Fatal(err)
	}
	m, _ := json.Marshal(meta)
	return steps.ArtifactRef{Hash: h, Type: typ, Size: int64(len(b)), Meta: m}
}

func (e *env) projectID(slug string) string {
	e.t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), "SELECT id FROM projects WHERE slug = $1", slug).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// A run on the real worker protocol: the worker publishes the fixture family, calibrates, trains with metric
// batches, is stopped (cancel) after saving its training state, and runs.resume continues from that state; lease
// time on the card is metered as GPU spend.
func TestRunsOnAWorker(t *testing.T) {
	e := start(t)
	e.useWorkers()
	ctx := context.Background()
	if err := pipelinestest.RegisterBaseModel(ctx, e.pool); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]any{}
	for _, f := range pipelinestest.TrainingFixtures {
		k := map[string]any{}
		for key, v := range f {
			if key != "name" && key != "runtime" && key != "runtimeVersionId" {
				k[key] = v
			}
		}
		kinds[f["name"].(string)] = k
	}
	reg, _ := json.Marshal(map[string]any{"host": "staging", "instance": "boot-1",
		"runtime":       map[string]any{"name": "fixture", "version": "1", "digest": "sha256:" + strings.Repeat("c", 64)},
		"stepKinds":     kinds,
		"modelFamilies": []any{pipelinestest.Family}})
	mixID := trainingProject(t, e, "worker")
	var w struct{ ID string }
	e.ok(e.do("POST", "/api/worker-registrations", string(reg)), 200, &w)

	// Calibrate on the worker.
	var cal struct{ PipelineRun struct{ ID string } }
	e.ok(e.do("POST", "/api/projects/worker/runs:calibrate", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"`+mixID+`"}`,
		"Idempotency-Key", e.key()), 201, &cal)
	l := e.claim(w.ID)
	if l.Spec.Kind != pipelinestest.KindCalibrate || !strings.HasPrefix(l.Inputs["data"], "cas://b3:") {
		t.Fatalf("calibrate lease %+v", l)
	}
	e.release(l.ID, steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{
		"calibration": e.putJSON(map[string]any{"measured": true}, "calibration", map[string]any{"secondsPerStep": 2.0, "secondsPerStepStd": 0.1}),
	}})
	e.waitPipelineRun(cal.PipelineRun.ID, "done")

	// The run: its calibrate step is reused, the train step is leased with the run id and posts metrics.
	var r runView
	e.ok(e.do("POST", "/api/projects/worker/runs", `{"baseModel":"`+pipelinestest.BaseModel+`","mix":"he-mix","steps":100}`,
		"Idempotency-Key", e.key()), 201, &r)
	if r.Estimate.Basis != "measured" || r.Status != "queued" {
		t.Fatalf("run %+v", r)
	}
	l = e.claim(w.ID)
	if l.Spec.Kind != pipelinestest.KindTrain || l.Spec.RunID != r.ID || !strings.HasPrefix(l.Inputs["base"], "cas://b3:") {
		t.Fatalf("train lease %+v", l)
	}
	if v := e.waitRun(r.ID, "running"); v.CurrentJobID != l.JobID {
		t.Fatalf("running run %+v (lease job %s)", v, l.JobID)
	}
	now := time.Now().UTC()
	e.ok(e.do("POST", "/api/worker-leases/"+l.ID+"/worker-metrics", fmt.Sprintf(
		`{"points":[{"name":"loss","step":1,"value":3.5,"wallTime":%q},{"name":"loss","step":2,"value":3.1,"wallTime":%q}]}`,
		now.Format(time.RFC3339Nano), now.Add(time.Second).Format(time.RFC3339Nano))), 204, nil)

	// A person stops the run (jobs.cancel of its current job); the worker then saves its training state.
	var job struct{ Rev int }
	e.ok(e.do("GET", "/api/jobs/"+l.JobID, ""), 200, &job)
	e.ok(e.do("POST", "/api/jobs/"+l.JobID+":cancel", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, job.Rev)), 200, nil)
	stopped := e.waitRun(r.ID, "cancelled")
	state := e.putJSON(map[string]any{"step": 40}, "training-state", map[string]any{"step": 40})
	e.release(l.ID, steps.Outcome{State: steps.StateCancelled, Error: &steps.StepError{Type: steps.ErrCancelled, Message: "stopped"},
		Outputs: map[string]steps.ArtifactRef{"state": state}})

	// runs.resume: the same stage continues from the saved state.
	expectProblem(t, e.do("POST", "/api/runs/"+r.ID+":resume", "", "Idempotency-Key", e.key(), "If-Match", `"99"`), 412, "precondition-failed")
	var resumed runView
	e.ok(e.do("POST", "/api/runs/"+r.ID+":resume", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, stopped.Rev)), 200, &resumed)
	if resumed.Status != "queued" || resumed.ResumedFrom != state.Hash {
		t.Fatalf("resumed %+v", resumed)
	}
	l = e.claim(w.ID)
	if l.Spec.Kind != pipelinestest.KindTrain || l.Spec.Overrides.ResumeFrom != state.Hash || l.Spec.Attempt != 2 {
		t.Fatalf("resumed lease %+v", l.Spec)
	}
	time.Sleep(20 * time.Millisecond) // some lease time to meter
	e.release(l.ID, steps.Outcome{State: steps.StateDone, Metrics: map[string]float64{"val_wer": 0.2}, Outputs: map[string]steps.ArtifactRef{
		"checkpoint": e.putJSON(map[string]any{"w": 1}, "checkpoint", map[string]any{"step": 100, "valWer": 0.2, "family": pipelinestest.FamilyName, "weightsHash": "abc"}),
		"state":      e.putJSON(map[string]any{"step": 100}, "training-state", map[string]any{"step": 100}),
	}})
	done := e.waitRun(r.ID, "done")
	if done.CheckpointCount != 1 || done.GPUHours <= 0 || done.FinalMetrics["val_wer"] != 0.2 || done.Timeline[1].Attempts != 2 {
		t.Fatalf("done %+v", done)
	}
	var set struct {
		Series []struct {
			Name  string
			Total int
		}
	}
	e.ok(e.do("GET", "/api/metrics/"+r.ID, ""), 200, &set)
	if len(set.Series) != 1 || set.Series[0].Name != "loss" || set.Series[0].Total != 2 {
		t.Fatalf("metrics %+v", set)
	}
	used, _, err := runs.Spend(ctx, e.pool, e.projectID("worker"), time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
	if err != nil || used <= 0 {
		t.Fatalf("metered spend %v (%v)", used, err)
	}
	// A done run cannot be resumed.
	expectProblem(t, e.do("POST", "/api/runs/"+r.ID+":resume", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, done.Rev)), 409, "conflict")
}
