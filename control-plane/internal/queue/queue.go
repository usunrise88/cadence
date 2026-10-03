// Package queue decides where a step job may run: which card of a host has room for it under the card's memory
// cap, whether the card accepts its job kind, and whether its availability window fits its estimate (R19). It is
// pure: the worker protocol (internal/workers) loads the cards, their active leases and the waiting jobs in a
// transaction, asks Fit, and records the lease.
//
// The rules (docs/review/2026-09-30-phase-2-plan.md "The step job"):
//   - one training job per card;
//   - an interactive job (a live transcription session, R49) shares a card with training under the cap, but never
//     with a benchmark (R30: latency measured beside a session is meaningless), and a benchmark never joins one;
//   - a job reserves its declared memory, or the card's whole remaining cap when it declares none;
//   - a job fits when its reservation fits under the cap beside the card's other leases;
//   - an idle card must also have the memory free that the reservation needs, by the worker's own telemetry (a
//     resident service outside Cadence may hold part of it);
//   - a job of a kind with availability windows starts only inside a window, and only when its estimate ends before
//     the window closes (an unknown estimate, or a job resuming from a saved training state, starts whenever the
//     window is open: it is paused at the close anyway).
package queue

import (
	"fmt"
	"slices"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/compute"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// TelemetrySlackMB is how far below the reservation an idle card's reported free memory may be and still take the
// job: the cap is set as a fraction of the card beside resident services, and driver bookkeeping makes the free
// figure wobble by a few hundred MB.
const TelemetrySlackMB = 1024

// Held is a lease already on a card.
type Held struct {
	JobKind  string
	MemoryMB int
}

// Card is a card with what it holds now.
type Card struct {
	Config compute.Card
	Held   []Held
	// FreeMB is the free memory the worker last reported (total − used by every process); nil when unknown.
	FreeMB *int
}

// CapMB is the card's memory cap in MB.
func (c Card) CapMB() int { return int(c.Config.MemoryCapGB * 1024) }

// reservedMB is what the card's leases hold.
func (c Card) reservedMB() int {
	n := 0
	for _, h := range c.Held {
		n += h.MemoryMB
	}
	return n
}

// Need is what a step job asks of a card.
type Need struct {
	JobKind         string
	MemoryMB        int // 0: the card's whole remaining cap
	EstimateSeconds *float64
	Resuming        bool // resumes from a training state (after a pause or a window close)
}

// NeedOf reads a step spec.
func NeedOf(s steps.Spec) Need {
	kind := s.Resources.JobKind
	if kind == "" {
		kind = steps.JobTraining
	}
	return Need{
		JobKind: kind, MemoryMB: int(s.Resources.MemoryGB * 1024), EstimateSeconds: s.EstimateSeconds,
		Resuming: s.Overrides.ResumeFrom != "",
	}
}

// Fit reports the memory (MB) the job gets on card c at now — its reservation and the cap handed to the step — or
// why it does not fit.
func Fit(c Card, n Need, now time.Time) (int, string) {
	if !slices.Contains(c.Config.AllowedJobKinds, n.JobKind) {
		return 0, fmt.Sprintf("card %d does not accept %s jobs", c.Config.Index, n.JobKind)
	}
	for _, h := range c.Held {
		switch {
		case n.JobKind == steps.JobTraining && h.JobKind == steps.JobTraining:
			return 0, fmt.Sprintf("card %d already runs a training job", c.Config.Index)
		case n.JobKind == steps.JobInteractive && h.JobKind == steps.JobBenchmark:
			return 0, fmt.Sprintf("card %d runs a benchmark; a live session never shares its card", c.Config.Index)
		case n.JobKind == steps.JobBenchmark && h.JobKind == steps.JobInteractive:
			return 0, fmt.Sprintf("card %d holds a live session; a benchmark never shares its card", c.Config.Index)
		}
	}
	free := c.CapMB() - c.reservedMB()
	want := n.MemoryMB
	if want <= 0 {
		want = free
	}
	if want <= 0 || want > free {
		return 0, fmt.Sprintf("card %d has %d MB left under its cap, the job needs %d MB", c.Config.Index, max(free, 0), n.MemoryMB)
	}
	if len(c.Held) == 0 && c.FreeMB != nil && *c.FreeMB+TelemetrySlackMB < want {
		return 0, fmt.Sprintf("card %d has only %d MB free by its telemetry (other processes hold the rest)", c.Config.Index, *c.FreeMB)
	}
	if reason := WindowFits(c.Config.Windows, n, now); reason != "" {
		return 0, reason
	}
	return want, ""
}

// WindowFits reports why the job may not start now under windows, or "" when it may.
func WindowFits(ws compute.Windows, n Need, now time.Time) string {
	open, always, closes := ws.Availability(n.JobKind, now)
	switch {
	case always:
		return ""
	case !open:
		return fmt.Sprintf("no availability window for %s jobs is open", n.JobKind)
	case n.EstimateSeconds == nil || n.Resuming:
		return ""
	case now.Add(time.Duration(*n.EstimateSeconds * float64(time.Second))).After(closes):
		return fmt.Sprintf("the job's estimate (%.0f s) does not fit before the window closes at %s", *n.EstimateSeconds,
			closes.UTC().Format(time.RFC3339))
	}
	return ""
}

// WindowClosed reports whether a running job of jobKind must stop at now because its card's window closed. Only
// training is stopped at a close (R19: it saves its state and resumes when a window opens); shorter jobs finish.
func WindowClosed(ws compute.Windows, jobKind string, now time.Time) bool {
	if jobKind != steps.JobTraining {
		return false
	}
	open, _, _ := ws.Availability(jobKind, now)
	return !open
}
