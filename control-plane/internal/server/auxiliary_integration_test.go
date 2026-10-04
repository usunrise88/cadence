//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Auxiliary models, their adoption and the triage queue against the real control plane (phase 4 · stream X): the
// seeded versions, adoption as a registry-scope approval for everyone and the licence refusal, registry references
// resolved into the step spec, the service check of a dry run, and disputed segments indexed by the segments hook.

// memberFixture is a step kind with a registry-reference parameter, as a worker pack publishes one.
var memberFixture = map[string]any{
	"name": "fx_member", "version": "1", "runtime": "test", "runtimeVersionId": "ver_test",
	"params": map[string]any{"type": "object", "properties": map[string]any{
		"auxiliary": map[string]any{"type": "string", "default": "auxiliary/oasis", "x-cadence": map[string]any{
			"default": "auxiliary/oasis", "description": "The member", "source": "test", "range": "any",
			"registryRef": map[string]any{"kind": "auxiliary", "role": "pseudolabel"},
		}},
	}},
	"consumes": map[string]string{"text": "text"}, "produces": map[string]string{"text": "text"},
	"resources": map[string]any{"gpu": false, "jobKind": "data"}, "help": "steps.fx-member",
}

type fakeProber struct{ err error }

func (f *fakeProber) Probe(context.Context, string) error { return f.err }

type auxiliaryView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
	Auxiliary struct {
		Roles                []string `json:"roles"`
		OutputsCommercialUse bool     `json:"outputsCommercialUse"`
		Conditions           []string `json:"conditions"`
		HfRepo               string   `json:"hfRepo"`
		Service              *struct {
			Endpoint, Protocol, TokenSecret string
		} `json:"service"`
	} `json:"auxiliary"`
	UsedBy []struct {
		ProjectSlug string `json:"projectSlug"`
	} `json:"usedBy"`
}

func (e *env) auxiliary(name string) auxiliaryView {
	e.t.Helper()
	var l struct{ Items []auxiliaryView }
	e.ok(e.do("GET", "/api/registry/auxiliaries?collection="+name, ""), 200, &l)
	if len(l.Items) != 1 {
		e.t.Fatalf("%s: %d versions", name, len(l.Items))
	}
	return l.Items[0]
}

func (e *env) projectRev(slug string) int {
	e.t.Helper()
	var p project
	e.ok(e.do("GET", "/api/projects/"+slug, ""), 200, &p)
	return p.Rev
}

// adoptAuxiliary asks to adopt (202 with a registry-scope approval), has the admin approve it and checks the replay.
func (e *env) adoptAuxiliary(slug, versionID string, send func(method, path, body string, hdr ...string) *http.Response) {
	e.t.Helper()
	var a accepted
	e.ok(send("POST", "/api/projects/"+slug+":adopt", `{"version":"`+versionID+`"}`, "Idempotency-Key", e.key(),
		"If-Match", `"`+strconv.Itoa(e.projectRev(slug))+`"`), 202, &a)
	ap := e.approval(a.ApprovalID)
	if ap.Scope != "registry" || ap.ProjectID != "" || ap.Rule != "auxiliary-adoption" || ap.Operation != "projects.adopt" {
		e.t.Fatalf("approval %+v", ap)
	}
	var decided approvalView
	e.ok(e.do("POST", "/api/approvals/"+a.ApprovalID+":approve", "{}", "Idempotency-Key", e.key(), "If-Match", `"1"`), 200, &decided)
	if decided.State != "approved" || decided.Result == nil || decided.Result.Status != 200 {
		e.t.Fatalf("decided %+v", decided)
	}
}

