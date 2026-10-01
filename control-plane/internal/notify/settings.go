package notify

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// SettingsID is the id of the one settings row.
const SettingsID = "instance"

// TokenSecret is the name of the bot token in the secret store (R9).
const TokenSecret = "telegram-bot-token" //nolint:gosec // a secret's name, not its value

// MaxChats bounds the allowlist (the contract's maxItems).
const MaxChats = 20

// maxSeen is how many not-allowed chats Settings lists.
const maxSeen = 5

// Chat is a Telegram chat as Settings shows it.
type Chat struct {
	ID     int64      `json:"id"`
	Title  string     `json:"title,omitempty"`
	SeenAt *time.Time `json:"seenAt,omitempty"`
}

// QuietHours are local clock times; Start after End spans midnight.
type QuietHours struct {
	Enabled bool   `json:"enabled"`
	Start   string `json:"start"`
	End     string `json:"end"`
}

// Settings are the effective notification settings. Its JSON form (with the Telegram status the server adds) is
// the contract's NotificationSettings.
type Settings struct {
	Rev        int
	UpdatedAt  time.Time
	QuietHours QuietHours
	DigestTime string
	Chats      []int64
	Seen       []Chat
	// Bot status, maintained by the Sender and the Bot.
	BotUsername  string
	LastError    string
	LastSentAt   *time.Time
	DigestSentOn *time.Time
}

// Allowed reports whether chat is allow-listed.
func (s Settings) Allowed(chat int64) bool { return slices.Contains(s.Chats, chat) }

// LoadSettings reads the settings row with defaults d for unset values.
func LoadSettings(ctx context.Context, q storage.Querier, d *defaults.Defaults) (Settings, error) {
	return loadSettings(ctx, q, d, "")
}

func loadSettings(ctx context.Context, q storage.Querier, d *defaults.Defaults, lock string) (Settings, error) {
	var (
		s                       Settings
		enabled                 *bool
		start, end, digest, usr *string
		errText                 *string
		chats, seen             []byte
	)
	err := q.QueryRow(ctx, `SELECT rev, updated_at, quiet_hours_enabled, quiet_hours_start, quiet_hours_end,
		digest_time, telegram_chats, telegram_seen, telegram_username, telegram_error, telegram_sent_at, digest_sent_on
		FROM notification_settings WHERE id = $1 `+lock, SettingsID).Scan(&s.Rev, &s.UpdatedAt, &enabled, &start, &end,
		&digest, &chats, &seen, &usr, &errText, &s.LastSentAt, &s.DigestSentOn)
	if err != nil {
		return Settings{}, fmt.Errorf("read notification settings: %w", err)
	}
	n := d.Notifications
	s.QuietHours = QuietHours{Enabled: or(enabled, n.QuietHoursEnabled.Value), Start: or(start, n.QuietHoursStart.Value),
		End: or(end, n.QuietHoursEnd.Value)}
	s.DigestTime = or(digest, n.DigestTime.Value)
	s.BotUsername, s.LastError = or(usr, ""), or(errText, "")
	if err := json.Unmarshal(chats, &s.Chats); err != nil {
		return Settings{}, fmt.Errorf("decode telegram chats: %w", err)
	}
	if err := json.Unmarshal(seen, &s.Seen); err != nil {
		return Settings{}, fmt.Errorf("decode telegram seen chats: %w", err)
	}
	if s.Chats == nil {
		s.Chats = []int64{}
	}
	return s, nil
}

