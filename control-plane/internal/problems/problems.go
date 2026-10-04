// Package problems is the single registry of Cadence error types and their RFC 9457 rendering.
//
// Every type has a slug; its `type` URI is https://cadence.local/help/errors/<slug>, which resolves to the help
// article docs/help/errors/<slug>.md (a test keeps the registry and the articles in step).
package problems

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/usunrise88/cadence/control-plane/internal/api"
)

// TypeBase prefixes every problem type URI.
const TypeBase = "https://cadence.local/help/errors/"

// ContentType is the media type of every error body.
const ContentType = "application/problem+json"

// Type is one registered error type.
type Type struct {
	Slug   string
	Status int
	Title  string
}

// The registry. Add a type here together with docs/help/errors/<slug>.md.
var (
	BadRequest           = Type{"bad-request", http.StatusBadRequest, "Bad request"}
	ValidationFailed     = Type{"validation-failed", http.StatusUnprocessableEntity, "Validation failed"}
	NotFound             = Type{"not-found", http.StatusNotFound, "Not found"}
	MethodNotAllowed     = Type{"method-not-allowed", http.StatusMethodNotAllowed, "Method not allowed"}
	Conflict             = Type{"conflict", http.StatusConflict, "Conflict"}
	PreconditionFailed   = Type{"precondition-failed", http.StatusPreconditionFailed, "Precondition failed"}
	PreconditionRequired = Type{"precondition-required", http.StatusPreconditionRequired, "Precondition required"}
	IdempotencyKeyReused = Type{"idempotency-key-reused", http.StatusUnprocessableEntity, "Idempotency key reused"}
	Unauthenticated      = Type{"unauthenticated", http.StatusUnauthorized, "Not signed in"}
	TOTPRequired         = Type{"totp-required", http.StatusUnauthorized, "TOTP code required"}
	Forbidden            = Type{"forbidden", http.StatusForbidden, "Forbidden"}
	RateLimited          = Type{"rate-limited", http.StatusTooManyRequests, "Too many attempts"}
	NotImplemented       = Type{"not-implemented", http.StatusNotImplemented, "Not implemented"}
	Internal             = Type{"internal", http.StatusInternalServerError, "Internal error"}
	PolicyDenied         = Type{"policy-denied", http.StatusForbidden, "Denied by policy"}

	// Registry and estimates (phase 1 · stream B).
	ReservedAlias       = Type{"reserved-alias", http.StatusConflict, "Reserved alias"}
	EstimateUnavailable = Type{"estimate-unavailable", http.StatusUnprocessableEntity, "Estimate unavailable"}

	// Search (phase 1 · wave 2).
	InvalidQuery = Type{"invalid-query", http.StatusBadRequest, "Invalid search query"}

	// Drafts (phase 1 · mix stream).
	DraftStale = Type{"draft-stale", http.StatusPreconditionFailed, "Draft is stale"}

	// Project repositories (phase 1 · wave 2, projects).
	MergeConflict         = Type{"merge-conflict", http.StatusConflict, "Merge conflict"}
	RepositoryUnavailable = Type{"repository-unavailable", http.StatusBadGateway, "Repository unavailable"}

	// Data entities (phase 2 · stream D).
	EvalOnlyDataset = Type{"eval-only-dataset", http.StatusUnprocessableEntity, "Eval-only dataset"}

	// Pipelines (phase 2 · stream P).
	PipelineInvalid = Type{"pipeline-invalid", http.StatusUnprocessableEntity, "Pipeline invalid"}

	// Worker protocol (phase 2 · stream W).
	LeaseEnded           = Type{"lease-ended", http.StatusConflict, "Lease ended"}
	StepKindConflict     = Type{"step-kind-conflict", http.StatusConflict, "Step kind conflict"}
	ArtifactHashMismatch = Type{"artifact-hash-mismatch", http.StatusUnprocessableEntity, "Artifact hash mismatch"}
	ArtifactMissing      = Type{"artifact-missing", http.StatusConflict, "Artifact missing"}

	// Playbooks (phase 2 · stream K).
	PlaybookDryRunRequired = Type{"playbook-dry-run-required", http.StatusConflict, "Dry run required first"}
	PlaybookStopped        = Type{"playbook-stopped", http.StatusConflict, "Playbook stopped"}
	PlaybookUnavailable    = Type{"playbook-unavailable", http.StatusConflict, "Playbook not available yet"}
	// Runs, checkpoints and metrics (phase 2 · wave 2 · stream R).
	FamilyUnavailable = Type{"family-unavailable", http.StatusUnprocessableEntity, "Model family unavailable"}
	RecipeMismatch    = Type{"recipe-mismatch", http.StatusUnprocessableEntity, "Recipe does not fit the run"}
	NoTrainingState   = Type{"no-training-state", http.StatusConflict, "No training state"}
	// Content-store retention (phase 2 · stream E).
	ArtifactNotEvictable = Type{"artifact-not-evictable", http.StatusConflict, "Artifact not evictable"}
	// ArtifactEvicted: a read of per-utterance eval artifacts that the age retention evicted (owner decision 2026-10-03).
	ArtifactEvicted = Type{"artifact-evicted", http.StatusGone, "Artifact evicted"}
	// Evals, gates and models (phase 3 · stream E).
	EvalBaselineMissing = Type{"eval-baseline-missing", http.StatusUnprocessableEntity, "No baseline to compare with"}
	GateNotPassed       = Type{"gate-not-passed", http.StatusConflict, "Gate not passed"}
	GateConfigInvalid   = Type{"gate-config-invalid", http.StatusUnprocessableEntity, "Gate configuration invalid"}
	// Golden sets, normalizers and leakage (phase 3 · stream G).
	GoldenSetLeakage     = Type{"golden-set-leakage", http.StatusUnprocessableEntity, "Golden set leakage"}
	GoldenSetNotEvalOnly = Type{"golden-set-not-eval-only", http.StatusUnprocessableEntity, "Dataset is not eval-only"}
	NormalizerUnknown    = Type{"normalizer-unknown", http.StatusUnprocessableEntity, "Unknown normalizer"}
	// Media: audio serving (phase 3 · stream A).
	MediaLinkInvalid    = Type{"media-link-invalid", http.StatusForbidden, "Audio link invalid or expired"}
	RangeNotSatisfiable = Type{"range-not-satisfiable", http.StatusRequestedRangeNotSatisfiable, "Range not satisfiable"}
	MediaBusy           = Type{"media-busy", http.StatusTooManyRequests, "Audio conversions busy"}
	MediaTilesFailed    = Type{"media-tiles-failed", http.StatusUnprocessableEntity, "Spectrogram tiles could not be built"}
	// Experiments and sweeps (phase 3 · stream X).
	SweepOverCap = Type{"sweep-over-cap", http.StatusUnprocessableEntity, "Sweep over its GPU-hour cap"}
	// Manual transcription tests and the live channel (phase 3 · stream T).
	TranscriptionInProgress         = Type{"transcription-in-progress", http.StatusConflict, "A transcription session is already open"}
	TranscriptionAllowanceExhausted = Type{"transcription-allowance-exhausted", http.StatusTooManyRequests, "Manual-test allowance used up"}
	TranscriptionTicketInvalid      = Type{"transcription-ticket-invalid", http.StatusForbidden, "Transcription ticket invalid"}
	TranscriptionInputInvalid       = Type{"transcription-input-invalid", http.StatusUnprocessableEntity, "Transcription input invalid"}
	TranscriptionLimit              = Type{"transcription-limit", http.StatusConflict, "Transcription session limit reached"}
	// Ingest, draft dataset versions and freezing (phase 4 · stream D).
	DatasetNotFrozen = Type{"dataset-not-frozen", http.StatusUnprocessableEntity, "Dataset version not frozen"}
	SourceUnlicensed = Type{"source-unlicensed", http.StatusUnprocessableEntity, "Source without a usable licence"}
	// Mounts and the local cache (phase 4 · stream M).
	MountUnhealthy       = Type{"mount-unhealthy", http.StatusConflict, "Mount unhealthy"}
	StorageQuotaExceeded = Type{"storage-quota-exceeded", http.StatusConflict, "Project storage quota exceeded"}
	// Auxiliary models and pseudo-labels (phase 4 · stream X, R26).
	AuxiliaryUnavailable    = Type{"auxiliary-unavailable", http.StatusServiceUnavailable, "Auxiliary service unavailable"}
	AuxiliaryLicenceRefused = Type{"auxiliary-licence-refused", http.StatusUnprocessableEntity, "Auxiliary licence refused"}
	// Registry in full (phase 4 · stream R): adoption checks, soft delete, step-kind deprecation, data.lock.
	LicenceForbidsAdoption = Type{"licence-forbids-adoption", http.StatusUnprocessableEntity, "Licence forbids adoption"}
	LocaleMismatch         = Type{"locale-mismatch", http.StatusUnprocessableEntity, "Not in the project's languages"}
	VersionInUse           = Type{"version-in-use", http.StatusConflict, "Registry version in use"}
	StepKindDeprecated     = Type{"step-kind-deprecated", http.StatusUnprocessableEntity, "Step kind deprecated"}
	NotAdopted             = Type{"not-adopted", http.StatusUnprocessableEntity, "Not in the project's data.lock"}
	// Interoperability: dataset exports (phase 4 · stream I).
	ExportNotAllowed = Type{"export-not-allowed", http.StatusUnprocessableEntity, "Export not allowed"}
	// Project bundles (phase 4 tail): projects.export, bundles.adopt, projects.new with bundle.
	BundleInvalid = Type{"bundle-invalid", http.StatusUnprocessableEntity, "Project bundle invalid"}
	// Annotation batches and reviewer invitations (phase 4 · stream A).
	BatchIncomplete        = Type{"batch-incomplete", http.StatusConflict, "Annotation batch incomplete"}
	AnnotationAgreementLow = Type{"annotation-agreement-low", http.StatusConflict, "Inter-annotator agreement too low"}
	BatchClosed            = Type{"batch-closed", http.StatusConflict, "Annotation batch closed"}
	InvitationInvalid      = Type{"invitation-invalid", http.StatusUnauthorized, "Invitation invalid"}
)

