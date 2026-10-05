//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/serving"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
	"github.com/usunrise88/cadence/control-plane/internal/transcriptions"
)

// fakeServer is a staging server as the control plane sees it: health, the model index and unload (the paths of
// defaults.yaml serving.servers.triton). No stream protocol: that is the family pack's.
type fakeServer struct {
	*httptest.Server
	mu       sync.Mutex
	down     atomic.Bool
	loaded   map[string]bool
	unloaded []string
}

func newFakeServer(t *testing.T) *fakeServer {
	f := &fakeServer{loaded: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v2/health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if f.down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("POST /v2/repository/index", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []map[string]string{}
		for m, ready := range f.loaded {
			state := "UNAVAILABLE"
			if ready {
				state = "READY"
			}
			out = append(out, map[string]string{"name": m, "version": "1", "state": state})
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /v2/repository/models/{model}/unload", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.loaded[r.PathValue("model")] = false
		f.unloaded = append(f.unloaded, r.PathValue("model"))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeServer) load(model string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loaded[model] = true
}

func (f *fakeServer) unloads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.unloaded...)
}

type servingTargetView struct {
	ID, Name, Kind string
	Health         *struct {
		State, Detail string
		LatencyMs     *int
	}
	ServedModels []struct {
		Model, DeployableHash, State string
		MemoryMb, Leases             int
	}
}

func serveKind() map[string]any {
	k := kind("1", "eval", true, false)
	k["params"] = map[string]any{"type": "object", "properties": map[string]any{
		"target": map[string]any{"type": "string", "x-cadence": map[string]any{"default": "staging", "description": "d",
			"source": "R30", "range": "a dtg_ id or a target name"}}}}
	k["consumes"] = map[string]any{"deployable": "deployable", "data": "dataset"}
	k["produces"] = map[string]any{"hypotheses": "hypotheses"}
	k["resources"] = map[string]any{"gpu": true, "gpus": 1, "memoryGb": 9, "jobKind": "eval"}
	k["role"] = "serve"
	return k
}

// A step that consumes a deployable is served through the staging target its params name: the lease carries the
// endpoint and the versioned model name, the queue reserves the model once per card, the control plane counts its
// leases, checks the server and unloads the model once no lease used it for serving.unload_idle_minutes.
func TestServedModelsAreCountedCheckedAndUnloaded(t *testing.T) {
	w := startWorkers(t)
	ctx := context.Background()
	fake := newFakeServer(t)
	if _, err := targets.Seed(ctx, w.pool, "staging", fake.URL, targets.Server{Kind: "triton", Version: "26.08"}, time.Now(),
		targets.Serves{Family: "fam", Formats: []string{"step-graph"}, Profiles: []string{"80ms"}}); err != nil {
		t.Fatal(err)
	}
	sv := w.admin.Serving()
	// The worker protocol's clock is the serving check's; it starts at the wall time because the authenticated
	// test server's grant hook (the fake worker's) stamps leases with it.
	w.clock.Set(time.Now())
	sv.Now = w.clock.Now

	// Before any check the target's health is unknown and it serves nothing.
	var st servingTargetView
	w.ok(w.do("GET", "/api/deployment-targets/staging", ""), 200, &st)
	if st.Health == nil || st.Health.State != "unknown" || st.ServedModels == nil || len(st.ServedModels) != 0 {
		t.Fatalf("staging before a check: %+v", st)
	}

	f := w.register(w.workerToken("staging"), "fam", map[string]any{"fam_serve": serveKind(),
		"fam_train": kind("1", "training", true, false)})
	data, _ := w.blobs.PutBytes([]byte("dataset"))
	dep, _ := w.blobs.PutBytes([]byte("deployable"))
	meta := json.RawMessage(`{"family":"fam","format":"step-graph","profile":"80ms","serving":{"memoryMb":7000}}`)
	serve := func(jobKind, target string) steps.Spec {
		return steps.Spec{Kind: "fam_serve", KindVersion: "1", Params: json.RawMessage(`{"target":"` + target + `"}`),
			Inputs: map[string]steps.ArtifactRef{"data": {Hash: data, Type: "dataset"},
				"deployable": {Hash: dep, Type: steps.TypeDeployable, Meta: meta}},
			Resources: steps.Resources{GPU: true, GPUs: 1, MemoryGB: 9, JobKind: jobKind}}
	}
	model := steps.ServedModelName(dep)

	// Training takes the cap minus the serving reserve (29 − 9 GB on the seeded card).
	train := w.enqueue(gpuSpec("fam_train"))
	lt := f.claim(2)
	if lt == nil || lt.JobID != train || lt.Card.MemoryCapMb != 20*1024 {
		t.Fatalf("training lease %+v", lt)
	}

	// The served model fits the reserve beside it; the lease names the target and the model.
	first := w.enqueue(serve("eval", "staging"))
	l1 := f.claim(2)
	if l1 == nil || l1.JobID != first || l1.Card.MemoryCapMb != 7000 || l1.Env[serving.EnvEndpoint] != fake.URL ||
		l1.Env[serving.EnvModel] != model || l1.Env[serving.EnvServer] != "triton" || l1.Env[serving.EnvServerVersion] != "26.08" ||
		l1.Env[serving.EnvRefused] != "" || !strings.Contains(l1.Env[serving.EnvModels], `"deployable":"`+model+`"`) {
		t.Fatalf("serve lease %+v", l1)
	}
	if n := w.count("SELECT count(*) FROM events WHERE payload::text LIKE '%CADENCE_SERVING%'"); n != 0 {
		t.Fatal("the lease environment reached an event")
	}
	// A second lease of the same model needs nothing more (shadow replay beside the eval): the reserve holds it once.
	second := w.enqueue(serve("shadow", "staging"))
	l2 := f.claim(2)
	if l2 == nil || l2.JobID != second || l2.Card.MemoryCapMb != 7000 {
		t.Fatalf("second serve lease %+v", l2)
	}
	fake.load(model)
	w.ok(w.do("GET", "/api/deployment-targets/staging", ""), 200, &st)
	if len(st.ServedModels) != 1 || st.ServedModels[0].Model != model || st.ServedModels[0].State != "in-use" ||
		st.ServedModels[0].Leases != 2 || st.ServedModels[0].MemoryMb != 7000 || st.ServedModels[0].DeployableHash != dep {
		t.Fatalf("served models %+v", st.ServedModels)
	}

	// The check finds the server up (an event on the target's topic); a model in use is never unloaded.
	if err := w.admin.CheckServing(ctx); err != nil {
		t.Fatal(err)
	}
	w.ok(w.do("GET", "/api/deployment-targets/staging", ""), 200, &st)
	if st.Health.State != "up" || st.Health.LatencyMs == nil {
		t.Fatalf("health %+v", st.Health)
	}
	if n := w.count(fmt.Sprintf("SELECT count(*) FROM events WHERE type = '%s' AND topic = 'entity.deployment_target.%s'", serving.EventHealth, st.ID)); n != 1 {
		t.Fatalf("%d health events", n)
	}
	w.releaseDone(f, l1.ID)
	w.releaseDone(f, l2.ID)
	w.clock.Add(10 * time.Minute)
	if err := w.admin.CheckServing(ctx); err != nil {
		t.Fatal(err)
	}
	if len(fake.unloads()) != 0 {
		t.Fatalf("unloaded before serving.unload_idle_minutes: %v", fake.unloads())
	}
	w.ok(w.do("GET", "/api/deployment-targets/staging", ""), 200, &st)
	if st.ServedModels[0].State != "loaded" || st.ServedModels[0].Leases != 0 {
		t.Fatalf("idle model %+v", st.ServedModels)
	}
	w.clock.Add(time.Duration(defaults.Get().Serving.UnloadIdleMinutes.Value) * time.Minute)
	if err := w.admin.CheckServing(ctx); err != nil {
		t.Fatal(err)
	}
	if u := fake.unloads(); len(u) != 1 || u[0] != model {
		t.Fatalf("unloads %v", u)
	}
	w.ok(w.do("GET", "/api/deployment-targets/staging", ""), 200, &st)
	if st.ServedModels[0].State != "unloaded" {
		t.Fatalf("unloaded model %+v", st.ServedModels)
	}

	// The server goes down: the next check records it, and work for it is refused serving-unavailable.
	fake.down.Store(true)
	if err := w.admin.CheckServing(ctx); err != nil {
		t.Fatal(err)
	}
	w.ok(w.do("GET", "/api/deployment-targets/staging", ""), 200, &st)
	if st.Health.State != "down" || !strings.Contains(st.Health.Detail, "503") {
		t.Fatalf("health %+v", st.Health)
	}
	_, err := sv.Check(ctx, w.pool, "staging", serving.Deployable{Family: "fam", Format: "step-graph", Profile: "80ms"})
	if pe, ok := problemOf(err); !ok || pe != "serving-unavailable" {
		t.Fatalf("check while down: %v", err)
	}
	fake.down.Store(false)
	_ = w.admin.CheckServing(ctx)
	for _, tc := range []struct {
		dep  serving.Deployable
		want string
	}{
		{serving.Deployable{Family: "fam", Format: "step-graph", Profile: "80ms"}, ""},
		{serving.Deployable{Family: "other"}, "does not serve model family other"},
		{serving.Deployable{Family: "fam", Format: "onnx"}, "not onnx"},
		{serving.Deployable{Family: "fam", Profile: "1120ms"}, "not at 1120ms"},
	} {
		_, err := sv.Check(ctx, w.pool, "", tc.dep)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("Check(%+v) = %v, want %q", tc.dep, err, tc.want)
		}
	}

	// A delivery target is never served through: the lease says why and carries no endpoint.
	if _, err := targets.Seed(ctx, w.pool, "unreached", "", targets.Server{Kind: "triton", Version: "26.08"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.pool.Exec(ctx, "UPDATE deployment_targets SET kind = 'delivery', config = config - 'endpoint' WHERE name = 'unreached'"); err != nil {
		t.Fatal(err)
	}
	w.enqueue(serve("eval", "unreached"))
	l3 := f.claim(2)
	if l3 == nil || l3.Env[serving.EnvEndpoint] != "" || !strings.HasPrefix(l3.Env[serving.EnvRefused], "target-does-not-serve: ") ||
		!strings.Contains(l3.Env[serving.EnvRefused], "delivery target") {
		t.Fatalf("delivery target lease %+v", l3)
	}
}

func (w *wenv) releaseDone(f *fakeWorker, leaseID string) {
	w.t.Helper()
	w.ok(f.release(leaseID, steps.Outcome{State: steps.StateDone}), http.StatusNoContent, nil)
}

func problemOf(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	slug, _, ok := strings.Cut(err.Error(), ":")
	return slug, ok
}

// ---------------------------------------------------------------- transcriptions through a deployment (R47)

const serveBase = "base-model/fixture-serve-base"

// fakeDeployments resolves one deployment of the test project.
type fakeDeployments struct{ dep transcriptions.Deployment }

func (f fakeDeployments) Resolve(_ context.Context, _ storage.Querier, projectID, id string) (transcriptions.Deployment, error) {
	if id != f.dep.ID {
		return transcriptions.Deployment{}, problems.NotFound.New("no deployment %q", id)
	}
	d := f.dep
	d.ProjectID = projectID
	return d, nil
}

func TestTranscriptionThroughADeployment(t *testing.T) {
	e := startWith(t, nil, func(_ *pgxpool.Pool, js *jobs.Service) {
		js.Register(steps.LiveJobKind, func(ctx context.Context, run *jobs.Run) (any, error) {
			return liveService.Load().Handle(ctx, run)
		}, jobs.KindOptions{Timeout: transcriptions.HandlerTimeout, Queue: jobs.QueueSteps})
	})
	liveService.Store(e.admin.Transcriptions())
	ctx := context.Background()
	if _, err := compute.Seed(ctx, e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
	var baseID string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(map[string]any{"hfRepo": "fixture/serve", "revision": "0123456789abcdef", "licence": "cc-by-4.0",
			"familyId": "fixture-serve", "checkpointFile": "serve.bin"})
		v, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindBaseModel, Name: serveBase, Payload: b, Freeze: true,
			Actor: auth.Actor{Kind: auth.KindAutomation, ID: "worker"}}, time.Now())
		baseID = v.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(map[string]any{"host": "staging", "instance": "serve-1",
		"runtime": map[string]any{"name": "fixture-serve", "version": "1", "digest": "sha256:" + strings.Repeat("e", 64)},
		"stepKinds": map[string]any{"fx_slive": map[string]any{
			"version": "1", "role": "live", "params": map[string]any{"type": "object", "properties": map[string]any{}},
			"consumes": map[string]string{"model": "checkpoint", "base": "base_model"}, "optionalInputs": []string{"model", "base"},
			"produces": map[string]string{}, "resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 6, "jobKind": "interactive"},
			"help": "steps.fx-slive",
		}, "fx_serve": map[string]any{
			"version": "1", "role": "serve", "params": map[string]any{"type": "object", "properties": map[string]any{}},
			"consumes": map[string]string{"deployable": "deployable", "data": "dataset"}, "optionalInputs": []string{"data"},
			"produces": map[string]string{}, "resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 9, "jobKind": "eval"},
			"help": "steps.fx-serve",
		}},
		"modelFamilies": []any{map[string]any{"name": "fixture-serve", "version": "1", "framework": "none", "architecture": "none",
			"latencyProfiles": []map[string]any{{"name": "80ms", "latencyMs": 80, "chunkMs": 80}, {"name": "160ms", "latencyMs": 160, "chunkMs": 160}},
			"capabilities":    map[string]any{"streaming": true},
			"roles":           map[string]string{"live": "fx_slive", "serve": "fx_serve"},
			"interactive":     map[string]any{"memoryMb": 6000, "extraCheckpointMb": 2600}}}})
	var wk struct{ ID string }
	e.ok(e.do("POST", "/api/worker-registrations", string(reg)), 200, &wk)
	e.newProject("served")

	fake := newFakeServer(t)
	if _, err := targets.Seed(ctx, e.pool, "staging", fake.URL, targets.Server{Kind: "triton", Version: "26.08"}, time.Now(),
		targets.Serves{Family: "fixture-serve", Formats: []string{"step-graph"}, Profiles: []string{"80ms"}}); err != nil {
		t.Fatal(err)
	}
	staging, err := targets.Get(ctx, e.pool, "staging")
	if err != nil {
		t.Fatal(err)
	}
	dep, err := e.admin.CAS.PutBytes([]byte(`{"schema":"cadence.deployable/1"}`))
	if err != nil {
		t.Fatal(err)
	}
	e.admin.Transcriptions().Deployments = fakeDeployments{transcriptions.Deployment{ID: "dep_1", Label: "model/served 2026-10-04 · shadow",
		BaseModelVersionID: baseID, Profile: "80ms", Format: "step-graph", TargetID: staging.ID,
		Deployable: steps.ArtifactRef{Hash: dep, Type: steps.TypeDeployable, Meta: json.RawMessage(`{"serving":{"memoryMb":5000}}`)}}}

	// Refused: a mixed session, another profile than the export's, a boost list, an unknown deployment.
	expectProblem(t, e.do("POST", "/api/projects/served/transcriptions", `{"input":{"kind":"microphone"},"targets":[{"deploymentId":"dep_1"},{"baseModelVersionId":"`+serveBase+`"}]}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")
	expectProblem(t, e.do("POST", "/api/projects/served/transcriptions", `{"input":{"kind":"microphone"},"targets":[{"deploymentId":"dep_1","profile":"160ms"}]}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")
	expectProblem(t, e.do("POST", "/api/projects/served/transcriptions", `{"input":{"kind":"microphone"},"targets":[{"deploymentId":"dep_1","boost":"lang/he-IL/boost/x.txt"}]}`,
		"Idempotency-Key", e.key()), 422, "validation-failed")
	// The staging server is down: serving-unavailable before anything is queued.
	fake.down.Store(true)
	if err := e.admin.CheckServing(ctx); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do("POST", "/api/projects/served/transcriptions", `{"input":{"kind":"microphone"},"targets":[{"deploymentId":"dep_1"}]}`,
		"Idempotency-Key", e.key()), 503, "serving-unavailable")
	fake.down.Store(false)
	if err := e.admin.CheckServing(ctx); err != nil {
		t.Fatal(err)
	}

	// A session through the deployment: the family's serve role in relay mode, reserving the served model.
	s := e.openLive("served", `{"input":{"kind":"microphone"},"targets":[{"deploymentId":"dep_1"}]}`)
	if s.LiveKind != "fx_serve@1" || s.ReservationMB != 5000 || len(s.Targets) != 1 || s.Targets[0].Kind != "deployment" ||
		s.Targets[0].ID != "dep_1" || s.Targets[0].Profile != "80ms" || s.Targets[0].WeightsKey != dep {
		t.Fatalf("session %+v", s)
	}
	l := e.claimLive(wk.ID)
	var p struct {
		Mode, Target string
		Targets      []struct{ Target, Model, Profile string }
	}
	if err := json.Unmarshal(l.Spec.Params, &p); err != nil {
		t.Fatal(err)
	}
	if l.JobID != s.JobID || p.Mode != "relay" || p.Target != staging.ID || len(p.Targets) != 1 || p.Targets[0].Model != "deployable.0" ||
		l.Spec.Inputs["deployable.0"].Hash != dep || l.Card.MemoryCapMB != 5000 || l.Env[transcriptions.EnvLiveToken] == "" ||
		l.Env[serving.EnvEndpoint] != fake.URL || l.Env[serving.EnvModel] != steps.ServedModelName(dep) {
		t.Fatalf("relay lease %+v params %+v", l, p)
	}
	if n := e.count("SELECT count(*) FROM serving_leases WHERE lease_id = '" + l.ID + "'"); n != 1 {
		t.Fatalf("%d served-model leases", n)
	}
}
