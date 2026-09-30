package playbooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/sessions"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Service runs playbooks and follows playbook sessions: it is the command pipeline's session hook
// (commands.SessionHook), the sessions' playbook watcher (sessions.PlaybookWatcher) and the observer of the reads a
// chain waits on.
type Service struct {
	Pool       *pgxpool.Pool
	Sessions   *sessions.Service
	Library    *Library
	Defaults   func() *defaults.Defaults
	Estimators map[string]Estimator
	Log        *slog.Logger
	Now        func() time.Time
}

var (
	_ commands.SessionHook     = (*Service)(nil)
	_ sessions.PlaybookWatcher = (*Service)(nil)
)

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

func (s *Service) estimators() map[string]Estimator {
	if s.Estimators != nil {
		return s.Estimators
	}
	return DefaultEstimators()
}

// ---------------------------------------------------------------- listing and running

// Estimate resolves p's inputs for prj (nil for none) and sums the chain's estimate. With given nil it is a listing:
// required inputs may stay unresolved (ResolvePartial).
func (s *Service) Estimate(ctx context.Context, q storage.Querier, p Playbook, prj *projects.Project, given map[string]any) (Resolved, Estimate, error) {
	d := s.defaults()
	var (
		r   Resolved
		err error
	)
	if given == nil {
		r, err = ResolvePartial(ctx, q, d, p, prj)
	} else {
		r, err = Resolve(ctx, q, d, p, prj, given)
	}
	if err != nil {
		return Resolved{}, Estimate{}, err
	}
	projectID := ""
	if prj != nil {
		projectID = prj.ID
	}
	e, err := EstimateChain(ctx, q, d, s.estimators(), p, r, projectID)
	return r, e, err
}

// Prepared is a playbook ready to start as a session: its resolved inputs, estimate, initial state and prompt, and
// the notice that opens the transcript.
type Prepared struct {
	Playbook Playbook
	Inputs   Resolved
	Estimate Estimate
	State    State
	Prompt   string
	Notice   string
}

// Prepare resolves the inputs, sums the estimate, renders the prompt and builds the initial plan of p in prj.
func (s *Service) Prepare(ctx context.Context, q storage.Querier, p Playbook, versionID string, prj projects.Project, given map[string]any) (Prepared, error) {
	if !p.Runnable() {
		return Prepared{}, problems.PlaybookUnavailable.New("the playbook %q runs from roadmap phase %d; this build ships phase %d",
			p.Name, p.AvailableFrom, CurrentPhase)
	}
	if given == nil {
		given = map[string]any{}
	}
	r, e, err := s.Estimate(ctx, q, p, &prj, given)
	if err != nil {
		return Prepared{}, err
	}
	prompt, err := Render(p, PromptData{Inputs: r.Text, Project: PromptProject{Name: prj.Name, Slug: prj.Slug,
		Locales: strings.Join(prj.Locales, ", ")}})
	if err != nil {
		return Prepared{}, fmt.Errorf("render the prompt of %s: %w", p.Name, err)
	}
	st := State{Name: p.Name, Title: p.Title, VersionID: versionID, State: StateRunning, Inputs: r.Values, Estimate: e,
		Plan: NewPlan(p, e), Stops: p.Stop, NextText: p.Next}
	return Prepared{Playbook: p, Inputs: r, Estimate: e, State: st, Prompt: prompt, Notice: EstimateNotice(p, e)}, nil
}

// EstimateNotice is the first transcript entry of a playbook session: the estimate, then the plan.
func EstimateNotice(p Playbook, e Estimate) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Playbook %q — estimate %.2f GPU-hours (%.2f–%.2f", p.Title, e.GPUHours.Value, e.GPUHours.Low, e.GPUHours.High)
	if e.PlusMinus > 0 {
		fmt.Fprintf(&b, ", ±%.0f%%", e.PlusMinus*100)
	}
	fmt.Fprintf(&b, ", basis %s)", e.Basis)
	if e.DurationSeconds.Value > 0 {
		fmt.Fprintf(&b, ", about %.0f min", e.DurationSeconds.Value/60)
	}
	if e.Budget != nil {
		if e.Budget.WithinDailyBudget {
			fmt.Fprintf(&b, "; within the daily budget of %.1f GPU-hours", e.Budget.GPUHoursPerProjectPerDay)
		} else {
			fmt.Fprintf(&b, "; over the daily budget of %.1f GPU-hours — spending steps will ask for approval", e.Budget.GPUHoursPerProjectPerDay)
		}
	}
	b.WriteString(".\n\nPlan:")
	for i, st := range e.Steps {
		fmt.Fprintf(&b, "\n%d. %s (%s)", i+1, p.Chain[i].Title, st.Command)
		switch {
		case st.Skipped:
			b.WriteString(" — skipped: " + st.Note)
		case st.GPUHours != nil:
			fmt.Fprintf(&b, " — %.2f GPU-hours (%s)", st.GPUHours.Value, st.Basis)
		case st.Note != "":
			b.WriteString(" — no estimate: " + st.Note)
		}
	}
	return b.String()
}

