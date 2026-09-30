// Package sessions is the agent session entity (docs/spec/05-agents.md "Agent integration"): the session row with
// its state, budget, use and merge state; the transcript (agent_messages) the Chat panel renders; the requests
// people make of the agent host (controls); the host protocol (claim work, report, ask) and the context bridge
// (references expanded into a context block, selection://current).
//
// The agent host runs sessions; this package keeps the truth the host reports and decides what the host may do
// next. Every change emits events on `agent.sessions` (the list) and `agent.session.{id}` (one transcript and its
// header), attributed to the person, the agent session or Cadence.
package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects/bootstrap"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind of a session in references and entity refs.
const Kind = "agent_session"

// TopicList carries the session list (docs/spec/06-platform.md "Topic scheme").
const TopicList = "agent.sessions"

// Topic is the transcript topic of one session.
func Topic(id string) string { return "agent.session." + id }

// Event types.
const (
	EventCreated        = "agent_session.created"
	EventChanged        = "agent_session.changed"
	EventMessageCreated = "agent_message.created"
	EventMessageUpdated = "agent_message.updated"
)

// Session kinds.
const (
	KindInteractive = "interactive"
	KindReadOnly    = "read-only"
)

// States.
const (
	StateCreated         = "created"
	StateRunning         = "running"
	StateWaitingApproval = "waiting_approval"
	StatePaused          = "paused"
	StateDone            = "done"
	StateFailed          = "failed"
	StateCancelled       = "cancelled"
)

// Merge states.
const (
	MergeNone      = "none"
	MergePending   = "pending"
	MergeMerged    = "merged"
	MergeConflict  = "conflict"
	MergeDiscarded = "discarded"
)

// Pause reasons.
const (
	PauseUser          = "user"
	PauseIdle          = "idle"
	PauseStuckTurn     = "stuck_turn"
	PauseRunaway       = "runaway"
	PauseBudgetTurns   = "budget_turns"
	PauseBudgetTokens  = "budget_tokens"
	PauseTurnTokens    = "turn_tokens"
	PauseProjectTokens = "project_tokens"
	PauseHostLost      = "host_lost"
)

// ReadOnlyPreset is the permission preset of read-only sessions.
const ReadOnlyPreset = "read-only"

// System is the actor of what Cadence does on its own (idle pauses, auto-merges, expiries).
var System = auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}

// Live reports whether state is not terminal.
func Live(state string) bool {
	switch state {
	case StateCreated, StateRunning, StateWaitingApproval, StatePaused:
		return true
	}
	return false
}

// Reason is why a session paused.
type Reason struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Budget is a session's allowance.
type Budget struct {
	Turns         int   `json:"turns"`
	Tokens        int64 `json:"tokens"`
	TokensPerTurn int64 `json:"tokensPerTurn"`
}

// Use is what a session has spent so far (as the agent host counts it).
type Use struct {
	Turns            int      `json:"turns"`
	InputTokens      int64    `json:"inputTokens"`
	OutputTokens     int64    `json:"outputTokens"`
	CachedReadTokens int64    `json:"cachedReadTokens,omitempty"`
	CostUSD          *float64 `json:"costUsd,omitempty"`
	ContextUsed      int64    `json:"contextUsed,omitempty"`
	ContextSize      int64    `json:"contextSize,omitempty"`
}

// Tokens is the budgeted measure: input plus output tokens (cached reads are not counted).
func (u Use) Tokens() int64 { return u.InputTokens + u.OutputTokens }

// Merge is what happened to the session branch.
type Merge struct {
	State       string      `json:"state"`
	Commit      string      `json:"commit,omitempty"`
	Head        string      `json:"head,omitempty"`
	Conflicts   []string    `json:"conflicts,omitempty"`
	FastForward *bool       `json:"fastForward,omitempty"`
	At          *time.Time  `json:"at,omitempty"`
	By          *auth.Actor `json:"by,omitempty"`
}

// Reference is one entity the user attached to a message: @run:123, @mix:mix_…, @utt:9f3c#t=1.5-3.0.
type Reference struct {
	Ref      string `json:"ref"`
	Kind     string `json:"kind,omitempty"`
	ID       string `json:"id,omitempty"`
	Fragment string `json:"fragment,omitempty"`
	Label    string `json:"label,omitempty"`
}

