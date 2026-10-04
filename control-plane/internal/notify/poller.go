package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/notify/telegram"
)

// Decider decides an approval for a verified button press: as the admin, with channel telegram (the server runs
// approvals.approve or approvals.deny through the command pipeline, so the audit log and the approval record the
// decision like one from the UI). It returns the approval's state afterwards; ErrAlreadyDecided when a person
// decided it elsewhere first.
type Decider interface {
	DecideApproval(ctx context.Context, approvalID string, approve bool, by Presser) (string, error)
}

// ErrAlreadyDecided is a press on an approval that is no longer pending.
var ErrAlreadyDecided = errors.New("the approval is no longer pending")

// Poller long-polls Telegram for updates (getUpdates; no public webhook): button presses of allow-listed chats
// decide approvals and their slash commands are answered (commands.go); chats that are not allow-listed are
// remembered for Settings and never answered.
type Poller struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Bot      Bot
	Signer   *Signer
	Decider  Decider
	Defaults func() *defaults.Defaults
	// Queue and Spend feed /status (commands.go); either may be nil.
	Queue QueueFunc
	Spend SpendFunc
	// Timeout is the long-poll wait in seconds (50 when zero); Idle is the pause while no token is stored or after
	// an error (30 s when zero).
	Timeout int
	Idle    time.Duration

	polling atomic.Bool
	offset  int64
}

// Polling reports whether the poller is talking to Telegram.
func (p *Poller) Polling() bool { return p.polling.Load() }

// Run polls until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) error {
	if p.Timeout <= 0 {
		p.Timeout = 50
	}
	if p.Idle <= 0 {
		p.Idle = 30 * time.Second
	}
	for ctx.Err() == nil {
		if err := p.Poll(ctx); err != nil && ctx.Err() == nil {
			p.polling.Store(false)
			if !errors.Is(err, errNoToken) {
				p.Log.WarnContext(ctx, "telegram: polling failed; retrying", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(p.Idle):
			}
		}
	}
	p.polling.Store(false)
	return nil
}

var errNoToken = errors.New("no telegram bot token")

// Poll runs one getUpdates round and handles what it brings.
func (p *Poller) Poll(ctx context.Context) error {
	ok, err := TokenStored(ctx, p.Pool)
	if err != nil {
		return err
	}
	if !ok {
		return errNoToken
	}
	c, err := p.Bot.Client(ctx)
	if err != nil {
		return err
	}
	updates, err := c.GetUpdates(ctx, p.offset, p.Timeout)
	if err != nil {
		_ = RecordBot(context.WithoutCancel(ctx), p.Pool, "", err, false)
		return err
	}
	p.polling.Store(true)
	for _, u := range updates {
		if u.UpdateID >= p.offset {
			p.offset = u.UpdateID + 1
		}
		if err := p.Handle(ctx, c, u); err != nil {
			p.Log.WarnContext(ctx, "telegram: update not handled", "update", u.UpdateID, "err", err)
		}
	}
	return nil
}

// Handle acts on one update.
func (p *Poller) Handle(ctx context.Context, c *telegram.Client, u telegram.Update) error {
	settings, err := LoadSettings(ctx, p.Pool, p.Defaults())
	if err != nil {
		return err
	}
	switch {
	case u.CallbackQuery != nil:
		q := u.CallbackQuery
		if q.Message == nil {
			return nil
		}
		if !settings.Allowed(q.Message.Chat.ID) {
			return p.seen(ctx, q.Message.Chat)
		}
		toast, note := p.press(ctx, q)
		if err := c.AnswerCallbackQuery(ctx, q.ID, toast); err != nil {
			return err
		}
		if note == "" {
			return nil
		}
		label, rest := pressedRow(q.Message.ReplyMarkup, q.Data)
		if label != "" {
			note = "[" + label + "] " + note
		}
		return c.EditMessage(ctx, q.Message.Chat.ID, q.Message.MessageID, q.Message.Text+"\n\n"+note, rest)
	case u.Message != nil:
		if !settings.Allowed(u.Message.Chat.ID) {
			return p.seen(ctx, u.Message.Chat)
		}
		if strings.HasPrefix(u.Message.Text, "/") {
			return p.command(ctx, c, u.Message.Chat.ID, u.Message.Text)
		}
	}
	return nil
}

func (p *Poller) seen(ctx context.Context, chat telegram.Chat) error {
	now := time.Now()
	return RecordSeen(ctx, p.Pool, Chat{ID: chat.ID, Title: chat.Label(), SeenAt: &now})
}

// pressedRow finds the keyboard row of the pressed button (data) and returns its number in a batched approval
// message ("2" of "Approve 2"; "" for a message of one approval) and the keyboard without that row: the approvals
// not decided yet keep their buttons.
func pressedRow(kb *telegram.Keyboard, data string) (label string, rest *telegram.Keyboard) {
	rest = &telegram.Keyboard{InlineKeyboard: [][]telegram.Button{}}
	if kb == nil {
		return "", rest
	}
	for _, row := range kb.InlineKeyboard {
		pressed := false
		for _, b := range row {
			if b.CallbackData == data {
				pressed = true
				if _, n, ok := strings.Cut(b.Text, " "); ok {
					label = n
				}
			}
		}
		if !pressed {
			rest.InlineKeyboard = append(rest.InlineKeyboard, row)
		}
	}
	return label, rest
}

// press verifies and spends a button's token and decides its approval. It returns the toast for the presser and,
// when the approval was decided (now or before), the line the message gains as that approval's buttons go.
func (p *Poller) press(ctx context.Context, q *telegram.CallbackQuery) (toast, note string) {
	id, action, err := p.Signer.Parse(q.Data)
	if err != nil {
		p.Log.WarnContext(ctx, "telegram: button with a bad signature", "chat", q.Message.Chat.ID)
		return "This button is not valid.", ""
	}
	by := Presser{ChatID: q.Message.Chat.ID, UserID: q.From.ID, Username: q.From.Username}
	var approvalID string
	err = pgx.BeginFunc(ctx, p.Pool, func(tx pgx.Tx) error {
		approvalID, err = p.Signer.Spend(ctx, tx, id, action, by)
		return err
	})
	switch {
	case errors.Is(err, ErrTokenSpent):
		return "Already decided.", "Already decided."
	case errors.Is(err, ErrTokenExpired):
		return "The approval expired.", "Expired — nobody decided in time."
	case err != nil:
		return "This button is not valid.", ""
	}
	state, err := p.Decider.DecideApproval(ctx, approvalID, action == ActionApprove, by)
	who := by.Username
	if who != "" {
		who = " by @" + who
	}
	switch {
	case errors.Is(err, ErrAlreadyDecided):
		return "Already decided in Cadence.", fmt.Sprintf("Already %s in Cadence.", state)
	case err != nil:
		// Nothing was decided: the buttons work again, and the approval can still be decided in Cadence.
		_, _ = p.Pool.Exec(context.WithoutCancel(ctx), `UPDATE notification_tokens SET used_at = NULL, used_by = NULL
			WHERE approval_id = $1`, approvalID)
		p.Log.WarnContext(ctx, "telegram: deciding an approval failed", "approval", approvalID, "err", err)
		return "Cadence could not record the decision; try again or decide in Cadence.", ""
	case action == ActionApprove:
		return "Approved.", "Approved from Telegram" + who + "."
	default:
		return "Denied.", "Denied from Telegram" + who + "."
	}
}
