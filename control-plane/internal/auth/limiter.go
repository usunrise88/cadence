package auth

import (
	"sync"
	"time"
)

// Limit is at most N events per window Per.
type Limit struct {
	N   int
	Per time.Duration
}

// DefaultLoginLimits allow 5 failed sign-ins a minute and 20 an hour, per address and per username
// (Cadence recommendation; OWASP Authentication Cheat Sheet, "Login throttling").
func DefaultLoginLimits() []Limit {
	return []Limit{{N: 5, Per: time.Minute}, {N: 20, Per: time.Hour}}
}

// Limiter counts events per key in sliding windows, in memory. One control plane serves one instance, so there is
// nothing to share across processes; a restart forgets the counts, which is acceptable for login throttling.
type Limiter struct {
	mu     sync.Mutex
	limits []Limit
	window time.Duration // the longest Per
	now    func() time.Time
	hits   map[string][]time.Time
}

// NewLimiter returns a limiter enforcing every limit; now is the clock (time.Now in production).
func NewLimiter(now func() time.Time, limits ...Limit) *Limiter {
	l := &Limiter{limits: limits, now: now, hits: map[string][]time.Time{}}
	for _, lim := range limits {
		l.window = max(l.window, lim.Per)
	}
	return l
}

// Check reports whether key is under every limit; when it is not, retry is how long until it is.
func (l *Limiter) Check(key string) (retry time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	hits := l.prune(key, now)
	for _, lim := range l.limits {
		// hits is oldest first; w is the part inside this window.
		first := len(hits)
		for first > 0 && now.Sub(hits[first-1]) < lim.Per {
			first--
		}
		w := hits[first:]
		if n := len(w); n >= lim.N {
			// Under the limit again once the n-N+1 oldest hits in the window have left it.
			retry = max(retry, lim.Per-now.Sub(w[n-lim.N]))
		}
	}
	return retry, retry == 0
}

// Record counts one event for key now.
func (l *Limiter) Record(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.hits[key] = append(l.prune(key, now), now)
	if len(l.hits) > 4096 {
		for k := range l.hits {
			l.prune(k, now)
		}
	}
}

// prune drops hits of key older than the longest window and returns the rest; call with mu held.
func (l *Limiter) prune(key string, now time.Time) []time.Time {
	hits := l.hits[key]
	i := 0
	for i < len(hits) && now.Sub(hits[i]) >= l.window {
		i++
	}
	hits = hits[i:]
	if len(hits) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = hits
	return hits
}
