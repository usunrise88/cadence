// Package commands runs every mutation the same way: one transaction with the actor, the Idempotency-Key, the
// dry-run switch, the policy decision, the outbox events it emits and its audit row.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Response headers set by the pipeline.
const (
	HeaderCommandID  = "Cadence-Command-Id"
	HeaderReplayed   = "Idempotent-Replayed"
	HeaderApprovalID = "Cadence-Approval-Id"
	// HeaderPolicy tells a dry run's caller that the real run would wait for an approval: "approval; rule=<id>".
	HeaderPolicy = "Cadence-Policy"
)

// idempotencyLockClass is the first key of the two-key pg_advisory_xact_lock that serialises commands sharing an
// actor and Idempotency-Key, so a concurrent repeat waits for the first and then replays it. Two-key advisory
// locks do not collide with the single-key outbox and migration locks.
const idempotencyLockClass int32 = 0x63646e01

// Outcomes of a run, as counted and logged (a failed run is counted by its problem slug).
const (
	outcomeOK       = "ok"
	outcomeReplayed = "replayed"
	outcomeDryRun   = "dry_run"
	outcomeApproval = "approval"
	outcomeDenied   = "denied"
)

// Command describes one invocation of a mutating operation.
type Command struct {
	Operation      string // <entity>.<verb>, e.g. projects.new
	Actor          auth.Actor
	IdempotencyKey string
	DryRun         bool
	RequestHash    string            // see HashRequest
	VerbClass      string            // read | mutate (api/vocabulary.yaml); empty means mutate
	PathParams     map[string]string // the route's path parameters, for policy rules such as aliases.set name=baseline
}

// Result is what a command answers on success.
type Result struct {
	Status int // HTTP status of a real run; a dry run always answers 200 because it creates nothing
	Body   any // marshalled as JSON
	ETag   string
}

// Response is the HTTP response of a command, exactly as stored for idempotent replay.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Func does the work of a command inside tx and returns the result and the events it emits.
type Func func(ctx context.Context, tx pgx.Tx) (Result, []events.Draft, error)

// Pipeline runs commands against the database.
type Pipeline struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	commands *prometheus.CounterVec // labels: operation, outcome
	policy   *policy.Engine
	onGate   GateHook
}

// GateHook runs in the transaction of a gated command, right after its approval is stored, with the agent tool
// call behind it; its events join the command's. Agent sessions use it to show the approval in the transcript and
// to wait for it (waiting_approval).
type GateHook func(ctx context.Context, tx pgx.Tx, a approvals.Approval, toolCallID string) ([]events.Draft, error)

// SetGateHook installs h; call it before the pipeline serves.
func (p *Pipeline) SetGateHook(h GateHook) { p.onGate = h }

// NewPipeline returns a pipeline that asks engine about every command; commands counts runs by operation and
// outcome.
func NewPipeline(pool *pgxpool.Pool, log *slog.Logger, commands *prometheus.CounterVec, engine *policy.Engine) *Pipeline {
	if engine == nil {
		panic("commands: NewPipeline needs a policy engine")
	}
	return &Pipeline{pool: pool, log: log, commands: commands, policy: engine}
}

// Policy returns the engine the pipeline asks.
func (p *Pipeline) Policy() *policy.Engine { return p.policy }

// trace is what one run learned, for the log line and the audit row.
type trace struct {
	projectID  string
	toolCallID string
	approvalID string
	draftID    string
	decision   policy.Decision
	status     int
}

