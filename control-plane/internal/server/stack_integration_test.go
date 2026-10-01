//go:build integration

package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The phase-2 stack end to end: pipelines.run over HTTP → the engine's step jobs → the worker protocol → a worker
// that claims, runs echo by writing its output into the content store, reports and releases → the engine records
// the output and starts the next step. Stream P's engine and stream W's protocol meet only here.
func TestPipelineRunsOnAWorker(t *testing.T) {
	e := start(t)
	e.useWorkers()
	ctx := context.Background()
	if _, err := compute.Seed(ctx, e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
	e.newProject("stack")

	// The worker registers echo@1 as the fixture publishes it (the admin server's fixed actor stands in for cwk_).
	echo := map[string]any{}
	for k, v := range pipelinestest.Fixtures[0] {
		if k != "name" && k != "runtime" && k != "runtimeVersionId" {
			echo[k] = v
		}
	}
	reg := map[string]any{"host": "staging", "instance": "boot-1",
		"runtime":   map[string]any{"name": "toy", "version": "1", "digest": "sha256:" + strings.Repeat("b", 64)},
		"stepKinds": map[string]any{"echo": echo}}
	b, _ := json.Marshal(reg)
	var w struct{ ID string }
	e.ok(e.do("POST", "/api/worker-registrations", string(b)), 200, &w)

	text := e.putText("hello")
	var run struct{ ID string }
	e.ok(e.do("POST", "/api/projects/stack/pipelines/echo:run", `{"inputs":{"text":`+text+`}}`,
		"Idempotency-Key", e.key(), "If-Match", "*"), 201, &run)

	// Serve both steps like a worker: claim, check the step is running, run echo, report, release.
	for i, prefix := range []string{"", "echo: "} {
		var claim struct {
			Lease *struct {
				ID     string            `json:"id"`
				Spec   steps.Spec        `json:"spec"`
				Inputs map[string]string `json:"inputs"`
			} `json:"lease"`
		}
		e.ok(e.do("POST", "/api/worker-leases:claim", `{"workerId":"`+w.ID+`","wait":20,"cards":[]}`), 200, &claim)
		l := claim.Lease
		if l == nil {
			t.Fatalf("step %d: no lease", i)
		}
		var pr pipelineRunView
		e.ok(e.do("GET", "/api/pipeline-runs/"+run.ID, ""), 200, &pr)
		if pr.Steps[i].State != "running" {
			t.Fatalf("step %d is %s while leased, want running", i, pr.Steps[i].State)
		}
		var params struct{ Prefix string }
		_ = json.Unmarshal(l.Spec.Params, &params)
		if params.Prefix != prefix || !strings.HasPrefix(l.Inputs["text"], "cas://b3:") {
			t.Fatalf("step %d lease: params %s inputs %v", i, l.Spec.Params, l.Inputs)
		}
		f, err := e.admin.CAS.Open(strings.TrimPrefix(l.Inputs["text"], "cas://"))
		if err != nil {
			t.Fatal(err)
		}
		in, _ := io.ReadAll(f)
		_ = f.Close()
		out := params.Prefix + string(in)
		h, err := e.admin.CAS.PutBytes([]byte(out))
		if err != nil {
			t.Fatal(err)
		}
		e.ok(e.do("POST", "/api/worker-leases/"+l.ID+":report", `{"progress":{"fraction":0.5,"message":"echoing"}}`), 200, nil)
		o, _ := json.Marshal(steps.Outcome{State: steps.StateDone,
			Outputs: map[string]steps.ArtifactRef{"text": {Hash: h, Type: "text", Size: int64(len(out))}}})
		resp := e.do("POST", "/api/worker-leases/"+l.ID+":release", string(o))
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("release: %d", resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	done := e.waitPipelineRun(run.ID, "done")
	last := done.Steps[1].Outputs["text"].Hash
	if last != cas.Hash([]byte("echo: hello")) {
		t.Fatalf("final output %s, want the hash of %q", last, "echo: hello")
	}
}
