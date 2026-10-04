package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/policies"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// CursorName is the router's row in event_cursors (migration 0015 starts it at the end of the outbox).
const CursorName = "notify"

// Delivery states.
const (
	StateQueued     = "queued"
	StateSent       = "sent"
	StateFailed     = "failed"
	StateSuppressed = "suppressed"
	StateDigest     = "digest"
	StateDigested   = "digested"
)

// ChannelTelegram is the one push channel.
const ChannelTelegram = "telegram"

// Router turns committed events into Telegram deliveries by the routing table. It reads events after its cursor
// and writes the deliveries in the transaction that advances it, so every event is routed exactly once; a restart
// resumes at the cursor.
type Router struct {
	Pool     *pgxpool.Pool
	Hub      *events.Hub // nil: poll only
	Log      *slog.Logger
	Defaults func() *defaults.Defaults
	Now      func() time.Time
	// Wake is signalled (non-blocking) after a batch queued deliveries; the Sender listens.
	Wake chan<- struct{}
	// PollInterval is how often the table is read without a wake-up from the hub.
	PollInterval time.Duration
	Batch        int
}

// Run routes until ctx is cancelled.
func (rt *Router) Run(ctx context.Context) error {
	if rt.PollInterval <= 0 {
		rt.PollInterval = time.Second
	}
	var sub *events.Subscription
	subscribe := func() <-chan events.Record {
		if rt.Hub == nil {
			return nil
		}
		sub = rt.Hub.Subscribe()
		return sub.C
	}
	wake := subscribe()
	defer func() {
		if sub != nil {
			sub.Close()
		}
	}()
	tick := time.NewTicker(rt.PollInterval)
	defer tick.Stop()
	for {
		if _, err := rt.CatchUp(ctx); err != nil && ctx.Err() == nil {
			rt.Log.WarnContext(ctx, "notifications: routing failed; retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		case _, ok := <-wake:
			if !ok {
				wake = nil
				if ctx.Err() == nil {
					wake = subscribe()
				}
			}
		}
	}
}

// CatchUp routes every committed event after the cursor and returns how many it consumed.
func (rt *Router) CatchUp(ctx context.Context) (int, error) {
	if rt.Batch <= 0 {
		rt.Batch = 500
	}
	total := 0
	for {
		n, queued, err := rt.step(ctx)
		total += n
		if queued > 0 && rt.Wake != nil {
			select {
			case rt.Wake <- struct{}{}:
			default:
			}
		}
		if err != nil || n < rt.Batch {
			return total, err
		}
	}
}

func (rt *Router) now() time.Time {
	if rt.Now != nil {
		return rt.Now()
	}
	return time.Now()
}

func (rt *Router) step(ctx context.Context) (n, queued int, err error) {
	err = pgx.BeginFunc(ctx, rt.Pool, func(tx pgx.Tx) error {
		var cursor int64
		err := tx.QueryRow(ctx, `SELECT seq FROM event_cursors WHERE name = $1 FOR UPDATE`, CursorName).Scan(&cursor)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := tx.Exec(ctx, `INSERT INTO event_cursors (name, seq) SELECT $1, coalesce(max(seq), 0) FROM events
				ON CONFLICT DO NOTHING`, CursorName); err != nil {
				return fmt.Errorf("create notify cursor: %w", err)
			}
			return nil
		} else if err != nil {
			return fmt.Errorf("read notify cursor: %w", err)
		}
		batch, err := events.List(ctx, tx, events.Filter{}, cursor, rt.Batch)
		if err != nil {
			return err
		}
		n = len(batch)
		if n == 0 {
			return nil
		}
		queued, err = rt.route(ctx, tx, batch)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE event_cursors SET seq = $2, updated_at = now() WHERE name = $1`,
			CursorName, batch[n-1].Seq); err != nil {
			return fmt.Errorf("advance notify cursor: %w", err)
		}
		return nil
	})
	return n, queued, err
}

// Decision is what the routing table says about one notice on Telegram.
type Decision struct {
	State  string // queued | suppressed | digest; "" = not sent to Telegram
	Silent bool   // sent without sound (the rule's silent flag)
}

// Decide applies rule, quiet hours and whether the bot is usable to a notice of rule's class at now.
func Decide(rule Rule, s Settings, botReady bool, now time.Time, loc *time.Location) Decision {
	if !rule.Telegram || !botReady {
		return Decision{}
	}
	switch rule.Timing {
	case TimingDigest:
		return Decision{State: StateDigest, Silent: rule.Silent}
	case TimingImmediate, TimingDaily:
		if InQuietHours(s.QuietHours, now, loc) && !rule.BypassQuietHours {
			return Decision{State: StateSuppressed, Silent: rule.Silent}
		}
		return Decision{State: StateQueued, Silent: rule.Silent}
	}
	return Decision{}
}

// Env is what routing needs to know beyond the rules: the settings, the timezone and whether the bot is usable.
type Env struct {
	Settings Settings
	Location *time.Location
	BotReady bool
	Rules    map[string]Rule
}

// LoadEnv reads the routing environment in q.
func LoadEnv(ctx context.Context, q storage.Querier, d *defaults.Defaults) (Env, error) {
	s, err := LoadSettings(ctx, q, d)
	if err != nil {
		return Env{}, err
	}
	p, err := policies.Get(ctx, q, d)
	if err != nil {
		return Env{}, err
	}
	rules, err := Rules(ctx, q)
	if err != nil {
		return Env{}, err
	}
	env := Env{Settings: s, Location: p.Location(), Rules: map[string]Rule{}}
	for _, r := range rules {
		env.Rules[r.EventClass] = r
	}
	tokenSet, err := TokenStored(ctx, q)
	if err != nil {
		return Env{}, err
	}
	env.BotReady = tokenSet && len(s.Chats) > 0
	return env, nil
}

// TokenStored reports whether the bot token secret exists.
func TokenStored(ctx context.Context, q storage.Querier) (bool, error) {
	var ok bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM secrets WHERE name = $1)`, TokenSecret).Scan(&ok); err != nil {
		return false, fmt.Errorf("look up the telegram token: %w", err)
	}
	return ok, nil
}

func (rt *Router) route(ctx context.Context, tx pgx.Tx, batch []events.Record) (int, error) {
	var (
		env    Env
		loaded bool
		queued int
	)
	for _, r := range batch {
		notice, ok := Classify(r)
		if !ok {
			continue
		}
		if !loaded {
			var err error
			if env, err = LoadEnv(ctx, tx, rt.Defaults()); err != nil {
				return 0, err
			}
			loaded = true
		}
		rule, ok := env.Rules[notice.Class]
		if !ok {
			continue
		}
		now := rt.now()
		d := Decide(rule, env.Settings, env.BotReady, now, env.Location)
		if d.State == "" {
			continue
		}
		var at *time.Time
		if d.State == StateQueued && notice.ApprovalID != "" {
			t, err := ApprovalSendAt(ctx, tx, now, time.Duration(rt.Defaults().Notifications.ApprovalBatchS.Value)*time.Second)
			if err != nil {
				return 0, err
			}
			at = &t
		}
		seq := r.Seq
		if err := InsertDelivery(ctx, tx, &seq, notice, d, at); err != nil {
			return 0, err
		}
		if d.State == StateQueued {
			queued++
		}
	}
	return queued, nil
}

// ApprovalSendAt is when an approval request routed at now goes to Telegram: approvals are gathered for window and
// sent as one message when the window the first of them opened closes (docs/spec/06-platform.md "Quieter
// Telegram"). A request joins the open window — a queued approval message not tried yet and not due — or opens one.
// window 0 sends at once.
func ApprovalSendAt(ctx context.Context, q storage.Querier, now time.Time, window time.Duration) (time.Time, error) {
	if window <= 0 {
		return now, nil
	}
	var open *time.Time
	if err := q.QueryRow(ctx, `SELECT max(next_at) FROM notification_deliveries WHERE state = 'queued' AND attempts = 0
		AND approval_id IS NOT NULL AND next_at > $1`, now).Scan(&open); err != nil {
		return time.Time{}, fmt.Errorf("find the open approval batch: %w", err)
	}
	if open != nil {
		return *open, nil
	}
	return now.Add(window), nil
}

// InsertDelivery writes one Telegram delivery as d decided, due at at (nil: now); an event already routed, or a
// notice whose Key was already delivered, is skipped.
func InsertDelivery(ctx context.Context, q storage.Querier, seq *int64, n Notice, d Decision, at *time.Time) error {
	id := "ntf_" + uuid.Must(uuid.NewV7()).String()
	if _, err := q.Exec(ctx, `INSERT INTO notification_deliveries (id, event_seq, event_class, channel, state, title, body,
		approval_id, silent, dedupe_key, next_at) VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), $9, NULLIF($10, ''),
		coalesce($11::timestamptz, now())) ON CONFLICT DO NOTHING`,
		id, seq, n.Class, ChannelTelegram, d.State, n.Title, n.Body, n.ApprovalID, d.Silent, n.Key, at); err != nil {
		return fmt.Errorf("store notification delivery: %w", err)
	}
	return nil
}