// Run executes fn as cmd in one transaction:
//
//  1. With an Idempotency-Key (and not a dry run), take a lock on (actor, key) and look the key up: the same
//     operation and request hash replays the stored response; anything else is idempotency-key-reused.
//  2. Ask the policy engine (actor, scope, operation, project from WithProject, estimate from WithEstimate, path
//     parameters). deny → 403 policy-denied. approval → store the request as an approval, emit
//     approval.requested, answer 202 {approvalId} and store that under the key; a dry run instead runs and says
//     so in the Cadence-Policy header. A session-scoped approval of the same operation on the same path allows it.
//  3. Run fn.
//  4. A dry run rolls back here and answers 200 with the would-be result; nothing is written, no event emitted.
//  5. Append fn's events to the outbox with causedBy (command, tool call, approval, draft), write the audit row, store the
//     response under the key, commit.
//
// Failed and denied commands roll back entirely and store nothing, so a retry with the same key runs again; their
// audit row is written on its own. Under WithReplay the command runs in a savepoint of the approve command's
// transaction and skips the policy.
func (p *Pipeline) Run(ctx context.Context, cmd Command, fn Func) (Response, error) {
	start := time.Now()
	id := "cmd_" + uuid.Must(uuid.NewV7()).String()
	if cmd.VerbClass == "" {
		cmd.VerbClass = "mutate"
	}
	tr := &trace{projectID: ProjectFromContext(ctx), toolCallID: ToolCallFromContext(ctx), draftID: DraftFromContext(ctx)}
	resp, outcome, err := p.run(ctx, cmd, id, fn, tr)
	p.commands.WithLabelValues(cmd.Operation, outcome).Inc()
	if err != nil && !cmd.DryRun && outcome != outcomeReplayed {
		p.auditFailure(ctx, cmd, id, outcome, err, tr)
	}
	attrs := []any{
		"actor", cmd.Actor.ID, "actor_kind", cmd.Actor.Kind, "operation", cmd.Operation, "commandId", id,
		"dryRun", cmd.DryRun, "outcome", outcome, "duration_ms", time.Since(start).Milliseconds(),
	}
	for _, kv := range [][2]string{
		{"projectId", tr.projectID}, {"toolCallId", tr.toolCallID}, {"approvalId", tr.approvalID},
		{"rule", tr.decision.Rule}, {"draftId", tr.draftID},
	} {
		if kv[1] != "" {
			attrs = append(attrs, kv[0], kv[1])
		}
	}
	level := slog.LevelInfo
	if outcome == problems.Internal.Slug {
		level = slog.LevelError
		attrs = append(attrs, "err", err)
	}
	p.log.Log(ctx, level, "command", attrs...)
	return resp, err
}

func (p *Pipeline) begin(ctx context.Context) (pgx.Tx, error) {
	if r := replayFrom(ctx); r != nil {
		return r.tx.Begin(ctx) // a savepoint of the approve command's transaction
	}
	return p.pool.Begin(ctx)
}

func (p *Pipeline) run(ctx context.Context, cmd Command, id string, fn Func, tr *trace) (Response, string, error) {
	tx, err := p.begin(ctx)
	if err != nil {
		return Response{}, problems.Internal.Slug, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }() // no-op after commit

	rep := replayFrom(ctx)
	useKey := cmd.IdempotencyKey != "" && !cmd.DryRun
	if useKey {
		if err := lockKey(ctx, tx, cmd); err != nil {
			return Response{}, problems.Internal.Slug, err
		}
		if rep == nil { // a replay supersedes the 202 stored under its key
			stored, found, err := lookupKey(ctx, tx, cmd)
			if err != nil {
				return Response{}, outcomeOf(err), err
			}
			if found {
				return stored, outcomeReplayed, nil
			}
		}
	}

	var policyHeader string
	if rep != nil {
		tr.approvalID = rep.approvalID
		tr.decision = policy.Decision{Outcome: policy.Allow, Rule: "approval", Reason: "approved by a person"}
	} else {
		d, grant, err := p.decide(ctx, tx, cmd, tr.projectID)
		if err != nil {
			return Response{}, problems.Internal.Slug, err
		}
		tr.decision, tr.approvalID = d, grant
		switch {
		case d.Outcome == policy.Deny:
			return Response{}, outcomeDenied, problems.PolicyDenied.New(
				"%s is denied by rule %q%s: %s", cmd.Operation, d.Rule, presetNote(d), d.Reason)
		case d.Outcome == policy.Approval && cmd.DryRun:
			policyHeader = "approval; rule=" + d.Rule
		case d.Outcome == policy.Approval:
			return p.gate(ctx, tx, cmd, id, tr)
		}
	}

	res, drafts, err := fn(ctx, tx)
	if err != nil {
		return Response{}, outcomeOf(err), err
	}
	resp, err := render(res, id, cmd.DryRun)
	if err != nil {
		return Response{}, problems.Internal.Slug, err
	}
	if cmd.DryRun {
		if policyHeader != "" {
			resp.Header.Set(HeaderPolicy, policyHeader)
		}
		return resp, outcomeDryRun, nil
	}
	tr.status = resp.Status
	if err := p.commit(ctx, tx, cmd, id, tr, audit.OutcomeOK, drafts, resp, useKey); err != nil {
		return Response{}, problems.Internal.Slug, err
	}
	return resp, outcomeOK, nil
}