// Session is one agent session row.
type Session struct {
	ID            string
	ProjectID     string
	ProjectSlug   string
	Number        int
	Kind          string
	Driver        string
	Model         string
	Preset        string
	State         string
	Busy          bool
	Turn          int
	PauseReason   *Reason
	Error         string
	Branch        string
	Merge         Merge
	AutoMerge     string
	Budget        Budget
	Use           Use
	Prompt        string
	Refs          []Reference
	StartedBy     auth.Actor
	CredentialID  string
	ACPSessionID  string
	HostID        string
	Rev           int
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	EndedAt       *time.Time
	LastMessageAt *time.Time
	// PendingControl is the oldest request the host has not taken yet (read with the session).
	PendingControl string
	// HostLeftAt is when the host released the session (HostID is then empty) or was found silent past the lapse.
	HostLeftAt *time.Time
	// ResumeNote is what the next host tells the agent before its next prompt (set when a host releases the session).
	ResumeNote string
}

// Host states of a live session (the contract's AgentSession.hostState).
const (
	HostWaiting   = "waiting"
	HostConnected = "connected"
	HostReleased  = "released"
	HostLost      = "lost"
)

// HostState is where the session's agent host stands: none took it yet, one runs it, it released the session (a
// restart) or it went silent. Empty for ended sessions.
func (s Session) HostState() string {
	switch {
	case !Live(s.State):
		return ""
	case s.HostLeftAt != nil && s.HostID == "":
		return HostReleased
	case s.HostLeftAt != nil:
		return HostLost
	case s.HostID == "":
		return HostWaiting
	}
	return HostConnected
}

// Agent is the actor of what the agent itself does in the session: the session token's credential, named after
// the driver.
func (s Session) Agent() auth.Actor {
	id := s.CredentialID
	if id == "" {
		id = s.ID
	}
	return auth.Actor{Kind: auth.KindAgent, ID: id, Name: s.Driver, SessionID: s.ID}
}

// View is a session's JSON form: the contract's AgentSession.
type View struct {
	ID             string      `json:"id"`
	Number         int         `json:"number"`
	ProjectID      string      `json:"projectId"`
	Project        string      `json:"project"`
	Kind           string      `json:"kind"`
	Driver         string      `json:"driver"`
	Model          string      `json:"model"`
	Preset         string      `json:"preset"`
	State          string      `json:"state"`
	Busy           bool        `json:"busy"`
	Turn           int         `json:"turn"`
	PauseReason    *Reason     `json:"pauseReason,omitempty"`
	PendingControl string      `json:"pendingControl,omitempty"`
	HostState      string      `json:"hostState,omitempty"`
	HostLeftAt     *time.Time  `json:"hostLeftAt,omitempty"`
	Error          string      `json:"error,omitempty"`
	Branch         string      `json:"branch"`
	Merge          Merge       `json:"merge"`
	AutoMerge      string      `json:"autoMerge"`
	Budget         Budget      `json:"budget"`
	Use            Use         `json:"use"`
	Prompt         string      `json:"prompt,omitempty"`
	References     []Reference `json:"references"`
	StartedBy      auth.Actor  `json:"startedBy"`
	Rev            int         `json:"rev"`
	CreatedAt      time.Time   `json:"createdAt"`
	UpdatedAt      time.Time   `json:"updatedAt"`
	StartedAt      *time.Time  `json:"startedAt,omitempty"`
	EndedAt        *time.Time  `json:"endedAt,omitempty"`
	LastMessageAt  *time.Time  `json:"lastMessageAt,omitempty"`
}

// JSON renders s as the contract's AgentSession.
func (s Session) JSON() View {
	refs := s.Refs
	if refs == nil {
		refs = []Reference{}
	}
	v := View{
		ID: s.ID, Number: s.Number, ProjectID: s.ProjectID, Project: s.ProjectSlug, Kind: s.Kind, Driver: s.Driver,
		Model: s.Model, Preset: s.Preset, State: s.State, Busy: s.Busy, Turn: s.Turn, PauseReason: s.PauseReason,
		PendingControl: s.PendingControl, HostState: s.HostState(), Error: s.Error, Branch: s.Branch, Merge: s.Merge, AutoMerge: s.AutoMerge,
		Budget: s.Budget, Use: s.Use, Prompt: s.Prompt, References: refs, StartedBy: s.StartedBy, Rev: s.Rev,
		CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt, StartedAt: s.StartedAt, EndedAt: s.EndedAt,
		LastMessageAt: s.LastMessageAt,
	}
	if v.HostState == HostReleased || v.HostState == HostLost {
		v.HostLeftAt = s.HostLeftAt
	}
	return v
}

