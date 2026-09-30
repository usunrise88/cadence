package agentcreds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// System is the actor of what the agent host reports (the host is a machine, not a person).
var System = auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}

// Task is what the agent host is asked to do (the contract's HostCredentialTask). Value is set on a write that
// carries a new value; it comes from the transit store and goes nowhere but the host.
type Task struct {
	ID           string   `json:"id"`
	Action       string   `json:"action"`
	CredentialID string   `json:"credentialId"`
	Agent        string   `json:"agent"`
	Provider     string   `json:"provider"`
	CatalogueID  string   `json:"catalogueId"`
	Name         string   `json:"name,omitempty"`
	Value        string   `json:"value,omitempty"` //nolint:gosec // the host protocol is the one place a value travels
	BaseURL      string   `json:"baseUrl,omitempty"`
	VerifyModel  string   `json:"verifyModel,omitempty"`
	Models       []string `json:"models,omitempty"`
}

// Service runs the host side: claims, reports and the transit sweeper.
type Service struct {
	Pool    *pgxpool.Pool
	Transit Transit
	Log     *slog.Logger
	// Poll is how often a waiting claim looks for new tasks (500 ms when zero).
	Poll time.Duration
}

func (s *Service) poll() time.Duration {
	if s.Poll > 0 {
		return s.Poll
	}
	return 500 * time.Millisecond
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// Claim waits up to wait for open tasks and hands them to hostID, oldest first; a task claimed by a host that did
// not acknowledge it within ClaimLease is offered again. The claim also records the host as seen.
func (s *Service) Claim(ctx context.Context, hostID, credentialID string, wait time.Duration) ([]Task, error) {
	deadline := time.Now().Add(wait)
	for {
		var tasks []Task
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var err error
			tasks, err = s.claimOnce(ctx, tx, hostID, credentialID)
			return err
		})
		if err != nil || len(tasks) > 0 || !time.Now().Before(deadline) {
			return tasks, err
		}
		select {
		case <-ctx.Done():
			return []Task{}, nil
		case <-time.After(s.poll()):
		}
	}
}

