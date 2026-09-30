package sessions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// InterruptedNote is the note on agent-permission approvals a host restart withdrew: the request was interrupted,
// nobody declined it.
const InterruptedNote = "interrupted by an agent-host restart; nobody declined it"

// ReleaseInput is a host that shuts down and gives its sessions back (hostSessions.release).
type ReleaseInput struct {
	HostID string
	// Messages the host took but never gave its agent: they go back to pending for the next host.
	Messages []string
}

// Release gives back every live session of in.HostID so the next host claims it at once instead of after the lapse:
// the session loses its host and token, a turn in progress counts as interrupted (the agent is told before its next
// prompt, and its pending permission requests are withdrawn as interrupted), messages the host never gave the agent
// go back to pending, and the Chat shows the session as reconnecting until a host takes it. It returns the ids of
// the sessions released.
func (s *Service) Release(ctx context.Context, in ReleaseInput) ([]string, error) {
	out := []string{}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id FROM agent_sessions WHERE host_id = $1
			AND state IN ('created', 'running', 'waiting_approval', 'paused') ORDER BY created_at FOR UPDATE`, in.HostID)
		if err != nil {
			return fmt.Errorf("find the host's sessions: %w", err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return fmt.Errorf("read the host's sessions: %w", err)
		}
		var drafts []events.Draft
		for _, id := range ids {
			d, err := s.release(ctx, tx, id, in)
			if err != nil {
				return err
			}
			drafts = append(drafts, d...)
			out = append(out, id)
		}
		return events.Append(ctx, tx, System, nil, drafts)
	})
	return out, err
}

func (s *Service) release(ctx context.Context, tx pgx.Tx, id string, in ReleaseInput) ([]events.Draft, error) {
	sess, err := Lock(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	var drafts []events.Draft
	if len(in.Messages) > 0 {
		rows, err := tx.Query(ctx, `UPDATE agent_messages SET delivery = 'pending', rev = rev + 1, updated_at = now()
			WHERE session_id = $1 AND id = ANY($2) AND delivery = 'delivered'
			RETURNING `+msgCols, sess.ID, in.Messages)
		if err != nil {
			return nil, fmt.Errorf("give back the messages of %s: %w", sess.ID, err)
		}
		list, err := pgx.CollectRows(rows, scanMessage)
		if err != nil {
			return nil, fmt.Errorf("read the messages given back: %w", err)
		}
		for _, m := range list {
			drafts = append(drafts, messageEvent(m, EventMessageUpdated))
		}
	}
	note, text, more, err := s.interrupted(ctx, tx, sess, "restarted")
	if err != nil {
		return nil, err
	}
	drafts = append(drafts, more...)
	if len(more) > 0 { // withdrawn approvals moved the session out of waiting_approval
		if sess, err = Lock(ctx, tx, id); err != nil {
			return nil, err
		}
	}
	if sess.CredentialID != "" {
		if err := credentials.Revoke(ctx, tx, sess.CredentialID); err != nil {
			if pe, ok := problems.As(err); !ok || pe.Type != problems.NotFound {
				return nil, err
			}
		}
	}
	if sess.ResumeNote != "" && note != "" {
		note = sess.ResumeNote + "\n\n" + note
	} else if note == "" {
		note = sess.ResumeNote
	}
	now := s.now()
	left := &now
	none, f := "", false
	next, d, err := apply(ctx, tx, sess, Update{HostID: &none, CredentialID: &none, Busy: &f, HostLeftAt: &left, ResumeNote: &note})
	if err != nil {
		return nil, err
	}
	drafts = append(drafts, d...)
	if text == "" {
		text = fmt.Sprintf("The agent host %s is restarting; the session continues on the next host (reconnecting…)", in.HostID)
	}
	if n, err := notice(ctx, tx, next, newID("host:"), "warning", text, false); err != nil {
		return nil, err
	} else if n != nil {
		drafts = append(drafts, *n)
	}
	return drafts, nil
}

// interrupted settles what a host that left (how: "restarted" or "went silent") took with it: the turn in progress
// and the agent-permission requests the agent waited for, which are withdrawn as interrupted — nobody declined them.
// It returns the note the agent reads before its next prompt and the notice people read (both empty when nothing
// was interrupted).
func (s *Service) interrupted(ctx context.Context, tx pgx.Tx, sess Session, how string) (string, string, []events.Draft, error) {
	rows, err := tx.Query(ctx, `SELECT id, coalesce(permission->>'title', operation) FROM approvals
		WHERE session_id = $1 AND kind = 'agent_permission' AND state = 'pending' ORDER BY created_at`, sess.ID)
	if err != nil {
		return "", "", nil, fmt.Errorf("find the permission requests of %s: %w", sess.ID, err)
	}
	type pending struct{ id, title string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
		var p pending
		return p, r.Scan(&p.id, &p.title)
	})
	if err != nil {
		return "", "", nil, fmt.Errorf("read the permission requests of %s: %w", sess.ID, err)
	}
	if !sess.Busy && len(list) == 0 {
		return "", "", nil, nil
	}
	var drafts []events.Draft
	titles := make([]string, 0, len(list))
	for _, p := range list {
		a, err := approvals.Get(ctx, tx, p.id)
		if err != nil {
			return "", "", nil, err
		}
		if a, err = approvals.Lock(ctx, tx, p.id, a.Rev); err != nil {
			return "", "", nil, err
		}
		a, d, err := approvals.Decide(ctx, tx, a, approvals.Decision{By: System, Note: InterruptedNote})
		if err != nil {
			return "", "", nil, err
		}
		more, err := s.ApprovalDecided(ctx, tx, a)
		if err != nil {
			return "", "", nil, err
		}
		drafts = append(append(drafts, d...), more...)
		titles = append(titles, "“"+p.title+"”")
	}
	var agent, people strings.Builder
	fmt.Fprintf(&agent, "[Cadence] The agent host %s", how)
	fmt.Fprintf(&people, "The agent host %s", how)
	if sess.Busy {
		fmt.Fprintf(&agent, " while your turn %d was running: that turn was interrupted by the restart — the person did not stop it.", sess.Turn)
		fmt.Fprintf(&people, " during turn %d: the turn was interrupted.", sess.Turn)
	} else {
		agent.WriteString(".")
		people.WriteString(".")
	}
	if len(titles) > 0 {
		req := "request"
		if len(titles) > 1 {
			req = "requests"
		}
		joined := strings.Join(titles, ", ")
		fmt.Fprintf(&agent, " Your permission %s for %s was withdrawn by the restart, not declined by the person; ask again if you still need it.", req, joined)
		fmt.Fprintf(&people, " The permission %s %s was withdrawn by the restart — it was not declined.", req, joined)
	}
	if sess.Busy {
		agent.WriteString(" Check what the interrupted turn already did before you continue.")
	}
	people.WriteString(" The session continues on the next host, and the agent is told when your next message reaches it.")
	return agent.String(), people.String(), drafts, nil
}

// markLost records that the host of a live session has been silent past the lapse (the Chat shows it); the session
// keeps its host, which may still come back, until another host takes it over.
func (s *Service) markLost(ctx context.Context, tx pgx.Tx, id string) ([]events.Draft, error) {
	sess, err := Lock(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	now := s.now()
	left := &now
	next, drafts, err := apply(ctx, tx, sess, Update{HostLeftAt: &left})
	if err != nil {
		return nil, err
	}
	text := fmt.Sprintf("The agent host %s stopped answering (silent for over %s); the session moves to the next host that claims work",
		sess.HostID, s.hostLapse().Round(time.Second))
	if n, err := notice(ctx, tx, next, newID("host:"), "warning", text, false); err != nil {
		return nil, err
	} else if n != nil {
		drafts = append(drafts, *n)
	}
	return drafts, nil
}

// reconnected clears the lost mark of a session whose own host answers again.
func reconnected(ctx context.Context, tx pgx.Tx, sess Session) (Session, []events.Draft, error) {
	if sess.HostLeftAt == nil || sess.HostID == "" {
		return sess, nil, nil
	}
	var none *time.Time
	next, drafts, err := apply(ctx, tx, sess, Update{HostLeftAt: &none})
	if err != nil {
		return Session{}, nil, err
	}
	if n, err := notice(ctx, tx, next, newID("host:"), "info", "The agent host "+sess.HostID+" answers again", false); err != nil {
		return Session{}, nil, err
	} else if n != nil {
		drafts = append(drafts, *n)
	}
	return next, drafts, nil
}
