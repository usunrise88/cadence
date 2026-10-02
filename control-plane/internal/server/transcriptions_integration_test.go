//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/transcriptions"
)

// liveService is the admin server's transcription service, for the live job kind registered before the server exists.
var liveService atomic.Pointer[transcriptions.Service]

const liveBase = "base-model/fixture-live-base"

// startLive starts the control plane with the live job kind, a fixture family with a live role (6000 MB per session,
// 2600 MB per further model), a base model of it, the seeded staging host and a project; it returns the worker id.
func startLive(t *testing.T, slug string) (*env, string) {
	t.Helper()
	e := startWith(t, nil, func(_ *pgxpool.Pool, js *jobs.Service) {
		js.Register(steps.LiveJobKind, func(ctx context.Context, run *jobs.Run) (any, error) {
			return liveService.Load().Handle(ctx, run)
		}, jobs.KindOptions{Timeout: transcriptions.HandlerTimeout, Queue: jobs.QueueSteps})
	})
	liveService.Store(e.admin.Transcriptions())
	e.admin.Transcriptions().Timing = transcriptions.Timing{Poll: 50 * time.Millisecond, Drain: 3 * time.Second}
	ctx := context.Background()
	if _, err := compute.Seed(ctx, e.pool, defaults.Get().Compute.Hosts, registry.Bundled()); err != nil {
		t.Fatal(err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		b, _ := json.Marshal(map[string]any{"hfRepo": "fixture/live", "revision": "0123456789abcdef", "licence": "cc-by-4.0",
			"familyId": "fixture-live", "checkpointFile": "live.bin"})
		_, _, _, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: registry.KindBaseModel, Name: liveBase, Payload: b, Freeze: true,
			Actor: auth.Actor{Kind: auth.KindAutomation, ID: "worker"}}, time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reg, _ := json.Marshal(map[string]any{"host": "staging", "instance": "live-1",
		"runtime": map[string]any{"name": "fixture-live", "version": "1", "digest": "sha256:" + strings.Repeat("d", 64)},
		"stepKinds": map[string]any{"fx_live": map[string]any{
			"version": "1", "role": "live", "params": map[string]any{"type": "object", "properties": map[string]any{}},
			"consumes":       map[string]string{"model": "checkpoint", "base": "base_model", "audio": "audio"},
			"optionalInputs": []string{"model", "base", "audio"}, "produces": map[string]string{},
			"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 6, "jobKind": "interactive"}, "help": "steps.fx-live",
		}, "fx_eval": map[string]any{
			"version": "1", "params": map[string]any{"type": "object", "properties": map[string]any{}},
			"consumes": map[string]string{}, "produces": map[string]string{},
			"resources": map[string]any{"gpu": true, "gpus": 1, "memoryGb": 4, "jobKind": "eval"}, "help": "steps.fx-eval",
		}},
		"modelFamilies": []any{map[string]any{"name": "fixture-live", "version": "1", "framework": "none", "architecture": "none",
			"latencyProfiles": []map[string]any{{"name": "offline", "latencyMs": 0}, {"name": "160ms", "latencyMs": 160, "chunkMs": 160}},
			"capabilities":    map[string]any{"streaming": true},
			"roles":           map[string]string{"live": "fx_live"},
			"interactive":     map[string]any{"memoryMb": 6000, "extraCheckpointMb": 2600}}}})
	var w struct{ ID string }
	e.ok(e.do("POST", "/api/worker-registrations", string(reg)), 200, &w)
	e.newProject(slug)
	return e, w.ID
}

type liveSession struct {
	ID            string `json:"id"`
	JobID         string `json:"jobId"`
	State         string `json:"state"`
	StreamURL     string `json:"streamUrl"`
	Ticket        string `json:"ticket"`
	LiveKind      string `json:"liveKind"`
	ReservationMB int    `json:"reservationMb"`
	Position      *int   `json:"position"`
	Targets       []struct {
		Target, Kind, ID, Profile, Language, WeightsKey string
	} `json:"targets"`
	Limits struct {
		SessionSeconds, IdleSeconds, TicketSeconds, FrameMs, MaxMessageBytes int
	} `json:"limits"`
	Allowance struct {
		GPUHoursPerDay, UsedGPUHours, RemainingGPUHours float64
	} `json:"allowance"`
}

const twoLanes = `{"input":{"kind":"microphone"},"targets":[{"baseModelVersionId":"` + liveBase + `","profile":"160ms"},` +
	`{"baseModelVersionId":"` + liveBase + `","profile":"offline"}]}`

func (e *env) openLive(slug, body string) liveSession {
	e.t.Helper()
	var s liveSession
	e.ok(e.do("POST", "/api/projects/"+slug+"/transcriptions", body, "Idempotency-Key", e.key()), 201, &s)
	return s
}

func (e *env) wsURL(path string) string { return "ws" + strings.TrimPrefix(e.url, "http") + path }

// dialBrowser opens the live socket as the page would (Origin = the server).
func (e *env) dialBrowser(path, origin string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return websocket.Dial(ctx, e.wsURL(path), &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}})
}