func TestAuxiliaryAdoptionAndRegistryRefs(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	if err := pipelinestest.Register(ctx, e.pool, append(append([]map[string]any{}, pipelinestest.Fixtures...), memberFixture)...); err != nil {
		t.Fatal(err)
	}
	e.newProject("demo")

	// The seeds: the OK rows of the licence table, with their payloads.
	var all struct{ Items []auxiliaryView }
	e.ok(e.do("GET", "/api/registry/auxiliaries", ""), 200, &all)
	if len(all.Items) != 5 {
		t.Fatalf("auxiliaries %+v", all.Items)
	}
	oasis, whisper := e.auxiliary("auxiliary/oasis"), e.auxiliary("auxiliary/whisper-large-v3")
	if oasis.Kind != "auxiliary" || oasis.State != "frozen" || oasis.Auxiliary.Service == nil ||
		oasis.Auxiliary.Service.Protocol != "oasis.v1" || !oasis.Auxiliary.OutputsCommercialUse || len(oasis.Auxiliary.Conditions) == 0 {
		t.Errorf("oasis %+v", oasis)
	}
	if whisper.Auxiliary.HfRepo != "openai/whisper-large-v3" || strings.Join(whisper.Auxiliary.Roles, ",") != "pseudolabel,lid" {
		t.Errorf("whisper %+v", whisper)
	}
	var got auxiliaryView
	e.ok(e.do("GET", "/api/registry/auxiliaries/"+oasis.ID, ""), 200, &got)
	if got.ID != oasis.ID {
		t.Errorf("get %+v", got)
	}

	// A step naming an auxiliary the project has not adopted is a plan problem.
	e.commitPipeline("demo", "member", "name: member\ninputs: {text: text}\nsteps:\n  - {id: m, kind: fx_member@1, in: {text: $inputs.text}}\n")
	body := `{"inputs":{"text":` + e.putText("hello") + `}}`
	run := "/api/projects/demo/pipelines/member:run"
	p := expectProblem(t, e.do("POST", run+"?dryRun=true", body, "Idempotency-Key", e.key(), "If-Match", "*"), 422, "pipeline-invalid")
	if len(p.Errors) != 1 || p.Errors[0].Path != "steps[0].params.auxiliary" || !strings.Contains(p.Errors[0].Message, "has not adopted auxiliary/oasis") {
		t.Fatalf("not adopted: %+v", p.Errors)
	}

	// Adoption is a registry-scope approval for everyone: the admin's own request and an agent's both wait for it.
	e.adoptAuxiliary("demo", oasis.ID, e.do)
	e.adoptAuxiliary("demo", whisper.ID, e.agent)
	var adopted struct {
		Items []struct{ Version struct{ Name string } }
	}
	e.ok(e.do("GET", "/api/projects/demo/adoptions?kind=auxiliary", ""), 200, &adopted)
	if len(adopted.Items) != 2 {
		t.Fatalf("adoptions %+v", adopted)
	}
	if u := e.auxiliary("auxiliary/oasis").UsedBy; len(u) != 1 || u[0].ProjectSlug != "demo" {
		t.Errorf("usedBy %+v", u)
	}

	// A licence that forbids commercial use of the outputs is refused before anyone is asked.
	var ncID string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{
			Kind: registry.KindAuxiliary, Name: "auxiliary/nc-lid", Description: "A classifier whose licence forbids it",
			Licence: "CC-BY-NC-4.0", Freeze: true, Actor: registry.Bundled(),
			Payload: json.RawMessage(`{"roles":["lid"],"licence":"CC-BY-NC-4.0","outputsCommercialUse":false,"languages":["*"],"hfRepo":"org/nc","revision":"abc"}`),
		}, time.Now())
		ncID = v.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := e.count("SELECT count(*) FROM approvals")
	expectProblem(t, e.do("POST", "/api/projects/demo:adopt", `{"version":"`+ncID+`"}`, "Idempotency-Key", e.key(),
		"If-Match", `"`+strconv.Itoa(e.projectRev("demo"))+`"`), 422, "auxiliary-licence-refused")
	if e.count("SELECT count(*) FROM approvals") != before {
		t.Error("a refused adoption asked for an approval")
	}

	// The dry run checks that the service answers; Cadence never starts it.
	prober := &fakeProber{err: errors.New("connection refused")}
	e.admin.Pipelines.SetProber(prober)
	p = expectProblem(t, e.do("POST", run+"?dryRun=true", body, "Idempotency-Key", e.key(), "If-Match", "*"), 503, "auxiliary-unavailable")
	if !strings.Contains(p.Detail, "host.docker.internal:50051") || !strings.Contains(p.Detail, "auxiliary/oasis") {
		t.Errorf("unavailable: %s", p.Detail)
	}
	// The same step marked optional only warns: the run goes on without it. auxiliaries.get says the service is down.
	e.commitPipeline("demo", "member-optional", "name: member-optional\ninputs: {text: text}\nsteps:\n  - {id: m, kind: fx_member@1, in: {text: $inputs.text}, optional: true}\n")
	var planned struct {
		Warnings []struct{ Code, Step string }
	}
	e.ok(e.do("POST", "/api/projects/demo/pipelines/member-optional:run?dryRun=true", body, "Idempotency-Key", e.key(), "If-Match", "*"), 200, &planned)
	if len(planned.Warnings) != 1 || planned.Warnings[0].Code != "auxiliary-unavailable" || planned.Warnings[0].Step != "m" {
		t.Errorf("optional step warnings %+v", planned.Warnings)
	}
	var reach struct{ Reachable *bool }
	e.ok(e.do("GET", "/api/registry/auxiliaries/"+oasis.ID, ""), 200, &reach)
	if reach.Reachable == nil || *reach.Reachable {
		t.Errorf("reachable while down: %v", reach.Reachable)
	}
	prober.err = nil
	e.ok(e.do("GET", "/api/registry/auxiliaries/"+oasis.ID, ""), 200, &reach)
	if reach.Reachable == nil || !*reach.Reachable {
		t.Errorf("reachable while up: %v", reach.Reachable)
	}
	var model struct{ Reachable *bool }
	e.ok(e.do("GET", "/api/registry/auxiliaries/"+whisper.ID, ""), 200, &model)
	if model.Reachable != nil {
		t.Errorf("a model auxiliary has no service: %v", model.Reachable)
	}
	e.ok(e.do("POST", run+"?dryRun=true", body, "Idempotency-Key", e.key(), "If-Match", "*"), 200, nil)

	// The run carries the resolved version and its payload in the step spec.
	var started pipelineRunView
	e.ok(e.do("POST", run, body, "Idempotency-Key", e.key(), "If-Match", "*"), 201, &started)
	e.waitPipelineRun(started.ID, "failed") // the in-process fake runs no fx_member; the spec is what matters
	calls := e.leases.CallsOf("m")
	if len(calls) != 1 {
		t.Fatalf("calls %+v", calls)
	}
	ref := calls[0].Spec.Auxiliaries["auxiliary"]
	var payload struct{ Service struct{ Endpoint string } }
	if err := json.Unmarshal(ref.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if ref.VersionID != oasis.ID || ref.Name != "auxiliary/oasis" || payload.Service.Endpoint != "host.docker.internal:50051" {
		t.Errorf("spec auxiliaries %+v", calls[0].Spec.Auxiliaries)
	}
}

