//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/obs"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/telemetry"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
	"github.com/usunrise88/cadence/control-plane/internal/workers"
	"github.com/usunrise88/cadence/control-plane/migrations"
)

// testClock is the workers' clock; tests move it to reap leases and cross availability windows.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *testClock) Set(t time.Time)     { c.mu.Lock(); c.now = t; c.mu.Unlock() }
func (c *testClock) Add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }

// wenv is a control plane with the worker protocol: the step kind's handler waits in Await (as stream P's will) and
// records each job's outcome.
type wenv struct {
	*env
	authURL  string // the API with authentication (worker tokens)
	workers  *workers.Service
	clock    *testClock
	blobs    *cas.Store
	outcomes sync.Map // job id → steps.Outcome (or error)
}

func startWorkers(t *testing.T) *wenv {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	pool, err := storage.Open(ctx, testdb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.Migrate(ctx, pool, migrations.FS, jobs.MigrateRiver); err != nil {
		t.Fatal(err)
	}
	if _, err := compute.Seed(ctx, pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	hub, metrics := events.NewHub(64), obs.NewMetrics()
	blobs, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store, err := secrets.NewStore(pool, t.TempDir(), &testMasterKey)
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{now: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)} // a Wednesday, noon
	svc := workers.New(workers.Options{Pool: pool, Secrets: store, CAS: blobs, LogDir: t.TempDir(), Log: quiet,
		Now: clk.Now, Poll: 50 * time.Millisecond})
	w := &wenv{workers: svc, clock: clk, blobs: blobs}
	js := jobs.New(pool, quiet)
	js.FetchPollInterval = 100 * time.Millisecond
	js.Register(steps.JobKind, func(ctx context.Context, run *jobs.Run) (any, error) {
		o, err := svc.Await(ctx, run.Job.ID)
		if err != nil {
			w.outcomes.Store(run.Job.ID, err)
			return nil, err
		}
		w.outcomes.Store(run.Job.ID, o)
		return o, nil
	}, jobs.KindOptions{Queue: jobs.QueueSteps, Timeout: time.Hour})
	if err := js.Start(ctx); err != nil {
		t.Fatal(err)
	}
	withAll := func(c *Config) { c.Jobs, c.Workers, c.Secrets, c.CAS = js, svc, store, blobs }
	admin := newTestServer(t, pool, hub, metrics, withAll)
	authed := newTestServer(t, pool, hub, obs.NewMetrics(), withAll, func(c *Config) { c.Actor = auth.Actor{} })
	srv, authSrv := httptest.NewServer(admin.Handler()), httptest.NewServer(authed.Handler())
	t.Cleanup(func() {
		hub.Close()
		srv.Close()
		authSrv.Close()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = js.Stop(sctx)
		scancel()
		cancel()
		pool.Close()
	})
	w.env = &env{t: t, url: srv.URL, pool: pool, metrics: metrics, jobs: js, admin: admin}
	w.authURL = authSrv.URL
	return w
}

// token issues a credential in one transaction.
func (w *wenv) token(issue func(tx pgx.Tx) (string, error)) string {
	w.t.Helper()
	var tok string
	if err := pgx.BeginFunc(context.Background(), w.pool, func(tx pgx.Tx) error {
		var err error
		tok, err = issue(tx)
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return tok
}

func (w *wenv) workerToken(host string) string {
	return w.token(func(tx pgx.Tx) (string, error) {
		tok, _, err := credentials.NewWorkerToken(context.Background(), tx, host, false)
		return tok, err
	})
}

// enqueue starts a step job with spec (stepId and attempt filled in) and returns its job id.
func (w *wenv) enqueue(spec steps.Spec) string {
	w.t.Helper()
	if spec.StepID == "" {
		spec.StepID = fmt.Sprintf("pls_%d", time.Now().UnixNano())
	}
	if spec.PipelineRunID == "" {
		spec.PipelineRunID = "plr_test"
	}
	if spec.Attempt == 0 {
		spec.Attempt = 1
	}
	ctx := auth.WithActor(context.Background(), auth.DevActor())
	var id string
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		j, drafts, err := w.jobs.Enqueue(ctx, tx, jobs.Spec{Kind: steps.JobKind, ProjectID: spec.ProjectID, Args: spec})
		if err != nil {
			return err
		}
		id = j.ID
		return events.Append(ctx, tx, auth.DevActor(), nil, drafts)
	}); err != nil {
		w.t.Fatal(err)
	}
	// Wait until the handler put it in the queue.
	deadline := time.Now().Add(10 * time.Second)
	for w.count(fmt.Sprintf("SELECT count(*) FROM step_jobs WHERE job_id = '%s'", id)) == 0 {
		if time.Now().After(deadline) {
			w.t.Fatalf("job %s never reached the step queue", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return id
}

// outcome waits for the job's handler to return.
func (w *wenv) outcome(jobID string) steps.Outcome {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := w.outcomes.Load(jobID); ok {
			if err, isErr := v.(error); isErr {
				w.t.Fatalf("Await %s: %v", jobID, err)
			}
			return v.(steps.Outcome)
		}
		time.Sleep(20 * time.Millisecond)
	}
	w.t.Fatalf("job %s: no outcome", jobID)
	return steps.Outcome{}
}

func (w *wenv) noOutcome(jobID string) {
	w.t.Helper()
	time.Sleep(150 * time.Millisecond)
	if v, ok := w.outcomes.Load(jobID); ok {
		w.t.Fatalf("job %s ended unexpectedly: %+v", jobID, v)
	}
}

// fakeWorker speaks the worker protocol over HTTP with its token, as the Python harness does.
type fakeWorker struct {
	w     *wenv
	token string
	id    string
	cards []map[string]any
}

func (f *fakeWorker) send(method, path, contentType, body string) *http.Response {
	f.w.t.Helper()
	req, err := http.NewRequestWithContext(f.w.t.Context(), method, f.w.authURL+"/api"+path, strings.NewReader(body))
	if err != nil {
		f.w.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.token)
	if body != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.w.t.Fatal(err)
	}
	return resp
}

func (f *fakeWorker) post(path string, body any) *http.Response {
	f.w.t.Helper()
	b, _ := json.Marshal(body)
	return f.send(http.MethodPost, path, "application/json", string(b))
}

func xc(def any) map[string]any {
	return map[string]any{"default": def, "description": "d", "source": "Cadence recommendation", "range": map[string]any{"min": 0}}
}

// kind is a step kind descriptor with complete x-cadence metadata.
func kind(version, jobKind string, gpu, neutral bool) map[string]any {
	return map[string]any{
		"version":  version,
		"params":   map[string]any{"type": "object", "properties": map[string]any{"steps": map[string]any{"type": "integer", "x-cadence": xc(10)}}},
		"consumes": map[string]any{"data": "dataset"}, "produces": map[string]any{"model": "checkpoint"},
		"resources": map[string]any{"gpu": gpu, "jobKind": jobKind}, "neutral": neutral, "help": "steps.test",
		"secrets": []string{},
	}
}

func registration(host, runtime, instance string, kinds map[string]any) map[string]any {
	return map[string]any{
		"host": host, "instance": instance,
		"runtime": map[string]any{"name": runtime, "version": "1.0", "digest": "sha256:" + strings.Repeat("a", 64),
			"environment": map[string]string{"torch": "2.9"}},
		"stepKinds": kinds,
		"modelFamilies": []any{map[string]any{
			"name": runtime + "-family", "version": "1", "framework": runtime, "architecture": "ctc",
			"latencyProfiles": []any{map[string]any{"name": "offline", "latencyMs": 0}},
			"roles":           map[string]any{"train": "train_" + strings.ReplaceAll(runtime, "-", "_")},
		}},
	}
}

func (w *wenv) register(token, runtime string, kinds map[string]any) *fakeWorker {
	w.t.Helper()
	f := &fakeWorker{w: w, token: token, cards: []map[string]any{{"index": 0, "name": "card", "memoryTotalMb": 49152,
		"memoryUsedMb": 24000, "utilization": 0.1}}}
	var out struct {
		ID        string   `json:"id"`
		StepKinds []string `json:"stepKinds"`
		State     string   `json:"state"`
	}
	w.ok(f.post("/worker-registrations", registration("staging", runtime, "boot-1", kinds)), http.StatusOK, &out)
	if out.ID == "" || out.State != "online" {
		w.t.Fatalf("registration: %+v", out)
	}
	f.id = out.ID
	return f
}

type lease struct {
	ID    string            `json:"id"`
	JobID string            `json:"jobId"`
	Spec  steps.Spec        `json:"spec"`
	Env   map[string]string `json:"env"`
	Card  struct {
		Index       int `json:"index"`
		MemoryCapMb int `json:"memoryCapMb"`
	} `json:"card"`
	Inputs           map[string]string `json:"inputs"`
	Traceparent      string            `json:"traceparent"`
	HeartbeatSeconds int               `json:"heartbeatSeconds"`
}

func (f *fakeWorker) claim(wait int) *lease {
	f.w.t.Helper()
	var out struct {
		Lease *lease `json:"lease"`
	}
	f.w.ok(f.post("/worker-leases:claim", map[string]any{"workerId": f.id, "wait": wait, "cards": f.cards}), http.StatusOK, &out)
	return out.Lease
}

func (f *fakeWorker) report(id string, body map[string]any) (stop bool, reason string) {
	f.w.t.Helper()
	var ack struct {
		Stop   bool   `json:"stop"`
		Reason string `json:"reason"`
	}
	f.w.ok(f.post("/worker-leases/"+id+":report", body), http.StatusOK, &ack)
	return ack.Stop, ack.Reason
}

func (f *fakeWorker) release(id string, o steps.Outcome) *http.Response {
	f.w.t.Helper()
	return f.post("/worker-leases/"+id+":release", o)
}

func gpuSpec(kindName string) steps.Spec {
	return steps.Spec{Kind: kindName, KindVersion: "1", Params: json.RawMessage(`{"steps": 10}`),
		Resources: steps.Resources{GPU: true, GPUs: 1, JobKind: steps.JobTraining}}
}

func (w *wenv) jobRev(id string) int {
	w.t.Helper()
	j, err := jobs.Get(context.Background(), w.pool, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return j.Rev
}

func (w *wenv) jobCmd(id, verb string) {
	w.t.Helper()
	w.ok(w.do(http.MethodPost, "/api/jobs/"+id+":"+verb, "", "Idempotency-Key", w.key(), "If-Match", fmt.Sprint(w.jobRev(id))), http.StatusOK, nil)
}

func TestWorkerProtocolEndToEnd(t *testing.T) {
	w := startWorkers(t)
	if w.admin.Leases != steps.Leases(w.workers) {
		t.Fatal("the server's Leases must be the worker protocol, not NoLeases")
	}
	tok := w.workerToken("staging")
	f := w.register(tok, "toy", map[string]any{"train_toy": kind("1", "training", true, false), "echo": kind("1", "data", false, true)})
	versions := w.count("SELECT count(*) FROM registry_versions")
	// A restart publishes the same content: nothing new is registered.
	w.register(tok, "toy", map[string]any{"train_toy": kind("1", "training", true, false), "echo": kind("1", "data", false, true)})
	if n := w.count("SELECT count(*) FROM registry_versions"); n != versions {
		t.Fatalf("re-registration added %d registry versions", n-versions)
	}
	var rts struct {
		Items []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Runtime struct{ Name, Digest string }
			Workers []struct{ ID, Host, State string }
		} `json:"items"`
	}
	w.ok(w.do(http.MethodGet, "/api/registry/runtimes", ""), http.StatusOK, &rts)
	if len(rts.Items) != 1 || rts.Items[0].Name != "runtime/toy" || len(rts.Items[0].Workers) != 1 || rts.Items[0].Workers[0].ID != f.id {
		t.Fatalf("runtimes.list = %+v", rts)
	}
	var sks struct {
		Items []struct {
			Name     string `json:"name"`
			StepKind struct {
				Name, Runtime, RuntimeVersionId, SchemaHash string
			} `json:"stepKind"`
		} `json:"items"`
	}
	w.ok(w.do(http.MethodGet, "/api/registry/step-kinds?collection=step-kind%2Ftrain_toy", ""), http.StatusOK, &sks)
	if len(sks.Items) != 1 || sks.Items[0].StepKind.Runtime != "toy" || sks.Items[0].StepKind.RuntimeVersionId != rts.Items[0].ID ||
		sks.Items[0].StepKind.SchemaHash == "" {
		t.Fatalf("stepKinds.list = %+v", sks)
	}
	var fams struct{ Items []struct{ Name string } }
	w.ok(w.do(http.MethodGet, "/api/registry/model-families", ""), http.StatusOK, &fams)
	if len(fams.Items) != 1 || fams.Items[0].Name != "model-family/toy-family" {
		t.Fatalf("modelFamilies.list = %+v", fams)
	}

	// A secret the step declares reaches the lease, and only the lease.
	if err := pgx.BeginFunc(context.Background(), w.pool, func(tx pgx.Tx) error {
		_, _, err := w.admin.Secrets.Create(context.Background(), tx, secrets.NewInput{Name: "hf-token", Kind: "huggingface",
			Value: []byte("hf_secret_value")}, auth.DevActor(), false)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	input, err := w.blobs.PutBytes([]byte("dataset lines"))
	if err != nil {
		t.Fatal(err)
	}
	spec := gpuSpec("train_toy")
	spec.RunID = "run_test"
	spec.SecretNames = []string{"hf-token"}
	spec.Inputs = map[string]steps.ArtifactRef{"data": {Hash: input, Type: "dataset"}}
	jobID := w.enqueue(spec)

	l := f.claim(2)
	if l == nil || l.JobID != jobID || l.Card.Index != 0 || l.Card.MemoryCapMb != 24*1024 || l.HeartbeatSeconds != 10 ||
		l.Env["HF_TOKEN"] != "hf_secret_value" || l.Inputs["data"] != "cas://"+input || !strings.HasPrefix(l.Traceparent, "00-") {
		t.Fatalf("lease = %+v", l)
	}
	if n := w.count("SELECT count(*) FROM leases WHERE outcome::text LIKE '%hf_secret%' OR message LIKE '%hf_secret%'"); n != 0 {
		t.Fatal("a secret value reached the lease row")
	}
	if n := w.count("SELECT count(*) FROM events WHERE payload::text LIKE '%hf_secret%'"); n != 0 {
		t.Fatal("a secret value reached an event")
	}
	// The queue shows it running on the card.
	var q struct {
		Items []struct {
			JobID, State string
			Lease        *struct {
				Host string
				Card int
			}
		} `json:"items"`
	}
	w.ok(w.do(http.MethodGet, "/api/queue-entries", ""), http.StatusOK, &q)
	if len(q.Items) != 1 || q.Items[0].State != "running" || q.Items[0].Lease == nil || q.Items[0].Lease.Host != "staging" {
		t.Fatalf("queueEntries.list = %+v", q)
	}

	// Heartbeat with progress and telemetry.
	if stop, _ := f.report(l.ID, map[string]any{"progress": map[string]any{"fraction": 0.5, "message": "step 5"}, "cards": f.cards}); stop {
		t.Fatal("stop without a reason")
	}
	j, _ := jobs.Get(context.Background(), w.pool, jobID)
	if j.Progress != 0.5 || j.Message != "step 5" {
		t.Fatalf("job progress = %v %q", j.Progress, j.Message)
	}
	if n := w.count("SELECT count(*) FROM events WHERE topic = 'gpu'"); n != 1 {
		t.Fatalf("gpu events = %d, want 1 (throttled to one per 5 s)", n)
	}
	var host struct {
		Health struct{ State string }
		Cards  []struct {
			Telemetry *struct{ MemoryUsedMb int } `json:"telemetry"`
		}
	}
	w.ok(w.do(http.MethodGet, "/api/compute/staging", ""), http.StatusOK, &host)
	if host.Health.State != "healthy" || host.Cards[0].Telemetry == nil || host.Cards[0].Telemetry.MemoryUsedMb != 24000 {
		t.Fatalf("compute.get = %+v", host)
	}

	// Logs: NDJSON lines append to the job's log and stream on job.{id}.log.
	lines := `{"t":"2026-09-30T12:00:01Z","level":"info","msg":"loading data"}
{"t":"2026-09-30T12:00:02Z","level":"warn","msg":"Slow batch","fields":{"batch":3}}
{"t":"2026-09-30T12:00:03Z","msg":"step 1 loss 2.3"}
`
	w.ok(f.send(http.MethodPost, "/worker-leases/"+l.ID+"/worker-logs", "application/x-ndjson", lines), http.StatusNoContent, nil)
	expectProblem(t, f.send(http.MethodPost, "/worker-leases/"+l.ID+"/worker-logs", "application/x-ndjson", "not json\n"), 422, "validation-failed")
	if n := w.count(fmt.Sprintf("SELECT count(*) FROM events WHERE topic = 'job.%s.log'", jobID)); n != 1 {
		t.Fatalf("log events = %d", n)
	}
	var page struct {
		Items []struct {
			Seq        int
			Level, Msg string
		}
		NextAfter int
	}
	w.ok(w.do(http.MethodGet, "/api/jobs/"+jobID+"/job-logs?level=warn", ""), http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].Seq != 2 || page.Items[0].Msg != "Slow batch" || page.NextAfter != 3 {
		t.Fatalf("jobLogs.list level=warn = %+v", page)
	}
	w.ok(w.do(http.MethodGet, "/api/jobs/"+jobID+"/job-logs?text=LOSS&after=1", ""), http.StatusOK, &page)
	if len(page.Items) != 1 || page.Items[0].Seq != 3 || page.Items[0].Level != "info" {
		t.Fatalf("jobLogs.list text = %+v", page)
	}

	// Metrics go to the table and, for a run, to run.{id}.metrics.
	pts := map[string]any{"points": []any{
		map[string]any{"name": "loss", "step": 1, "value": 2.3, "wallTime": "2026-09-30T12:00:03Z"},
		map[string]any{"name": "loss", "step": 2, "value": 2.1, "wallTime": "2026-09-30T12:00:04Z"},
		map[string]any{"name": "val_wer", "step": 2, "value": 0.31, "epoch": 0.5, "wallTime": "2026-09-30T12:00:04Z"},
	}}
	w.ok(f.post("/worker-leases/"+l.ID+"/worker-metrics", pts), http.StatusNoContent, nil)
	if n := w.count("SELECT count(*) FROM events WHERE topic = 'run.run_test.metrics'"); n != 1 {
		t.Fatalf("metrics events = %d", n)
	}
	series, err := telemetry.Get(context.Background(), w.pool, telemetry.Query{RunID: "run_test"})
	if err != nil || len(series) != 2 || series[0].Name != "loss" || len(series[0].Points) != 2 || series[1].Points[0].Value != 0.31 {
		t.Fatalf("telemetry.Get = %+v, %v", series, err)
	}

	// Release: outputs must be in the store.
	missing := steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{"model": {Hash: "b3:" + strings.Repeat("0", 64), Type: "checkpoint"}}}
	expectProblem(t, f.release(l.ID, missing), 409, "artifact-missing")
	out, err := w.blobs.PutBytes([]byte("weights"))
	if err != nil {
		t.Fatal(err)
	}
	done := steps.Outcome{State: steps.StateDone, Outputs: map[string]steps.ArtifactRef{"model": {Hash: out, Type: "checkpoint"}},
		Metrics: map[string]float64{"val_wer": 0.31}}
	w.ok(f.release(l.ID, done), http.StatusNoContent, nil)
	w.ok(f.release(l.ID, done), http.StatusNoContent, nil) // a retried release changes nothing
	o := w.outcome(jobID)
	if o.State != steps.StateDone || o.Outputs["model"].Hash != out || o.Metrics["val_wer"] != 0.31 {
		t.Fatalf("outcome = %+v", o)
	}
	expectProblem(t, f.post("/worker-leases/"+l.ID+":report", map[string]any{}), 409, "lease-ended")

	// Uploads are checked against their hash.
	blob := []byte("uploaded blob")
	h := cas.Hash(blob)
	w.ok(f.send(http.MethodPut, "/worker-artifacts/"+h, "application/octet-stream", string(blob)), http.StatusNoContent, nil)
	if ok, _, _ := w.blobs.Has(h); !ok {
		t.Fatal("upload not stored")
	}
	expectProblem(t, f.send(http.MethodPut, "/worker-artifacts/"+cas.Hash([]byte("other")), "application/octet-stream", "tampered"), 422, "artifact-hash-mismatch")

	// A step without a GPU goes to a worker without cards.
	cpu := &fakeWorker{w: w, token: tok, id: f.id, cards: []map[string]any{}}
	cjob := w.enqueue(steps.Spec{Kind: "echo", KindVersion: "1", Params: json.RawMessage(`{}`), Resources: steps.Resources{JobKind: steps.JobData}})
	cl := cpu.claim(2)
	if cl == nil || cl.JobID != cjob || cl.Card.Index != -1 {
		t.Fatalf("cpu lease = %+v", cl)
	}
	w.ok(cpu.release(cl.ID, steps.Outcome{State: steps.StateFailed, Error: &steps.StepError{Type: steps.ErrStep, Message: "boom"}}), http.StatusNoContent, nil)
	if o := w.outcome(cjob); o.State != steps.StateFailed || o.Error == nil || o.Error.Type != steps.ErrStep {
		t.Fatalf("cpu outcome = %+v", o)
	}
}

func TestWorkerRegistrationRefusals(t *testing.T) {
	w := startWorkers(t)
	tok := w.workerToken("staging")
	f := &fakeWorker{w: w, token: tok}
	bad := kind("1", "training", true, false)
	bad["params"] = map[string]any{"type": "object", "properties": map[string]any{
		"lr": map[string]any{"type": "number", "x-cadence": map[string]any{"default": 1, "description": "d"}}}}
	p := expectProblem(t, f.post("/worker-registrations", registration("staging", "toy", "b", map[string]any{"train_toy": bad})), 422, "validation-failed")
	if len(p.Errors) != 1 || !strings.Contains(p.Errors[0].Path, "/params/properties/lr/x-cadence") {
		t.Fatalf("problem = %+v", p)
	}
	w.register(tok, "toy", map[string]any{"train_toy": kind("1", "training", true, false), "echo": kind("1", "data", false, true)})
	// A framework kind lives in one runtime (R40).
	expectProblem(t, f.post("/worker-registrations", registration("staging", "nemo", "b", map[string]any{"train_toy": kind("1", "training", true, false)})), 409, "step-kind-conflict")
	// A neutral kind with another schema is refused; with the same schema it is shared.
	other := kind("1", "data", false, true)
	other["params"] = map[string]any{"type": "object", "properties": map[string]any{}}
	expectProblem(t, f.post("/worker-registrations", registration("staging", "nemo", "b", map[string]any{"echo": other})), 409, "step-kind-conflict")
	w.register(tok, "nemo", map[string]any{"echo": kind("1", "data", false, true), "train_nemo": kind("1", "training", true, false)})
	// A token of one host cannot register another.
	expectProblem(t, f.post("/worker-registrations", registration("elsewhere", "toy", "b", map[string]any{})), 403, "forbidden")
}

func TestWorkerTokenScope(t *testing.T) {
	w := startWorkers(t)
	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})
	// A worker token reaches the worker protocol only.
	for _, path := range []string{"/projects", "/compute", "/registry/runtimes", "/me/workspaces"} {
		expectProblem(t, f.send(http.MethodGet, path, "", ""), 403, "forbidden")
	}
	// Every other credential is refused by the worker protocol.
	host := w.token(func(tx pgx.Tx) (string, error) {
		tok, _, err := credentials.NewHostToken(context.Background(), tx, credentials.HostName, false)
		return tok, err
	})
	other := &fakeWorker{w: w, token: host, id: f.id}
	expectProblem(t, other.post("/worker-leases:claim", map[string]any{"workerId": f.id, "cards": []any{}}), 403, "forbidden")
	// Re-issuing the host's worker token revokes the old one.
	w.token(func(tx pgx.Tx) (string, error) {
		tok, _, err := credentials.NewWorkerToken(context.Background(), tx, "staging", true)
		return tok, err
	})
	expectProblem(t, f.post("/worker-leases:claim", map[string]any{"workerId": f.id, "cards": []any{}}), 401, "unauthenticated")
}

func TestLeaseReaped(t *testing.T) {
	w := startWorkers(t)
	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})
	first := w.enqueue(gpuSpec("train_toy"))
	second := w.enqueue(gpuSpec("train_toy"))
	l := f.claim(2)
	if l == nil || l.JobID != first {
		t.Fatalf("lease = %+v", l)
	}
	if again := f.claim(0); again != nil {
		t.Fatalf("one training job per card, got a second lease %+v", again)
	}
	w.clock.Add(20 * time.Second)
	if n, err := w.workers.Reap(context.Background()); err != nil || n != 0 {
		t.Fatalf("reaped %d after two missed beats (%v)", n, err)
	}
	w.clock.Add(11 * time.Second) // three beats missed
	if n, err := w.workers.Reap(context.Background()); err != nil || n != 1 {
		t.Fatalf("Reap = %d, %v", n, err)
	}
	o := w.outcome(first)
	if o.State != steps.StateFailed || o.Error == nil || o.Error.Type != steps.ErrLost || !o.Error.Retryable {
		t.Fatalf("outcome = %+v", o)
	}
	expectProblem(t, f.post("/worker-leases/"+l.ID+":report", map[string]any{}), 409, "lease-ended")
	expectProblem(t, f.release(l.ID, steps.Outcome{State: steps.StateDone}), 409, "lease-ended")
	// The card is free again.
	if next := f.claim(2); next == nil || next.JobID != second {
		t.Fatalf("after reaping: %+v", next)
	}
	// A restart (new instance) reaps the old process's leases at once.
	w.ok(f.post("/worker-registrations", registration("staging", "toy", "boot-2", map[string]any{"train_toy": kind("1", "training", true, false)})), http.StatusOK, nil)
	if o := w.outcome(second); o.Error == nil || o.Error.Type != steps.ErrLost {
		t.Fatalf("restart outcome = %+v", o)
	}
}

