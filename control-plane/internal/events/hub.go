package events

import "sync"

// Hub fans committed events out to live subscribers. Publish never blocks: a subscriber whose buffer is full is
// dropped (its channel closed) and reconnects with Last-Event-ID, catching up from the table.
type Hub struct {
	mu     sync.Mutex
	subs   map[*Subscription]struct{}
	buffer int
	closed bool
}

// Subscription receives events in seq order until C is closed (hub closed or subscriber too slow).
type Subscription struct {
	C   <-chan Record
	c   chan Record
	hub *Hub
}

// NewHub returns a hub whose subscribers buffer up to buffer events.
func NewHub(buffer int) *Hub {
	return &Hub{subs: map[*Subscription]struct{}{}, buffer: buffer}
}

// Subscribe registers a subscriber. On a closed hub the subscription's channel is already closed.
func (h *Hub) Subscribe() *Subscription {
	c := make(chan Record, h.buffer)
	s := &Subscription{C: c, c: c, hub: h}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(c)
		return s
	}
	h.subs[s] = struct{}{}
	return s
}

// Close unregisters s; safe to call more than once.
func (s *Subscription) Close() {
	h := s.hub
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		close(s.c)
	}
}

// Publish delivers r to every subscriber, dropping those that cannot keep up.
func (h *Hub) Publish(r Record) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.c <- r:
		default:
			delete(h.subs, s)
			close(s.c)
		}
	}
}

// Close ends every subscription and refuses new ones; used at shutdown so streams return.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for s := range h.subs {
		delete(h.subs, s)
		close(s.c)
	}
}
