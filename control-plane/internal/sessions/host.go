package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mcp"
	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
)

// IdenticalCalls is the runaway rule: the same tool with the same arguments this many times in a row pauses the
// session (docs/spec/05-agents.md "Budgets and runaway protection").
const IdenticalCalls = 3

// ---------------------------------------------------------------- claim

// ClaimInput is one long-poll of an agent host.
type ClaimInput struct {
	HostID       string
	CredentialID string
	Version      string
	Wait         time.Duration
	Capacity     int
}

// Start is a session the host starts (or resumes after another host went silent, or after a pause).
type Start struct {
	Session  View        `json:"session"`
	Token    string      `json:"token"`
	CloneURL string      `json:"cloneUrl"`
	MCPURL   string      `json:"mcpUrl"`
	Budget   Budget      `json:"budget"`
	Clocks   Clocks      `json:"clocks"`
	Resume   *ResumeInfo `json:"resume,omitempty"`
}

// Clocks are the host-side limits of a session (R5 and the runaway rule).
type Clocks struct {
	StuckTurnSeconds int `json:"stuckTurnSeconds"`
	IdenticalCalls   int `json:"identicalCalls"`
}

// ResumeInfo lets the host restore the agent's context: ACP resume of the agent's own session, else a new ACP
// session primed with the summary.
type ResumeInfo struct {
	ACPSessionID string `json:"acpSessionId,omitempty"`
	Summary      string `json:"summary,omitempty"`
}

// Control is a request the host carries out.
type Control struct {
	ID        string      `json:"id"`
	SessionID string      `json:"sessionId"`
	Action    string      `json:"action"`
	Reason    *Reason     `json:"reason,omitempty"`
	Budget    *Budget     `json:"budget,omitempty"`
	Resume    *ResumeInfo `json:"resume,omitempty"`
}

// HostMessage is a user message or notice the agent reads as its next turn.
type HostMessage struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Context   string `json:"context,omitempty"`
}

// Decision answers a permission request.
type Decision struct {
	SessionID  string `json:"sessionId,omitempty"`
	ApprovalID string `json:"approvalId,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	Outcome    string `json:"outcome"` // pending | allow_once | allow_always | reject_once
	Rule       string `json:"rule,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// Work is what one claim hands the host.
type Work struct {
	Start     []Start       `json:"start"`
	Messages  []HostMessage `json:"messages"`
	Controls  []Control     `json:"controls"`
	Decisions []Decision    `json:"decisions"`
}

func (w Work) empty() bool {
	return len(w.Start)+len(w.Messages)+len(w.Controls)+len(w.Decisions) == 0
}

// Claim waits up to in.Wait for work and hands it to the host: sessions nobody runs (new ones, and live ones whose
// host went silent), pending messages, controls and permission decisions of the sessions it runs. Everything a claim
// returns is taken: a host that crashes loses it, and its sessions resume on the next host.
func (s *Service) Claim(ctx context.Context, in ClaimInput) (Work, error) {
	deadline := s.now().Add(in.Wait)
	for {
		var w Work
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			w, err = s.claimOnce(ctx, tx, in)
			return err
		})
		if err != nil || !w.empty() || !s.now().Before(deadline) {
			return w, err
		}
		select {
		case <-ctx.Done():
			return w, nil
		case <-time.After(s.claimPoll()):
		}
	}
}