// decide asks the engine; an approval answer is turned into an allow when the agent session holds a
// session-scoped approval of the same operation on the same path (grant is that approval's id).
func (p *Pipeline) decide(ctx context.Context, tx pgx.Tx, cmd Command, projectID string) (policy.Decision, string, error) {
	d, err := p.policy.Decide(ctx, policy.Input{
		Actor: cmd.Actor, Scope: policy.ScopeFromContext(ctx), Operation: cmd.Operation, VerbClass: cmd.VerbClass,
		ProjectID: projectID, Estimate: estimateFrom(ctx), PathParams: cmd.PathParams,
	})
	if err != nil || d.Outcome != policy.Approval || cmd.Actor.SessionID == "" {
		return d, "", err
	}
	req, ok := RequestFromContext(ctx)
	if !ok {
		return d, "", nil
	}
	grant, found, err := approvals.SessionGrant(ctx, tx, cmd.Actor.SessionID, cmd.Operation, req.Path)
	if err != nil || !found {
		return d, "", err
	}
	return policy.Decision{Outcome: policy.Allow, Rule: "session-grant", Preset: d.Preset,
		Reason: "approved for this agent session by " + grant}, grant, nil
}

func presetNote(d policy.Decision) string {
	if d.Preset == "" {
		return ""
	}
	return " of the " + d.Preset + " preset"
}

// gate stores the request as an approval and answers 202 {approvalId}.
func (p *Pipeline) gate(ctx context.Context, tx pgx.Tx, cmd Command, id string, tr *trace) (Response, string, error) {
	req, ok := RequestFromContext(ctx)
	if !ok {
		return Response{}, problems.Internal.Slug, errors.New("a gated command needs its HTTP request to store")
	}
	a, drafts, err := approvals.Create(ctx, tx, approvals.NewInput{
		Operation: cmd.Operation, ProjectID: tr.projectID, Actor: cmd.Actor, Scope: policy.ScopeFromContext(ctx),
		Decision: tr.decision,
		Request: approvals.Request{Method: req.Method, Path: req.Path, Query: req.Query,
			Header: approvals.StoredHeaders(req.Header), Body: req.Body},
	})
	if err != nil {
		return Response{}, outcomeOf(err), err
	}
	if p.onGate != nil {
		more, err := p.onGate(ctx, tx, a, tr.toolCallID)
		if err != nil {
			return Response{}, outcomeOf(err), err
		}
		drafts = append(drafts, more...)
	}
	resp, err := render(Result{Status: http.StatusAccepted, Body: map[string]string{"approvalId": a.ID}}, id, false)
	if err != nil {
		return Response{}, problems.Internal.Slug, err
	}
	resp.Header.Set(HeaderApprovalID, a.ID)
	tr.approvalID, tr.status = a.ID, resp.Status
	useKey := cmd.IdempotencyKey != ""
	if err := p.commit(ctx, tx, cmd, id, tr, audit.OutcomeApproval, drafts, resp, useKey); err != nil {
		return Response{}, problems.Internal.Slug, err
	}
	return resp, outcomeApproval, nil
}

// commit appends the events, writes the audit row, stores the response under the key and commits.
func (p *Pipeline) commit(ctx context.Context, tx pgx.Tx, cmd Command, id string, tr *trace, outcome string,
	drafts []events.Draft, resp Response, useKey bool) error {
	cause := &events.CausedBy{CommandID: id, ToolCallID: tr.toolCallID, ApprovalID: tr.approvalID, DraftID: tr.draftID}
	if err := events.Append(ctx, tx, cmd.Actor, cause, drafts); err != nil {
		return err
	}
	if err := audit.Write(ctx, tx, p.auditEntry(ctx, cmd, id, outcome, tr)); err != nil {
		return err
	}
	if useKey {
		if err := storeKey(ctx, tx, cmd, resp); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", cmd.Operation, err)
	}
	return nil
}