func TestTriageQueue(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	e.newProject("demo")
	pid := e.projectID("demo")
	h := func(c string) string { return "b3:" + strings.Repeat(c, 64) }
	rows := []map[string]any{
		{"uri": "mount://corpora/calls/c1.wav#t=0.4,3.1&ch=0", "hash": h("1"), "start": 0.4, "end": 3.1, "channel": 0,
			"role": "caller", "language": "sr-RS", "text": "dobar dan", "origin": "pseudo-label", "confidence": 0.66},
		{"uri": "mount://corpora/calls/c1.wav#t=6.0,9.2&ch=0", "hash": h("3"), "start": 6.0, "end": 9.2, "channel": 0,
			"role": "caller", "language": "sr-RS", "text": "zovem zbog računa za struju", "origin": "pseudo-label:disputed",
			"confidence": 0.4, "dispute": map[string]any{"reason": "disagreement", "candidates": []map[string]any{
				{"member": "whisper-large-v3", "text": "Zovem zbog računa za struju.", "meanWer": 0.6},
				{"member": "oasis", "text": "zovem zbog računa za vodu", "meanWer": 0.6, "confidence": 0.7},
			}, "lid": map[string]any{"language": "sr", "confidence": 0.9, "agrees": true}}},
	}
	var lines []byte
	for _, r := range rows {
		b, _ := json.Marshal(r)
		lines = append(append(lines, b...), '\n')
	}
	put := func(b []byte) cas.File {
		hash, err := e.admin.CAS.PutBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		return cas.File{Hash: hash, Size: int64(len(b))}
	}
	fRows, fHead := put(lines), put([]byte(`{"format":"cadence.segments/1"}`))
	fRows.Path, fHead.Path = "segments.jsonl", "segments.json"
	segs, err := e.admin.CAS.PutManifest(cas.Manifest{Files: []cas.File{fRows, fHead}})
	if err != nil {
		t.Fatal(err)
	}
	hook := func(ref steps.ArtifactRef) int {
		t.Helper()
		n := 0
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			drafts, err := e.admin.StepHooks.Run(ctx, tx, steps.Output{ProjectID: pid, PipelineRunID: "plr_test",
				StepID: "pls_ensemble", Name: "segments", Artifact: ref})
			if err != nil {
				return err
			}
			n = len(drafts)
			return events.Append(ctx, tx, auth.Actor{Kind: auth.KindAutomation, ID: "plr_test"}, nil, drafts)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ref := steps.ArtifactRef{Hash: segs, Type: "segments", Meta: json.RawMessage(`{"disputed":1}`)}
	if n := hook(ref); n != 1 {
		t.Fatalf("first hook emitted %d events", n)
	}
	if n := hook(ref); n != 0 { // a reused output indexes nothing twice
		t.Fatalf("second hook emitted %d events", n)
	}
	// An artifact whose producer says nothing is disputed is not read at all.
	if n := hook(steps.ArtifactRef{Hash: h("9"), Type: "segments", Meta: json.RawMessage(`{"disputed":0}`)}); n != 0 {
		t.Fatalf("undisputed hook emitted %d events", n)
	}
	// Chained segments outputs (the phase-4 audit's C2): text_normalise keeps the disputed row with a new text under a
	// new segments hash and no meta; a second ensemble pass disputes the same segment again. Neither adds an item.
	rows[1]["text"] = "Zovem zbog računa za struju."
	var normalised []byte
	for _, r := range rows {
		b, _ := json.Marshal(r)
		normalised = append(append(normalised, b...), '\n')
	}
	fNorm := put(normalised)
	fNorm.Path = "segments.jsonl"
	norm, err := e.admin.CAS.PutManifest(cas.Manifest{Files: []cas.File{fNorm, fHead}})
	if err != nil {
		t.Fatal(err)
	}
	if n := hook(steps.ArtifactRef{Hash: norm, Type: "segments"}); n != 0 {
		t.Fatalf("a step after the ensemble indexed the disputes again (%d events)", n)
	}
	if n := hook(steps.ArtifactRef{Hash: norm, Type: "segments", Meta: json.RawMessage(`{"disputed":1}`)}); n != 0 {
		t.Fatalf("a second ensemble pass indexed an open dispute again (%d events)", n)
	}

	var list struct {
		Items []struct {
			ID, State, Reason, Best, PipelineRunID, SegmentsHash string
			Segment                                              struct{ Hash, URI, Role string }
			Candidates                                           []struct {
				Member, Text string
			}
			Lid        struct{ Language string }
			Confidence float64
		}
	}
	e.ok(e.do("GET", "/api/projects/demo/triage", ""), 200, &list)
	if len(list.Items) != 1 {
		t.Fatalf("triage %+v", list.Items)
	}
	it := list.Items[0]
	if !strings.HasPrefix(it.ID, "tri_") || it.State != "open" || it.Reason != "disagreement" || it.Segment.Hash != h("3") ||
		it.Segment.Role != "caller" || len(it.Candidates) != 2 || it.Best != "zovem zbog računa za struju" ||
		it.Lid.Language != "sr" || it.Confidence != 0.4 || it.SegmentsHash != segs || it.PipelineRunID != "plr_test" {
		t.Errorf("item %+v", it)
	}
	e.ok(e.do("GET", "/api/projects/demo/triage?reason=no-speech", ""), 200, &list)
	if len(list.Items) != 0 {
		t.Errorf("filtered %+v", list.Items)
	}
	if evs := e.events("triage.new"); len(evs) != 1 || evs[0].Type != "triage.item_added" || evs[0].ProjectID != pid {
		t.Errorf("events %+v", evs)
	}
}