func (s *Service) claimOnce(ctx context.Context, tx pgx.Tx, in ClaimInput) (Work, error) {
	w := Work{Start: []Start{}, Messages: []HostMessage{}, Controls: []Control{}, Decisions: []Decision{}}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_hosts (id, credential_id, version) VALUES ($1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET seen_at = now(), version = excluded.version`, in.HostID, in.CredentialID, in.Version); err != nil {
		return w, fmt.Errorf("record the agent host: %w", err)
	}
	var drafts []events.Draft
	if in.Capacity > 0 {
		starts, d, err := s.claimStarts(ctx, tx, in)
		if err != nil {
			return w, err
		}
		w.Start, drafts = starts, d
	}
	msgs, d, err := s.claimMessages(ctx, tx, in.HostID)
	if err != nil {
		return w, err
	}
	w.Messages, drafts = msgs, append(drafts, d...)
	if w.Controls, err = s.claimControls(ctx, tx, in.HostID); err != nil {
		return w, err
	}
	if w.Decisions, err = claimDecisions(ctx, tx, in.HostID); err != nil {
		return w, err
	}
	// Lists are never null on the wire.
	if w.Start == nil {
		w.Start = []Start{}
	}
	if w.Decisions == nil {
		w.Decisions = []Decision{}
	}
	return w, events.Append(ctx, tx, System, nil, drafts)
}

func (s *Service) claimStarts(ctx context.Context, tx pgx.Tx, in ClaimInput) ([]Start, []events.Draft, error) {
	lapse := fmt.Sprintf("%d milliseconds", s.hostLapse().Milliseconds())
	rows, err := tx.Query(ctx, `SELECT s.id FROM agent_sessions s LEFT JOIN agent_hosts h ON h.id = s.host_id
		WHERE (s.host_id IS NULL OR h.seen_at IS NULL OR h.seen_at < now() - $2::interval) AND s.host_id IS DISTINCT FROM $1
		AND (s.state IN ('created', 'running', 'waiting_approval')
			OR (s.state = 'paused' AND EXISTS (SELECT 1 FROM agent_session_controls c WHERE c.session_id = s.id
				AND c.action = 'resume' AND c.delivered_at IS NULL)))
		ORDER BY s.created_at LIMIT $3 FOR UPDATE OF s SKIP LOCKED`, in.HostID, lapse, in.Capacity)
	if err != nil {
		return nil, nil, fmt.Errorf("find sessions to start: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, fmt.Errorf("read sessions to start: %w", err)
	}
	var (
		out    []Start
		drafts []events.Draft
	)
	d := s.defaults()
	for _, id := range ids {
		sess, err := Lock(ctx, tx, id)
		if err != nil {
			return nil, nil, err
		}
		if sess.CredentialID != "" {
			_ = credentials.Revoke(ctx, tx, sess.CredentialID) // the old host's token dies with it
		}
		tok, credID, err := credentials.MintAgentToken(ctx, tx, sess.ID, sess.ProjectID, sess.Preset)
		if err != nil {
			return nil, nil, err
		}
		previous := sess.HostID
		f := false
		u := Update{CredentialID: &credID, HostID: &in.HostID, Busy: &f}
		st := Start{Token: tok, CloneURL: projects.CloneURL(sess.ProjectSlug), MCPURL: mcp.Path, Budget: sess.Budget,
			Clocks: Clocks{StuckTurnSeconds: d.Timeouts.StuckTurnMinutes.Value * 60, IdenticalCalls: IdenticalCalls}}
		if sess.State == StatePaused {
			// A resume nobody could deliver (the host that paused it is gone): take it here.
			var budget *Budget
			if err := tx.QueryRow(ctx, `UPDATE agent_session_controls SET delivered_at = now()
				WHERE session_id = $1 AND action = 'resume' AND delivered_at IS NULL RETURNING budget`, sess.ID).Scan(&budget); err != nil {
				return nil, nil, fmt.Errorf("take the resume of %s: %w", sess.ID, err)
			}
			if budget != nil {
				u.Budget, st.Budget = budget, *budget
			}
		}
		if sess.State != StateCreated {
			sum, err := summary(ctx, tx, sess)
			if err != nil {
				return nil, nil, err
			}
			st.Resume = &ResumeInfo{ACPSessionID: sess.ACPSessionID, Summary: sum}
		}
		next, more, err := apply(ctx, tx, sess, u)
		if err != nil {
			return nil, nil, err
		}
		drafts = append(drafts, more...)
		msg := "The agent host " + in.HostID + " took the session"
		if previous != "" {
			msg = fmt.Sprintf("The agent host %s resumes the session: its host %s went silent", in.HostID, previous)
		}
		if n, err := notice(ctx, tx, next, newID("host:"), "info", msg, false); err != nil {
			return nil, nil, err
		} else if n != nil {
			drafts = append(drafts, *n)
		}
		st.Session = next.JSON()
		out = append(out, st)
	}
	return out, drafts, nil
}

// summary is what a new ACP session is told when the agent's own session cannot be resumed: the turns so far, the
// user's messages and the agent's last answers.
func summary(ctx context.Context, tx pgx.Tx, sess Session) (string, error) {
	rows, err := tx.Query(ctx, `SELECT kind, body->>'text' FROM agent_messages WHERE session_id = $1
		AND kind IN ('user_message', 'agent_message') AND coalesce(body->>'text', '') <> '' ORDER BY seq DESC LIMIT 12`, sess.ID)
	if err != nil {
		return "", fmt.Errorf("summarise session %s: %w", sess.ID, err)
	}
	type line struct{ kind, text string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (line, error) {
		var l line
		return l, r.Scan(&l.kind, &l.text)
	})
	if err != nil {
		return "", fmt.Errorf("summarise session %s: %w", sess.ID, err)
	}
	if len(list) == 0 {
		return "", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "This continues Cadence agent session %d after a restart (%d turns so far). The conversation so far, oldest first:\n",
		sess.Number, sess.Use.Turns)
	for i := len(list) - 1; i >= 0; i-- {
		who := "User"
		if list[i].kind == EntryAgentMessage {
			who = "You"
		}
		t := list[i].text
		if len(t) > 600 {
			t = t[:600] + "…"
		}
		fmt.Fprintf(&b, "%s: %s\n", who, t)
	}
	b.WriteString("Your earlier file changes are committed on this branch; NOTES.md has what earlier sessions learned.")
	return b.String(), nil
}

func (s *Service) claimMessages(ctx context.Context, tx pgx.Tx, hostID string) ([]HostMessage, []events.Draft, error) {
	rows, err := tx.Query(ctx, `UPDATE agent_messages m SET delivery = 'delivered', rev = m.rev + 1, updated_at = now()
		FROM agent_sessions s WHERE m.session_id = s.id AND m.delivery = 'pending' AND s.host_id = $1
		AND s.state IN ('running', 'waiting_approval')
		RETURNING m.id, m.session_id, m.project_id, m.seq, m.key, m.kind, m.turn, m.actor, m.body, m.delivery, m.rev,
			m.created_at, m.updated_at`, hostID)
	if err != nil {
		return nil, nil, fmt.Errorf("take messages: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanMessage)
	if err != nil {
		return nil, nil, fmt.Errorf("read messages: %w", err)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].SessionID != list[j].SessionID {
			return list[i].SessionID < list[j].SessionID
		}
		return list[i].Seq < list[j].Seq
	})
	out := make([]HostMessage, 0, len(list))
	drafts := make([]events.Draft, 0, len(list))
	for _, m := range list {
		text, _ := m.Body["text"].(string)
		ctxBlock, _ := m.Body["context"].(string)
		out = append(out, HostMessage{ID: m.ID, SessionID: m.SessionID, Kind: m.Kind, Text: text, Context: ctxBlock})
		drafts = append(drafts, messageEvent(m, EventMessageUpdated))
	}
	return out, drafts, nil
}

func (s *Service) claimControls(ctx context.Context, tx pgx.Tx, hostID string) ([]Control, error) {
	rows, err := tx.Query(ctx, `UPDATE agent_session_controls c SET delivered_at = now() FROM agent_sessions s
		WHERE c.session_id = s.id AND s.host_id = $1 AND c.delivered_at IS NULL
		RETURNING c.id, c.session_id, c.action, c.reason, c.budget, c.created_at`, hostID)
	if err != nil {
		return nil, fmt.Errorf("take controls: %w", err)
	}
	type row struct {
		c  Control
		at time.Time
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.c.ID, &x.c.SessionID, &x.c.Action, &x.c.Reason, &x.c.Budget, &x.at)
	})
	if err != nil {
		return nil, fmt.Errorf("read controls: %w", err)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	out := make([]Control, 0, len(list))
	for _, x := range list {
		if x.c.Action == "resume" {
			sess, err := Get(ctx, tx, x.c.SessionID)
			if err != nil {
				return nil, err
			}
			sum, err := summary(ctx, tx, sess)
			if err != nil {
				return nil, err
			}
			x.c.Resume = &ResumeInfo{ACPSessionID: sess.ACPSessionID, Summary: sum}
		}
		out = append(out, x.c)
	}
	return out, nil
}

func claimDecisions(ctx context.Context, tx pgx.Tx, hostID string) ([]Decision, error) {
	rows, err := tx.Query(ctx, `UPDATE approvals a SET delivered_at = now() FROM agent_sessions s
		WHERE a.session_id = s.id AND s.host_id = $1 AND a.kind = 'agent_permission' AND a.state <> 'pending'
		AND a.delivered_at IS NULL
		RETURNING a.id, a.session_id, a.state, coalesce(a.grant_scope, 'once'), coalesce(a.permission->>'toolCallId', ''),
			coalesce(a.note, '')`, hostID)
	if err != nil {
		return nil, fmt.Errorf("take decisions: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Decision, error) {
		var (
			d            Decision
			state, grant string
		)
		err := r.Scan(&d.ApprovalID, &d.SessionID, &state, &grant, &d.ToolCallID, &d.Reason)
		d.Outcome = outcome(state, grant)
		return d, err
	})
}

func outcome(state, grant string) string {
	switch {
	case state == approvals.StatePending:
		return "pending"
	case state == approvals.StateApproved && grant == approvals.GrantSession:
		return "allow_always"
	case state == approvals.StateApproved:
		return "allow_once"
	}
	return "reject_once"
}

// ---------------------------------------------------------------- report

// HostEntry is a transcript entry the host reports.
type HostEntry struct {
	Key  string
	Kind string
	Turn int
	Body map[string]any
}

// StateReport is the state the host reports.
type StateReport struct {
	State  string
	Busy   *bool
	Turn   *int
	Reason *Reason
	Error  string
}

// ReportInput is one host report.
type ReportInput struct {
	HostID       string
	Entries      []HostEntry
	State        *StateReport
	Use          *Use
	ACPSessionID string
	Note         string
	Withdraw     []string
}

// hostKinds are the entry kinds a host may report; user messages and permission entries are the server's.
var hostKinds = map[string]bool{EntryNotice: true, EntryAgentMessage: true, EntryThought: true, EntryPlan: true,
	EntryToolCall: true, EntryTurn: true, EntryCommit: true}

// Report applies a host report in one transaction: entries (upserts by key), use, the ACP session id, state
// transitions (a session that ends revokes its token and merges per the auto-merge policy), a note for the next
// session, and permission requests the agent withdrew.
func (s *Service) Report(ctx context.Context, id string, in ReportInput) (Session, error) {
	var out Session
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		sess, err := Lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if sess.HostID != in.HostID {
			return problems.Conflict.New("agent session %s runs on host %q, not %q", id, sess.HostID, in.HostID)
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_hosts SET seen_at = now() WHERE id = $1`, in.HostID); err != nil {
			return fmt.Errorf("touch the agent host: %w", err)
		}
		var agentDrafts, systemDrafts []events.Draft
		for _, e := range in.Entries {
			if !hostKinds[e.Kind] {
				return problems.BadRequest.New("a host does not report %s entries", e.Kind)
			}
			actor := sess.Agent()
			if e.Kind == EntryNotice {
				actor = System
			}
			_, d, err := upsert(ctx, tx, sess, entry{Key: "h:" + e.Key, Kind: e.Kind, Turn: e.Turn, Actor: actor, Body: e.Body})
			if err != nil {
				return err
			}
			if d != nil {
				agentDrafts = append(agentDrafts, *d)
			}
		}
		u := Update{}
		if in.Use != nil {
			u.Use = in.Use
		}
		if in.ACPSessionID != "" {
			u.ACPSessionID = &in.ACPSessionID
		}
		if st := in.State; st != nil {
			u.Busy, u.Turn = st.Busy, st.Turn
		}
		sess, d, err := apply(ctx, tx, sess, u)
		if err != nil {
			return err
		}
		agentDrafts = append(agentDrafts, d...)
		if in.Note != "" {
			if d, err := s.note(ctx, tx, sess, in.Note); err != nil {
				s.log().WarnContext(ctx, "write the note for the next session", "session", id, "err", err)
			} else {
				systemDrafts = append(systemDrafts, d...)
			}
		}
		for _, apr := range in.Withdraw {
			d, err := s.withdraw(ctx, tx, sess, apr)
			if err != nil {
				return err
			}
			systemDrafts = append(systemDrafts, d...)
		}
		if st := in.State; st != nil {
			if sess, d, err = s.transition(ctx, tx, sess, *st); err != nil {
				return err
			}
			systemDrafts = append(systemDrafts, d...)
		}
		if in.Use != nil && Live(sess.State) {
			d, err := s.checkProjectTokens(ctx, tx, sess)
			if err != nil {
				return err
			}
			systemDrafts = append(systemDrafts, d...)
		}
		if err := events.Append(ctx, tx, sess.Agent(), nil, agentDrafts); err != nil {
			return err
		}
		if err := events.Append(ctx, tx, System, nil, systemDrafts); err != nil {
			return err
		}
		out = sess
		return nil
	})
	return out, err
}

