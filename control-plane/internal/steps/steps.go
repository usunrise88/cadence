// Package steps is the seam between the pipeline engine and the worker protocol (docs/review/2026-09-30-phase-2-plan.md
// "The step job"). A pipeline step that runs on a worker is a River job of kind "step" whose args are a Spec; the
// pipeline engine enqueues it and waits for its Outcome through Leases, which the worker protocol implements. Output
// hooks let the domains react to what a step produced (a dataset import registers a dataset version, a training step
// registers checkpoints) in the transaction that marks the step done.
//
// The package holds types and small registries only: no database access and no dependency on the engine or the
// worker protocol, so both build without the other.
package steps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
)

// JobKind is the River job kind of a step that runs on a worker.
const JobKind = "step"

// Job kinds of compute cards (compute.allowed_job_kinds) a step can need.
const (
	JobTraining = "training"
	JobEval     = "eval"
	JobExport   = "export"
	JobData     = "data"
)

// Outcome states.
const (
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Error types a step can fail with.
const (
	ErrOOM       = "oom"       // out of card memory: one automatic retry at 0.75× batch
	ErrStep      = "step"      // the step itself failed
	ErrLost      = "lost"      // the lease was reaped after missed heartbeats
	ErrCancelled = "cancelled" // the job was cancelled
	ErrInput     = "input"     // an input artifact was missing or of the wrong type
)

// OOMBatchScale is the batch scale of the automatic retry after an out-of-memory failure (docs/spec/06-platform.md
// "Operations", Failures).
const OOMBatchScale = 0.75

var hashRe = regexp.MustCompile(`^b3:[0-9a-f]{64}$`)

// ValidHash reports whether h is an artifact hash: "b3:" and 64 lowercase hex digits of BLAKE3-256.
func ValidHash(h string) bool { return hashRe.MatchString(h) }

// ArtifactRef names an artifact in the content store by hash, with its type and neutral metadata (R42).
type ArtifactRef struct {
	Hash string          `json:"hash"`
	Type string          `json:"type"`
	Size int64           `json:"size,omitempty"`
	Meta json.RawMessage `json:"meta,omitempty"`
}

// Resources is what a step needs from a card.
type Resources struct {
	GPU      bool    `json:"gpu,omitempty"`
	GPUs     int     `json:"gpus,omitempty"` // 1 in v1 (R44)
	MemoryGB float64 `json:"memoryGb,omitempty"`
	DiskGB   float64 `json:"diskGb,omitempty"`
	JobKind  string  `json:"jobKind,omitempty"` // training | eval | export | data
}

// Overrides change how one attempt runs.
type Overrides struct {
	BatchScale float64 `json:"batchScale,omitempty"` // OOMBatchScale on the automatic retry
	ResumeFrom string  `json:"resumeFrom,omitempty"` // hash of a training-state artifact
}

// Spec is the args of a "step" job: everything a worker needs to run one pipeline step (the contract's StepSpec).
type Spec struct {
	StepID          string                 `json:"stepId"`
	PipelineRunID   string                 `json:"pipelineRunId"`
	ProjectID       string                 `json:"projectId,omitempty"`
	RunID           string                 `json:"runId,omitempty"`
	Kind            string                 `json:"kind"`
	KindVersion     string                 `json:"kindVersion"`
	Params          json.RawMessage        `json:"params"`
	Inputs          map[string]ArtifactRef `json:"inputs"`
	Outputs         map[string]string      `json:"outputs"`
	Resources       Resources              `json:"resources"`
	Priority        int                    `json:"priority,omitempty"`
	EstimateSeconds *float64               `json:"estimateSeconds,omitempty"`
	Overrides       Overrides              `json:"overrides"`
	SecretNames     []string               `json:"secretNames,omitempty"`
	Attempt         int                    `json:"attempt"`
}

// KindRef is the pinned "kind@version" of the spec.
func (s Spec) KindRef() string { return s.Kind + "@" + s.KindVersion }

// Validate checks what the scheduler and the worker rely on.
func (s Spec) Validate() error {
	switch {
	case s.StepID == "" || s.PipelineRunID == "":
		return errors.New("step spec: stepId and pipelineRunId are required")
	case s.Kind == "" || s.KindVersion == "":
		return errors.New("step spec: kind and kindVersion are required")
	case s.Attempt < 1:
		return errors.New("step spec: attempt starts at 1")
	case s.Resources.GPUs > 1:
		return errors.New("step spec: one card per step in v1 (R44)")
	}
	for name, in := range s.Inputs {
		if !ValidHash(in.Hash) {
			return fmt.Errorf("step spec: input %q: %q is not an artifact hash", name, in.Hash)
		}
	}
	return nil
}

// StepError is a typed step failure.
type StepError struct {
	Type      string `json:"type"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

func (e *StepError) Error() string { return e.Type + ": " + e.Message }

// Outcome is how a lease ended (the contract's StepOutcome).
type Outcome struct {
	State   string                 `json:"state"`
	Error   *StepError             `json:"error,omitempty"`
	Outputs map[string]ArtifactRef `json:"outputs,omitempty"`
	Metrics map[string]float64     `json:"metrics,omitempty"`
}

// Leases is the worker protocol as the pipeline engine sees it.
type Leases interface {
	// Await blocks until the step job jobID ends on a worker — released, reaped (Outcome with ErrLost) or cancelled —
	// or ctx ends. It is called from the "step" job's handler after the job was enqueued.
	Await(ctx context.Context, jobID string) (Outcome, error)
}

// ErrNoWorkerProtocol is what NoLeases answers.
var ErrNoWorkerProtocol = errors.New("no worker protocol: this control plane cannot run steps on workers")

// NoLeases is the Leases of a control plane without the worker protocol (tests; builds before it exists).
type NoLeases struct{}

// Await fails at once.
func (NoLeases) Await(context.Context, string) (Outcome, error) {
	return Outcome{}, ErrNoWorkerProtocol
}

// Output is one artifact a finished step produced, as an output hook sees it.
type Output struct {
	ProjectID     string
	PipelineRunID string
	StepID        string
	RunID         string
	Name          string // the step's output name
	Artifact      ArtifactRef
	Metrics       map[string]float64 // the step's final metrics
	Spec          Spec
}

// OutputHook reacts to an output of one artifact type inside the transaction that marks the step done; the events
// it returns are emitted with that transaction. An error fails the step.
type OutputHook func(ctx context.Context, tx pgx.Tx, out Output) ([]events.Draft, error)

// Hooks is a registry of output hooks by artifact type. The zero value is ready to use.
type Hooks struct {
	mu    sync.RWMutex
	hooks map[string][]OutputHook
}

// On registers h for outputs of artifactType; hooks run in registration order.
func (h *Hooks) On(artifactType string, hook OutputHook) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.hooks == nil {
		h.hooks = map[string][]OutputHook{}
	}
	h.hooks[artifactType] = append(h.hooks[artifactType], hook)
}

// Types lists the artifact types that have hooks, sorted.
func (h *Hooks) Types() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.hooks))
	for t := range h.hooks {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Run calls the hooks of out's artifact type and collects their events.
func (h *Hooks) Run(ctx context.Context, tx pgx.Tx, out Output) ([]events.Draft, error) {
	h.mu.RLock()
	hooks := append([]OutputHook(nil), h.hooks[out.Artifact.Type]...)
	h.mu.RUnlock()
	var all []events.Draft
	for _, hook := range hooks {
		ev, err := hook(ctx, tx, out)
		if err != nil {
			return nil, fmt.Errorf("output %q (%s): %w", out.Name, out.Artifact.Type, err)
		}
		all = append(all, ev...)
	}
	return all, nil
}