// Types lists every registered type.
func Types() []Type {
	return []Type{
		BadRequest, ValidationFailed, NotFound, MethodNotAllowed, Conflict, PreconditionFailed,
		PreconditionRequired, IdempotencyKeyReused, Unauthenticated, TOTPRequired, Forbidden, RateLimited, NotImplemented,
		Internal, PolicyDenied, ReservedAlias, EstimateUnavailable, InvalidQuery, DraftStale, MergeConflict,
		RepositoryUnavailable, EvalOnlyDataset, PipelineInvalid,
		LeaseEnded, StepKindConflict, ArtifactHashMismatch, ArtifactMissing,
		PlaybookDryRunRequired, PlaybookStopped, PlaybookUnavailable,
		FamilyUnavailable, RecipeMismatch, NoTrainingState, ArtifactNotEvictable, ArtifactEvicted,
		EvalBaselineMissing, GateNotPassed, GateConfigInvalid,
		GoldenSetLeakage, GoldenSetNotEvalOnly, NormalizerUnknown,
		MediaLinkInvalid, RangeNotSatisfiable, MediaBusy, MediaTilesFailed,
		SweepOverCap,
		TranscriptionInProgress, TranscriptionAllowanceExhausted, TranscriptionTicketInvalid, TranscriptionInputInvalid,
		TranscriptionLimit,
		DatasetNotFrozen, SourceUnlicensed,
		MountUnhealthy, StorageQuotaExceeded,
		AuxiliaryUnavailable, AuxiliaryLicenceRefused,
		LicenceForbidsAdoption, LocaleMismatch, VersionInUse, StepKindDeprecated, NotAdopted,
		ExportNotAllowed, BundleInvalid,
		BatchIncomplete, AnnotationAgreementLow, BatchClosed, InvitationInvalid,
	}
}