// transition applies a state the host reports.
func (s *Service) transition(ctx context.Context, tx pgx.Tx, sess Session, st StateReport) (Session, []events.Draft, error) {
	if !Live(sess.State) {
		if st.State == sess.State {
			return sess, nil, nil
		}
		return sess, nil, problems.Conflict.New("agent session %s already ended (%s)", sess.ID, sess.State)
	}
	switch st.State {
	case StateRunning:
		state := StateRunning
		var nr *Reason
		next, d, err := apply(ctx, tx, sess, Update{State: &state, PauseReason: &nr, Started: true})
		if err != nil {
			return Session{}, nil, err
		}
		more, d2, err := syncApprovals(ctx, tx, next)
		return more, append(d, d2...), err
	case StatePaused:
		state, reason := StatePaused, st.Reason
		if reason == nil {
			reason = &Reason{Code: PauseUser, Message: "paused"}
		}
		f := false
		return apply(ctx, tx, sess, Update{State: &state, PauseReason: &reason, Busy: &f})
	case StateDone, StateCancelled, StateFailed:
		return s.finish(ctx, tx, sess, st.State, st.Error, System)
	}
	return Session{}, nil, problems.BadRequest.New("a host cannot report state %q", st.State)
}

// note files text in NOTES.md for the next session (projects.note), as Cadence.
func (s *Service) note(ctx context.Context, tx pgx.Tx, sess Session, text string) ([]events.Draft, error) {
	p, err := projects.GetByID(ctx, tx, sess.ProjectID)
	if err != nil {
		return nil, err
	}
	_, drafts, err := s.Projects.AddNote(ctx, tx, p, fmt.Sprintf("Agent session %d (%s): %s", sess.Number, sess.ID, text), System, false)
	return drafts, err
}