func (p *Pipeline) auditEntry(ctx context.Context, cmd Command, id, outcome string, tr *trace) audit.Entry {
	preset := tr.decision.Preset
	if preset == "" && cmd.Actor.Kind != auth.KindUser {
		preset = policy.ScopeFromContext(ctx).Preset
	}
	return audit.Entry{
		CommandID: id, Operation: cmd.Operation, Actor: cmd.Actor, Preset: preset, ProjectID: tr.projectID,
		Outcome: outcome, Status: tr.status, Rule: tr.decision.Rule, ToolCallID: tr.toolCallID, ApprovalID: tr.approvalID,
	}
}

// auditFailure records a denied or failed attempt in its own transaction; the command's own rolled back.
func (p *Pipeline) auditFailure(ctx context.Context, cmd Command, id, outcome string, err error, tr *trace) {
	pe, _ := problems.As(err)
	tr.status = pe.Type.Status
	e := p.auditEntry(ctx, cmd, id, outcome, tr)
	if werr := audit.Write(context.WithoutCancel(ctx), p.pool, e); werr != nil {
		p.log.ErrorContext(ctx, "audit a failed command", "operation", cmd.Operation, "commandId", id, "err", werr)
	}
}

func render(res Result, id string, dryRun bool) (Response, error) {
	body, err := json.Marshal(res.Body)
	if err != nil {
		return Response{}, fmt.Errorf("marshal result: %w", err)
	}
	status := res.Status
	if dryRun || status == 0 {
		status = http.StatusOK
	}
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set(HeaderCommandID, id)
	if res.ETag != "" {
		h.Set("ETag", res.ETag)
	}
	return Response{Status: status, Header: h, Body: append(body, '\n')}, nil
}

func lockKey(ctx context.Context, tx pgx.Tx, cmd Command) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, hashtext($2))",
		idempotencyLockClass, cmd.Actor.ID+"\n"+cmd.IdempotencyKey); err != nil {
		return fmt.Errorf("lock idempotency key: %w", err)
	}
	return nil
}

func lookupKey(ctx context.Context, tx pgx.Tx, cmd Command) (Response, bool, error) {
	var (
		op, hash string
		resp     Response
	)
	err := tx.QueryRow(ctx, `SELECT operation, request_hash, status, headers, body FROM idempotency_keys
		WHERE actor_id = $1 AND key = $2`, cmd.Actor.ID, cmd.IdempotencyKey).
		Scan(&op, &hash, &resp.Status, &resp.Header, &resp.Body)
	if errors.Is(err, pgx.ErrNoRows) {
		return Response{}, false, nil
	}
	if err != nil {
		return Response{}, false, fmt.Errorf("look up idempotency key: %w", err)
	}
	if op != cmd.Operation || hash != cmd.RequestHash {
		return Response{}, false, problems.IdempotencyKeyReused.New(
			"this Idempotency-Key was already used for a different request (%s); use a new key for a new request", op)
	}
	resp.Header.Set(HeaderReplayed, "true")
	return resp, true, nil
}

// storeKey stores the response; a replayed approval overwrites the 202 its request first answered, so a client
// repeating the original request after the approval gets the real result.
func storeKey(ctx context.Context, tx pgx.Tx, cmd Command, resp Response) error {
	if _, err := tx.Exec(ctx, `INSERT INTO idempotency_keys (actor_id, key, operation, request_hash, status, headers, body)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (actor_id, key) DO UPDATE SET status = excluded.status, headers = excluded.headers, body = excluded.body`,
		cmd.Actor.ID, cmd.IdempotencyKey, cmd.Operation, cmd.RequestHash, resp.Status, resp.Header, resp.Body); err != nil {
		return fmt.Errorf("store idempotency key: %w", err)
	}
	return nil
}

func outcomeOf(err error) string {
	pe, _ := problems.As(err)
	return pe.Type.Slug
}
