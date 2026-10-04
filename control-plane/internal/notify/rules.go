// Package notify routes domain events to people (docs/spec/06-platform.md "Notifications"): one routing table by
// event class (notification_rules, seeded with the spec's table), two channels — the in-app history, which the web
// shell builds from the event stream, and a Telegram bot — quiet hours that hold Telegram back except for failures,
// a daily digest, and approvals decided from the phone through single-use signed buttons.
//
// The Router reads committed events after its own cursor (like the search indexer), classifies each one and writes
// a delivery row per Telegram message in the transaction that advances the cursor; the Sender delivers queued rows
// outside any transaction and retries failures; the Bot long-polls Telegram for button presses. Nothing here
// decides an approval itself: the Bot hands a verified press to a Decider (the server), which runs approvals.approve
// or approvals.deny as the admin with channel telegram.
package notify

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity kinds and the topic of notification changes.
const (
	RuleKind     = "notification_rule"
	SettingsKind = "notification_settings"
	// Topic carries the digest and delivery failures for the in-app history.
	Topic = "notifications"
)

// Event classes (the contract's NotificationClass).
const (
	ClassApproval = "approval_requested"
	ClassFailure  = "failure"
	ClassOutcome  = "outcome"
	ClassProgress = "progress"
	ClassDigest   = "digest"
)

// Timings (the contract's NotificationTiming).
const (
	TimingImmediate = "immediate"
	TimingDigest    = "digest"
	TimingDaily     = "daily"
	TimingNone      = "none"
)

// seeded is the spec's table (migrations 0015 and 0043), the baseline departures are measured against.
var seeded = map[string]Rule{
	ClassApproval: {InApp: true, Telegram: true, Timing: TimingImmediate},
	ClassFailure:  {InApp: true, Telegram: true, Timing: TimingImmediate},
	ClassOutcome:  {InApp: true, Telegram: true, Timing: TimingImmediate, Silent: true},
	ClassProgress: {InApp: true, Telegram: false, Timing: TimingNone},
	ClassDigest:   {InApp: true, Telegram: true, Timing: TimingDaily, Silent: true},
}

// Rule is one row of the routing table. Its JSON form is the contract's NotificationRule.
type Rule struct {
	ID               string    `json:"id"`
	EventClass       string    `json:"eventClass"`
	Label            string    `json:"label"`
	Events           []string  `json:"events"`
	InApp            bool      `json:"-"`
	Telegram         bool      `json:"-"`
	Timing           string    `json:"timing"`
	Silent           bool      `json:"silent"` // Telegram messages arrive without sound (disable_notification)
	BypassQuietHours bool      `json:"bypassQuietHours"`
	Rev              int       `json:"rev"`
	UpdatedAt        time.Time `json:"updatedAt"`
}

// View is a rule's JSON form.
type View struct {
	Rule
	Channels   Channels `json:"channels"`
	Departures []string `json:"departures"`
}

// Channels is the contract's NotificationChannels.
type Channels struct {
	InApp    bool `json:"inApp"`
	Telegram bool `json:"telegram"`
}

// JSON renders r as the contract's NotificationRule.
func (r Rule) JSON() View {
	v := View{Rule: r, Channels: Channels{InApp: r.InApp, Telegram: r.Telegram}, Departures: []string{}}
	if v.Events == nil {
		v.Events = EventTypes(r.EventClass)
	}
	if base, ok := seeded[r.EventClass]; ok {
		if r.InApp != base.InApp {
			v.Departures = append(v.Departures, "channels.inApp")
		}
		if r.Telegram != base.Telegram {
			v.Departures = append(v.Departures, "channels.telegram")
		}
		if r.Timing != base.Timing {
			v.Departures = append(v.Departures, "timing")
		}
		if r.Silent != base.Silent {
			v.Departures = append(v.Departures, "silent")
		}
	}
	return v
}

const ruleCols = "id, event_class, label, in_app, telegram, timing, silent, bypass_quiet_hours, rev, updated_at"

func scanRule(row pgx.CollectableRow) (Rule, error) {
	var r Rule
	err := row.Scan(&r.ID, &r.EventClass, &r.Label, &r.InApp, &r.Telegram, &r.Timing, &r.Silent, &r.BypassQuietHours,
		&r.Rev, &r.UpdatedAt)
	r.Events = EventTypes(r.EventClass)
	return r, err
}

