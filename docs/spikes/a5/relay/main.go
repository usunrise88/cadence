// A5 throwaway: the control plane's half of R48 — a WebSocket relay between a browser session and a worker `live`
// job. Not product code; it shows the shape phase 3 stream T builds into internal/server.
//
//	POST /api/transcriptions                 -> {id, ticket, streamUrl}; ticket single-use, 60 s
//	GET  /api/transcriptions/{id}/stream     browser socket (?ticket=, Origin allow-list)
//	GET  /worker/live/{jobId}                worker dials out (Bearer token); one job serves one session
//	GET  /                                   static files (the capture page for the headless browser test)
//
// Frames are forwarded unchanged in both directions. The relay enforces the R48 limits (frame size, a bounded
// upstream queue = backpressure, idle and session caps) and measures its own forwarding time per message, which it
// reports in a `stats` message (source "relay") before the worker's `summary`. `{"type":"ping"}` is answered by the
// relay itself, so a client can separate the relay hop from the worker hop.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

var (
	listen      = flag.String("listen", "127.0.0.1:18480", "listen address")
	static      = flag.String("static", "", "directory served at /")
	origins     = flag.String("origins", "http://127.0.0.1:18480,http://localhost:18480", "allowed Origin values")
	workerToken = flag.String("worker-token", "cwk_a5spike", "token a worker presents")
	maxFrame    = flag.Int64("max-frame", 64<<10, "largest message accepted from the browser")
	queueLen    = flag.Int("queue", 64, "upstream queue (80 ms frames) before the relay closes with 1013")
	idle        = flag.Duration("idle", 5*time.Minute, "close after this long without audio or keepalive")
	sessionCap  = flag.Duration("session-cap", 15*time.Minute, "longest session")
)

type session struct {
	id      string
	ticket  string
	expires time.Time
	used    bool
}

type relay struct {
	mu       sync.Mutex
	sessions map[string]*session
	workers  chan jobConn // idle worker jobs waiting for a session
}

type jobConn struct {
	c    *websocket.Conn
	done chan struct{} // closed when the session that took the job ends
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

func (r *relay) create(w http.ResponseWriter, req *http.Request) {
	s := &session{id: newID("trs_"), ticket: newID("tkt_"), expires: time.Now().Add(60 * time.Second)}
	r.mu.Lock()
	r.sessions[s.id] = s
	r.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"id": s.id, "ticket": s.ticket,
		"streamUrl": "/api/transcriptions/" + s.id + "/stream?ticket=" + url.QueryEscape(s.ticket),
	})
}

func (r *relay) claim(id, ticket string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.sessions[id]
	switch {
	case s == nil:
		return errors.New("unknown session")
	case s.used:
		return errors.New("ticket already used")
	case time.Now().After(s.expires):
		return errors.New("ticket expired")
	case s.ticket != ticket:
		return errors.New("wrong ticket")
	}
	s.used = true
	return nil
}