const cols = `s.id, s.project_id, p.slug, s.number, s.kind, s.driver, s.model, s.preset, s.state, s.busy, s.turn,
	s.pause_reason, coalesce(s.error, ''), s.branch, s.merge, s.auto_merge, s.budget, s.use, coalesce(s.prompt, ''),
	s.refs, s.started_by, coalesce(s.credential_id, ''), coalesce(s.acp_session_id, ''), coalesce(s.host_id, ''),
	s.rev, s.created_at, s.updated_at, s.started_at, s.ended_at, s.last_message_at, s.host_left_at,
	coalesce(s.resume_note, ''), coalesce((SELECT c.action FROM agent_session_controls c WHERE c.session_id = s.id AND c.delivered_at IS NULL
		ORDER BY c.created_at LIMIT 1), '')`

const from = ` FROM agent_sessions s JOIN projects p ON p.id = s.project_id`

func scan(row pgx.CollectableRow) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.ProjectID, &s.ProjectSlug, &s.Number, &s.Kind, &s.Driver, &s.Model, &s.Preset, &s.State,
		&s.Busy, &s.Turn, &s.PauseReason, &s.Error, &s.Branch, &s.Merge, &s.AutoMerge, &s.Budget, &s.Use, &s.Prompt,
		&s.Refs, &s.StartedBy, &s.CredentialID, &s.ACPSessionID, &s.HostID, &s.Rev, &s.CreatedAt, &s.UpdatedAt,
		&s.StartedAt, &s.EndedAt, &s.LastMessageAt, &s.HostLeftAt, &s.ResumeNote, &s.PendingControl)
	return s, err
}

func one(rows pgx.Rows, err error, id string) (Session, error) {
	if err != nil {
		return Session{}, fmt.Errorf("query agent session: %w", err)
	}
	s, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, problems.NotFound.New("no agent session %q", id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("read agent session: %w", err)
	}
	return s, nil
}

// Get returns the session with id, or not-found.
func Get(ctx context.Context, q storage.Querier, id string) (Session, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+from+" WHERE s.id = $1", id)
	return one(rows, err, id)
}

// Lock reads the session for update (the row lock serialises transcript appends of one session).
func Lock(ctx context.Context, tx pgx.Tx, id string) (Session, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+from+" WHERE s.id = $1 FOR UPDATE OF s", id)
	return one(rows, err, id)
}

