// Package approvals stores gated commands for a person to decide (docs/spec/05-agents.md, Guardrails).
//
// The command pipeline creates an approval when the policy engine answers "approval": the request (method, path,
// query, headers without credentials, body), who sent it with their scope, and the rule and reason. A person
// approves or denies it; approving replays the stored request as its original actor (internal/server), and the
// replay's status and body are stored here. Pending approvals expire after 24 h and count as denied (R5). A
// session-scoped approval lets the same agent session repeat the operation on the same path without asking again.
//
// Every change emits an event on the `approvals` topic and on entity.approval.{id}.
package approvals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind in topics and references.
const Kind = "approval"

// Topic carries approval requests and decisions (docs/spec/06-platform.md, Topic scheme).
const Topic = "approvals"

// TTL is how long an approval waits for a person before it is denied (R5).
const TTL = 24 * time.Hour

// States.
const (
	StatePending  = "pending"
	StateApproved = "approved"
	StateDenied   = "denied"
)

// Kinds: a gated command is replayed when approved; an agent permission (an ACP permission request of an agent
// session) is answered to the agent host and never replayed.
const (
	KindCommand         = "command"
	KindAgentPermission = "agent_permission"
)

// Grants: how far an approval reaches.
const (
	GrantOnce    = "once"
	GrantSession = "session"
)

// Event types.
const (
	EventRequested = "approval.requested"
	EventDecided   = "approval.decided"
)

// System is the actor of decisions nobody made (expiry).
var System = auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}

// Request is a stored HTTP request.
type Request struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  string            `json:"query,omitempty"`
	Header map[string]string `json:"headers"`
	Body   []byte            `json:"-"`
}

// Approval is one stored gated command.
type Approval struct {
	ID        string
	State     string
	Operation string
	ProjectID string
	Actor     auth.Actor
	Scope     policy.Scope
	Rule      string
	Reason    string
	Estimate  *policy.Estimate
	Remaining *float64
	Request   Request
	Rev       int
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedAt *time.Time
	DecidedBy *auth.Actor
	Grant     string
	Note      string
	Expired   bool
	Result    *Result
	// Kind is KindCommand or KindAgentPermission; Permission describes what the agent asked (agent permissions).
	Kind       string
	Permission map[string]any
}

// Result is what the replayed request answered.
type Result struct {
	Status    int
	CommandID string
	Body      []byte
}

// ScopeName is "project" for work approvals and "registry" for the rest; only the admin decides registry ones.
func (a Approval) ScopeName() string {
	if a.ProjectID != "" {
		return "project"
	}
	return "registry"
}

// credentialHeaders are never stored: the replay runs as the stored actor, not with the caller's credentials.
var credentialHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie"}

// StoredHeaders returns h without credentials, cookies or anything that looks like a token or CSRF value, one
// value per name.
func StoredHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for name, vs := range h {
		canon := http.CanonicalHeaderKey(name)
		if isCredential(canon) || len(vs) == 0 {
			continue
		}
		out[canon] = vs[0]
	}
	return out
}