func (e *env) dialWorker(jobID, token string) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return websocket.Dial(ctx, e.wsURL("/api/worker-live/"+jobID), &websocket.DialOptions{HTTPHeader: http.Header{"Cadence-Live-Token": {token}}})
}

type liveLease struct {
	ID     string            `json:"id"`
	JobID  string            `json:"jobId"`
	Spec   steps.Spec        `json:"spec"`
	Inputs map[string]string `json:"inputs"`
	Env    map[string]string `json:"env"`
	Card   struct {
		Index       int `json:"index"`
		MemoryCapMB int `json:"memoryCapMb"`
	} `json:"card"`
}

func (e *env) claimLive(workerID string) liveLease {
	e.t.Helper()
	var c struct{ Lease *liveLease }
	cards := `[{"index":0,"name":"card","memoryTotalMb":49152,"memoryUsedMb":24000,"utilization":0.1}]`
	e.ok(e.do("POST", "/api/worker-leases:claim", `{"workerId":"`+workerID+`","wait":20,"cards":`+cards+`}`), 200, &c)
	if c.Lease == nil {
		e.t.Fatal("no lease")
	}
	return *c.Lease
}

type liveMsg struct {
	Type     string          `json:"type"`
	State    string          `json:"state"`
	Position int             `json:"position"`
	Reason   string          `json:"reason"`
	Source   string          `json:"source"`
	T        float64         `json:"t"`
	Text     string          `json:"text"`
	Fatal    bool            `json:"fatal"`
	Problem  json.RawMessage `json:"problem"`
}

func read(t *testing.T, c *websocket.Conn) (websocket.MessageType, []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	typ, b, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return typ, b
}

// next reads the browser's next message other than the relay's own stats and waiting reports (unless wanted).
func next(t *testing.T, c *websocket.Conn, keep ...string) liveMsg {
	t.Helper()
	for {
		_, b := read(t, c)
		var m liveMsg
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("not JSON: %s", b)
		}
		if (m.Type == "stats" && m.Source == "relay" || m.Type == "waiting") && !contains(keep, m.Type) {
			continue
		}
		return m
	}
}