// withdraw denies an agent-permission approval the agent stopped waiting for.
func (s *Service) withdraw(ctx context.Context, tx pgx.Tx, sess Session, id string) ([]events.Draft, error) {
	a, err := approvals.Get(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if a.Actor.SessionID != sess.ID || a.Kind != approvals.KindAgentPermission || a.State != approvals.StatePending {
		return nil, nil
	}
	a, err = approvals.Lock(ctx, tx, id, a.Rev)
	if err != nil {
		return nil, err
	}
	a, drafts, err := approvals.Decide(ctx, tx, a, approvals.Decision{By: System,
		Note: "the agent stopped waiting: its turn was cancelled"})
	if err != nil {
		return nil, err
	}
	more, err := s.ApprovalDecided(ctx, tx, a)
	return append(drafts, more...), err
}

// checkProjectTokens pauses the session when the project spent its agent tokens for today.
func (s *Service) checkProjectTokens(ctx context.Context, tx pgx.Tx, sess Session) ([]events.Draft, error) {
	if sess.State != StateRunning && sess.State != StateWaitingApproval {
		return nil, nil
	}
	p, err := projects.GetByID(ctx, tx, sess.ProjectID)
	if err != nil {
		return nil, err
	}
	var used int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(coalesce((body->'turnInfo'->>'inputTokens')::bigint, 0)
		+ coalesce((body->'turnInfo'->>'outputTokens')::bigint, 0)), 0)
		FROM agent_messages WHERE project_id = $1 AND kind = 'turn' AND created_at >= date_trunc('day', now())`,
		sess.ProjectID).Scan(&used); err != nil {
		return nil, fmt.Errorf("read today's agent tokens: %w", err)
	}
	if p.Budgets.AgentTokensPerDay <= 0 || used < p.Budgets.AgentTokensPerDay || sess.PendingControl == "pause" {
		return nil, nil
	}
	_, drafts, err := s.request(ctx, tx, sess, "pause", &Reason{Code: PauseProjectTokens, Message: fmt.Sprintf(
		"the project used %d of its %d agent tokens for today", used, p.Budgets.AgentTokensPerDay)}, nil, System)
	return drafts, err
}

// ---------------------------------------------------------------- permissions

// AskInput is an ACP permission request the host forwards.
type AskInput struct {
	HostID   string
	Turn     int
	ToolCall map[string]any
	Options  []map[string]any
}

// operationOf turns an MCP tool name as agents report it back into the operation: Claude Code and opencode replace
// the dot of <entity>.<verb> with an underscore (mixes_get), and verbs have no underscores.
func operationOf(tool string) string {
	if tool == "" || strings.Contains(tool, ".") {
		return tool
	}
	if i := strings.LastIndex(tool, "_"); i > 0 {
		return tool[:i] + "." + tool[i+1:]
	}
	return tool
}

// manifestVerbClass maps MCP tool names to their verb class (read for read-only tools).
func manifestVerbClass(op string) string {
	m, err := mcp.LoadManifest()
	if err != nil {
		return "mutate"
	}
	for _, t := range m.Tools {
		if t.Name == op {
			if t.Annotations.ReadOnlyHint {
				return "read"
			}
			return "mutate"
		}
	}
	return "mutate"
}

// Ask answers an ACP permission request: the session's preset decides what it already decides (allow or deny, as a
// transcript entry naming the rule); anything else becomes an agent-permission approval a person decides in the
// Approvals panel or the Chat card, and the session waits for it (waiting_approval).
func (s *Service) Ask(ctx context.Context, id string, in AskInput) (Decision, error) {
	var out Decision
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		sess, err := Lock(ctx, tx, id)
		if err != nil {
			return err
		}
		if sess.HostID != in.HostID {
			return problems.Conflict.New("agent session %s runs on host %q, not %q", id, sess.HostID, in.HostID)
		}
		if !Live(sess.State) {
			return problems.Conflict.New("agent session %s already ended (%s)", id, sess.State)
		}
		str := func(k string) string { v, _ := in.ToolCall[k].(string); return v }
		toolCallID, class, title := str("id"), str("class"), str("title")
		req := policy.PermissionRequest{Class: class, Operation: operationOf(str("operation"))}
		if req.Operation != "" {
			req.VerbClass = manifestVerbClass(req.Operation)
		}
		if sh, ok := in.ToolCall["shell"].(map[string]any); ok {
			req.Command, _ = sh["command"].(string)
		}
		if locs, ok := in.ToolCall["locations"].([]any); ok {
			for _, l := range locs {
				if p, ok := l.(string); ok {
					req.Paths = append(req.Paths, p)
				}
			}
		}
		preset, ok := s.Policy.Preset(sess.Preset)
		if !ok {
			return fmt.Errorf("session %s names the unknown preset %q", id, sess.Preset)
		}
		ans := preset.AnswerPermission(req)
		kinds := make([]string, 0, len(in.Options))
		for _, o := range in.Options {
			if k, ok := o["kind"].(string); ok {
				kinds = append(kinds, k)
			}
		}
		perm := map[string]any{"source": "agent", "toolCallId": toolCallID, "title": title, "class": class,
			"options": in.Options, "rule": ans.Rule, "note": ans.Reason}
		var drafts []events.Draft
		switch ans.Access {
		case policy.AccessAllow, policy.AccessDeny:
			out = Decision{SessionID: id, ToolCallID: toolCallID, Outcome: "allow_once", Rule: ans.Rule, Reason: ans.Reason}
			perm["state"] = approvals.StateApproved
			if ans.Access == policy.AccessDeny {
				out.Outcome, perm["state"] = "reject_once", approvals.StateDenied
			}
			_, d, err := upsert(ctx, tx, sess, entry{Key: newID("perm:"), Kind: EntryPermission, Turn: in.Turn, Actor: System,
				Body: map[string]any{"permission": perm}})
			if err != nil {
				return err
			}
			if d != nil {
				drafts = append(drafts, *d)
			}
		default:
			body, err := json.Marshal(map[string]any{"toolCall": in.ToolCall, "options": in.Options})
			if err != nil {
				return fmt.Errorf("marshal the permission request: %w", err)
			}
			info := map[string]any{"sessionId": id, "toolCallId": toolCallID, "title": title, "class": class,
				"options": kinds}
			if req.Command != "" {
				info["command"] = req.Command
			}
			if len(req.Paths) > 0 {
				info["paths"] = req.Paths
			}
			a, ad, err := approvals.Create(ctx, tx, approvals.NewInput{
				Operation: "agent." + class, ProjectID: sess.ProjectID, Actor: sess.Agent(),
				Scope:    policy.Scope{ProjectID: sess.ProjectID, RegistryRead: true, Preset: sess.Preset},
				Decision: policy.Decision{Outcome: policy.Approval, Rule: ans.Rule, Preset: sess.Preset, Reason: ans.Reason},
				Request: approvals.Request{Method: "ACP", Path: "session/request_permission", Header: map[string]string{},
					Body: body},
				Kind: approvals.KindAgentPermission, Permission: info,
			})
			if err != nil {
				return err
			}
			drafts = append(drafts, ad...)
			perm["approvalId"], perm["state"] = a.ID, approvals.StatePending
			_, d, err := upsert(ctx, tx, sess, entry{Key: "perm:" + a.ID, Kind: EntryPermission, Turn: in.Turn, Actor: sess.Agent(),
				Body: map[string]any{"permission": perm}})
			if err != nil {
				return err
			}
			if d != nil {
				drafts = append(drafts, *d)
			}
			_, more, err := syncApprovals(ctx, tx, sess)
			if err != nil {
				return err
			}
			drafts = append(drafts, more...)
			out = Decision{SessionID: id, ApprovalID: a.ID, ToolCallID: toolCallID, Outcome: "pending", Rule: ans.Rule, Reason: ans.Reason}
		}
		return events.Append(ctx, tx, sess.Agent(), nil, drafts)
	})
	return out, err
}

// Decision reads the decision on an agent-permission approval of session id, waiting up to wait for it; a decided
// one is marked delivered.
func (s *Service) Decision(ctx context.Context, id, hostID, approvalID string, wait time.Duration) (Decision, error) {
	deadline := s.now().Add(wait)
	for {
		var d Decision
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			sess, err := Get(ctx, tx, id)
			if err != nil {
				return err
			}
			if sess.HostID != hostID {
				return problems.Conflict.New("agent session %s runs on host %q, not %q", id, sess.HostID, hostID)
			}
			a, err := approvals.Get(ctx, tx, approvalID)
			if err != nil {
				return err
			}
			if a.Actor.SessionID != id || a.Kind != approvals.KindAgentPermission {
				return problems.NotFound.New("no permission request %s in session %s", approvalID, id)
			}
			tc, _ := a.Permission["toolCallId"].(string)
			d = Decision{SessionID: id, ApprovalID: a.ID, ToolCallID: tc, Outcome: outcome(a.State, a.Grant), Reason: a.Note}
			if a.State != approvals.StatePending {
				_, err = tx.Exec(ctx, `UPDATE approvals SET delivered_at = coalesce(delivered_at, now()) WHERE id = $1`, a.ID)
			}
			return err
		})
		if err != nil || d.Outcome != "pending" || !s.now().Before(deadline) {
			return d, err
		}
		select {
		case <-ctx.Done():
			return d, nil
		case <-time.After(s.claimPoll()):
		}
	}
}

// ---------------------------------------------------------------- approvals of sessions

// syncApprovals moves a session between running and waiting_approval as its pending approvals come and go
// (a pending approval never pauses a session, R5).
func syncApprovals(ctx context.Context, tx pgx.Tx, sess Session) (Session, []events.Draft, error) {
	if sess.State != StateRunning && sess.State != StateWaitingApproval {
		return sess, nil, nil
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE session_id = $1 AND state = 'pending'`, sess.ID).Scan(&pending); err != nil {
		return Session{}, nil, fmt.Errorf("count pending approvals: %w", err)
	}
	state := StateRunning
	if pending > 0 {
		state = StateWaitingApproval
	}
	return apply(ctx, tx, sess, Update{State: &state})
}

