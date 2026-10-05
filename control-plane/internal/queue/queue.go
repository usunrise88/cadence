// Package queue decides where a step job may run: which card of a host has room for it under the card's memory
// cap, whether the card accepts its job kind, and whether its availability window fits its estimate (R19). It is
// pure: the worker protocol (internal/workers) loads the cards, their active leases and the waiting jobs in a
// transaction, asks Fit, and records the lease.
//
// The rules (docs/review/2026-09-30-phase-2-plan.md "The step job"; docs/spec/06-platform.md "Staging serving"):
//   - one training job per card;
//   - an interactive job (a live transcription session, R49) shares a card with training under the cap, but never
//     with a benchmark (R30: latency measured beside a session is meaningless), and a benchmark never joins one;
//   - a job reserves its declared memory, or the card's whole remaining cap when it declares none;
//   - a job fits when its reservation fits under the cap beside the card's other leases;
//   - a card's serving reserve (Compute servingReserveGb, R30) is a share of its cap that training and other work
//     leave alone: served models, shadow replay and live sessions take their memory from it first (and from the rest
//     of the cap when it is full); every other job fits under the cap minus the reserve;
//   - a served model (a step that consumes a deployable, steps.ServedOf) is reserved once per card however many
//     leases use it: a lease of a model the card already holds needs no more memory;
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
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
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
	// ServedModel is the model the lease serves through a staging target ("" for none); its memory counts once per
	// card.
	ServedModel string
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

// ReserveMB is the card's serving reserve in MB, at most its cap.
func (c Card) ReserveMB() int { return min(max(int(c.Config.ServingReserveGB*1024), 0), c.CapMB()) }

// Serving reports whether a job of jobKind (serving model served, "" for none) takes its memory from the serving
// reserve first: served models, shadow replay and live sessions (06 "Staging serving").
func Serving(jobKind, served string) bool {
	return served != "" || jobKind == steps.JobShadow || jobKind == steps.JobInteractive
}

// usage is what the card's leases hold outside the serving reserve's users (general) and inside it (serving), each
// served model counted once.
func (c Card) usage() (general, serving int) {
	seen := map[string]bool{}
	for _, h := range c.Held {
		switch {
		case h.ServedModel != "":
			if !seen[h.ServedModel] {
				seen[h.ServedModel] = true
				serving += h.MemoryMB
			}
		case Serving(h.JobKind, ""):
			serving += h.MemoryMB
		default:
			general += h.MemoryMB
		}
	}
	return general, serving
}

// heldModel is the reservation of served model m on the card, or false when no lease on the card serves it.
func (c Card) heldModel(m string) (int, bool) {
	if m == "" {
		return 0, false
	}
	for _, h := range c.Held {
		if h.ServedModel == m {
			return h.MemoryMB, true
		}
	}
	return 0, false
}

// Need is what a step job asks of a card.
type Need struct {
	JobKind         string
	MemoryMB        int // 0: the card's whole remaining cap
	EstimateSeconds *float64
	Resuming        bool // resumes from a training state (after a pause or a window close)
	// ServedModel is the (first) model the step serves through a staging target ("" for none); MemoryMB is the
	// reservation of every model it serves (serving.model_memory_gb each, unless a deployable states its own).
	ServedModel string
}

// NeedOf reads a step spec.
func NeedOf(s steps.Spec) Need {
	kind := s.Resources.JobKind
	if kind == "" {
		kind = steps.JobTraining
	}
	n := Need{
		JobKind: kind, MemoryMB: int(s.Resources.MemoryGB * 1024), EstimateSeconds: s.EstimateSeconds,
		Resuming: s.Overrides.ResumeFrom != "",
	}
	if sv, ok := steps.ServedOf(s, int(defaults.Get().Serving.ModelMemoryGB.Value*1024)); ok {
		n.ServedModel = sv.Model
		if sv.MemoryMB > 0 {
			n.MemoryMB = sv.MemoryMB
		}
	}
	return n
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
	if mb, ok := c.heldModel(n.ServedModel); ok {
		// The model is on the card already: its memory is reserved once, by the leases that use it.
		if reason := WindowFits(c.Config.Windows, n, now); reason != "" {
			return 0, reason
		}
		return mb, ""
	}
	general, serving := c.usage()
	var free int
	if Serving(n.JobKind, n.ServedModel) {
		free = c.CapMB() - general - serving
	} else {
		// Everything else stays out of the serving reserve, and out of what serving work took beyond it.
		free = c.CapMB() - c.ReserveMB() - general - max(0, serving-c.ReserveMB())
	}
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