func write(t *testing.T, c *websocket.Conn, typ websocket.MessageType, b []byte) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Write(ctx, typ, b); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// closeCode reads until the socket closes and returns its close status.
func closeCode(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func (e *env) sessionRow(id string) (state, reason string, gpu float64, ticket *string) {
	e.t.Helper()
	if err := e.pool.QueryRow(context.Background(), `SELECT state, coalesce(end_reason, ''), gpu_seconds, ticket_hash
		FROM transcriptions WHERE id = $1`, id).Scan(&state, &reason, &gpu, &ticket); err != nil {
		e.t.Fatal(err)
	}
	return
}

func (e *env) waitEnded(id string) (string, float64) {
	e.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if st, reason, gpu, _ := e.sessionRow(id); st == "ended" {
			return reason, gpu
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.t.Fatalf("session %s did not end", id)
	return "", 0
}

// The whole live path with a fake worker: transcriptions.new (dry run, agents refused, one session per person), the
// socket's gates (Origin, ticket), the queue place and loading reports, the worker's dial with its lease's token,
// frames relayed byte for byte both ways, ping answered by the relay, end → summary → 1000, and the record closed
// with its GPU time.
func TestTranscriptionLiveChannel(t *testing.T) {
	e, workerID := startLive(t, "live")
	e.useWorkers()

	// A dry run answers the session without opening it; agents are refused.
	var dry liveSession
	e.ok(e.do("POST", "/api/projects/live/transcriptions?dryRun=true", twoLanes, "Idempotency-Key", e.key()), 200, &dry)
	if dry.Ticket != "" || dry.StreamURL != "" || dry.ReservationMB != 6000 || dry.LiveKind != "fx_live@1" || len(dry.Targets) != 2 ||
		dry.Targets[0].Target != "A" || dry.Targets[1].Profile != "offline" || dry.Targets[0].WeightsKey != dry.Targets[1].WeightsKey {
		t.Fatalf("dry run %+v", dry)
	}
	if n := e.count("SELECT count(*) FROM transcriptions"); n != 0 {
		t.Fatalf("a dry run recorded %d sessions", n)
	}
	expectProblem(t, e.agent("POST", "/api/projects/live/transcriptions", twoLanes, "Idempotency-Key", e.key()), 403, "forbidden")
	expectProblem(t, e.do("POST", "/api/projects/live/transcriptions",
		`{"input":{"kind":"microphone"},"targets":[{"baseModelVersionId":"`+liveBase+`","profile":"80ms"}]}`, "Idempotency-Key", e.key()),
		422, "validation-failed")

	s := e.openLive("live", twoLanes)
	if s.Ticket == "" || !strings.HasPrefix(s.StreamURL, "/api/transcriptions/"+s.ID+"/stream?ticket=") || s.JobID == "" ||
		s.Limits.FrameMs != 20 || s.Limits.SessionSeconds != 900 || s.Allowance.GPUHoursPerDay != 1 {
		t.Fatalf("session %+v", s)
	}
	expectProblem(t, e.do("POST", "/api/projects/live/transcriptions", twoLanes, "Idempotency-Key", e.key()), 409, "transcription-in-progress")

	// The socket's gates: a foreign Origin, a wrong ticket.
	if _, resp, err := e.dialBrowser(s.StreamURL, "https://evil.example"); err == nil || resp == nil || resp.StatusCode != 403 {
		t.Fatalf("foreign origin: %v %v", err, resp)
	}
	if _, resp, err := e.dialBrowser("/api/transcriptions/"+s.ID+"/stream?ticket=wrong-ticket-0123456789", e.url); err == nil || resp.StatusCode != 403 {
		t.Fatalf("wrong ticket: %v %v", err, resp)
	}
	browser, _, err := e.dialBrowser(s.StreamURL, e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = browser.CloseNow() }()
	if m := next(t, browser, "waiting"); m.Type != "waiting" || m.State != "queued" || m.Position != 1 || m.Reason == "" {
		t.Fatalf("first message %+v", m)
	}
	if _, resp, err := e.dialBrowser(s.StreamURL, e.url); err == nil || resp.StatusCode != 403 {
		t.Fatalf("a used ticket opened a second socket: %v", err)
	}

	// The page says what it sends before the worker is there; the relay holds it.
	start := []byte(`{"type":"start","input":{"kind":"microphone","sampleRate":48000,"frameMs":20}}`)
	write(t, browser, websocket.MessageText, start)

	// The worker leases the interactive job: its reservation and a live token.
	l := e.claimLive(workerID)
	if l.JobID != s.JobID || l.Spec.Kind != "fx_live" || l.Spec.Resources.JobKind != "interactive" || l.Card.MemoryCapMB != 6000 ||
		l.Env[transcriptions.EnvLiveToken] == "" || !strings.HasPrefix(l.Inputs["base.0"], "cas://b3:") || len(l.Inputs) != 1 {
		t.Fatalf("lease %+v", l)
	}
	var params struct {
		Session string `json:"session"`
		Targets []struct{ Target, Model, Profile, Language string }
		Input   struct{ Kind string }
		Pace    string
		FrameMs int
	}
	if err := json.Unmarshal(l.Spec.Params, &params); err != nil || params.Session != s.ID || len(params.Targets) != 2 ||
		params.Targets[0].Model != "base.0" || params.Targets[1].Model != "base.0" || params.Input.Kind != "microphone" ||
		params.Pace != "realtime" || params.FrameMs != 20 {
		t.Fatalf("params %s (%v)", l.Spec.Params, err)
	}
	for i := 0; ; i++ { // reports from before the lease may still be in flight
		m := next(t, browser, "waiting")
		if m.Type == "waiting" && m.State == "loading" {
			break
		}
		if m.Type != "waiting" || m.State != "queued" || i > 100 {
			t.Fatalf("loading %+v", m)
		}
	}
	if _, resp, err := e.dialWorker(l.JobID, "not-the-token"); err == nil || resp.StatusCode != 403 {
		t.Fatalf("a wrong live token dialled: %v", err)
	}
	worker, _, err := e.dialWorker(l.JobID, l.Env[transcriptions.EnvLiveToken])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.CloseNow() }()

	// Frames pass unchanged both ways.
	if typ, b := read(t, worker); typ != websocket.MessageText || string(b) != string(start) {
		t.Fatalf("worker got %s", b)
	}
	pcm := make([]byte, 1920) // 20 ms at 48 kHz, PCM16
	for i := range pcm {
		pcm[i] = byte(i)
	}
	write(t, browser, websocket.MessageBinary, pcm)
	if typ, b := read(t, worker); typ != websocket.MessageBinary || string(b) != string(pcm) {
		t.Fatalf("worker got %d bytes of %v", len(b), typ)
	}
	write(t, browser, websocket.MessageText, []byte(`{"type":"ping","t":12.5}`))
	if m := next(t, browser); m.Type != "pong" || m.Source != "relay" || m.T != 12.5 {
		t.Fatalf("pong %+v", m)
	}
	for _, msg := range []string{
		`{"type": "started", "targets": [{"target": "A", "profile": "160ms", "chunkMs": 160, "language": "he-IL", "loadS": 0.1}], "resampler": "polyphase"}`,
		`{"type": "partial", "target": "A", "segment": 0, "seq": 1, "text": "שלום", "audioEnd": 0.32}`,
		`{"type": "final", "target": "A", "segment": 0, "seq": 2, "text": "שלום", "words": [{"word": "שלום", "start": 0.1, "end": 0.3}], "endpoint": "eou", "audioEnd": 0.48, "space": true}`,
	} {
		write(t, worker, websocket.MessageText, []byte(msg))
		if _, b := read(t, browser); string(b) != msg {
			t.Fatalf("browser got %s, want %s", b, msg)
		}
	}
	write(t, browser, websocket.MessageText, []byte(`{"type":"keepalive","t":1}`))
	if _, b := read(t, worker); msgTypeOf(b) != "keepalive" {
		t.Fatalf("worker got %s", b)
	}
	write(t, browser, websocket.MessageText, []byte(`{"type":"end"}`))
	if _, b := read(t, worker); msgTypeOf(b) != "end" {
		t.Fatalf("worker got %s", b)
	}
	write(t, worker, websocket.MessageText, []byte(`{"type": "summary", "audioS": 0.5, "rtf": 0.1, "targets": {}}`))
	if m := next(t, browser, "stats"); m.Type != "stats" || m.Source != "relay" {
		t.Fatalf("relay stats %+v", m)
	}
	if m := next(t, browser); m.Type != "summary" {
		t.Fatalf("summary %+v", m)
	}
	if code := closeCode(t, browser); code != websocket.StatusNormalClosure {
		t.Fatalf("close %d", code)
	}
	e.release(l.ID, steps.Outcome{State: steps.StateDone})
	e.waitJob(s.JobID, "done")
	reason, gpu := e.waitEnded(s.ID)
	if reason != "done" || gpu <= 0 {
		t.Fatalf("record: %q %v", reason, gpu)
	}
	// The record keeps who, when, which targets and the GPU time: no ticket, no token, no input.
	var targets string
	var ticket, token *string
	if err := e.pool.QueryRow(context.Background(), "SELECT targets::text, ticket_hash, live_token_hash FROM transcriptions WHERE id = $1",
		s.ID).Scan(&targets, &ticket, &token); err != nil {
		t.Fatal(err)
	}
	if ticket != nil || token != nil || !strings.Contains(targets, `"profile": "160ms"`) || strings.Contains(targets, "microphone") {
		t.Fatalf("record %s %v %v", targets, ticket, token)
	}
	// Interactive lease time is the manual-test allowance's, not the project's GPU budget.
	var a liveSession
	e.ok(e.do("POST", "/api/projects/live/transcriptions?dryRun=true", twoLanes, "Idempotency-Key", e.key()), 200, &a)
	if a.Allowance.UsedGPUHours <= 0 && gpu > 1.8 {
		t.Fatalf("allowance %+v after %v s", a.Allowance, gpu)
	}
}

func msgTypeOf(b []byte) string {
	var m struct{ Type string }
	_ = json.Unmarshal(b, &m)
	return m.Type
}

// The relay's limits: a worker that dialled before its page is pinged and paired; an idle session gets end injected
// and closes 4001 after the summary; a lost worker closes 4003 and cancels the job; an unused ticket is swept; and
// the allowance refuses a new session once used up.
func TestTranscriptionLimits(t *testing.T) {
	e, workerID := startLive(t, "limits")
	e.useWorkers()
	svc := e.admin.Transcriptions()
	one := `{"input":{"kind":"microphone"},"targets":[{"baseModelVersionId":"` + liveBase + `"}]}`

	// Idle: the worker dials first (parked), the page joins, nobody speaks.
	svc.Timing.Idle = 400 * time.Millisecond
	s := e.openLive("limits", one)
	if s.Targets[0].Profile != "160ms" || s.Targets[0].Language == "" {
		t.Fatalf("defaults %+v", s.Targets)
	}
	l := e.claimLive(workerID)
	worker, _, err := e.dialWorker(l.JobID, l.Env[transcriptions.EnvLiveToken])
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = worker.CloseNow() }()
	readPump := make(chan []byte, 16) // a worker reads all the time (the relay pings a parked socket)
	go func() {
		for {
			_, b, err := worker.Read(context.Background())
			if err != nil {
				close(readPump)
				return
			}
			readPump <- b
		}
	}()
	browser, _, err := e.dialBrowser(s.StreamURL, e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = browser.CloseNow() }()
	if m := next(t, browser); m.Type != "error" || !m.Fatal || !strings.Contains(string(m.Problem), "transcription-limit") {
		t.Fatalf("idle error %+v %s", m, m.Problem)
	}
	select {
	case b := <-readPump:
		if msgTypeOf(b) != "end" {
			t.Fatalf("worker got %s, want the injected end", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no end injected")
	}
	write(t, worker, websocket.MessageText, []byte(`{"type":"summary","audioS":0,"rtf":0,"targets":{}}`))
	if m := next(t, browser); m.Type != "summary" {
		t.Fatalf("summary %+v", m)
	}
	if code := closeCode(t, browser); code != transcriptions.CloseIdle {
		t.Fatalf("close %d, want 4001", code)
	}
	e.release(l.ID, steps.Outcome{State: steps.StateDone})
	e.waitEnded(s.ID)
	svc.Timing.Idle = 0

	// A lost worker: the page hears it, the job is cancelled, the record closes.
	s = e.openLive("limits", one)
	browser2, _, err := e.dialBrowser(s.StreamURL, e.url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = browser2.CloseNow() }()
	l = e.claimLive(workerID)
	worker2, _, err := e.dialWorker(l.JobID, l.Env[transcriptions.EnvLiveToken])
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // paired
	_ = worker2.Close(websocket.StatusInternalError, "crashed")
	if m := next(t, browser2); m.Type != "error" || !m.Fatal {
		t.Fatalf("lost worker %+v", m)
	}
	if code := closeCode(t, browser2); code != transcriptions.CloseWorkerLost {
		t.Fatalf("close %d, want 4003", code)
	}
	e.waitJob(s.JobID, "cancelled")
	e.waitEnded(s.ID)
	e.release(l.ID, steps.Outcome{State: steps.StateCancelled, Error: &steps.StepError{Type: steps.ErrCancelled, Message: "stopped"}})

	// A ticket never used: the sweep ends the session once it expired.
	s = e.openLive("limits", one)
	if _, err := e.pool.Exec(context.Background(), "UPDATE transcriptions SET ticket_expires_at = now() - interval '1 minute' WHERE id = $1", s.ID); err != nil {
		t.Fatal(err)
	}
	if err := e.admin.SweepTranscriptions(context.Background()); err != nil {
		t.Fatal(err)
	}
	reason, _ := e.waitEnded(s.ID)
	if !strings.Contains(reason, "expired") {
		t.Fatalf("swept for %q", reason)
	}

	// The allowance: used up, a new session is refused.
	d := *defaults.Get()
	d.Budgets.ManualTestGPUHoursPerProjectPerDay.Value = 1e-6
	e.admin.Defaults = &d
	defer func() { e.admin.Defaults = nil }()
	p := expectProblem(t, e.do("POST", "/api/projects/limits/transcriptions", one, "Idempotency-Key", e.key()), 429, "transcription-allowance-exhausted")
	if !strings.Contains(p.Detail, "limits") {
		t.Fatalf("detail %q", p.Detail)
	}
	if n := e.count(fmt.Sprintf("SELECT count(*) FROM transcriptions WHERE state <> 'ended' AND project_id = '%s'", e.projectID("limits"))); n != 0 {
		t.Fatalf("%d sessions still open", n)
	}
}

// Interactive jobs start before every other kind (R49): an eval step of a higher-priority project, queued an hour
// earlier with a higher job priority, is leased after the live session's job, beside it on the same card.
func TestInteractiveJobsLeaseFirst(t *testing.T) {
	e, workerID := startLive(t, "first")
	e.useWorkers()
	ctx := context.Background()
	pid := e.projectID("first")
	if _, err := e.pool.Exec(ctx, `UPDATE projects SET budgets = budgets || '{"queuePriority": 50}'::jsonb WHERE id = $1`, pid); err != nil {
		t.Fatal(err)
	}
	spec := steps.Spec{StepID: "eval", PipelineRunID: "plr_fixture", ProjectID: pid, Kind: "fx_eval", KindVersion: "1",
		Params: json.RawMessage(`{}`), Inputs: map[string]steps.ArtifactRef{}, Outputs: map[string]string{},
		Resources: steps.Resources{GPU: true, GPUs: 1, MemoryGB: 4, JobKind: steps.JobEval}, Priority: 100, Attempt: 1}
	actor, _ := json.Marshal(auth.DevActor())
	if _, err := e.pool.Exec(ctx, `INSERT INTO jobs (id, river_id, kind, project_id, actor, state, priority)
		VALUES ('job_eval', -1, 'step', $1, $2, 'running', 100)`, pid, actor); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO step_jobs (job_id, project_id, spec, kind_ref, job_kind, gpu, memory_mb, enqueued_at)
		VALUES ('job_eval', $1, $2, 'fx_eval@1', 'eval', true, 4096, now() - interval '1 hour')`, pid, spec); err != nil {
		t.Fatal(err)
	}
	s := e.openLive("first", `{"input":{"kind":"microphone"},"targets":[{"baseModelVersionId":"`+liveBase+`"}]}`)
	deadline := time.Now().Add(20 * time.Second)
	for e.count(fmt.Sprintf("SELECT count(*) FROM step_jobs WHERE job_id = '%s'", s.JobID)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the live job never reached the queue")
		}
		time.Sleep(20 * time.Millisecond)
	}
	first := e.claimLive(workerID)
	second := e.claimLive(workerID)
	if first.JobID != s.JobID || second.JobID != "job_eval" || first.Card.MemoryCapMB != 6000 || second.Card.MemoryCapMB != 4096 {
		t.Fatalf("leased %s (%d MB) then %s (%d MB)", first.JobID, first.Card.MemoryCapMB, second.JobID, second.Card.MemoryCapMB)
	}
	if second.Env[transcriptions.EnvLiveToken] != "" {
		t.Fatal("a step job got a live token")
	}
	e.release(second.ID, steps.Outcome{State: steps.StateDone})
	e.release(first.ID, steps.Outcome{State: steps.StateDone})
	e.waitEnded(s.ID)
}

// The manual-test allowance is granted, not only checked: an open session's grant (session_seconds, less what its job
// leased) is spent already for every other session of the project, so sessions opened side by side cannot overspend.
// And someone else's session never reaches the relay's hub.
func TestTranscriptionAllowanceGrants(t *testing.T) {
	e, _ := startLive(t, "grants")
	ctx := context.Background()
	d := *defaults.Get()
	d.Budgets.ManualTestGPUHoursPerProjectPerDay.Value = 0.3 // 1080 s
	e.admin.Defaults = &d
	defer func() { e.admin.Defaults = nil }()
	one := `{"input":{"kind":"microphone"},"targets":[{"baseModelVersionId":"` + liveBase + `"}]}`
	pid := e.projectID("grants")

	// Another person's open session was granted 900 s and has leased nothing yet: 180 s are left.
	actor, _ := json.Marshal(auth.Actor{Kind: auth.KindUser, ID: "usr_other", Name: "other"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO transcriptions (id, project_id, user_id, actor, targets, state, session_seconds)
		VALUES ('trs_00000000-0000-7000-8000-000000000001', $1, 'usr_other', $2, '[]', 'queued', 900)`, pid, actor); err != nil {
		t.Fatal(err)
	}
	var dry liveSession
	e.ok(e.do("POST", "/api/projects/grants/transcriptions?dryRun=true", one, "Idempotency-Key", e.key()), 200, &dry)
	if dry.Limits.SessionSeconds != 180 || dry.Allowance.RemainingGPUHours != 0.05 {
		t.Fatalf("dry run beside a granted session: limits %+v allowance %+v", dry.Limits, dry.Allowance)
	}
	s := e.openLive("grants", one)
	var granted int
	if err := e.pool.QueryRow(ctx, `SELECT session_seconds FROM transcriptions WHERE id = $1`, s.ID).Scan(&granted); err != nil {
		t.Fatal(err)
	}
	if s.Limits.SessionSeconds != 180 || granted != 180 {
		t.Fatalf("granted %d s (limits %d), want the 180 s left", granted, s.Limits.SessionSeconds)
	}

	// Someone else's session: refused before its socket is registered (its owner's socket is not displaced).
	resp := e.do("GET", "/api/transcriptions/trs_00000000-0000-7000-8000-000000000001/stream?ticket=xxxxxxxxxxxxxxxx", "", "Origin", e.url)
	if body := readAll(t, resp); resp.StatusCode != http.StatusForbidden || !strings.Contains(string(body), "someone else") {
		t.Fatalf("someone else's session: %d %s", resp.StatusCode, body)
	}
	if e.admin.Transcriptions().Connected("trs_00000000-0000-7000-8000-000000000001") {
		t.Fatal("someone else's session reached the hub")
	}

	// Everything granted: the next person is refused at once.
	if _, err := e.pool.Exec(ctx, `UPDATE transcriptions SET state = 'ended' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE transcriptions SET session_seconds = 1080 WHERE id = 'trs_00000000-0000-7000-8000-000000000001'`); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do("POST", "/api/projects/grants/transcriptions", one, "Idempotency-Key", e.key()), 429, "transcription-allowance-exhausted")
}