// GatedCommand records a gated command an agent session sent (the pipeline answered 202 {approvalId}): a permission
// entry next to the tool call and the waiting_approval state. Sessions it does not know (test actors) are skipped.
func (s *Service) GatedCommand(ctx context.Context, tx pgx.Tx, a approvals.Approval, toolCallID string) ([]events.Draft, error) {
	if a.Actor.SessionID == "" {
		return nil, nil
	}
	sess, err := Lock(ctx, tx, a.Actor.SessionID)
	var pe *problems.Error
	if errors.As(err, &pe) && pe.Type == problems.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	perm := map[string]any{"source": "command", "approvalId": a.ID, "operation": a.Operation, "title": a.Operation,
		"state": approvals.StatePending, "rule": a.Rule, "note": a.Reason}
	if toolCallID != "" {
		perm["toolCallId"] = toolCallID
	}
	_, d, err := upsert(ctx, tx, sess, entry{Key: "perm:" + a.ID, Kind: EntryPermission, Turn: sess.Turn, Actor: sess.Agent(),
		Body: map[string]any{"permission": perm}})
	if err != nil {
		return nil, err
	}
	var drafts []events.Draft
	if d != nil {
		drafts = append(drafts, *d)
	}
	_, more, err := syncApprovals(ctx, tx, sess)
	return append(drafts, more...), err
}

