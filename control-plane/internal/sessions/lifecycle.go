package sessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/audit"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// NewInput is a session to create.
type NewInput struct {
	Project   projects.Project
	Kind      string
	Driver    string
	Model     string
	Preset    string
	AutoMerge string
	Budget    Budget
	Prompt    string
	Refs      []Reference
	StartedBy auth.Actor
	DryRun    bool
}

// numberLockClass is the first key of the advisory lock that numbers a project's sessions.
const numberLockClass int32 = 0x63646e02

// Create inserts a session in state created, its branch session/<id> off main (interactive sessions, not in a dry
// run) and the first user message; it returns the session with its events.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, in NewInput) (Session, []events.Draft, error) {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, hashtext($2))", numberLockClass, in.Project.ID); err != nil {
		return Session{}, nil, fmt.Errorf("lock session numbers: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(number), 0) + 1 FROM agent_sessions WHERE project_id = $1`, in.Project.ID).Scan(&n); err != nil {
		return Session{}, nil, fmt.Errorf("number the session: %w", err)
	}
	id := newID("ses_")
	branch := ""
	if in.Kind == KindInteractive {
		branch = repos.SessionBranch(id)
	}
	refs := in.Refs
	if refs == nil {
		refs = []Reference{}
	}
	var lastMsg *time.Time
	if in.Prompt != "" {
		now := s.now()
		lastMsg = &now
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_sessions (id, project_id, number, kind, driver, model, preset, branch,
		auto_merge, budget, prompt, refs, started_by, last_message_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, ''), $12, $13, $14)`,
		id, in.Project.ID, n, in.Kind, in.Driver, in.Model, in.Preset, branch, in.AutoMerge, in.Budget, in.Prompt, refs,
		in.StartedBy, lastMsg); err != nil {
		return Session{}, nil, fmt.Errorf("create agent session: %w", err)
	}
	sess, err := Lock(ctx, tx, id)
	if err != nil {
		return Session{}, nil, err
	}
	drafts := changed(sess, EventCreated)
	if in.Prompt != "" {
		_, d, err := s.post(ctx, tx, sess, in.Prompt, refs, in.StartedBy)
		if err != nil {
			return Session{}, nil, err
		}
		drafts = append(drafts, d...)
	}
	if branch != "" && !in.DryRun {
		if _, err := s.Projects.Repos().CreateBranch(ctx, in.Project.Slug, branch, ""); err != nil {
			return Session{}, nil, fmt.Errorf("create %s: %w", branch, err)
		}
	}
	return sess, drafts, nil
}

// post appends a user message (delivery pending) with its references expanded, and moves the idle clock.
func (s *Service) post(ctx context.Context, tx pgx.Tx, sess Session, text string, refs []Reference, by auth.Actor) (Message, []events.Draft, error) {
	block, err := ExpandReferences(ctx, tx, sess.ProjectID, refs)
	if err != nil {
		return Message{}, nil, err
	}
	body := map[string]any{"text": text, "references": refs}
	if block != "" {
		body["context"] = block
	}
	m, d, err := upsert(ctx, tx, sess, entry{Key: newID("user:"), Kind: EntryUserMessage, Turn: sess.Turn, Actor: by,
		Body: body, Delivery: DeliveryPending})
	if err != nil {
		return Message{}, nil, err
	}
	now := s.now()
	_, more, err := apply(ctx, tx, sess, Update{LastMessageAt: &now})
	if err != nil {
		return Message{}, nil, err
	}
	return m, append([]events.Draft{*d}, more...), nil
}

// Post is agentMessages.new: a user message for a live session. A session paused for any reason but its budget
// resumes; a read-only session takes one message.
func (s *Service) Post(ctx context.Context, tx pgx.Tx, id, text string, refs []Reference, by auth.Actor) (Message, []events.Draft, error) {
	sess, err := Lock(ctx, tx, id)
	if err != nil {
		return Message{}, nil, err
	}
	if !Live(sess.State) {
		return Message{}, nil, problems.Conflict.New("agent session %s is %s; start a new session", id, sess.State)
	}
	if sess.Kind == KindReadOnly {
		return Message{}, nil, problems.Conflict.New("read-only session %s takes one message; start a new session", id)
	}
	var drafts []events.Draft
	if sess.State == StatePaused {
		if budgetPause(sess.PauseReason) {
			return Message{}, nil, problems.Conflict.New("agent session %s paused on its budget (%s); resume it with a larger budget (agentSessions.resume)",
				id, sess.PauseReason.Message)
		}
		if sess.PendingControl != "resume" {
			if sess, drafts, err = s.request(ctx, tx, sess, "resume", nil, nil, by); err != nil {
				return Message{}, nil, err
			}
		}
	}
	m, more, err := s.post(ctx, tx, sess, text, refs, by)
	if err != nil {
		return Message{}, nil, err
	}
	return m, append(drafts, more...), nil
}

func budgetPause(r *Reason) bool {
	if r == nil {
		return false
	}
	switch r.Code {
	case PauseBudgetTurns, PauseBudgetTokens, PauseProjectTokens:
		return true
	}
	return false
}

// request queues a control for the host and bumps the session (pendingControl is visible).
func (s *Service) request(ctx context.Context, tx pgx.Tx, sess Session, action string, reason *Reason, budget *Budget, by auth.Actor) (Session, []events.Draft, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO agent_session_controls (id, session_id, action, reason, budget, actor)
		VALUES ($1, $2, $3, $4, $5, $6)`, newID("ctl_"), sess.ID, action, reason, budget, by); err != nil {
		return Session{}, nil, fmt.Errorf("queue %s for session %s: %w", action, sess.ID, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_sessions SET rev = rev + 1, updated_at = now() WHERE id = $1`, sess.ID); err != nil {
		return Session{}, nil, fmt.Errorf("touch session %s: %w", sess.ID, err)
	}
	next, err := Lock(ctx, tx, sess.ID)
	if err != nil {
		return Session{}, nil, err
	}
	return next, changed(next, EventChanged), nil
}

// hostAlive reports whether the session's host claimed or reported within the lapse.
func (s *Service) hostAlive(ctx context.Context, q pgx.Tx, sess Session) (bool, error) {
	if sess.HostID == "" {
		return false, nil
	}
	var seen time.Time
	err := q.QueryRow(ctx, `SELECT seen_at FROM agent_hosts WHERE id = $1`, sess.HostID).Scan(&seen)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read agent host: %w", err)
	}
	return s.now().Sub(seen) < s.hostLapse(), nil
}

// Cancel is agentSessions.cancel: stop the current turn; with end, end the session (done) — at once when no host
// runs it, else through the host, which commits the last changes first.
func (s *Service) Cancel(ctx context.Context, tx pgx.Tx, id string, rev int, end bool, by auth.Actor) (Session, []events.Draft, error) {
	sess, err := s.lockAt(ctx, tx, id, rev)
	if err != nil {
		return Session{}, nil, err
	}
	if !Live(sess.State) {
		return Session{}, nil, problems.Conflict.New("agent session %s already ended (%s)", id, sess.State)
	}
	alive, err := s.hostAlive(ctx, tx, sess)
	if err != nil {
		return Session{}, nil, err
	}
	switch {
	case end && sess.State == StateCreated && !alive:
		return s.finish(ctx, tx, sess, StateCancelled, "", by)
	case end && !alive:
		return s.finish(ctx, tx, sess, StateDone, "", by)
	case end:
		return s.request(ctx, tx, sess, "end", nil, nil, by)
	case sess.State == StateRunning || sess.State == StateWaitingApproval:
		return s.request(ctx, tx, sess, "cancel", nil, nil, by)
	}
	return sess, nil, nil // nothing runs: nothing to stop
}

// Pause is agentSessions.pause.
func (s *Service) Pause(ctx context.Context, tx pgx.Tx, id string, rev int, by auth.Actor) (Session, []events.Draft, error) {
	sess, err := s.lockAt(ctx, tx, id, rev)
	if err != nil {
		return Session{}, nil, err
	}
	if sess.State != StateRunning && sess.State != StateWaitingApproval {
		return Session{}, nil, problems.Conflict.New("agent session %s is %s; only a running session pauses", id, sess.State)
	}
	return s.request(ctx, tx, sess, "pause", &Reason{Code: PauseUser, Message: "paused by " + actorName(by)}, nil, by)
}

// Resume is agentSessions.resume; budget raises the session budget first.
func (s *Service) Resume(ctx context.Context, tx pgx.Tx, id string, rev int, budget *Budget, by auth.Actor) (Session, []events.Draft, error) {
	sess, err := s.lockAt(ctx, tx, id, rev)
	if err != nil {
		return Session{}, nil, err
	}
	if sess.State != StatePaused {
		return Session{}, nil, problems.Conflict.New("agent session %s is %s; only a paused session resumes", id, sess.State)
	}
	var drafts []events.Draft
	if budget != nil {
		nb := sess.Budget
		if budget.Turns > 0 {
			nb.Turns = budget.Turns
		}
		if budget.Tokens > 0 {
			nb.Tokens = budget.Tokens
		}
		if sess, drafts, err = apply(ctx, tx, sess, Update{Budget: &nb}); err != nil {
			return Session{}, nil, err
		}
	}
	if sess.Use.Turns >= sess.Budget.Turns || sess.Use.Tokens() >= sess.Budget.Tokens {
		return Session{}, nil, problems.Conflict.New("agent session %s has no budget left (%d of %d turns, %d of %d tokens); resume with a larger budget",
			id, sess.Use.Turns, sess.Budget.Turns, sess.Use.Tokens(), sess.Budget.Tokens)
	}
	b := sess.Budget
	next, more, err := s.request(ctx, tx, sess, "resume", nil, &b, by)
	return next, append(drafts, more...), err
}

// lockAt locks the session and checks its revision (If-Match).
func (s *Service) lockAt(ctx context.Context, tx pgx.Tx, id string, rev int) (Session, error) {
	sess, err := Lock(ctx, tx, id)
	if err != nil {
		return Session{}, err
	}
	if sess.Rev != rev {
		return Session{}, problems.Stale(sess.Rev, "agent session %s is at revision %d, not %d; re-read it", id, sess.Rev, rev)
	}
	return sess, nil
}

func actorName(a auth.Actor) string {
	if a.Name != "" {
		return a.Name
	}
	return a.ID
}

// finish ends a session: state (done, cancelled or failed), token revoked, and the branch merged per the project's
// auto-merge policy when it ended well (when-clean: merge if it applies cleanly), else left as Session changes for a
// person to accept or revert.
func (s *Service) finish(ctx context.Context, tx pgx.Tx, sess Session, state, errMsg string, by auth.Actor) (Session, []events.Draft, error) {
	var drafts []events.Draft
	if sess.CredentialID != "" {
		if err := credentials.Revoke(ctx, tx, sess.CredentialID); err != nil {
			var pe *problems.Error
			if !errors.As(err, &pe) || pe.Type != problems.NotFound {
				return Session{}, nil, err
			}
		}
	}
	merge := sess.Merge
	if sess.Branch != "" && merge.State == MergeNone {
		m, more, err := s.autoMerge(ctx, tx, sess, state == StateDone)
		if err != nil {
			return Session{}, nil, err
		}
		merge, drafts = m, append(drafts, more...)
	}
	u := Update{State: &state, Merge: &merge, Ended: true}
	if sess.Working != nil {
		// The worktree's uncommitted changes end with the session (a failed one keeps its worktree on the host,
		// but nothing reports it any more).
		var clean *Working
		u.Working = &clean
		drafts = append(drafts, workingEvents(sess, sess.Working, nil)...)
	}
	f := false
	u.Busy = &f
	if errMsg != "" {
		u.Error = &errMsg
	}
	if state != StatePaused {
		var nr *Reason
		u.PauseReason = &nr
	}
	next, more, err := apply(ctx, tx, sess, u)
	if err != nil {
		return Session{}, nil, err
	}
	// Agent-permission requests nobody decided die with the session.
	if _, err := tx.Exec(ctx, `UPDATE approvals SET state = 'denied', decided_at = now(), decided_by = $2,
		grant_scope = 'once', note = 'the agent session ended', rev = rev + 1
		WHERE session_id = $1 AND kind = 'agent_permission' AND state = 'pending'`, sess.ID, System); err != nil {
		return Session{}, nil, fmt.Errorf("close the session's permission requests: %w", err)
	}
	_ = by
	return next, append(drafts, more...), nil
}

// autoMerge decides what happens to the branch of an ending session.
func (s *Service) autoMerge(ctx context.Context, tx pgx.Tx, sess Session, ok bool) (Merge, []events.Draft, error) {
	p, err := projects.GetByID(ctx, tx, sess.ProjectID)
	if err != nil {
		return Merge{}, nil, err
	}
	st := s.Projects.Repos()
	d, err := st.DiffBranch(ctx, p.Slug, sess.Branch)
	if errors.Is(err, repos.ErrNotFound) {
		return Merge{State: MergeNone}, nil, nil
	}
	if err != nil {
		return Merge{}, nil, fmt.Errorf("read %s: %w", sess.Branch, err)
	}
	if d.Branch.Ahead == 0 {
		return Merge{State: MergeNone}, nil, nil
	}
	if len(d.Conflicts) > 0 {
		return Merge{State: MergeConflict, Head: d.Branch.Head, Conflicts: d.Conflicts}, nil, nil
	}
	if !ok || sess.AutoMerge != "when-clean" || p.Archived() {
		return Merge{State: MergePending, Head: d.Branch.Head}, nil, nil
	}
	m, drafts, err := s.Projects.Merge(ctx, tx, p, sess.Branch, d.Branch.Head, System)
	if err != nil {
		var pe *problems.Error
		if errors.As(err, &pe) && (pe.Type == problems.MergeConflict || pe.Type == problems.PreconditionFailed) {
			return Merge{State: MergeConflict, Head: d.Branch.Head, Conflicts: d.Conflicts}, nil, nil
		}
		return Merge{}, nil, err
	}
	now := s.now()
	ff := m.FastForward
	if err := audit.Write(ctx, tx, audit.Entry{Operation: "agentSessions.accept", Actor: System, ProjectID: p.ID,
		Outcome: audit.OutcomeOK, Status: 200, Rule: "auto-merge", At: now}); err != nil {
		return Merge{}, nil, err
	}
	return Merge{State: MergeMerged, Commit: m.Main, Head: m.Head, FastForward: &ff, At: &now, By: &System}, drafts, nil
}

// Accept is agentSessions.accept: merge the branch into main as by; a paused session ends first.
func (s *Service) Accept(ctx context.Context, tx pgx.Tx, id string, rev int, by auth.Actor, dryRun bool) (Session, []events.Draft, error) {
	sess, p, err := s.decidable(ctx, tx, id, rev)
	if err != nil {
		return Session{}, nil, err
	}
	d, err := s.Projects.Repos().DiffBranch(ctx, p.Slug, sess.Branch)
	if errors.Is(err, repos.ErrNotFound) {
		return Session{}, nil, problems.Conflict.New("the branch %s no longer exists", sess.Branch)
	}
	if err != nil {
		return Session{}, nil, err
	}
	if len(d.Conflicts) > 0 {
		return Session{}, nil, problems.MergeConflict.New("merging %s into main conflicts on %s; resolve it on the branch or revert the session",
			sess.Branch, strings.Join(d.Conflicts, ", "))
	}
	if dryRun {
		return sess, nil, nil
	}
	var drafts []events.Draft
	merge := Merge{State: MergeNone, Head: d.Branch.Head}
	if d.Branch.Ahead > 0 {
		m, more, err := s.Projects.Merge(ctx, tx, p, sess.Branch, d.Branch.Head, by)
		if err != nil {
			return Session{}, nil, err
		}
		now, ff := s.now(), m.FastForward
		merge = Merge{State: MergeMerged, Commit: m.Main, Head: m.Head, FastForward: &ff, At: &now, By: &by}
		drafts = more
	}
	next, more, err := s.decided(ctx, tx, sess, merge, by)
	return next, append(drafts, more...), err
}

// Revert is agentSessions.revert: delete the branch; a paused session ends first.
func (s *Service) Revert(ctx context.Context, tx pgx.Tx, id string, rev int, by auth.Actor, dryRun bool) (Session, []events.Draft, error) {
	sess, p, err := s.decidable(ctx, tx, id, rev)
	if err != nil {
		return Session{}, nil, err
	}
	if dryRun {
		return sess, nil, nil
	}
	now := s.now()
	merge := Merge{State: MergeDiscarded, At: &now, By: &by}
	b, err := s.Projects.Discard(ctx, p, sess.Branch, "")
	var pe *problems.Error
	switch {
	case err == nil:
		merge.Head = b.Head
	case errors.As(err, &pe) && pe.Type == problems.NotFound:
	default:
		return Session{}, nil, err
	}
	return s.decided(ctx, tx, sess, merge, by)
}

// decidable locks a session whose branch a person may accept or revert: interactive, paused or ended, not merged
// or discarded yet.
func (s *Service) decidable(ctx context.Context, tx pgx.Tx, id string, rev int) (Session, projects.Project, error) {
	sess, err := s.lockAt(ctx, tx, id, rev)
	if err != nil {
		return Session{}, projects.Project{}, err
	}
	switch {
	case sess.Branch == "":
		return Session{}, projects.Project{}, problems.Conflict.New("read-only session %s has no branch", id)
	case sess.State != StatePaused && Live(sess.State):
		return Session{}, projects.Project{}, problems.Conflict.New(
			"agent session %s is %s; pause or end it first (agentSessions.pause, agentSessions.cancel with end)", id, sess.State)
	case sess.Merge.State == MergeMerged || sess.Merge.State == MergeDiscarded:
		return Session{}, projects.Project{}, problems.Conflict.New("the changes of session %s were already %s", id, sess.Merge.State)
	}
	p, err := projects.GetByID(ctx, tx, sess.ProjectID)
	if err != nil {
		return Session{}, projects.Project{}, err
	}
	return sess, p, nil
}

// decided records the merge state; a paused session ends (done) and its host drops the worktree.
func (s *Service) decided(ctx context.Context, tx pgx.Tx, sess Session, merge Merge, by auth.Actor) (Session, []events.Draft, error) {
	if sess.State != StatePaused {
		return apply(ctx, tx, sess, Update{Merge: &merge})
	}
	sess.Merge = merge // finish keeps a decided merge state
	next, drafts, err := s.finish(ctx, tx, sess, StateDone, "", by)
	if err != nil {
		return Session{}, nil, err
	}
	if next.HostID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_session_controls (id, session_id, action, actor) VALUES ($1, $2, 'end', $3)`,
			newID("ctl_"), next.ID, by); err != nil {
			return Session{}, nil, fmt.Errorf("tell the host to drop the worktree: %w", err)
		}
	}
	return next, drafts, nil
}