func or[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// SettingsEdit is the body of notificationSettings.edit; nil fields stay as they are.
type SettingsEdit struct {
	QuietEnabled *bool
	QuietStart   *string
	QuietEnd     *string
	DigestTime   *string
	Chats        *[]int64
}

// EditSettings changes the settings at revision rev and returns them with the event to emit.
func EditSettings(ctx context.Context, tx pgx.Tx, rev int, in SettingsEdit, d *defaults.Defaults) (Settings, []events.Draft, error) {
	cur, err := loadSettings(ctx, tx, d, "FOR UPDATE")
	if err != nil {
		return Settings{}, nil, err
	}
	if err := commands.CheckRev(SettingsKind, rev, cur.Rev); err != nil {
		return Settings{}, nil, err
	}
	var fields []problems.FieldError
	for path, v := range map[string]*string{"/quietHours/start": in.QuietStart, "/quietHours/end": in.QuietEnd, "/digestTime": in.DigestTime} {
		if v == nil {
			continue
		}
		if _, err := ParseClock(*v); err != nil {
			fields = append(fields, problems.FieldError{Path: path, Message: err.Error()})
		}
	}
	chats := cur.Chats
	if in.Chats != nil {
		chats = []int64{}
		for _, c := range *in.Chats {
			if c == 0 {
				fields = append(fields, problems.FieldError{Path: "/telegramChats", Message: "0 is not a Telegram chat id"})
			}
			if !slices.Contains(chats, c) {
				chats = append(chats, c)
			}
		}
		if len(chats) > MaxChats {
			fields = append(fields, problems.FieldError{Path: "/telegramChats", Message: fmt.Sprintf("at most %d chats", MaxChats)})
		}
	}
	if len(fields) > 0 {
		slices.SortFunc(fields, func(a, b problems.FieldError) int { return cmp.Compare(a.Path, b.Path) })
		return Settings{}, nil, problems.Validation(fields)
	}
	// An allowed chat leaves the not-allowed list.
	seen := slices.DeleteFunc(slices.Clone(cur.Seen), func(c Chat) bool { return slices.Contains(chats, c.ID) })
	chatsJSON, _ := json.Marshal(chats)
	seenJSON, _ := json.Marshal(seen)
	if _, err := tx.Exec(ctx, `UPDATE notification_settings SET
			quiet_hours_enabled = coalesce($2, quiet_hours_enabled), quiet_hours_start = coalesce($3, quiet_hours_start),
			quiet_hours_end = coalesce($4, quiet_hours_end), digest_time = coalesce($5, digest_time),
			telegram_chats = $6, telegram_seen = $7, rev = rev + 1, updated_at = now()
		WHERE id = $1`, SettingsID, in.QuietEnabled, in.QuietStart, in.QuietEnd, in.DigestTime, chatsJSON, seenJSON); err != nil {
		return Settings{}, nil, fmt.Errorf("update notification settings: %w", err)
	}
	s, err := loadSettings(ctx, tx, d, "")
	if err != nil {
		return Settings{}, nil, err
	}
	return s, SettingsEvent(s, "notification_settings.edited"), nil
}

// SettingsEvent is the event of a settings change; the payload names the revision only (the view with the bot's
// status is the server's to render).
func SettingsEvent(s Settings, typ string) []events.Draft {
	return []events.Draft{{
		Topic: events.EntityTopic(SettingsKind, SettingsID), Type: typ,
		Entity:  &events.EntityRef{Kind: SettingsKind, ID: SettingsID, Rev: s.Rev},
		Payload: map[string]any{"rev": s.Rev},
	}}
}

// BumpSettings advances the settings revision (the bot token changed) and returns the event.
func BumpSettings(ctx context.Context, tx pgx.Tx, d *defaults.Defaults) (Settings, []events.Draft, error) {
	if _, err := tx.Exec(ctx, `UPDATE notification_settings SET rev = rev + 1, updated_at = now(), telegram_error = NULL,
		telegram_username = NULL WHERE id = $1`, SettingsID); err != nil {
		return Settings{}, nil, fmt.Errorf("update notification settings: %w", err)
	}
	s, err := loadSettings(ctx, tx, d, "")
	if err != nil {
		return Settings{}, nil, err
	}
	return s, SettingsEvent(s, "notification_settings.edited"), nil
}

// RecordBot stores the bot's status after a Bot API call: the username after a getMe, the last error ("" clears
// it), and the time of the last message sent. It is status, not configuration: no revision, no event.
func RecordBot(ctx context.Context, q storage.Querier, username string, callErr error, sent bool) error {
	var errText *string
	if callErr != nil {
		t := callErr.Error()
		errText = &t
	}
	_, err := q.Exec(ctx, `UPDATE notification_settings SET telegram_username = coalesce(NULLIF($2, ''), telegram_username),
		telegram_error = $3, telegram_sent_at = CASE WHEN $4 THEN now() ELSE telegram_sent_at END WHERE id = $1`,
		SettingsID, username, errText, sent)
	if err != nil {
		return fmt.Errorf("record telegram status: %w", err)
	}
	return nil
}

// RecordSeen remembers a chat that wrote to the bot without being allowed (the newest maxSeen).
func RecordSeen(ctx context.Context, pool *pgxpool.Pool, c Chat) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT telegram_seen FROM notification_settings WHERE id = $1 FOR UPDATE`, SettingsID).Scan(&raw); err != nil {
			return fmt.Errorf("read telegram seen chats: %w", err)
		}
		var seen []Chat
		if err := json.Unmarshal(raw, &seen); err != nil {
			return fmt.Errorf("decode telegram seen chats: %w", err)
		}
		seen = slices.DeleteFunc(seen, func(x Chat) bool { return x.ID == c.ID })
		seen = append([]Chat{c}, seen...)
		if len(seen) > maxSeen {
			seen = seen[:maxSeen]
		}
		b, _ := json.Marshal(seen)
		if _, err := tx.Exec(ctx, `UPDATE notification_settings SET telegram_seen = $2 WHERE id = $1`, SettingsID, b); err != nil {
			return fmt.Errorf("record telegram seen chat: %w", err)
		}
		return nil
	})
}