// ApprovalDecided brings a decision to the session that asked: the permission entry shows it, the session leaves
// waiting_approval when nothing else is pending, and a decided gated command is told to the agent as a notice (its
// next turn), since the agent does not follow the approvals topic.
func (s *Service) ApprovalDecided(ctx context.Context, tx pgx.Tx, a approvals.Approval) ([]events.Draft, error) {
	if a.Actor.SessionID == "" {
		return nil, nil
	}
	sess, err := Lock(ctx, tx, a.Actor.SessionID)
	var pe *problems.Error
	if errors.As(err, &pe) && pe.Type == problems.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var drafts []events.Draft
	if m, ok, err := entryByKey(ctx, tx, sess.ID, "perm:"+a.ID); err != nil {
		return nil, err
	} else if ok {
		perm, _ := m.Body["permission"].(map[string]any)
		if perm == nil {
			perm = map[string]any{}
		}
		perm["state"], perm["grant"] = a.State, a.Grant
		if a.DecidedBy != nil {
			perm["decidedBy"] = a.DecidedBy
		}
		if a.Note != "" {
			perm["note"] = a.Note
		}
		_, d, err := upsert(ctx, tx, sess, entry{Key: m.Key, Kind: m.Kind, Turn: m.Turn, Actor: m.Actor,
			Body: map[string]any{"permission": perm}})
		if err != nil {
			return nil, err
		}
		if d != nil {
			drafts = append(drafts, *d)
		}
	}
	if a.Kind == approvals.KindCommand && Live(sess.State) && sess.Kind == KindInteractive {
		text := fmt.Sprintf("Approval %s for %s was %s", a.ID, a.Operation, a.State)
		if a.DecidedBy != nil {
			text += " by " + actorName(*a.DecidedBy)
		}
		if a.Result != nil {
			text += fmt.Sprintf("; the command ran and answered HTTP %d (approvals.get id=%s has the result)", a.Result.Status, a.ID)
		}
		if a.Note != "" {
			text += ". Note: " + a.Note
		}
		if d, err := notice(ctx, tx, sess, "decided:"+a.ID, "info", text+".", true); err != nil {
			return nil, err
		} else if d != nil {
			drafts = append(drafts, *d)
		}
	}
	_, more, err := syncApprovals(ctx, tx, sess)
	return append(drafts, more...), err
}

