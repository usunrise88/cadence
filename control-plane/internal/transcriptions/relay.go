package transcriptions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The relay (R48): the browser's socket (stream.connect) and the worker's dial-out (workerLive.connect) meet here,
// in process. Frames pass unchanged both ways; the relay reads only "type" — to answer ping itself, to inject end
// at the idle and session limits and to see the summary. It owns the limits: the queue towards the worker is
// bounded (backpressure), the session is capped and closed when idle, and a waiting session reports its place.

// Close codes of the browser socket (the contract's stream.connect).
const (
	CloseIdle       websocket.StatusCode = 4001
	CloseCap        websocket.StatusCode = 4002
	CloseWorkerLost websocket.StatusCode = 4003
	CloseNotStarted websocket.StatusCode = 4004
)

// workerReadLimit is the largest message a worker sends (a final with many words).
const workerReadLimit = 1 << 20

// errBackpressure ends a session whose worker fell behind.
var errBackpressure = errors.New("backpressure: the upstream queue stayed full")

type frame struct {
	typ  websocket.MessageType
	data []byte
	at   time.Time
}

// workerConn is a worker's live socket with its read pump running (a ping needs a reader).
type workerConn struct {
	c    *websocket.Conn
	down chan frame    // closed when the read pump ends
	done chan struct{} // closed when the session is finished with it
	once sync.Once
	// parked: the socket waited for its browser, so its worker may have died since (spike A5): pinged before pairing.
	parked bool
}

func newWorkerConn(ctx context.Context, c *websocket.Conn) *workerConn {
	w := &workerConn{c: c, down: make(chan frame, 64), done: make(chan struct{})}
	c.SetReadLimit(workerReadLimit)
	go func() {
		defer close(w.down)
		for {
			t, b, err := c.Read(ctx)
			if err != nil {
				return
			}
			select {
			case w.down <- frame{typ: t, data: b, at: time.Now()}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return w
}

func (w *workerConn) finish() { w.once.Do(func() { close(w.done) }) }

// alive pings the socket (a parked socket may belong to a worker that died since it dialled, spike A5).
func (w *workerConn) alive(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return w.c.Ping(ctx) == nil
}

// liveSession is a browser waiting for, or relaying to, its worker.
type liveSession struct {
	id, jobID, userID, projectID string
	reservationMB                int
	worker                       chan *workerConn // a worker's socket, handed over by the hub
	ended                        chan steps.Outcome
}

type hub struct {
	mu       sync.Mutex
	sessions map[string]*liveSession // by session id
	byJob    map[string]*liveSession
	parked   map[string]*workerConn // by job id: a worker that dialled before its browser
	closing  chan struct{}
	stopped  bool
}

func (h *hub) init() {
	if h.sessions == nil {
		h.sessions, h.byJob, h.parked, h.closing = map[string]*liveSession{}, map[string]*liveSession{}, map[string]*workerConn{}, make(chan struct{})
	}
}

func (h *hub) register(ls *liveSession) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if h.stopped {
		return problems.Conflict.New("the control plane is stopping")
	}
	if _, dup := h.sessions[ls.id]; dup {
		return problems.TranscriptionTicketInvalid.New("session %s already has its socket", ls.id)
	}
	h.sessions[ls.id] = ls
	if ls.jobID != "" {
		h.byJob[ls.jobID] = ls
		if w := h.parked[ls.jobID]; w != nil {
			delete(h.parked, ls.jobID)
			w.parked = true
			ls.worker <- w
		}
	}
	return nil
}

func (h *hub) unregister(ls *liveSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions[ls.id] == ls {
		delete(h.sessions, ls.id)
	}
	if h.byJob[ls.jobID] == ls {
		delete(h.byJob, ls.jobID)
	}
}

// has reports whether session id has its browser on a socket here.
func (h *hub) has(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.sessions[id]
	return ok
}

// attachWorker hands the worker's socket to its session, or parks it until the browser comes.
func (h *hub) attachWorker(jobID string, w *workerConn) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if h.stopped {
		return problems.Conflict.New("the control plane is stopping")
	}
	if ls := h.byJob[jobID]; ls != nil {
		select {
		case ls.worker <- w:
			return nil
		default:
			return problems.Conflict.New("job %s's session already has a worker socket waiting", jobID)
		}
	}
	if old := h.parked[jobID]; old != nil {
		old.finish()
	}
	h.parked[jobID] = w
	return nil
}

// unpark removes a parked socket; false when a session took it already.
func (h *hub) unpark(jobID string, w *workerConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.parked[jobID] == w {
		delete(h.parked, jobID)
		return true
	}
	return false
}

func (h *hub) notify(ls *liveSession, o steps.Outcome) {
	if ls == nil {
		return
	}
	select {
	case ls.ended <- o:
	default:
	}
}

func (h *hub) jobEnded(id string, o steps.Outcome) {
	h.mu.Lock()
	ls := h.sessions[id]
	h.mu.Unlock()
	h.notify(ls, o)
}

func (h *hub) jobEndedByJob(jobID string, o steps.Outcome) {
	h.mu.Lock()
	ls := h.byJob[jobID]
	h.mu.Unlock()
	h.notify(ls, o)
}

func (h *hub) stop() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if !h.stopped {
		h.stopped = true
		close(h.closing)
	}
	for id, w := range h.parked {
		w.finish()
		delete(h.parked, id)
	}
}