func (s *Service) claimOnce(ctx context.Context, tx pgx.Tx, hostID, credentialID string) ([]Task, error) {
	if _, err := tx.Exec(ctx, `INSERT INTO agent_hosts (id, credential_id) VALUES ($1, $2)
		ON CONFLICT (id) DO UPDATE SET seen_at = now()`, hostID, credentialID); err != nil {
		return nil, fmt.Errorf("record the agent host: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT t.id, t.action, t.transit, c.id, c.agent, c.provider, c.catalogue_id, c.name,
			coalesce(c.base_url, ''), c.models
		FROM agent_credential_tasks t JOIN agent_credentials c ON c.id = t.credential_id
		WHERE t.done_at IS NULL AND (t.claimed_at IS NULL OR t.claimed_at < now() - $1::interval)
		ORDER BY t.created_at, t.id LIMIT 20 FOR UPDATE OF t SKIP LOCKED`,
		fmt.Sprintf("%d milliseconds", ClaimLease.Milliseconds()))
	if err != nil {
		return nil, fmt.Errorf("find agent credential tasks: %w", err)
	}
	type row struct {
		t       Task
		transit bool
		models  []byte
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		err := r.Scan(&x.t.ID, &x.t.Action, &x.transit, &x.t.CredentialID, &x.t.Agent, &x.t.Provider, &x.t.CatalogueID,
			&x.t.Name, &x.t.BaseURL, &x.models)
		return x, err
	})
	if err != nil {
		return nil, fmt.Errorf("read agent credential tasks: %w", err)
	}
	out := make([]Task, 0, len(list))
	var drafts []events.Draft
	for _, x := range list {
		t := x.t
		entry := CatalogueFor(t.Agent, t.Provider)
		if t.Action == ActionVerify {
			t.VerifyModel = entry.VerifyModel
		}
		if entry.Custom && len(x.models) > 0 {
			_ = json.Unmarshal(x.models, &t.Models)
		}
		if x.transit {
			v, err := s.Transit.OpenTransit(t.ID)
			if errors.Is(err, fs.ErrNotExist) {
				// The value is gone (a data directory restored without it): the write cannot happen.
				d, ferr := finish(ctx, tx, t, false, "the value was lost before the agent host took it; set it again", "", nil)
				if ferr != nil {
					return nil, ferr
				}
				drafts = append(drafts, d...)
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("open the value of %s: %w", t.ID, err)
			}
			t.Value = string(v)
		}
		if _, err := tx.Exec(ctx, `UPDATE agent_credential_tasks SET claimed_by = $2, claimed_at = now() WHERE id = $1`,
			t.ID, hostID); err != nil {
			return nil, fmt.Errorf("claim %s: %w", t.ID, err)
		}
		out = append(out, t)
	}
	return out, events.Append(ctx, tx, System, nil, drafts)
}

// Report is the outcome of one task from the host.
type Report struct {
	HostID string
	OK     bool
	Detail string
	Model  string
	Models []string
}

// Report records the outcome of task id: a write makes the credential written (or failed), a remove removed, a
// verify ok or failed with the model list. A task that was superseded meanwhile only closes. The transit copy of a
// value is deleted once the task is closed.
func (s *Service) Report(ctx context.Context, id string, r Report) (string, error) {
	state := "done"
	var transit bool
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var (
			t       Task
			doneAt  *time.Time
			claimed string
		)
		err := tx.QueryRow(ctx, `SELECT t.id, t.action, t.transit, t.done_at, coalesce(t.claimed_by, ''), c.id, c.agent,
				c.provider, c.catalogue_id
			FROM agent_credential_tasks t JOIN agent_credentials c ON c.id = t.credential_id WHERE t.id = $1 FOR UPDATE OF t`, id).
			Scan(&t.ID, &t.Action, &transit, &doneAt, &claimed, &t.CredentialID, &t.Agent, &t.Provider, &t.CatalogueID)
		if errors.Is(err, pgx.ErrNoRows) {
			return problems.NotFound.New("no agent credential task %q", id)
		}
		if err != nil {
			return fmt.Errorf("read task %s: %w", id, err)
		}
		if doneAt != nil {
			state = "superseded"
			return nil
		}
		if claimed != "" && claimed != r.HostID {
			return problems.Conflict.New("task %s was claimed by another agent host (%s)", id, claimed)
		}
		drafts, err := finish(ctx, tx, t, r.OK, r.Detail, r.Model, r.Models)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, System, nil, drafts)
	})
	if err != nil {
		return "", err
	}
	if transit {
		if err := s.Transit.DropTransit(id); err != nil {
			s.log().WarnContext(ctx, "drop a transit value", "task", id, "err", err)
		}
	}
	return state, nil
}

// finish closes task t with its outcome and updates its credential.
func finish(ctx context.Context, tx pgx.Tx, t Task, ok bool, detail, model string, models []string) ([]events.Draft, error) {
	outcome := "ok"
	if !ok {
		outcome = "failed"
	}
	if _, err := tx.Exec(ctx, `UPDATE agent_credential_tasks SET done_at = now(), outcome = $2 WHERE id = $1`, t.ID, outcome); err != nil {
		return nil, fmt.Errorf("close task %s: %w", t.ID, err)
	}
	detail = shorten(detail, 500)
	var (
		q   string
		typ string
		arg []any
	)
	switch t.Action {
	case ActionWrite, ActionRemove:
		// A newer write or remove of the same credential decides its delivery state.
		var newer bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM agent_credential_tasks n, agent_credential_tasks o
			WHERE o.id = $1 AND n.credential_id = o.credential_id AND n.action IN ('write', 'remove') AND n.id <> o.id
			AND (n.created_at, n.id) > (o.created_at, o.id))`, t.ID).Scan(&newer); err != nil {
			return nil, fmt.Errorf("check newer tasks of %s: %w", t.CredentialID, err)
		}
		if newer {
			return nil, nil
		}
		st := DeliveryWritten
		typ = "agent_credential.written"
		if t.Action == ActionRemove {
			st, typ = DeliveryRemoved, "agent_credential.removed"
		}
		if !ok {
			st, typ = DeliveryFailed, "agent_credential.delivery_failed"
		}
		q = `UPDATE agent_credentials SET delivery_state = $2, delivery_detail = $3, delivery_at = now(), rev = rev + 1,
			updated_at = now() WHERE id = $1 RETURNING ` + cols
		arg = []any{t.CredentialID, st, detail}
	case ActionVerify:
		st := VerifyOK
		if !ok {
			st = VerifyFailed
		}
		typ = "agent_credential.verified"
		clean := []string{}
		for _, m := range models {
			m = strings.TrimSpace(m)
			if strings.HasPrefix(m, t.Provider+"/") && len(m) > len(t.Provider)+1 && len(clean) < 2000 {
				clean = append(clean, m)
			}
		}
		keep := !ok || len(clean) == 0 // a failed check or one without a list keeps the last list
		raw, err := json.Marshal(clean)
		if err != nil {
			return nil, fmt.Errorf("encode models: %w", err)
		}
		q = `UPDATE agent_credentials SET verify_state = $2, verify_detail = $3, verify_model = $4, verified_at = now(),
			models = CASE WHEN $6 THEN models ELSE $5 END, rev = rev + 1, updated_at = now()
			WHERE id = $1 AND archived_at IS NULL RETURNING ` + cols
		arg = []any{t.CredentialID, st, detail, shorten(model, 200), raw, keep}
	default:
		return nil, fmt.Errorf("task %s has an unknown action %q", t.ID, t.Action)
	}
	rows, err := tx.Query(ctx, q, arg...)
	if err != nil {
		return nil, fmt.Errorf("record %s of %s: %w", t.Action, t.CredentialID, err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // verified after a disconnect: nothing to record
	}
	if err != nil {
		return nil, fmt.Errorf("record %s of %s: %w", t.Action, t.CredentialID, err)
	}
	return event(c, typ), nil
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Sweep drops transit values no open task needs (superseded writes, commits that failed after the seal) once they
// are older than a minute.
func (s *Service) Sweep(ctx context.Context, names func() (map[string]time.Time, error)) error {
	have, err := names()
	if err != nil || len(have) == 0 {
		return err
	}
	ids := make([]string, 0, len(have))
	for n := range have {
		ids = append(ids, n)
	}
	rows, err := s.Pool.Query(ctx, `SELECT id FROM agent_credential_tasks WHERE id = ANY($1) AND done_at IS NULL`, ids)
	if err != nil {
		return fmt.Errorf("sweep transit values: %w", err)
	}
	open, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("sweep transit values: %w", err)
	}
	keep := map[string]bool{}
	for _, id := range open {
		keep[id] = true
	}
	for n, mod := range have {
		if keep[n] || time.Since(mod) < time.Minute {
			continue
		}
		if err := s.Transit.DropTransit(n); err != nil {
			return err
		}
	}
	return nil
}