// URI is the type URI of t.
func (t Type) URI() string { return TypeBase + t.Slug }

// New returns an error of type t with a detail message.
func (t Type) New(format string, args ...any) *Error {
	return &Error{Type: t, Detail: fmt.Sprintf(format, args...)}
}

// FieldError is one field-level problem of a validation-failed error.
type FieldError = api.ProblemFieldError

// Error is a Cadence error that renders as problem+json.
type Error struct {
	Type       Type
	Detail     string
	CurrentRev *int
	Errors     []FieldError
	// RetryAfter, when positive, is sent as the Retry-After header (seconds), e.g. on rate-limited.
	RetryAfter int
}

func (e *Error) Error() string { return e.Type.Slug + ": " + e.Detail }

// Stale is a precondition-failed error carrying the revision to rebase on.
func Stale(current int, format string, args ...any) *Error {
	e := PreconditionFailed.New(format, args...)
	e.CurrentRev = &current
	return e
}

// Validation is a validation-failed error listing field problems.
func Validation(fields []FieldError) *Error {
	e := ValidationFailed.New("the request does not match the operation's schema")
	e.Errors = fields
	return e
}

// As extracts a *Error from err's chain; any other error becomes internal.
func As(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return Internal.New("something went wrong on the server; the log has the details"), false
}

// Body renders e as the contract's Problem.
func (e *Error) Body() api.Problem {
	p := api.Problem{Type: e.Type.URI(), Title: e.Type.Title, Status: e.Type.Status, CurrentRev: e.CurrentRev}
	if e.Detail != "" {
		p.Detail = &e.Detail
	}
	if len(e.Errors) > 0 {
		p.Errors = &e.Errors
	}
	return p
}

// Write renders err as problem+json. Errors that are not *Error are logged and answered as internal, so internals
// never leak into a response.
func Write(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	pe, ok := As(err)
	if !ok {
		log.ErrorContext(r.Context(), "request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	body := pe.Body()
	if r.URL != nil {
		instance := r.URL.Path
		body.Instance = &instance
	}
	b, merr := json.Marshal(body)
	if merr != nil {
		log.ErrorContext(r.Context(), "marshal problem", "err", merr)
		return
	}
	w.Header().Set("Content-Type", ContentType)
	w.Header().Set("Cache-Control", "no-store")
	if pe.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(pe.RetryAfter))
	}
	w.WriteHeader(pe.Type.Status)
	if _, werr := w.Write(append(b, '\n')); werr != nil {
		log.DebugContext(r.Context(), "write problem", "err", werr)
	}
}