func (h *hub) closingCh() <-chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	return h.closing
}

// Close ends every live socket (1001): call it when the control plane stops, before the HTTP server's shutdown (which
// does not wait for hijacked connections).
func (s *Service) Close() { s.hub.stop() }

// Connected reports whether session id has its browser on a socket in this process.
func (s *Service) Connected(id string) bool { return s.hub.has(id) }

// ---------------------------------------------------------------- messages

// msgType reads a JSON message's "type" without decoding the rest: the relay stays a byte pump. Messages lead with
// type (both ends write it first); any other is decoded.
func msgType(b []byte) string {
	head := b[:min(len(b), 64)]
	for i := 0; i+6 <= len(head); i++ {
		if string(head[i:i+6]) != `"type"` {
			continue
		}
		j := i + 6
		for j < len(head) && (head[j] == ' ' || head[j] == ':' || head[j] == '\t') {
			j++
		}
		if j >= len(head) || head[j] != '"' {
			break
		}
		k := j + 1
		for k < len(head) && head[k] != '"' {
			k++
		}
		if k < len(head) {
			return string(head[j+1 : k])
		}
		break
	}
	var m struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(b, &m)
	return m.Type
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// errorMessage is a LiveError carrying a problem.
func errorMessage(err *problems.Error, fatal bool) []byte {
	return mustJSON(map[string]any{"type": "error", "problem": err.Body(), "fatal": fatal})
}

type hist struct {
	mu sync.Mutex
	us []float64
}

func (h *hist) add(d time.Duration) {
	h.mu.Lock()
	h.us = append(h.us, float64(d.Microseconds()))
	h.mu.Unlock()
}

func (h *hist) stats() (p50, p95 float64, n int) {
	h.mu.Lock()
	s := slices.Clone(h.us)
	h.mu.Unlock()
	if len(s) == 0 {
		return 0, 0, 0
	}
	slices.Sort(s)
	return s[int(0.5*float64(len(s)-1))], s[int(0.95*float64(len(s)-1))], len(s)
}

func closeNote(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		return fmt.Sprintf("closed %d", s)
	}
	return err.Error()
}

// ---------------------------------------------------------------- the browser's socket

