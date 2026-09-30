//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

type pipelineListView struct {
	Ref    string `json:"ref"`
	Commit string `json:"commit"`
	Items  []struct {
		Name, Source, Path, Version, Error string
		Inputs                             map[string]string
		Steps                              []struct{ ID, Kind string }
	} `json:"items"`
}

type pipelineRunView struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	Rev      int    `json:"rev"`
	Pipeline string `json:"pipeline"`
	Source   string `json:"source"`
	Commit   string `json:"commit"`
	Version  string `json:"version"`
	Error    string `json:"error"`
	Steps    []struct {
		ID, Step, State, JobID string
		Attempts               int
		Outputs                map[string]steps.ArtifactRef
		Departures             []map[string]any
	} `json:"steps"`
}

func (e *env) putText(text string) string {
	e.t.Helper()
	h, err := e.admin.CAS.PutBytes([]byte(text))
	if err != nil {
		e.t.Fatal(err)
	}
	return fmt.Sprintf(`{"hash":%q,"type":"text","size":%d}`, h, len(text))
}

func (e *env) commitPipeline(slug, name, doc string) {
	e.t.Helper()
	if _, err := e.repos.Repos().Commit(context.Background(), slug, repos.Change{Message: "add " + name,
		Author: repos.Signature{Name: "admin", Email: "usr_admin@cadence.local"},
		Files:  map[string][]byte{"pipelines/" + name + ".yaml": []byte(doc)}}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) waitPipelineRun(id, state string) pipelineRunView {
	e.t.Helper()
	var r pipelineRunView
	e.ok(e.do("GET", "/api/pipeline-runs/"+id+":wait?timeout=30", ""), 200, &r)
	if r.State != state {
		e.t.Fatalf("pipeline run %s is %s (%s), want %s: %+v", id, r.State, r.Error, state, r.Steps)
	}
	return r
}

func TestPipelinesOverHTTP(t *testing.T) {
	e := start(t)
	if err := pipelinestest.RegisterKinds(context.Background(), e.pool); err != nil {
		t.Fatal(err)
	}
	e.newProject("demo")
	e.commitPipeline("demo", "mismatch", "name: mismatch\ninputs: {text: text}\nsteps:\n  - {id: a, kind: tally@1, in: {text: $inputs.text}}\n  - {id: b, kind: echo@1, in: {text: a.tally}}\n")
	e.commitPipeline("demo", "broken", "name: broken\nsteps: nope\n")

	// pipelines.list: the bootstrap copied the bundled templates into the repository; a broken file carries error.
	var list pipelineListView
	e.ok(e.do("GET", "/api/projects/demo/pipelines", ""), 200, &list)
	byName := map[string]int{}
	for i, p := range list.Items {
		byName[p.Name] = i
	}
	for _, want := range []string{"echo", "train-stage", "mismatch", "broken"} {
		if _, ok := byName[want]; !ok {
			t.Fatalf("pipelines.list lacks %s: %+v", want, list.Items)
		}
	}
	echo := list.Items[byName["echo"]]
	if echo.Source != "repository" || echo.Path != "pipelines/echo.yaml" || len(echo.Version) != 40 || echo.Inputs["text"] != "text" ||
		len(echo.Steps) != 2 || echo.Steps[1].Kind != "echo@1" || list.Commit == "" {
		t.Errorf("echo %+v (commit %s)", echo, list.Commit)
	}
	if b := list.Items[byName["broken"]]; b.Error == "" || len(b.Steps) != 0 {
		t.Errorf("broken %+v", b)
	}

	text := e.putText("hello")
	body := `{"inputs":{"text":` + text + `}}`
	run := func(name string, query ...string) string {
		q := ""
		if len(query) > 0 {
			q = "?" + query[0]
		}
		return "/api/projects/demo/pipelines/" + name + ":run" + q
	}

	// dryRun: validation errors come back together as pipeline-invalid.
	p := expectProblem(t, e.do("POST", run("train-stage", "dryRun=true"),
		`{"inputs":{"mix":{"hash":"b3:`+strings.Repeat("1", 64)+`","type":"mix"},"base":{"hash":"b3:`+strings.Repeat("2", 64)+`","type":"base_model"}}}`,
		"Idempotency-Key", e.key(), "If-Match", "*"), 422, "pipeline-invalid")
	if len(p.Errors) != 2 || !strings.Contains(p.Errors[0].Message, "oomptimizer_calibrate@1") {
		t.Errorf("unknown kinds: %+v", p.Errors)
	}
	p = expectProblem(t, e.do("POST", run("mismatch", "dryRun=true"), body, "Idempotency-Key", e.key(), "If-Match", "*"), 422, "pipeline-invalid")
	if len(p.Errors) != 1 || p.Errors[0].Path != "steps[1].in.text" {
		t.Errorf("type mismatch: %+v", p.Errors)
	}
	p = expectProblem(t, e.do("POST", run("echo", "dryRun=true"), `{"inputs":{"text":`+text+`},"params":{"second":{"prefix":7,"shout":true}}}`,
		"Idempotency-Key", e.key(), "If-Match", "*"), 422, "pipeline-invalid")
	if len(p.Errors) != 1 || p.Errors[0].Path != "steps[1].params.shout" {
		t.Errorf("bad params: %+v", p.Errors)
	}
	expectProblem(t, e.do("POST", run("broken"), body, "Idempotency-Key", e.key(), "If-Match", "*"), 422, "pipeline-invalid")
	expectProblem(t, e.do("POST", run("nothing-here"), body, "Idempotency-Key", e.key(), "If-Match", "*"), 404, "not-found")

	// A valid dry run answers the plan: resolved parameters, departures, the estimate; nothing is written.
	var plan struct {
		Pipeline, Source, Version string
		Steps                     []struct {
			Step, Kind, KindVersion string
			Params                  map[string]any
			Departures              []map[string]any
			Produces                map[string]string
		}
		Estimate struct {
			Known        bool
			UnknownSteps []string
		}
	}
	e.ok(e.do("POST", run("echo", "dryRun=true"), body, "Idempotency-Key", e.key(), "If-Match", echo.Version), 200, &plan)
	if plan.Version != echo.Version || plan.Source != "repository" || len(plan.Steps) != 2 || plan.Steps[1].Params["prefix"] != "echo: " ||
		len(plan.Steps[1].Departures) != 1 || plan.Steps[1].Departures[0]["default"] != "" || plan.Steps[0].Produces["text"] != "text" ||
		plan.Estimate.Known || len(plan.Estimate.UnknownSteps) != 2 {
		t.Errorf("plan %+v", plan)
	}
	if n := e.count("SELECT count(*) FROM pipeline_runs"); n != 0 {
		t.Fatalf("a dry run wrote %d pipeline runs", n)
	}

	// A stale version is a precondition failure; the real run answers 201 with the pipeline run.
	expectProblem(t, e.do("POST", run("echo"), body, "Idempotency-Key", e.key(), "If-Match", `"`+strings.Repeat("0", 40)+`"`), 412, "precondition-failed")
	key := e.key()
	var started pipelineRunView
	resp := e.ok(e.do("POST", run("echo"), body, "Idempotency-Key", key, "If-Match", `"`+echo.Version+`"`), 201, &started)
	if resp.Header.Get("ETag") != `"1"` || started.Pipeline != "echo" || started.Version != echo.Version || started.Commit != list.Commit ||
		len(started.Steps) != 2 || started.Steps[0].State != "queued" {
		t.Fatalf("started %+v (ETag %s)", started, resp.Header.Get("ETag"))
	}
	var replay pipelineRunView
	resp = e.ok(e.do("POST", run("echo"), body, "Idempotency-Key", key, "If-Match", `"`+echo.Version+`"`), 201, &replay)
	if replay.ID != started.ID || resp.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("replay %s (%s)", replay.ID, resp.Header.Get("Idempotent-Replayed"))
	}
	done := e.waitPipelineRun(started.ID, "done")
	out := done.Steps[1].Outputs["text"]

	// artifacts.get with content; the producer is the second step.
	var art struct {
		Hash, Type, Encoding, Content string
		Size                          int64
		Producer                      struct{ Step, Output, PipelineRunID string }
	}
	e.ok(e.do("GET", "/api/artifacts/"+out.Hash+"?content=true", ""), 200, &art)
	if art.Content != "echo: hello" || art.Encoding != "utf8" || art.Type != "text" || art.Size != 11 || art.Producer.Step != "second" ||
		art.Producer.PipelineRunID != started.ID {
		t.Errorf("artifact %+v", art)
	}
	expectProblem(t, e.do("GET", "/api/artifacts/b3:"+strings.Repeat("a", 64), ""), 404, "not-found")

	var runs struct{ Items []pipelineRunView }
	e.ok(e.do("GET", "/api/projects/demo/pipeline-runs?pipeline=echo", ""), 200, &runs)
	if len(runs.Items) != 1 || runs.Items[0].ID != started.ID || runs.Items[0].Steps != nil {
		t.Errorf("pipelineRuns.list %+v", runs.Items)
	}
	if n := e.count("SELECT count(*) FROM events WHERE topic = 'pipeline_run." + started.ID + "' AND caused_by->>'commandId' LIKE 'cmd_%'"); n < 2 {
		t.Errorf("the run's command events carry no causedBy (%d)", n)
	}

	// Retry one failed step over HTTP.
	e.leases.Script("first", pipelinestest.Action{Fail: &steps.StepError{Type: steps.ErrStep, Message: "boom"}})
	var failedStart pipelineRunView
	e.ok(e.do("POST", run("echo"), `{"inputs":{"text":`+e.putText("again")+`}}`, "Idempotency-Key", e.key(), "If-Match", "*"), 201, &failedStart)
	failed := e.waitPipelineRun(failedStart.ID, "failed")
	expectProblem(t, e.do("POST", "/api/pipeline-runs/"+failed.ID+":retry", `{"step":"first"}`, "Idempotency-Key", e.key(), "If-Match", `"99"`), 412, "precondition-failed")
	var retried pipelineRunView
	e.ok(e.do("POST", "/api/pipeline-runs/"+failed.ID+":retry", `{"step":"first"}`, "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, failed.Rev)), 200, &retried)
	if retried.State != "running" {
		t.Errorf("retried %+v", retried)
	}
	if r := e.waitPipelineRun(failed.ID, "done"); r.Steps[0].Attempts != 2 {
		t.Errorf("retried run %+v", r.Steps)
	}

	// Cancel a running pipeline run.
	e.leases.Script("first", pipelinestest.Action{Block: true})
	var blocking pipelineRunView
	e.ok(e.do("POST", run("echo"), `{"inputs":{"text":`+e.putText("blocked")+`}}`, "Idempotency-Key", e.key(), "If-Match", "*"), 201, &blocking)
	var cancelled pipelineRunView
	e.ok(e.do("POST", "/api/pipeline-runs/"+blocking.ID+":cancel", "", "Idempotency-Key", e.key(), "If-Match", fmt.Sprintf(`"%d"`, blocking.Rev)), 200, &cancelled)
	if cancelled.State != "cancelled" || cancelled.Steps[0].State != "cancelled" || cancelled.Steps[1].State != "cancelled" {
		t.Errorf("cancelled %+v", cancelled)
	}
	e.waitJob(cancelled.Steps[0].JobID, "cancelled")
	var got pipelineRunView
	resp = e.ok(e.do("GET", "/api/pipeline-runs/"+blocking.ID, ""), 200, &got)
	if got.State != "cancelled" || resp.Header.Get("ETag") != fmt.Sprintf(`"%d"`, got.Rev) {
		t.Errorf("pipelineRuns.get %+v", got)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), "secret") {
		t.Errorf("a pipeline run leaks secret names: %s", raw)
	}
}
