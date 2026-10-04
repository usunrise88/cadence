package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/notify/telegram"
)

// SecretReader reads a secret's value on the server (secrets.Store).
type SecretReader interface {
	Read(ctx context.Context, name string) ([]byte, error)
}

// Bot is how the notification channel reaches Telegram: the Bot API base URL (a fake server in tests), the HTTP
// client and the secret store holding the token.
type Bot struct {
	BaseURL string
	HTTP    *http.Client
	Secrets SecretReader
}

// Client returns a Bot API client with the stored token; an unset token is an error.
func (b Bot) Client(ctx context.Context) (*telegram.Client, error) {
	if b.Secrets == nil {
		return nil, errors.New("no secret store is configured")
	}
	tok, err := b.Secrets.Read(ctx, TokenSecret)
	if err != nil {
		return nil, fmt.Errorf("read the telegram bot token: %w", err)
	}
	return &telegram.Client{BaseURL: b.BaseURL, Token: string(tok), HTTP: b.HTTP}, nil
}

// MaxAttempts is how often a delivery is tried before it is failed.
const MaxAttempts = 5

// lease is how long a claimed delivery stays hidden from other senders while it is being sent.
const lease = 2 * time.Minute

// Sender delivers queued Telegram rows: it claims due rows, sends each to every allow-listed chat, and records the
// outcome; a failure is retried with backoff up to MaxAttempts.
type Sender struct {
	Pool     *pgxpool.Pool
	Log      *slog.Logger
	Bot      Bot
	Signer   *Signer
	Defaults func() *defaults.Defaults
	Now      func() time.Time
	// Wake makes the sender flush at once (the router signals it); Interval is the fallback poll.
	Wake     <-chan struct{}
	Interval time.Duration
}

func (s *Sender) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Run delivers until ctx is cancelled.
func (s *Sender) Run(ctx context.Context) error {
	if s.Interval <= 0 {
		s.Interval = 5 * time.Second
	}
	tick := time.NewTicker(s.Interval)
	defer tick.Stop()
	for {
		if _, err := s.Flush(ctx); err != nil && ctx.Err() == nil {
			s.Log.WarnContext(ctx, "notifications: delivery failed; retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case <-s.Wake:
		}
	}
}

type delivery struct {
	ID, Class, Title, Body, ApprovalID string
	Silent                             bool
	Attempts                           int
}

// MaxBatch is how many approval requests one Telegram message lists (each with its own button row): ten keep the
// text well inside Telegram's 4096 characters.
const MaxBatch = 10

// Flush sends every due delivery and returns how many went out. The approval requests due together (one batching
// window, ApprovalSendAt) go out as one message per MaxBatch, every other delivery as its own message.
func (s *Sender) Flush(ctx context.Context) (int, error) {
	var due []delivery
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		// In event order: a batched approval message numbers its requests as they were made.
		rows, err := tx.Query(ctx, `WITH claimed AS (UPDATE notification_deliveries SET next_at = $1::timestamptz + $2::interval
			WHERE id IN (SELECT id FROM notification_deliveries WHERE state = 'queued' AND next_at <= $1
				ORDER BY created_at, event_seq NULLS LAST LIMIT 20 FOR UPDATE SKIP LOCKED)
			RETURNING id, event_class, title, body, coalesce(approval_id, '') AS approval_id, silent, attempts, created_at, event_seq)
			SELECT id, event_class, title, body, approval_id, silent, attempts FROM claimed
			ORDER BY created_at, event_seq NULLS LAST`, s.now(), lease.String())
		if err != nil {
			return fmt.Errorf("claim deliveries: %w", err)
		}
		due, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (delivery, error) {
			var d delivery
			err := row.Scan(&d.ID, &d.Class, &d.Title, &d.Body, &d.ApprovalID, &d.Silent, &d.Attempts)
			return d, err
		})
		return err
	})
	if err != nil || len(due) == 0 {
		return 0, err
	}
	settings, err := LoadSettings(ctx, s.Pool, s.Defaults())
	if err != nil {
		return 0, err
	}
	client, cerr := s.Bot.Client(ctx)
	results := map[string]error{}
	var approvals []delivery
	for _, d := range due {
		switch {
		case cerr != nil:
			results[d.ID] = cerr
		case d.ApprovalID != "":
			approvals = append(approvals, d)
		default:
			results[d.ID] = s.send(ctx, client, settings, d.Title+body(d.Body), nil, d.Silent)
		}
	}
	if len(approvals) > 0 {
		s.deliverApprovals(ctx, client, settings, approvals, results)
	}
	sent := 0
	for _, d := range due {
		derr := results[d.ID]
		if derr == nil {
			sent++
		}
		if err := s.record(ctx, d, derr); err != nil {
			return sent, err
		}
	}
	return sent, nil
}

func body(b string) string {
	if b == "" {
		return ""
	}
	return "\n\n" + b
}