// ServeClient checks the ticket (single-use, userID's) and upgrades the browser's request, then relays the session
// until it ends. The caller checked the Origin. Before the upgrade a failure is returned as a problem.
func (s *Service) ServeClient(w http.ResponseWriter, r *http.Request, id, ticket, userID string) error {
	var jobID string
	if err := s.Pool.QueryRow(r.Context(), "SELECT coalesce(job_id, '') FROM transcriptions WHERE id = $1", id).Scan(&jobID); err != nil {
		return problems.TranscriptionTicketInvalid.New("no transcription session %s", id)
	}
	ls := &liveSession{id: id, jobID: jobID, userID: userID, worker: make(chan *workerConn, 1), ended: make(chan steps.Outcome, 1)}
	// The session is in the hub before its ticket is used, so the sweeper never takes a used ticket without a socket
	// for an orphan.
	if err := s.hub.register(ls); err != nil {
		return err
	}
	c, err := s.claimTicket(r.Context(), id, ticket, userID)
	if err != nil {
		s.hub.unregister(ls)
		return err
	}
	ls.projectID, ls.reservationMB = c.projectID, c.reservationMB
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	ctx := context.WithoutCancel(r.Context())
	if err != nil {
		s.hub.unregister(ls)
		s.endRecord(ctx, id, "the socket upgrade failed")
		s.cancelJob(ctx, jobID)
		return nil // Accept answered the request
	}
	conn.SetReadLimit(int64(s.defaults().Transcriptions.MaxMessageKB.Value) << 10)
	s.relay(ctx, ls, conn)
	return nil
}

// endRecord notes why the relay ended (the job's end closes the record).
func (s *Service) endRecord(ctx context.Context, id, reason string) {
	if _, err := s.Pool.Exec(ctx, "UPDATE transcriptions SET end_reason = coalesce(end_reason, $2) WHERE id = $1", id, truncate(reason, 500)); err != nil {
		s.log().WarnContext(ctx, "record a session's end", "session", id, "err", err)
	}
}

// relayEnd is how a relay ended: the close code and reason sent to the browser, the record's note, and whether the
// worker sent its summary (then it releases its lease itself).
type relayEnd struct {
	code    websocket.StatusCode
	reason  string
	note    string
	summary bool
}

// conversation is one relayed session's shared state.
type conversation struct {
	s         *Service
	ls        *liveSession
	client    *websocket.Conn
	t         Timing
	upQ       chan frame
	clientErr chan error
	activity  chan struct{}
	paired    atomic.Bool
	up, down  hist
}

func (cv *conversation) send(ctx context.Context, b []byte) error {
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return cv.client.Write(wctx, websocket.MessageText, b)
}

// read takes the browser's frames: ping is answered here; everything else queues for the worker. Until the worker
// joins the queue blocks the reader (a file uploads while the model loads); once live, a queue full for longer than
// the backpressure wait ends the session (the worker fell behind).
func (cv *conversation) read(ctx context.Context) {
	for {
		typ, b, err := cv.client.Read(ctx)
		if err != nil {
			cv.clientErr <- err
			return
		}
		select {
		case cv.activity <- struct{}{}:
		default:
		}
		f := frame{typ: typ, data: b, at: time.Now()}
		if typ == websocket.MessageText && msgType(b) == "ping" {
			var m struct {
				T *float64 `json:"t"`
			}
			_ = json.Unmarshal(b, &m)
			pong := map[string]any{"type": "pong", "source": "relay"}
			if m.T != nil {
				pong["t"] = *m.T
			}
			_ = cv.send(ctx, mustJSON(pong))
			continue
		}
		if !cv.paired.Load() {
			select {
			case cv.upQ <- f:
			case <-ctx.Done():
				return
			}
			continue
		}
		select {
		case cv.upQ <- f:
			continue
		default:
		}
		timer := time.NewTimer(cv.t.Backpressure)
		select {
		case cv.upQ <- f:
			timer.Stop()
		case <-timer.C:
			cv.clientErr <- errBackpressure
			return
		case <-ctx.Done():
			timer.Stop()
			return
		}
	}
}