func TestCancelPauseResume(t *testing.T) {
	w := startWorkers(t)
	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})

	// Cancel a waiting job.
	waiting := w.enqueue(gpuSpec("train_toy"))
	w.jobCmd(waiting, "cancel")
	if o := w.outcome(waiting); o.State != steps.StateCancelled {
		t.Fatalf("cancelled waiting job: %+v", o)
	}

	// Pause a running job: it stops, saves its state and waits in the queue.
	job := w.enqueue(gpuSpec("train_toy"))
	l := f.claim(2)
	if l == nil || l.JobID != job {
		t.Fatalf("lease = %+v", l)
	}
	w.jobCmd(job, "pause")
	if stop, reason := f.report(l.ID, map[string]any{}); !stop || reason != "paused" {
		t.Fatalf("report after pause = %v %q", stop, reason)
	}
	state, err := w.blobs.PutBytes([]byte("optimizer state"))
	if err != nil {
		t.Fatal(err)
	}
	w.ok(f.release(l.ID, steps.Outcome{State: steps.StateCancelled, Error: &steps.StepError{Type: steps.ErrCancelled, Message: "paused"},
		Outputs: map[string]steps.ArtifactRef{"state": {Hash: state, Type: "training-state"}}}), http.StatusNoContent, nil)
	w.noOutcome(job)
	var q struct {
		Items []struct{ JobID, State, ResumeFrom string } `json:"items"`
	}
	w.ok(w.do(http.MethodGet, "/api/queue-entries", ""), http.StatusOK, &q)
	if len(q.Items) != 1 || q.Items[0].State != "paused" || q.Items[0].ResumeFrom != state {
		t.Fatalf("queue after pause = %+v", q)
	}
	if again := f.claim(0); again != nil {
		t.Fatalf("a paused job was leased: %+v", again)
	}
	w.jobCmd(job, "resume")
	l = f.claim(2)
	if l == nil || l.JobID != job || l.Spec.Overrides.ResumeFrom != state {
		t.Fatalf("resumed lease = %+v", l)
	}

	// Cancel the running job: the next heartbeat says stop, Await returns at once, the release frees the card.
	w.jobCmd(job, "cancel")
	if o := w.outcome(job); o.State != steps.StateCancelled {
		t.Fatalf("cancelled running job: %+v", o)
	}
	if stop, reason := f.report(l.ID, map[string]any{}); !stop || reason != "cancelled" {
		t.Fatalf("report after cancel = %v %q", stop, reason)
	}
	w.ok(f.release(l.ID, steps.Outcome{State: steps.StateCancelled}), http.StatusNoContent, nil)

	// Priority reorders the queue: the higher one starts first.
	low := w.enqueue(gpuSpec("train_toy"))
	high := w.enqueue(gpuSpec("train_toy"))
	w.ok(w.do(http.MethodPatch, "/api/jobs/"+high, `{"priority": 5}`, "Idempotency-Key", w.key(), "If-Match", fmt.Sprint(w.jobRev(high))), http.StatusOK, nil)
	if l := f.claim(2); l == nil || l.JobID != high {
		t.Fatalf("after reordering: %+v", l)
	}
	_ = low
	// Only step jobs can be paused.
	var noop string
	ctx := auth.WithActor(context.Background(), auth.DevActor())
	if err := pgx.BeginFunc(ctx, w.pool, func(tx pgx.Tx) error {
		j, _, err := w.jobs.Enqueue(ctx, tx, jobs.Spec{Kind: jobs.KindNoop, Args: map[string]any{"sleepMs": 2000}})
		noop = j.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	expectProblem(t, w.do(http.MethodPost, "/api/jobs/"+noop+":pause", "", "Idempotency-Key", w.key(), "If-Match", fmt.Sprint(w.jobRev(noop))), 409, "conflict")
}

func TestAvailabilityWindowClosed(t *testing.T) {
	w := startWorkers(t)
	// Training may run on weeknights only (Europe/Berlin is UTC+2 in September).
	body := `{"cards": [{"index": 0, "windows": {"training": [{"days": ["mon", "tue", "wed", "thu", "fri"], "start": "22:00", "end": "08:00", "timezone": "Europe/Berlin"}]}}]}`
	var host struct{ Rev int }
	w.ok(w.do(http.MethodGet, "/api/compute/staging", ""), http.StatusOK, &host)
	w.ok(w.do(http.MethodPatch, "/api/compute/staging", body, "Idempotency-Key", w.key(), "If-Match", fmt.Sprint(host.Rev)), http.StatusOK, nil)
	w.ok(w.do(http.MethodGet, "/api/compute/staging", ""), http.StatusOK, &host)
	expectProblem(t, w.do(http.MethodPatch, "/api/compute/staging", `{"cards": [{"index": 0, "windows": {"training": [{"days": ["mon"], "start": "22:00", "end": "08:00", "timezone": "Mars/Base"}]}}]}`,
		"Idempotency-Key", w.key(), "If-Match", fmt.Sprint(host.Rev)), 422, "validation-failed")

	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})
	spec := gpuSpec("train_toy")
	est := 4 * 3600.0
	spec.EstimateSeconds = &est
	job := w.enqueue(spec)
	if l := f.claim(0); l != nil { // Wednesday 14:00 in Berlin: closed
		t.Fatalf("leased outside the window: %+v", l)
	}
	w.clock.Set(time.Date(2026, 9, 30, 20, 30, 0, 0, time.UTC)) // 22:30 Berlin: open until 08:00
	long := spec
	longEst := 12 * 3600.0
	long.EstimateSeconds = &longEst
	tooLong := w.enqueue(long)
	l := f.claim(2)
	if l == nil || l.JobID != job {
		t.Fatalf("lease in the window = %+v", l)
	}
	w.clock.Set(time.Date(2026, 10, 1, 6, 1, 0, 0, time.UTC)) // 08:01 Berlin: closed
	if stop, reason := f.report(l.ID, map[string]any{}); !stop || reason != "window-closed" {
		t.Fatalf("report after the close = %v %q", stop, reason)
	}
	state, _ := w.blobs.PutBytes([]byte("state at the close"))
	w.ok(f.release(l.ID, steps.Outcome{State: steps.StateCancelled,
		Outputs: map[string]steps.ArtifactRef{"state": {Hash: state, Type: "training-state"}}}), http.StatusNoContent, nil)
	w.noOutcome(job)
	if l := f.claim(0); l != nil {
		t.Fatalf("leased while closed: %+v", l)
	}
	w.clock.Set(time.Date(2026, 10, 1, 20, 0, 0, 0, time.UTC)) // Thursday 22:00 Berlin: open again
	l = f.claim(2)
	if l == nil || l.JobID != job || l.Spec.Overrides.ResumeFrom != state {
		t.Fatalf("resumed after the window opened: %+v", l)
	}
	w.ok(f.release(l.ID, steps.Outcome{State: steps.StateDone}), http.StatusNoContent, nil)
	if o := w.outcome(job); o.State != steps.StateDone {
		t.Fatalf("outcome = %+v", o)
	}
	// A 12-hour estimate never fits the 10-hour window.
	if l := f.claim(0); l != nil {
		t.Fatalf("a job whose estimate does not fit was leased: %+v", l)
	}
	w.jobCmd(tooLong, "cancel")
}