// Rules returns the routing table in table order.
func Rules(ctx context.Context, q storage.Querier) ([]Rule, error) {
	rows, err := q.Query(ctx, "SELECT "+ruleCols+" FROM notification_rules ORDER BY position")
	if err != nil {
		return nil, fmt.Errorf("list notification rules: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanRule)
	if err != nil {
		return nil, fmt.Errorf("read notification rules: %w", err)
	}
	return out, nil
}

// RuleFor returns the rule of class.
func RuleFor(ctx context.Context, q storage.Querier, class string) (Rule, error) {
	rows, err := q.Query(ctx, "SELECT "+ruleCols+" FROM notification_rules WHERE event_class = $1", class)
	if err != nil {
		return Rule{}, fmt.Errorf("read notification rule %s: %w", class, err)
	}
	r, err := pgx.CollectExactlyOneRow(rows, scanRule)
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, problems.NotFound.New("no notification rule for class %q", class)
	}
	if err != nil {
		return Rule{}, fmt.Errorf("read notification rule %s: %w", class, err)
	}
	return r, nil
}

// RuleEdit is the body of notificationRules.edit; nil fields stay as they are.
type RuleEdit struct {
	InApp    *bool
	Telegram *bool
	Timing   *string
	Silent   *bool
}

// EditRule changes the rule id at revision rev. The digest rule's timing is always daily and no other rule may
// be daily; progress never interrupts anyone, so it may be held for the digest but not sent immediately.
func EditRule(ctx context.Context, tx pgx.Tx, id string, rev int, in RuleEdit) (Rule, []events.Draft, error) {
	rows, err := tx.Query(ctx, "SELECT "+ruleCols+" FROM notification_rules WHERE id = $1 FOR UPDATE", id)
	if err != nil {
		return Rule{}, nil, fmt.Errorf("read notification rule: %w", err)
	}
	r, err := pgx.CollectExactlyOneRow(rows, scanRule)
	if errors.Is(err, pgx.ErrNoRows) {
		return Rule{}, nil, problems.NotFound.New("no notification rule %q", id)
	}
	if err != nil {
		return Rule{}, nil, fmt.Errorf("read notification rule: %w", err)
	}
	if err := commands.CheckRev(RuleKind, rev, r.Rev); err != nil {
		return Rule{}, nil, err
	}
	if in.InApp != nil {
		r.InApp = *in.InApp
	}
	if in.Telegram != nil {
		r.Telegram = *in.Telegram
	}
	if in.Timing != nil {
		r.Timing = *in.Timing
	}
	if in.Silent != nil {
		r.Silent = *in.Silent
	}
	var fields []problems.FieldError
	switch {
	case !slices.Contains([]string{TimingImmediate, TimingDigest, TimingDaily, TimingNone}, r.Timing):
		fields = append(fields, problems.FieldError{Path: "/timing", Message: fmt.Sprintf("unknown timing %q", r.Timing)})
	case r.EventClass == ClassDigest && r.Timing != TimingDaily && r.Timing != TimingNone:
		fields = append(fields, problems.FieldError{Path: "/timing", Message: "the digest goes out daily (or not at all: none)"})
	case r.EventClass != ClassDigest && r.Timing == TimingDaily:
		fields = append(fields, problems.FieldError{Path: "/timing", Message: "only the digest rule is daily; hold events for it with timing digest"})
	case r.EventClass == ClassProgress && r.Telegram && r.Timing == TimingImmediate:
		fields = append(fields, problems.FieldError{Path: "/timing", Message: "progress is never pushed to the phone as it happens; use digest"})
	}
	if len(fields) > 0 {
		return Rule{}, nil, problems.Validation(fields)
	}
	rows, err = tx.Query(ctx, `UPDATE notification_rules SET in_app = $2, telegram = $3, timing = $4, silent = $5,
		rev = rev + 1, updated_at = now() WHERE id = $1 RETURNING `+ruleCols, id, r.InApp, r.Telegram, r.Timing, r.Silent)
	if err != nil {
		return Rule{}, nil, fmt.Errorf("update notification rule: %w", err)
	}
	r, err = pgx.CollectExactlyOneRow(rows, scanRule)
	if err != nil {
		return Rule{}, nil, fmt.Errorf("update notification rule: %w", err)
	}
	return r, []events.Draft{{
		Topic: events.EntityTopic(RuleKind, r.ID), Type: "notification_rule.edited",
		Entity:  &events.EntityRef{Kind: RuleKind, ID: r.ID, Rev: r.Rev},
		Payload: map[string]any{"rule": r.JSON()},
	}}, nil
}