// ---------------------------------------------------------------- the session's playbook

// load reads the playbook of a playbook session inside tx, locked; ok is false for any other session.
func load(ctx context.Context, tx pgx.Tx, sessionID string) (sessions.Session, State, bool, error) {
	var kind string
	err := tx.QueryRow(ctx, `SELECT kind FROM agent_sessions WHERE id = $1`, sessionID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && kind != sessions.KindPlaybook) {
		return sessions.Session{}, State{}, false, nil
	}
	if err != nil {
		return sessions.Session{}, State{}, false, fmt.Errorf("read the kind of session %s: %w", sessionID, err)
	}
	sess, err := sessions.Lock(ctx, tx, sessionID)
	if err != nil {
		return sessions.Session{}, State{}, false, err
	}
	st, err := decode(sess)
	return sess, st, err == nil, err
}

func decode(sess sessions.Session) (State, error) {
	var st State
	if len(sess.Playbook) == 0 {
		return st, fmt.Errorf("playbook session %s has no playbook", sess.ID)
	}
	if err := json.Unmarshal(sess.Playbook, &st); err != nil {
		return st, fmt.Errorf("decode the playbook of %s: %w", sess.ID, err)
	}
	return st, nil
}

// save writes st to the locked session with its changed events and, when the playbook just ended, the summary notice.
func (s *Service) save(ctx context.Context, tx pgx.Tx, sess sessions.Session, prev string, st State) (sessions.Session, []events.Draft, error) {
	raw, err := json.Marshal(st)
	if err != nil {
		return sessions.Session{}, nil, fmt.Errorf("encode the playbook: %w", err)
	}
	next, drafts, err := sessions.SetPlaybook(ctx, tx, sess, raw)
	if err != nil {
		return sessions.Session{}, nil, err
	}
	if prev == StateRunning && st.State != StateRunning {
		level := "info"
		if st.State == StateStopped {
			level = "warning"
		}
		text := st.Summary
		if st.Next != "" {
			text += "\n\nNext: " + st.Next
		}
		d, err := sessions.Notice(ctx, tx, next, "playbook:end", level, text, false)
		if err != nil {
			return sessions.Session{}, nil, err
		}
		if d != nil {
			drafts = append(drafts, *d)
		}
		if !next.Busy {
			// No turn runs (the playbook stopped between turns, e.g. on a denied approval): end it now.
			ended, more, err := s.Sessions.RequestEnd(ctx, tx, next)
			if err != nil {
				return sessions.Session{}, nil, err
			}
			next, drafts = ended, append(drafts, more...)
		}
	}
	return next, drafts, nil
}

// observe applies o to the session's playbook inside tx.
func (s *Service) observe(ctx context.Context, tx pgx.Tx, sessionID string, o Observation) ([]events.Draft, error) {
	sess, st, ok, err := load(ctx, tx, sessionID)
	if err != nil || !ok || !sessions.Live(sess.State) {
		return nil, err
	}
	prev := st.State
	if !st.Observe(o) {
		return nil, nil
	}
	_, drafts, err := s.save(ctx, tx, sess, prev, st)
	return drafts, err
}

// ---------------------------------------------------------------- commands.SessionHook

// Admit refuses a real spending command in a playbook session unless the session ran a successful dry run of the
// same operation since its last real one, and any spending command once the playbook ended.
func (s *Service) Admit(ctx context.Context, tx pgx.Tx, cmd commands.Command) error {
	if !Spending[cmd.Operation] {
		return nil
	}
	_, st, ok, err := load(ctx, tx, cmd.Actor.SessionID)
	if err != nil || !ok {
		return err
	}
	if st.State != StateRunning {
		return problems.PlaybookStopped.New("the playbook %q of this session is %s; %s spends GPU time and is refused — %s",
			st.Title, st.State, cmd.Operation, st.Summary)
	}
	if cmd.DryRun {
		return nil
	}
	for _, op := range st.DryRuns {
		if op == cmd.Operation {
			return nil
		}
	}
	return problems.PlaybookDryRunRequired.New(
		"%s spends GPU time: in a playbook session call it with dryRun=true first, report the estimate, then call it again without dryRun", cmd.Operation)
}

func bodyMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// Done ticks the plan from a command that succeeded, in its transaction.
func (s *Service) Done(ctx context.Context, tx pgx.Tx, cmd commands.Command, res commands.Result) ([]events.Draft, error) {
	return s.observe(ctx, tx, cmd.Actor.SessionID, Observation{Operation: cmd.Operation, Status: res.Status,
		Body: bodyMap(res.Body), ToolCallID: commands.ToolCallFromContext(ctx), At: s.now()})
}