// errDecided marks an approval decided before its message went out: nothing to send.
var errDecided = errors.New("the approval was decided before the message went out")

// deliverApprovals sends the approval requests of ds that are still pending, MaxBatch to a message, and stores each
// one's outcome in results. One request is the single message it always was (Approve / Deny); several are a list
// whose numbered rows of buttons ("Approve 2" / "Deny 2") each decide their own approval with their own single-use
// tokens. A message is silent only when every request in it is.
func (s *Sender) deliverApprovals(ctx context.Context, c *telegram.Client, settings Settings, ds []delivery, results map[string]error) {
	type pending struct {
		d       delivery
		approve string
		deny    string
	}
	var live []pending
	for _, d := range ds {
		var (
			state   string
			expires time.Time
		)
		if err := s.Pool.QueryRow(ctx, `SELECT state, expires_at FROM approvals WHERE id = $1`, d.ApprovalID).
			Scan(&state, &expires); err != nil {
			results[d.ID] = fmt.Errorf("read approval %s: %w", d.ApprovalID, err)
			continue
		}
		if state != "pending" {
			results[d.ID] = errDecided
			continue
		}
		approve, deny, err := s.Signer.Mint(ctx, s.Pool, d.ApprovalID, expires)
		if err != nil {
			results[d.ID] = err
			continue
		}
		live = append(live, pending{d: d, approve: approve, deny: deny})
	}
	for start := 0; start < len(live); start += MaxBatch {
		chunk := live[start:min(start+MaxBatch, len(live))]
		var (
			text   string
			kb     = &telegram.Keyboard{}
			silent = true
		)
		if len(chunk) == 1 {
			p := chunk[0]
			text = p.d.Title + body(p.d.Body)
			kb.InlineKeyboard = [][]telegram.Button{{{Text: "Approve", CallbackData: p.approve}, {Text: "Deny", CallbackData: p.deny}}}
			silent = p.d.Silent
		} else {
			parts := []string{fmt.Sprintf("%d approvals requested", len(chunk))}
			for i, p := range chunk {
				n := strconv.Itoa(i + 1)
				parts = append(parts, "["+n+"] "+p.d.Title+body(p.d.Body))
				kb.InlineKeyboard = append(kb.InlineKeyboard, []telegram.Button{
					{Text: "Approve " + n, CallbackData: p.approve}, {Text: "Deny " + n, CallbackData: p.deny},
				})
				silent = silent && p.d.Silent
			}
			text = strings.Join(parts, "\n\n")
		}
		err := s.send(ctx, c, settings, text, kb, silent)
		for _, p := range chunk {
			results[p.d.ID] = err
		}
	}
}

// send sends text (with kb) to every allow-listed chat; reaching one is success, the others are in the bot status.
func (s *Sender) send(ctx context.Context, c *telegram.Client, settings Settings, text string, kb *telegram.Keyboard, silent bool) error {
	if len(settings.Chats) == 0 {
		return errors.New("no allow-listed Telegram chat")
	}
	var errs []error
	delivered := 0
	for _, chat := range settings.Chats {
		if _, err := c.Send(ctx, chat, text, kb, silent); err != nil {
			errs = append(errs, fmt.Errorf("chat %d: %w", chat, err))
			continue
		}
		delivered++
	}
	err := errors.Join(errs...)
	if delivered > 0 {
		err = nil // reached someone; the others are in the bot status
	}
	_ = RecordBot(ctx, s.Pool, "", errors.Join(errs...), delivered > 0)
	return err
}

func (s *Sender) record(ctx context.Context, d delivery, derr error) error {
	var err error
	switch {
	case derr == nil:
		_, err = s.Pool.Exec(ctx, `UPDATE notification_deliveries SET state = 'sent', attempts = attempts + 1, error = NULL,
			sent_at = $2 WHERE id = $1`, d.ID, s.now())
	case errors.Is(derr, errDecided):
		_, err = s.Pool.Exec(ctx, `UPDATE notification_deliveries SET state = 'suppressed', error = $2 WHERE id = $1`,
			d.ID, derr.Error())
	case d.Attempts+1 >= MaxAttempts:
		_, err = s.Pool.Exec(ctx, `UPDATE notification_deliveries SET state = 'failed', attempts = attempts + 1, error = $2
			WHERE id = $1`, d.ID, derr.Error())
		s.Log.WarnContext(ctx, "notifications: telegram delivery failed", "delivery", d.ID, "class", d.Class, "err", derr)
	default:
		backoff := time.Duration(1<<d.Attempts) * 30 * time.Second
		_, err = s.Pool.Exec(ctx, `UPDATE notification_deliveries SET attempts = attempts + 1, error = $2, next_at = $3
			WHERE id = $1`, d.ID, derr.Error(), s.now().Add(backoff))
	}
	if err != nil {
		return fmt.Errorf("record delivery %s: %w", d.ID, err)
	}
	return nil
}