func (s *Service) relay(ctx context.Context, ls *liveSession, client *websocket.Conn) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer s.hub.unregister(ls)
	cv := &conversation{s: s, ls: ls, client: client, t: s.timing(), clientErr: make(chan error, 1), activity: make(chan struct{}, 1),
		upQ: make(chan frame, max(s.defaults().Transcriptions.RelayQueueMessages.Value, 1))}
	go cv.read(ctx)
	w, end := cv.wait(ctx)
	if end == nil {
		cv.paired.Store(true)
		end = cv.pump(ctx, w)
	}
	if w != nil {
		w.finish()
	}
	s.hub.unregister(ls)
	select { // a worker socket handed over after the session ended
	case extra := <-ls.worker:
		extra.finish()
	default:
	}
	p50u, p95u, nu := cv.up.stats()
	p50d, p95d, nd := cv.down.stats()
	s.log().InfoContext(ctx, "live session closed", "session", ls.id, "job", ls.jobID, "code", int(end.code), "why", end.note,
		"upP50Us", p50u, "upP95Us", p95u, "upN", nu, "downP50Us", p50d, "downP95Us", p95d, "downN", nd)
	s.endRecord(ctx, ls.id, end.note)
	_ = client.Close(end.code, end.reason)
	if !end.summary {
		s.cancelJob(ctx, ls.jobID)
		return
	}
	// The worker sent its summary and releases its lease itself; a job still running after a grace is cancelled.
	go func() {
		timer := time.NewTimer(cv.t.Drain)
		defer timer.Stop()
		<-timer.C
		s.cancelJob(context.WithoutCancel(ctx), ls.jobID)
	}()
}

// wait reports the session's place until its worker joins, or the session ends first.
func (cv *conversation) wait(ctx context.Context) (*workerConn, *relayEnd) {
	s, ls := cv.s, cv.ls
	deadline := time.NewTimer(cv.t.QueueWait)
	defer deadline.Stop()
	poll := time.NewTicker(cv.t.Poll)
	defer poll.Stop()
	started := time.Now()
	ended := func(o steps.Outcome) *relayEnd {
		pe := problems.TranscriptionLimit.New("the session's job ended before it started: %s", endReason(o))
		if o.Error != nil && o.Error.Type == steps.ErrInput {
			pe = problems.TranscriptionInputInvalid.New("the worker could not start the session: %s", o.Error.Message)
		}
		_ = cv.send(ctx, errorMessage(pe, true))
		return &relayEnd{code: CloseNotStarted, reason: "the job ended", note: "the job ended before the session started: " + endReason(o)}
	}
	report := func() *relayEnd {
		state, pos, reason, err := s.place(ctx, s.Pool, ls.jobID, ls.reservationMB)
		if err != nil {
			s.log().WarnContext(ctx, "read a session's place", "session", ls.id, "err", err)
			return nil
		}
		if state == StateEnded {
			o := steps.Outcome{State: steps.StateFailed}
			var raw []byte
			if err := s.Pool.QueryRow(ctx, "SELECT outcome FROM step_jobs WHERE job_id = $1", ls.jobID).Scan(&raw); err == nil && raw != nil {
				_ = json.Unmarshal(raw, &o)
			}
			return ended(o)
		}
		m := map[string]any{"type": "waiting", "state": state, "waitedS": round3(time.Since(started).Seconds())}
		if state == StateQueued {
			m["position"], m["reason"], m["reservationMb"] = pos, reason, ls.reservationMB
		}
		if err := cv.send(ctx, mustJSON(m)); err != nil {
			return &relayEnd{code: websocket.StatusGoingAway, reason: "client gone", note: "the page left while waiting"}
		}
		return nil
	}
	if e := report(); e != nil {
		return nil, e
	}
	closing := s.hub.closingCh()
	for {
		select {
		case w := <-ls.worker:
			if w.parked && !w.alive(ctx) {
				w.finish() // a stale socket (its worker died): wait for the next dial
				continue
			}
			if _, err := s.Pool.Exec(ctx, `UPDATE transcriptions SET state = 'live', started_at = now() WHERE id = $1 AND state <> 'ended'`,
				ls.id); err != nil {
				s.log().WarnContext(ctx, "record a live session", "session", ls.id, "err", err)
			}
			return w, nil
		case o := <-ls.ended:
			return nil, ended(o)
		case err := <-cv.clientErr:
			return nil, &relayEnd{code: websocket.StatusNormalClosure, reason: "client gone", note: "the page left while waiting: " + closeNote(err)}
		case <-deadline.C:
			pe := problems.TranscriptionLimit.New("no card took the session within %s (transcriptions.queue_wait_minutes); try again later", cv.t.QueueWait)
			_ = cv.send(ctx, errorMessage(pe, true))
			return nil, &relayEnd{code: CloseNotStarted, reason: "queue wait limit", note: "the queue wait limit passed"}
		case <-poll.C:
			if e := report(); e != nil {
				return nil, e
			}
		case <-closing:
			return nil, &relayEnd{code: websocket.StatusGoingAway, reason: "the control plane is stopping", note: "the control plane stopped"}
		}
	}
}

