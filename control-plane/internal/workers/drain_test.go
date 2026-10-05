package workers

import (
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/queue"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// A waiting benchmark drains the cards that accept benchmarks (06 "Exclusive benchmarks"): jobs behind it in start
// order leave them alone, jobs ahead of it (and the benchmark itself) do not.
func TestPlaceDrainsForABenchmark(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	t0 := now.Add(-time.Minute)
	cards := func(kinds ...string) []queue.Card {
		return []queue.Card{{Config: compute.Card{Index: 0, MemoryCapGB: 24, AllowedJobKinds: kinds}, Held: []queue.Held{{JobKind: "training", MemoryMB: 8192}}}}
	}
	bench := queueKey{projectPriority: 0, jobPriority: 0, enqueuedAt: t0, jobID: "job_bench"}
	eval := func(key queueKey) candidate {
		return candidate{jobID: key.jobID, key: key, spec: steps.Spec{Resources: steps.Resources{GPU: true, MemoryGB: 4, JobKind: "eval"}}}
	}
	behind := queueKey{enqueuedAt: t0.Add(time.Second), jobID: "job_later"}
	ahead := queueKey{jobPriority: 5, enqueuedAt: t0.Add(time.Second), jobID: "job_urgent"}
	tests := []struct {
		name  string
		cand  candidate
		cards []queue.Card
		drain *queueKey
		want  bool
	}{
		{"no benchmark waits", eval(behind), cards("eval", "benchmark"), nil, true},
		{"behind a waiting benchmark", eval(behind), cards("eval", "benchmark"), &bench, false},
		{"ahead of it by priority", eval(ahead), cards("eval", "benchmark"), &bench, true},
		{"a card that takes no benchmarks does not drain", eval(behind), cards("eval"), &bench, true},
		{"a step without a card is never held", candidate{key: behind, spec: steps.Spec{}}, cards("eval", "benchmark"), &bench, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, ok := place(tt.cand, tt.cards, now, tt.drain); ok != tt.want {
				t.Fatalf("placed %v, want %v", ok, tt.want)
			}
		})
	}
	if !bench.before(behind) || !ahead.before(behind) || bench.before(ahead) {
		t.Fatal("start order")
	}
}