// ---------------------------------------------------------------- the idle clock and housekeeping

// Sweep runs the server-side clocks: an interactive session idle (no turn, no pending approval) since its last
// user message for timeouts.idle_session_minutes pauses (R5), and sessions whose approvals expired leave
// waiting_approval.
func (s *Service) Sweep(ctx context.Context) error {
	idle := time.Duration(s.defaults().Timeouts.IdleSessionMinutes.Value) * time.Minute
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT s.id FROM agent_sessions s WHERE s.state = 'running' AND NOT s.busy
			AND s.kind = 'interactive' AND coalesce(s.last_message_at, s.started_at, s.created_at) < $1
			AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.session_id = s.id AND a.state = 'pending')
			AND NOT EXISTS (SELECT 1 FROM agent_session_controls c WHERE c.session_id = s.id AND c.delivered_at IS NULL)
			AND NOT EXISTS (SELECT 1 FROM agent_messages m WHERE m.session_id = s.id AND m.delivery = 'pending')
			FOR UPDATE OF s SKIP LOCKED`, s.now().Add(-idle))
		if err != nil {
			return fmt.Errorf("find idle sessions: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read idle sessions: %w", err)
		}
		var drafts []events.Draft
		for _, id := range ids {
			sess, err := Lock(ctx, tx, id)
			if err != nil {
				return err
			}
			_, d, err := s.request(ctx, tx, sess, "pause", &Reason{Code: PauseIdle,
				Message: fmt.Sprintf("no message for %d min", int(idle.Minutes()))}, nil, System)
			if err != nil {
				return err
			}
			drafts = append(drafts, d...)
		}
		rows, err = tx.Query(ctx, `SELECT s.id FROM agent_sessions s WHERE s.state = 'waiting_approval'
			AND NOT EXISTS (SELECT 1 FROM approvals a WHERE a.session_id = s.id AND a.state = 'pending')
			FOR UPDATE OF s SKIP LOCKED`)
		if err != nil {
			return fmt.Errorf("find sessions whose approvals expired: %w", err)
		}
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return fmt.Errorf("read sessions whose approvals expired: %w", err)
		}
		for _, id := range ids {
			sess, err := Lock(ctx, tx, id)
			if err != nil {
				return err
			}
			_, d, err := syncApprovals(ctx, tx, sess)
			if err != nil {
				return err
			}
			drafts = append(drafts, d...)
		}
		return events.Append(ctx, tx, System, nil, drafts)
	})
}

// principalHost reports whether p may speak the host protocol: the agent host credential, or the fixed actor of
// tests and development (fixed is true then).
func principalHost(p auth.Principal, fixed bool) bool {
	return fixed || p.CredentialKind == credentials.KindAgentHost
}

// CheckHost fails unless the request comes from the agent host credential (fixed allows the fixed actor of tests
// and development).
func CheckHost(ctx context.Context, fixed bool) (auth.Principal, error) {
	p, ok := auth.PrincipalFromContext(ctx)
	if !ok {
		return auth.Principal{}, problems.Unauthenticated.New("send the agent host token (cah_…) as a Bearer token")
	}
	if !principalHost(p, fixed) {
		return auth.Principal{}, problems.Forbidden.New("only the agent host (a cah_… token) speaks the host protocol")
	}
	return p, nil
}
