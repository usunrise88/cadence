//go:build integration

package server

import "testing"

// TestClaimLooksPastJobsThatDoNotFit: more waiting jobs than one page that cannot fit the worker's card do not hide
// a job further down the queue that can.
func TestClaimLooksPastJobsThatDoNotFit(t *testing.T) {
	w := startWorkers(t)
	f := w.register(w.workerToken("staging"), "toy", map[string]any{"train_toy": kind("1", "training", true, false)})
	big := gpuSpec("train_toy")
	big.Priority, big.Resources.MemoryGB = 10, 100 // over the card's 29 GB cap
	for range 55 {
		w.enqueue(big)
	}
	small := gpuSpec("train_toy")
	small.Resources.MemoryGB = 10
	want := w.enqueue(small)
	l := f.claim(0)
	if l == nil || l.JobID != want {
		t.Fatalf("lease %+v, want job %s", l, want)
	}
	if n := w.count("SELECT count(*) FROM step_jobs WHERE state = 'waiting'"); n != 55 {
		t.Fatalf("%d jobs waiting, want the 55 that do not fit", n)
	}
}