func TestTwoRuntimesShareACard(t *testing.T) {
	w := startWorkers(t)
	tok := w.workerToken("staging")
	toy := w.register(tok, "toy", map[string]any{"train_toy": kind("1", "training", true, false)})
	nemo := w.register(tok, "nemo", map[string]any{"train_nemo": kind("1", "training", true, false)})
	a := w.enqueue(gpuSpec("train_toy"))
	b := w.enqueue(gpuSpec("train_nemo"))
	// Both claim at once; the card's slot lock lets exactly one training job onto the card.
	var (
		wg     sync.WaitGroup
		leased atomic.Int32
		got    [2]*lease
	)
	for i, f := range []*fakeWorker{toy, nemo} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l := f.claim(1); l != nil {
				leased.Add(1)
				got[i] = l
			}
		}()
	}
	wg.Wait()
	if leased.Load() != 1 {
		t.Fatalf("%d training leases on one card", leased.Load())
	}
	holder, waiter, waitJob := toy, nemo, b
	if got[1] != nil {
		holder, waiter, waitJob = nemo, toy, a
	}
	held := got[0]
	if held == nil {
		held = got[1]
	}
	w.ok(holder.release(held.ID, steps.Outcome{State: steps.StateDone}), http.StatusNoContent, nil)
	if l := waiter.claim(2); l == nil || l.JobID != waitJob {
		t.Fatalf("the other runtime's job after the release: %+v", l)
	}
	if n := w.count("SELECT count(*) FROM leases WHERE state = 'active' AND card_index = 0"); n != 1 {
		t.Fatalf("%d active leases on card 0", n)
	}
}