// pump relays between the browser and the worker until the summary, a limit or a lost peer.
func (cv *conversation) pump(ctx context.Context, w *workerConn) *relayEnd {
	s, ls, t := cv.s, cv.ls, cv.t
	sessionCap := t.Cap
	if a, err := s.allowance(ctx, s.Pool, ls.projectID); err == nil {
		// What the allowance has left now (this session's loading time included) bounds the session.
		if left := time.Duration(a.RemainingGPUHours * float64(time.Hour)); left > 0 && left < sessionCap {
			sessionCap = left
		}
	}
	workerErr := make(chan error, 1)
	go func() { // queue → worker
		for {
			select {
			case f := <-cv.upQ:
				wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
				err := w.c.Write(wctx, f.typ, f.data)
				wcancel()
				if err != nil {
					workerErr <- err
					return
				}
				cv.up.add(time.Since(f.at))
			case <-ctx.Done():
				return
			case <-w.done:
				return
			}
		}
	}()
	injectEnd := func() {
		wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
		defer wcancel()
		_ = w.c.Write(wctx, websocket.MessageText, []byte(`{"type":"end"}`))
	}
	relayStats := func() []byte {
		p50u, p95u, nu := cv.up.stats()
		p50d, p95d, nd := cv.down.stats()
		return mustJSON(map[string]any{"type": "stats", "source": "relay", "queued": len(cv.upQ),
			"upP50Us": p50u, "upP95Us": p95u, "upN": nu, "downP50Us": p50d, "downP95Us": p95d, "downN": nd})
	}
	idle := time.NewTimer(t.Idle)
	defer idle.Stop()
	capTimer := time.NewTimer(sessionCap)
	defer capTimer.Stop()
	statsTick := time.NewTicker(5 * time.Second)
	defer statsTick.Stop()
	var (
		drain   <-chan time.Time
		pending *relayEnd // the end a limit chose: it applies once the summary came or the drain passed
	)
	limit := func(code websocket.StatusCode, why string, pe *problems.Error) {
		if pending != nil {
			return
		}
		_ = cv.send(ctx, errorMessage(pe, true))
		pending = &relayEnd{code: code, reason: why, note: why}
		injectEnd()
		drain = time.After(t.Drain)
	}
	closing := s.hub.closingCh()
	for {
		select {
		case f, ok := <-w.down:
			if !ok {
				if pending != nil {
					return pending
				}
				_ = cv.send(ctx, errorMessage(problems.Internal.New("the worker's socket closed before the session's summary"), true))
				return &relayEnd{code: CloseWorkerLost, reason: "worker lost", note: "the worker's socket closed"}
			}
			summary := f.typ == websocket.MessageText && msgType(f.data) == "summary"
			if summary {
				_ = cv.send(ctx, relayStats())
			}
			wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
			err := cv.client.Write(wctx, f.typ, f.data)
			wcancel()
			if err != nil {
				injectEnd()
				return &relayEnd{code: websocket.StatusGoingAway, reason: "client gone", note: "writing to the page failed"}
			}
			cv.down.add(time.Since(f.at))
			if summary {
				if pending != nil {
					pending.summary = true
					return pending
				}
				return &relayEnd{code: websocket.StatusNormalClosure, reason: "summary", note: "done", summary: true}
			}
		case err := <-cv.clientErr:
			if errors.Is(err, errBackpressure) {
				pe := problems.TranscriptionLimit.New("the worker fell behind the audio for %s; the session closes", t.Backpressure)
				_ = cv.send(ctx, errorMessage(pe, true))
				injectEnd()
				return &relayEnd{code: websocket.StatusTryAgainLater, reason: "upstream queue full", note: "backpressure"}
			}
			injectEnd()
			return &relayEnd{code: websocket.StatusNormalClosure, reason: "client gone", note: "the page left: " + closeNote(err)}
		case err := <-workerErr:
			_ = cv.send(ctx, errorMessage(problems.Internal.New("writing to the worker failed: %v", err), true))
			return &relayEnd{code: CloseWorkerLost, reason: "worker lost", note: "writing to the worker failed"}
		case o := <-ls.ended:
			if pending != nil {
				return pending
			}
			_ = cv.send(ctx, errorMessage(problems.Internal.New("the worker's job ended during the session: %s", endReason(o)), true))
			return &relayEnd{code: CloseWorkerLost, reason: "worker lost", note: "the job ended: " + endReason(o)}
		case <-cv.activity:
			if !idle.Stop() {
				select {
				case <-idle.C:
				default:
				}
			}
			idle.Reset(t.Idle)
		case <-idle.C:
			limit(CloseIdle, "idle", problems.TranscriptionLimit.New("no audio, keepalive or ping for %s (transcriptions.idle_minutes); the session ends", t.Idle))
		case <-capTimer.C:
			limit(CloseCap, "session cap", problems.TranscriptionLimit.New(
				"the session reached its %s limit (transcriptions.session_max_minutes, or what the project's manual-test allowance had left)",
				sessionCap.Round(time.Second)))
		case <-drain:
			return pending
		case <-statsTick.C:
			_ = cv.send(ctx, relayStats())
		case <-closing:
			injectEnd()
			return &relayEnd{code: websocket.StatusGoingAway, reason: "the control plane is stopping", note: "the control plane stopped"}
		}
	}
}