func isCredential(name string) bool {
	for _, c := range credentialHeaders {
		if name == c {
			return true
		}
	}
	l := strings.ToLower(name)
	for _, s := range []string{"token", "csrf", "secret", "api-key", "apikey", "password", "session"} {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// NewInput is what the pipeline knows when it gates a command.
type NewInput struct {
	Operation string
	ProjectID string
	Actor     auth.Actor
	Scope     policy.Scope
	Decision  policy.Decision
	Request   Request
	Now       time.Time
	// Kind is KindCommand (the default) or KindAgentPermission with Permission.
	Kind       string
	Permission map[string]any
}

// Create stores a pending approval and returns it with the events to emit.
func Create(ctx context.Context, tx pgx.Tx, in NewInput) (Approval, []events.Draft, error) {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	a := Approval{
		ID: "apr_" + uuid.Must(uuid.NewV7()).String(), State: StatePending, Operation: in.Operation,
		ProjectID: in.ProjectID, Actor: in.Actor, Scope: in.Scope, Rule: in.Decision.Rule, Reason: in.Decision.Reason,
		Estimate: in.Decision.Estimate, Remaining: in.Decision.RemainingGPUHours, Request: in.Request, Rev: 1,
		CreatedAt: in.Now, ExpiresAt: in.Now.Add(TTL), Kind: in.Kind, Permission: in.Permission,
	}
	if a.Kind == "" {
		a.Kind = KindCommand
	}
	est, err := estimateJSON(a.Estimate, a.Remaining)
	if err != nil {
		return Approval{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO approvals (id, operation, project_id, actor, actor_id, session_id, scope,
		rule, reason, estimate, method, path, query, headers, body, created_at, expires_at, kind, permission)
		VALUES ($1, $2, NULLIF($3, ''), $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
		a.ID, a.Operation, a.ProjectID, a.Actor, a.Actor.ID, a.Actor.SessionID, a.Scope, a.Rule, a.Reason, est,
		a.Request.Method, a.Request.Path, a.Request.Query, a.Request.Header, a.Request.Body, a.CreatedAt,
		a.ExpiresAt, a.Kind, a.Permission); err != nil {
		return Approval{}, nil, fmt.Errorf("store approval: %w", err)
	}
	return a, drafts(a, EventRequested), nil
}

type estimateRow struct {
	GPUHours          float64  `json:"gpuHours"`
	RemainingGPUHours *float64 `json:"remainingGpuHours,omitempty"`
}

// storedEstimate is the estimate column: the view's fields and whether the estimate was unknown (a retry inherits an
// approval of an unknown estimate differently from one of a known estimate, Inherit).
type storedEstimate struct {
	estimateRow
	Unknown bool `json:"unknown,omitempty"`
}

func estimateJSON(e *policy.Estimate, remaining *float64) ([]byte, error) {
	if e == nil {
		return nil, nil
	}
	b, err := json.Marshal(storedEstimate{estimateRow: estimateRow{GPUHours: e.GPUHours, RemainingGPUHours: remaining},
		Unknown: e.Unknown})
	if err != nil {
		return nil, fmt.Errorf("marshal estimate: %w", err)
	}
	return b, nil
}

const cols = `id, state, operation, coalesce(project_id, ''), actor, scope, rule, reason, estimate, method, path, query,
	headers, body, rev, created_at, expires_at, decided_at, decided_by, coalesce(grant_scope, ''), coalesce(note, ''),
	expired, result_status, result_body, coalesce(result_command_id, ''), kind, permission`

func scan(row pgx.CollectableRow) (Approval, error) {
	var (
		a      Approval
		est    []byte
		status *int
		body   []byte
		cmdID  string
	)
	err := row.Scan(&a.ID, &a.State, &a.Operation, &a.ProjectID, &a.Actor, &a.Scope, &a.Rule, &a.Reason, &est,
		&a.Request.Method, &a.Request.Path, &a.Request.Query, &a.Request.Header, &a.Request.Body, &a.Rev,
		&a.CreatedAt, &a.ExpiresAt, &a.DecidedAt, &a.DecidedBy, &a.Grant, &a.Note, &a.Expired, &status, &body, &cmdID,
		&a.Kind, &a.Permission)
	if err != nil {
		return Approval{}, err
	}
	if len(est) > 0 {
		var e storedEstimate
		if err := json.Unmarshal(est, &e); err != nil {
			return Approval{}, fmt.Errorf("decode estimate: %w", err)
		}
		a.Estimate, a.Remaining = &policy.Estimate{GPUHours: e.GPUHours, Unknown: e.Unknown}, e.RemainingGPUHours
	}
	if status != nil {
		a.Result = &Result{Status: *status, Body: body, CommandID: cmdID}
	}
	return a, nil
}

func one(rows pgx.Rows, err error, id string) (Approval, error) {
	if err != nil {
		return Approval{}, fmt.Errorf("query approval: %w", err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Approval{}, problems.NotFound.New("no approval %q", id)
	}
	if err != nil {
		return Approval{}, fmt.Errorf("read approval: %w", err)
	}
	return a, nil
}

// Get returns the approval with id, or not-found.
func Get(ctx context.Context, q storage.Querier, id string) (Approval, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM approvals WHERE id = $1", id)
	return one(rows, err, id)
}

// Filter selects approvals for List.
type Filter struct {
	State     string // pending | approved | denied | decided | ""
	ProjectID string
	Limit     int
}

// List returns approvals pending first (oldest first), then decided ones (newest first).
func List(ctx context.Context, q storage.Querier, f Filter) ([]Approval, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	where, args := []string{"true"}, []any{}
	switch f.State {
	case "":
	case "decided":
		where = append(where, "state <> 'pending'")
	default:
		args = append(args, f.State)
		where = append(where, fmt.Sprintf("state = $%d", len(args)))
	}
	if f.ProjectID != "" {
		args = append(args, f.ProjectID)
		where = append(where, fmt.Sprintf("project_id = $%d", len(args)))
	}
	args = append(args, f.Limit)
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM approvals WHERE "+strings.Join(where, " AND ")+
		fmt.Sprintf(` ORDER BY state <> 'pending',
			CASE WHEN state = 'pending' THEN created_at END ASC,
			coalesce(decided_at, created_at) DESC, id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list approvals: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("read approvals: %w", err)
	}
	return out, nil
}

// Lock reads the approval for update and checks that it is pending at revision rev.
func Lock(ctx context.Context, tx pgx.Tx, id string, rev int) (Approval, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+" FROM approvals WHERE id = $1 FOR UPDATE", id)
	a, err := one(rows, err, id)
	if err != nil {
		return Approval{}, err
	}
	if a.Rev != rev {
		return Approval{}, problems.Stale(a.Rev, "the approval is at revision %d, not %d; re-read it and decide again",
			a.Rev, rev)
	}
	if a.State != StatePending {
		return Approval{}, problems.Conflict.New("approval %s is already %s", id, a.State)
	}
	return a, nil
}

// Decision is a person's answer.
type Decision struct {
	By      auth.Actor
	Approve bool
	Grant   string // once | session (approve only)
	Note    string
	Expired bool
	Result  *Result // the replay's answer (approve only)
	Now     time.Time
}

// Decide records d on the approval locked by Lock and returns it with the events to emit.
func Decide(ctx context.Context, tx pgx.Tx, a Approval, d Decision) (Approval, []events.Draft, error) {
	if d.Now.IsZero() {
		d.Now = time.Now()
	}
	state, grant := StateDenied, GrantOnce
	if d.Approve {
		state = StateApproved
		if d.Grant == GrantSession {
			grant = GrantSession
		}
	}
	var (
		status *int
		body   []byte
		cmdID  string
	)
	if d.Result != nil {
		status, body, cmdID = &d.Result.Status, d.Result.Body, d.Result.CommandID
	}
	rows, err := tx.Query(ctx, `UPDATE approvals SET state = $2, decided_at = $3, decided_by = $4, grant_scope = $5,
		note = NULLIF($6, ''), expired = $7, result_status = $8, result_body = $9, result_command_id = NULLIF($10, ''),
		rev = rev + 1 WHERE id = $1 RETURNING `+cols,
		a.ID, state, d.Now, d.By, grant, d.Note, d.Expired, status, body, cmdID)
	a, err = one(rows, err, a.ID)
	if err != nil {
		return Approval{}, nil, err
	}
	return a, drafts(a, EventDecided), nil
}

// SessionGrant finds an approved, session-scoped approval of the same operation on the same path for the session.
func SessionGrant(ctx context.Context, q storage.Querier, sessionID, operation, path string) (string, bool, error) {
	if sessionID == "" {
		return "", false, nil
	}
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM approvals WHERE session_id = $1 AND operation = $2 AND path = $3
		AND state = 'approved' AND grant_scope = 'session' ORDER BY decided_at DESC LIMIT 1`,
		sessionID, operation, path).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("look up session grant: %w", err)
	}
	return id, true, nil
}

// ExpiredApproval is one approval Expire denied, with its events.
type ExpiredApproval struct {
	Approval Approval
	Drafts   []events.Draft
}

// Expire denies pending approvals whose time passed before now, in tx, and returns them with their events.
func Expire(ctx context.Context, tx pgx.Tx, now time.Time) ([]ExpiredApproval, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+` FROM approvals WHERE state = 'pending' AND expires_at <= $1
		ORDER BY expires_at FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return nil, fmt.Errorf("find expired approvals: %w", err)
	}
	due, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("read expired approvals: %w", err)
	}
	out := make([]ExpiredApproval, 0, len(due))
	for _, a := range due {
		a, d, err := Decide(ctx, tx, a, Decision{By: System, Expired: true, Now: now,
			Note: fmt.Sprintf("nobody decided within %s", TTL)})
		if err != nil {
			return nil, err
		}
		out = append(out, ExpiredApproval{Approval: a, Drafts: d})
	}
	return out, nil
}

// drafts are the events of a change: one on the approvals topic, one on the entity topic.
func drafts(a Approval, typ string) []events.Draft {
	ref := &events.EntityRef{Kind: Kind, ID: a.ID, Rev: a.Rev}
	payload := map[string]any{"approval": JSON(a)}
	return []events.Draft{
		{Topic: Topic, Type: typ, ProjectID: a.ProjectID, Entity: ref, Payload: payload},
		{Topic: events.EntityTopic(Kind, a.ID), Type: typ, ProjectID: a.ProjectID, Entity: ref, Payload: payload},
	}
}

// View is an approval's JSON form: the contract's Approval.
type View struct {
	ID         string         `json:"id"`
	State      string         `json:"state"`
	Scope      string         `json:"scope"`
	Operation  string         `json:"operation"`
	ProjectID  string         `json:"projectId,omitempty"`
	Actor      auth.Actor     `json:"actor"`
	Rule       string         `json:"rule"`
	Reason     string         `json:"reason"`
	Estimate   *estimateRow   `json:"estimate,omitempty"`
	Request    RequestView    `json:"request"`
	Rev        int            `json:"rev"`
	CreatedAt  time.Time      `json:"createdAt"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	DecidedAt  *time.Time     `json:"decidedAt,omitempty"`
	DecidedBy  *auth.Actor    `json:"decidedBy,omitempty"`
	Decision   *DecisionView  `json:"decision,omitempty"`
	Result     *ResultView    `json:"result,omitempty"`
	Kind       string         `json:"kind"`
	Permission map[string]any `json:"permission,omitempty"`
}

// RequestView is the stored request with its JSON body inlined.
type RequestView struct {
	Method string            `json:"method"`
	Path   string            `json:"path"`
	Query  string            `json:"query,omitempty"`
	Header map[string]string `json:"headers"`
	Body   json.RawMessage   `json:"body,omitempty"`
}

// DecisionView is how the approval was decided.
type DecisionView struct {
	Grant   string `json:"grant"`
	Note    string `json:"note,omitempty"`
	Expired bool   `json:"expired,omitempty"`
}

// ResultView is the replay's answer with its JSON body inlined.
type ResultView struct {
	Status    int             `json:"status"`
	CommandID string          `json:"commandId"`
	Body      json.RawMessage `json:"body,omitempty"`
}

// JSON renders a as the contract's Approval.
func JSON(a Approval) View {
	v := View{
		ID: a.ID, State: a.State, Scope: a.ScopeName(), Operation: a.Operation, ProjectID: a.ProjectID, Actor: a.Actor,
		Rule: a.Rule, Reason: a.Reason, Rev: a.Rev, CreatedAt: a.CreatedAt, ExpiresAt: a.ExpiresAt,
		DecidedAt: a.DecidedAt, DecidedBy: a.DecidedBy, Kind: a.Kind, Permission: a.Permission,
		Request: RequestView{Method: a.Request.Method, Path: a.Request.Path, Query: a.Request.Query,
			Header: a.Request.Header, Body: rawJSON(a.Request.Body)},
	}
	if v.Request.Header == nil {
		v.Request.Header = map[string]string{}
	}
	if a.Estimate != nil {
		v.Estimate = &estimateRow{GPUHours: a.Estimate.GPUHours, RemainingGPUHours: a.Remaining}
	}
	if a.State != StatePending {
		v.Decision = &DecisionView{Grant: a.Grant, Note: a.Note, Expired: a.Expired}
	}
	if a.Result != nil {
		v.Result = &ResultView{Status: a.Result.Status, CommandID: a.Result.CommandID, Body: rawJSON(a.Result.Body)}
	}
	return v
}

// rawJSON returns b when it is valid JSON, else nil (a non-JSON body is not shown).
func rawJSON(b []byte) json.RawMessage {
	if len(b) == 0 || !json.Valid(b) {
		return nil
	}
	return json.RawMessage(b)
}
