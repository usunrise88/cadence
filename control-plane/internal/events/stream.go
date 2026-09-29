package events

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

const replayBatch = 500

// Streamer serves the event stream to SSE clients.
type Streamer struct {
	db        storage.Querier
	hub       *Hub
	log       *slog.Logger
	clients   prometheus.Gauge
	Heartbeat time.Duration // comment line sent when idle, default 15 s
	WriteWait time.Duration // per-write deadline for stuck clients, default 30 s
}

// NewStreamer returns a streamer reading history from db and live events from hub.
func NewStreamer(db storage.Querier, hub *Hub, log *slog.Logger, clients prometheus.Gauge) *Streamer {
	return &Streamer{db: db, hub: hub, log: log, clients: clients, Heartbeat: 15 * time.Second, WriteWait: 30 * time.Second}
}

// Serve streams events passing f until the client leaves, the hub drops it or the hub closes. With resume it
// first replays events with seq > after from the table; without it the stream starts at the newest event.
//
// Handover is race-free: the subscription is taken before the replay query, so every event committed after the
// query's snapshot is in the subscription; events the replay already sent are skipped by seq. A missing one would
// need a commit that is neither in the snapshot nor published after subscribing, which the outbox lock rules out.
func (s *Streamer) Serve(w http.ResponseWriter, r *http.Request, f Filter, after int64, resume bool) {
	ctx := r.Context()
	sub := s.hub.Subscribe()
	defer sub.Close()
	s.clients.Inc()
	defer s.clients.Dec()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	out := &sseWriter{w: w, rc: http.NewResponseController(w), wait: s.WriteWait}
	if err := out.raw("retry: 3000\n\n"); err != nil {
		return
	}

	last := after
	if !resume {
		// Live only: start at the head as of now, so the stream does not depend on how far the dispatcher is.
		if err := s.db.QueryRow(ctx, "SELECT coalesce(max(seq), 0) FROM events").Scan(&last); err != nil {
			s.log.WarnContext(ctx, "read event head failed", "err", err)
			return
		}
	}
	if resume {
		for {
			batch, err := List(ctx, s.db, f, last, replayBatch)
			if err != nil {
				s.log.WarnContext(ctx, "event replay failed", "err", err)
				return
			}
			for _, rec := range batch {
				if err := out.event(rec); err != nil {
					return
				}
				last = rec.Seq
			}
			if len(batch) < replayBatch {
				break
			}
		}
	}

	ping := time.NewTicker(s.Heartbeat)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if err := out.raw(": ping\n\n"); err != nil {
				return
			}
		case rec, ok := <-sub.C:
			if !ok {
				return // dropped as too slow, or shutting down: the client resumes with Last-Event-ID
			}
			if rec.Seq <= last {
				continue
			}
			last = rec.Seq
			if !f.Match(rec) {
				continue
			}
			if err := out.event(rec); err != nil {
				return
			}
		}
	}
}

type sseWriter struct {
	w    http.ResponseWriter
	rc   *http.ResponseController
	wait time.Duration
}

func (o *sseWriter) raw(s string) error {
	_ = o.rc.SetWriteDeadline(time.Now().Add(o.wait)) // unsupported by some test recorders; best effort
	if _, err := o.w.Write([]byte(s)); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := o.rc.Flush(); err != nil {
		return fmt.Errorf("flush: %w", err)
	}
	return nil
}

func (o *sseWriter) event(r Record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal event %d: %w", r.Seq, err)
	}
	// No "event:" line: browsers deliver named events only to listeners registered per name, so a client would
	// miss types it does not know. The type is inside the JSON.
	return o.raw(fmt.Sprintf("id: %d\ndata: %s\n\n", r.Seq, data))
}