// ---------------------------------------------------------------- the worker's socket

// ServeWorker upgrades a worker's dial for job jobID (already authenticated) and hands the socket to the job's
// session; it returns when the session is finished with it, or when no browser joined within the dial timeout.
func (s *Service) ServeWorker(w http.ResponseWriter, r *http.Request, jobID string) error {
	if _, err := s.SessionOfJob(r.Context(), jobID); err != nil {
		return err
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return nil // Accept answered the request
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	wc := newWorkerConn(ctx, c)
	if err := s.hub.attachWorker(jobID, wc); err != nil {
		pe, _ := problems.As(err)
		_ = c.Close(websocket.StatusPolicyViolation, pe.Detail)
		return nil
	}
	timer := time.NewTimer(s.timing().WorkerDial)
	defer timer.Stop()
	select {
	case <-wc.done:
	case <-timer.C:
		if s.hub.unpark(jobID, wc) {
			_ = c.Close(CloseNotStarted, "no browser joined the session")
			return nil
		}
	case <-s.hub.closingCh():
		if s.hub.unpark(jobID, wc) {
			_ = c.Close(websocket.StatusGoingAway, "the control plane is stopping")
			return nil
		}
	}
	<-wc.done
	_ = c.Close(websocket.StatusNormalClosure, "session finished")
	return nil
}
