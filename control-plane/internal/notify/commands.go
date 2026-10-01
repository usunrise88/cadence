package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/notify/telegram"
)

// The bot's commands for allow-listed chats: read-only views of the instance (the queue, today's GPU spend, the
// pending approvals) and the approvals again with fresh buttons. Nothing here acts; a decision still goes through a
// signed single-use button and the command pipeline (Poller.press).

// QueueItem is one step job not yet ended, as the bot tells it.
type QueueItem struct {
	Kind     string   // kind@version
	JobKind  string   // training, eval, …
	State    string   // running, stopping, waiting, paused
	Project  string   // the project's id; empty outside one
	Where    string   // host · card, once leased
	Progress *float64 // 0–1 while running
	Message  string   // the worker's latest progress message
}

// QueueFunc lists the step jobs not yet ended, running first (the server's workers.Queue).
type QueueFunc func(ctx context.Context) ([]QueueItem, error)

// maxListed bounds the approvals /approvals sends (one message each) and the queue lines of /status.
const maxListed = 10

const helpText = `Cadence bot commands:
/status — the queue, today's GPU spend against budgets, pending approvals
/approvals — the pending approvals again, each with Approve and Deny
/help — this list

Approval requests, failures and the daily digest arrive here by themselves (Settings → Notifications).`

// command answers a message that starts with a slash; unknown commands get the help.
func (p *Poller) command(ctx context.Context, c *telegram.Client, chat int64, text string) error {
	name, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	name, _, _ = strings.Cut(name, "@") // /status@cadence_bot in groups
	switch strings.ToLower(name) {
	case "/start":
		_, err := c.SendMessage(ctx, chat, "Cadence notifications reach this chat. Approvals arrive with Approve and Deny buttons.\n\n"+helpText, nil)
		return err
	case "/status":
		msg, err := p.status(ctx)
		if err != nil {
			msg = "Cadence could not read its status; see the control plane's log."
			p.Log.WarnContext(ctx, "telegram: /status failed", "err", err)
		}
		_, err = c.SendMessage(ctx, chat, msg, nil)
		return err
	case "/approvals":
		return p.approvals(ctx, c, chat)
	default:
		_, err := c.SendMessage(ctx, chat, helpText, nil)
		return err
	}
}

// status renders the queue, today's spend (since local midnight in the instance timezone) and the open approvals.
func (p *Poller) status(ctx context.Context) (string, error) {
	env, err := LoadEnv(ctx, p.Pool, p.Defaults())
	if err != nil {
		return "", err
	}
	now := time.Now().In(env.Location)
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, env.Location)
	d, err := BuildDigest(ctx, p.Pool, midnight, now, p.Spend)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Queue:")
	switch {
	case p.Queue == nil:
		b.WriteString(" this control plane runs no worker protocol")
	default:
		items, err := p.Queue(ctx)
		if err != nil {
			return "", err
		}
		writeQueue(&b, items)
	}
	b.WriteString("\n\nGPU spend today:")
	if len(d.Spend) == 0 {
		b.WriteString(" no projects")
	}
	for _, s := range d.Spend {
		if s.UsedGPUHours == nil {
			fmt.Fprintf(&b, "\n• %s: budget %.1f GPU-h/day", s.Project, s.BudgetGPUHours)
			continue
		}
		fmt.Fprintf(&b, "\n• %s: %.2f of %.1f GPU-h", s.Project, *s.UsedGPUHours, s.BudgetGPUHours)
	}
	fmt.Fprintf(&b, "\n\nPending approvals: %d", d.OpenApprovals)
	for _, a := range d.Approvals {
		b.WriteString("\n• " + a)
	}
	if d.OpenApprovals > 0 {
		b.WriteString("\n/approvals sends them with buttons.")
	}
	return b.String(), nil
}

func writeQueue(b *strings.Builder, items []QueueItem) {
	if len(items) == 0 {
		b.WriteString(" empty")
		return
	}
	running := 0
	for _, it := range items {
		if it.State == "running" || it.State == "stopping" {
			running++
		}
	}
	fmt.Fprintf(b, " %d running, %d waiting", running, len(items)-running)
	for i, it := range items {
		if i == maxListed {
			fmt.Fprintf(b, "\n• … %d more", len(items)-maxListed)
			break
		}
		parts := []string{it.State}
		if it.Progress != nil && it.State == "running" {
			parts[0] = fmt.Sprintf("%.0f%%", *it.Progress*100)
		}
		for _, s := range []string{it.Where, it.Message} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		fmt.Fprintf(b, "\n• %s (%s): %s", it.Kind, it.JobKind, strings.Join(parts, " · "))
	}
}

// approvals sends each pending approval (the oldest first, at most maxListed) with fresh Approve / Deny buttons; the
// buttons of earlier messages stay valid, and the first press of any of them spends them all.
func (p *Poller) approvals(ctx context.Context, c *telegram.Client, chat int64) error {
	rows, err := p.Pool.Query(ctx, `SELECT id, operation, coalesce(project_id, ''), reason, actor, estimate, expires_at,
			count(*) OVER ()
		FROM approvals WHERE state = 'pending' AND expires_at > now() ORDER BY created_at LIMIT $1`, maxListed)
	if err != nil {
		return fmt.Errorf("pending approvals: %w", err)
	}
	total := 0
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (approvalView, error) {
		var (
			a               approvalView
			actor, estimate []byte
		)
		if err := row.Scan(&a.ID, &a.Operation, &a.ProjectID, &a.Reason, &actor, &estimate, &a.ExpiresAt, &total); err != nil {
			return a, err
		}
		if err := json.Unmarshal(actor, &a.Actor); err != nil {
			return a, fmt.Errorf("approval %s actor: %w", a.ID, err)
		}
		if len(estimate) > 0 && string(estimate) != "null" {
			if err := json.Unmarshal(estimate, &a.Estimate); err != nil {
				return a, fmt.Errorf("approval %s estimate: %w", a.ID, err)
			}
		}
		return a, nil
	})
	if err != nil {
		return fmt.Errorf("pending approvals: %w", err)
	}
	if len(list) == 0 {
		_, err := c.SendMessage(ctx, chat, "Nothing waits for you.", nil)
		return err
	}
	for _, a := range list {
		approve, deny, err := p.Signer.Mint(ctx, p.Pool, a.ID, a.ExpiresAt)
		if err != nil {
			return err
		}
		n := a.notice()
		kb := &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{{
			{Text: "Approve", CallbackData: approve}, {Text: "Deny", CallbackData: deny},
		}}}
		if _, err := c.SendMessage(ctx, chat, n.Title+"\n\n"+n.Body, kb); err != nil {
			return err
		}
	}
	if total > len(list) {
		_, err := c.SendMessage(ctx, chat, fmt.Sprintf("%d more wait in Cadence → Approvals.", total-len(list)), nil)
		return err
	}
	return nil
}