// List returns a project's sessions: live ones first, then the most recently changed. state filters ("live" for
// every live state).
func List(ctx context.Context, q storage.Querier, projectID, state string, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	where := "s.project_id = $1"
	args := []any{projectID}
	switch state {
	case "":
	case "live":
		where += " AND s.state IN ('created', 'running', 'waiting_approval', 'paused')"
	default:
		args = append(args, state)
		where += " AND s.state = $2"
	}
	args = append(args, limit)
	rows, err := q.Query(ctx, "SELECT "+cols+from+" WHERE "+where+fmt.Sprintf(` ORDER BY
		s.state NOT IN ('created', 'running', 'waiting_approval', 'paused'), s.updated_at DESC, s.id DESC LIMIT $%d`, len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list agent sessions: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("read agent sessions: %w", err)
	}
	return out, nil
}

// changed are the events of a change to the session row: on the list topic and on the session's own topic, so the
// Chat header follows state and use from the one subscription it has.
func changed(s Session, typ string) []events.Draft {
	ref := &events.EntityRef{Kind: Kind, ID: s.ID, Rev: s.Rev}
	payload := map[string]any{"session": s.JSON()}
	return []events.Draft{
		{Topic: TopicList, Type: typ, ProjectID: s.ProjectID, Entity: ref, Payload: payload},
		{Topic: Topic(s.ID), Type: typ, ProjectID: s.ProjectID, Entity: ref, Payload: payload},
	}
}

// Update is a change to the session row; nil fields stay.
type Update struct {
	State         *string
	Busy          *bool
	Turn          *int
	PauseReason   **Reason
	Error         *string
	Merge         *Merge
	Budget        *Budget
	Use           *Use
	CredentialID  *string
	ACPSessionID  *string
	HostID        *string
	Started       bool
	Ended         bool
	LastMessageAt *time.Time
	HostLeftAt    **time.Time
	ResumeNote    *string
}

// apply writes u to the locked session and returns it with its changed events (none when nothing changed).
func apply(ctx context.Context, tx pgx.Tx, cur Session, u Update) (Session, []events.Draft, error) {
	next := cur
	if u.State != nil {
		next.State = *u.State
	}
	if u.Busy != nil {
		next.Busy = *u.Busy
	}
	if u.Turn != nil {
		next.Turn = *u.Turn
	}
	if u.PauseReason != nil {
		next.PauseReason = *u.PauseReason
	}
	if u.Error != nil {
		next.Error = *u.Error
	}
	if u.Merge != nil {
		next.Merge = *u.Merge
	}
	if u.Budget != nil {
		next.Budget = *u.Budget
	}
	if u.Use != nil {
		next.Use = *u.Use
	}
	if u.CredentialID != nil {
		next.CredentialID = *u.CredentialID
	}
	if u.ACPSessionID != nil {
		next.ACPSessionID = *u.ACPSessionID
	}
	if u.HostID != nil {
		next.HostID = *u.HostID
	}
	if u.LastMessageAt != nil {
		next.LastMessageAt = u.LastMessageAt
	}
	if u.HostLeftAt != nil {
		next.HostLeftAt = *u.HostLeftAt
	}
	if u.ResumeNote != nil {
		next.ResumeNote = *u.ResumeNote
	}
	visible := next.State != cur.State || next.Busy != cur.Busy || next.Turn != cur.Turn ||
		!jsonEqual(next.PauseReason, cur.PauseReason) || next.Error != cur.Error || !jsonEqual(next.Merge, cur.Merge) ||
		!jsonEqual(next.Budget, cur.Budget) || !jsonEqual(next.Use, cur.Use) || u.Started || u.Ended ||
		u.LastMessageAt != nil || (next.HostLeftAt == nil) != (cur.HostLeftAt == nil) ||
		(next.HostLeftAt != nil && next.HostID != cur.HostID)
	hidden := next.CredentialID != cur.CredentialID || next.ACPSessionID != cur.ACPSessionID || next.HostID != cur.HostID ||
		next.ResumeNote != cur.ResumeNote
	if !visible && !hidden {
		return cur, nil, nil
	}
	rev := cur.Rev
	if visible {
		rev++
	}
	_, err := tx.Exec(ctx, `UPDATE agent_sessions SET state = $2, busy = $3, turn = $4, pause_reason = $5,
		error = NULLIF($6, ''), merge = $7, budget = $8, use = $9, credential_id = NULLIF($10, ''),
		acp_session_id = NULLIF($11, ''), host_id = NULLIF($12, ''), rev = $13, updated_at = now(),
		started_at = CASE WHEN $14 AND started_at IS NULL THEN now() ELSE started_at END,
		ended_at = CASE WHEN $15 THEN now() ELSE ended_at END,
		last_message_at = coalesce($16, last_message_at), host_left_at = $17, resume_note = NULLIF($18, '')
		WHERE id = $1`,
		cur.ID, next.State, next.Busy, next.Turn, next.PauseReason, next.Error, next.Merge, next.Budget, next.Use,
		next.CredentialID, next.ACPSessionID, next.HostID, rev, u.Started, u.Ended, u.LastMessageAt, next.HostLeftAt,
		next.ResumeNote)
	if err != nil {
		return Session{}, nil, fmt.Errorf("update agent session %s: %w", cur.ID, err)
	}
	s, err := Lock(ctx, tx, cur.ID)
	if err != nil {
		return Session{}, nil, err
	}
	if !visible {
		return s, nil, nil
	}
	return s, changed(s, EventChanged), nil
}

func jsonEqual(a, b any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

func newID(prefix string) string { return prefix + uuid.Must(uuid.NewV7()).String() }

// Service is the session logic that needs more than the database: the project repositories (branches, merges,
// notes), the policy engine's presets (permission answers) and the defaults (budgets, clocks).
type Service struct {
	Pool     *pgxpool.Pool
	Projects *bootstrap.Service
	Policy   *policy.Engine
	Defaults func() *defaults.Defaults
	Log      *slog.Logger
	Now      func() time.Time
	// HostLapse is how long a host may stay silent before its sessions go to another host (default 90 s).
	HostLapse time.Duration
	// ClaimPoll is how often a waiting claim looks for work (default 200 ms).
	ClaimPoll time.Duration
	// Attribution renames a command's synthetic tool-call id in drafts and revisions (the draft store).
	Attribution ToolCallRetagger
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

func (s *Service) hostLapse() time.Duration {
	if s.HostLapse > 0 {
		return s.HostLapse
	}
	return 90 * time.Second
}

func (s *Service) claimPoll() time.Duration {
	if s.ClaimPoll > 0 {
		return s.ClaimPoll
	}
	return 200 * time.Millisecond
}