func (r *relay) worker(w http.ResponseWriter, req *http.Request) {
	if req.Header.Get("Authorization") != "Bearer "+*workerToken {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	c, err := websocket.Accept(w, req, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	log.Printf("worker job %s connected", strings.TrimPrefix(req.URL.Path, "/worker/live/"))
	j := jobConn{c: c, done: make(chan struct{})}
	select {
	case r.workers <- j:
	case <-req.Context().Done():
		return
	}
	<-j.done // the session goroutine owns the connection until then
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

func (h *hist) pct(p float64) float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.us) == 0 {
		return 0
	}
	s := slices.Clone(h.us)
	slices.Sort(s)
	return s[int(p*float64(len(s)-1))]
}

// msgType reads only the leading "type" of a JSON message (every R48 message starts with it), without decoding the
// rest: the relay stays a byte pump.
func msgType(b []byte) string {
	s := string(b[:min(len(b), 40)])
	i := strings.Index(s, `"type"`)
	if i < 0 {
		return ""
	}
	s = strings.TrimLeft(s[i+6:], ": ")
	if !strings.HasPrefix(s, `"`) {
		return ""
	}
	s = s[1:]
	if j := strings.Index(s, `"`); j >= 0 {
		return s[:j]
	}
	return ""
}

func (r *relay) stream(w http.ResponseWriter, req *http.Request) {
	parts := strings.Split(strings.Trim(req.URL.Path, "/"), "/") // api transcriptions {id} stream
	if len(parts) != 4 {
		http.NotFound(w, req)
		return
	}
	if o := req.Header.Get("Origin"); !slices.Contains(strings.Split(*origins, ","), o) {
		http.Error(w, "origin not allowed: "+o, http.StatusForbidden)
		return
	}
	if err := r.claim(parts[2], req.URL.Query().Get("ticket")); err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	client, err := websocket.Accept(w, req, &websocket.AcceptOptions{InsecureSkipVerify: true}) // Origin checked above
	if err != nil {
		return
	}
	client.SetReadLimit(*maxFrame)
	var j jobConn
	select {
	case j = <-r.workers:
	case <-time.After(10 * time.Second):
		_ = client.Close(websocket.StatusTryAgainLater, "no live job available")
		return
	}
	job := j.c
	defer close(j.done)
	log.Printf("session %s paired", parts[2])
	ctx, cancel := context.WithTimeout(context.Background(), *sessionCap)
	defer cancel()
	var up, down hist
	type msg struct {
		t    websocket.MessageType
		b    []byte
		recv time.Time
	}
	queue := make(chan msg, *queueLen)
	activity := make(chan struct{}, 1)
	done := make(chan string, 3)

	go func() { // browser -> queue
		for {
			t, b, err := client.Read(ctx)
			if err != nil {
				done <- "client: " + err.Error()
				return
			}
			select {
			case activity <- struct{}{}:
			default:
			}
			now := time.Now()
			if t == websocket.MessageText && msgType(b) == "ping" {
				_ = client.Write(ctx, websocket.MessageText, []byte(strings.Replace(string(b), `"ping"`, `"pong","source":"relay"`, 1)))
				continue
			}
			select {
			case queue <- msg{t, b, now}:
			default:
				_ = client.Close(websocket.StatusTryAgainLater, "upstream queue full")
				done <- "backpressure"
				return
			}
		}
	}()
	go func() { // queue -> worker
		for m := range queue {
			if err := job.Write(ctx, m.t, m.b); err != nil {
				done <- "job write: " + err.Error()
				return
			}
			up.add(time.Since(m.recv))
		}
	}()
	go func() { // worker -> browser
		for {
			t, b, err := job.Read(ctx)
			if err != nil {
				done <- "job: " + err.Error()
				return
			}
			now := time.Now()
			summary := t == websocket.MessageText && msgType(b) == "summary"
			if summary {
				st, _ := json.Marshal(map[string]any{
					"type": "stats", "source": "relay",
					"upP50Us": up.pct(0.5), "upP95Us": up.pct(0.95), "upN": len(up.us),
					"downP50Us": down.pct(0.5), "downP95Us": down.pct(0.95), "downN": len(down.us),
				})
				_ = client.Write(ctx, websocket.MessageText, st)
			}
			if err := client.Write(ctx, t, b); err != nil {
				done <- "client write: " + err.Error()
				return
			}
			down.add(time.Since(now))
			if summary {
				done <- "summary"
				return
			}
		}
	}()
	timer := time.NewTimer(*idle)
	var why string
loop:
	for {
		select {
		case why = <-done:
			break loop
		case <-activity:
			timer.Reset(*idle)
		case <-timer.C:
			why = "idle"
			break loop
		case <-ctx.Done():
			why = "session cap"
			break loop
		}
	}
	log.Printf("session %s closing: %s (up p50 %.0f us p95 %.0f us n %d; down p50 %.0f us p95 %.0f us n %d)",
		parts[2], why, up.pct(.5), up.pct(.95), len(up.us), down.pct(.5), down.pct(.95), len(down.us))
	_ = client.Close(websocket.StatusNormalClosure, why)
	_ = job.Close(websocket.StatusNormalClosure, why)
}

func main() {
	flag.Parse()
	r := &relay{sessions: map[string]*session{}, workers: make(chan jobConn)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/transcriptions", r.create)
	mux.HandleFunc("GET /api/transcriptions/{id}/stream", r.stream)
	mux.HandleFunc("GET /worker/live/{job}", r.worker)
	if *static != "" {
		mux.Handle("GET /", http.FileServer(http.Dir(*static)))
	}
	log.Printf("relay on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