// DryRun records a successful dry run (and marks a spending item running) in its own transaction.
func (s *Service) DryRun(ctx context.Context, cmd commands.Command, res commands.Result) {
	s.apart(ctx, cmd.Actor, Observation{Operation: cmd.Operation, DryRun: true, Status: 200, Body: bodyMap(res.Body),
		ToolCallID: commands.ToolCallFromContext(ctx), At: s.now()})
}

// Observed ticks the plan from a read of the session that succeeded (jobs.wait, checkpoints.list), in its own
// transaction; body is the response.
func (s *Service) Observed(ctx context.Context, actor auth.Actor, op string, status int, body []byte) {
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	s.apart(ctx, actor, Observation{Operation: op, Status: status, Body: m, ToolCallID: commands.ToolCallFromContext(ctx), At: s.now()})
}

func (s *Service) apart(ctx context.Context, actor auth.Actor, o Observation) {
	if actor.SessionID == "" {
		return
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		drafts, err := s.observe(ctx, tx, actor.SessionID, o)
		if err != nil || len(drafts) == 0 {
			return err
		}
		return events.Append(ctx, tx, actor, nil, drafts)
	})
	if err != nil {
		s.log().ErrorContext(ctx, "tick the playbook plan", "session", actor.SessionID, "operation", o.Operation, "err", err)
	}
}

// Reads reports whether op is a read the server observes for playbook plans (a chain names it and its verb reads).
func (s *Service) Reads(op string) bool {
	if s == nil || s.Library == nil || !s.Library.Operations()[op] {
		return false
	}
	_, verb, _ := strings.Cut(op, ".")
	switch verb {
	case "get", "list", "search", "compare", "lineage", "wait":
		return true
	}
	return false
}

// ---------------------------------------------------------------- sessions.PlaybookWatcher

// budgetPause lists the pause reasons that stop a playbook on budget: the session's or the project's agent budget.
var budgetPause = map[string]bool{
	sessions.PauseBudgetTurns: true, sessions.PauseBudgetTokens: true, sessions.PauseProjectTokens: true,
}

// Reported follows a host report: a pause on the agent budget stops the playbook; a turn that ended with the
// playbook done or stopped ends the session; a turn that ended without progress reminds the agent of the next step
// (at most MaxNudges times in a row).
func (s *Service) Reported(ctx context.Context, tx pgx.Tx, prev, next sessions.Session) ([]events.Draft, error) {
	if !sessions.Live(next.State) {
		return nil, nil
	}
	st, err := decode(next)
	if err != nil {
		return nil, err
	}
	was := st.State
	if next.State == sessions.StatePaused && prev.State != sessions.StatePaused && next.PauseReason != nil &&
		budgetPause[next.PauseReason.Code] {
		if st.StopOn("budget", "exceeded", "the session paused on its budget: "+next.PauseReason.Message, s.now()) {
			_, drafts, err := s.save(ctx, tx, next, was, st)
			return drafts, err
		}
		return nil, nil
	}
	if !prev.Busy || next.Busy || next.State != sessions.StateRunning {
		return nil, nil // not the end of a turn
	}
	if st.State != StateRunning {
		_, drafts, err := s.Sessions.RequestEnd(ctx, tx, next)
		return drafts, err
	}
	var pending int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM approvals WHERE session_id = $1 AND state = 'pending'`, next.ID).Scan(&pending); err != nil {
		return nil, fmt.Errorf("count the session's pending approvals: %w", err)
	}
	if pending > 0 || st.Nudges >= MaxNudges || st.NextItem() == "" {
		return nil, nil
	}
	st.Nudges++
	next2, drafts, err := s.save(ctx, tx, next, was, st)
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("The playbook %q is not finished: the next step is %s. Continue the plan with Cadence tools; "+
		"if you cannot, say why and stop.", st.Title, st.NextItem())
	d, err := sessions.Notice(ctx, tx, next2, fmt.Sprintf("playbook:nudge:%d:%d", next.Turn, st.Nudges), "info", text, true)
	if err != nil {
		return nil, err
	}
	if d != nil {
		drafts = append(drafts, *d)
	}
	return drafts, nil
}

// Decided stops the playbook when a person denied a command the session asked for.
func (s *Service) Decided(ctx context.Context, tx pgx.Tx, sess sessions.Session, a approvals.Approval) ([]events.Draft, error) {
	if a.Kind != approvals.KindCommand || a.State != approvals.StateDenied || !sessions.Live(sess.State) {
		return nil, nil
	}
	st, err := decode(sess)
	if err != nil {
		return nil, err
	}
	was := st.State
	msg := fmt.Sprintf("approval %s for %s was denied", a.ID, a.Operation)
	if a.Note != "" {
		msg += " (" + a.Note + ")"
	}
	if !st.StopOn("approval", "denied", msg, s.now()) {
		return nil, nil
	}
	_, drafts, err := s.save(ctx, tx, sess, was, st)
	return drafts, err
}
