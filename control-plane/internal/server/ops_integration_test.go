//go:build integration

package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/approvals"
	"github.com/usunrise88/cadence/control-plane/internal/backups"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/notify"
	"github.com/usunrise88/cadence/control-plane/internal/notify/telegram"
	"github.com/usunrise88/cadence/control-plane/internal/testdb"
)

// fakeTelegram is a Bot API stand-in: it records every call and serves queued updates to getUpdates.
type fakeTelegram struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []fakeCall
	updates []telegram.Update
	nextID  int64
	token   string
}

type fakeCall struct {
	Method string
	Body   map[string]any
}

func newFakeTelegram(t *testing.T) *fakeTelegram {
	f := &fakeTelegram{nextID: 100}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.token = parts[0]
		f.calls = append(f.calls, fakeCall{Method: parts[1], Body: body})
		var result any = true
		switch parts[1] {
		case "getMe":
			result = map[string]any{"id": 1, "is_bot": true, "first_name": "Cadence", "username": "cadence_test_bot"}
		case "sendMessage":
			f.nextID++
			result = map[string]any{"message_id": f.nextID, "chat": map[string]any{"id": body["chat_id"], "type": "private"}, "text": body["text"]}
		case "getUpdates":
			ups := f.updates
			if ups == nil {
				ups = []telegram.Update{}
			}
			result, f.updates = ups, nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeTelegram) push(u telegram.Update) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u.UpdateID = int64(len(f.calls) + 1000)
	f.updates = append(f.updates, u)
}

// sent returns the calls of method since the last take and forgets them.
func (f *fakeTelegram) take(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out, rest []fakeCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		} else {
			rest = append(rest, c)
		}
	}
	f.calls = rest
	return out
}

func press(chat int64, messageID int64, text, data string) telegram.Update {
	return telegram.Update{CallbackQuery: &telegram.CallbackQuery{
		ID: "cb-" + data[:min(4, len(data))], From: telegram.User{ID: 7, Username: "anton"}, Data: data,
		Message: &telegram.Message{MessageID: messageID, Chat: telegram.Chat{ID: chat, Type: "private"}, Text: text},
	}}
}

type opsNotify struct {
	router *notify.Router
	sender *notify.Sender
	poller *notify.Poller
	signer *notify.Signer
}

func (e *env) notifyLoops(f *fakeTelegram) opsNotify {
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	bot := e.admin.telegram()
	bot.BaseURL = f.URL
	signer := notify.NewSigner(e.admin.Secrets.DeriveKey("telegram-callback"))
	return opsNotify{
		router: &notify.Router{Pool: e.pool, Log: quiet, Defaults: defaults.Get},
		sender: &notify.Sender{Pool: e.pool, Log: quiet, Bot: bot, Signer: signer, Defaults: defaults.Get},
		poller: &notify.Poller{Pool: e.pool, Log: quiet, Bot: bot, Signer: signer, Decider: e.admin, Defaults: defaults.Get, Timeout: 1},
		signer: signer,
	}
}

type settingsView struct {
	Rev        int    `json:"rev"`
	Timezone   string `json:"timezone"`
	DigestTime string `json:"digestTime"`
	QuietHours struct {
		Enabled    bool
		Start, End string
	} `json:"quietHours"`
	Telegram struct {
		TokenSet     bool   `json:"tokenSet"`
		BotUsername  string `json:"botUsername"`
		Chats        []struct{ ID int64 }
		PendingChats []struct {
			ID    int64
			Title string
		} `json:"pendingChats"`
		LastError string `json:"lastError"`
	} `json:"telegram"`
}

func (e *env) settings() settingsView {
	e.t.Helper()
	var s settingsView
	e.ok(e.do("GET", "/api/notification-settings", ""), 200, &s)
	return s
}

func rev(n int) string { return strconv.Quote(strconv.Itoa(n)) }

// setUpBot stores a token and allows chat 1001.
func (e *env) setUpBot() {
	e.t.Helper()
	var s settingsView
	e.ok(e.do("PUT", "/api/telegram-bot", `{"token":"123456:ABCdefGHIjklMNOpqrSTU"}`, "Idempotency-Key", e.key()), 200, &s)
	if !s.Telegram.TokenSet {
		e.t.Fatalf("token not stored: %+v", s)
	}
	e.ok(e.do("PATCH", "/api/notification-settings", `{"telegramChats":[1001]}`, "Idempotency-Key", e.key(), "If-Match", rev(s.Rev)), 200, &s)
	if len(s.Telegram.Chats) != 1 || s.Telegram.Chats[0].ID != 1001 {
		e.t.Fatalf("chats %+v", s.Telegram.Chats)
	}
}

func TestNotificationRulesAndSettings(t *testing.T) {
	e := start(t)
	var rules struct {
		Items []struct {
			ID, EventClass, Timing string
			Channels               struct{ InApp, Telegram bool }
			Silent                 bool
			BypassQuietHours       bool
			Rev                    int
			Departures             []string
		}
	}
	e.ok(e.do("GET", "/api/notification-rules", ""), 200, &rules)
	got := []string{}
	for _, r := range rules.Items {
		got = append(got, r.EventClass+":"+r.Timing+":"+strconv.FormatBool(r.Channels.Telegram)+":"+strconv.FormatBool(r.Silent))
	}
	if strings.Join(got, " ") != "approval_requested:immediate:true:false failure:immediate:true:false outcome:immediate:true:true progress:none:false:false digest:daily:true:true" ||
		!rules.Items[1].BypassQuietHours || rules.Items[0].BypassQuietHours {
		t.Fatalf("seeded table %v", got)
	}

	var r struct {
		Timing     string
		Rev        int
		Departures []string
		Channels   struct{ Telegram bool }
	}
	e.ok(e.do("PATCH", "/api/notification-rules/ntr_outcome", `{"channels":{"telegram":false},"timing":"digest"}`,
		"Idempotency-Key", e.key(), "If-Match", rev(1)), 200, &r)
	if r.Rev != 2 || r.Channels.Telegram || r.Timing != "digest" || strings.Join(r.Departures, ",") != "channels.telegram,timing" {
		t.Fatalf("edited rule %+v", r)
	}
	expectProblem(t, e.do("PATCH", "/api/notification-rules/ntr_outcome", `{"timing":"immediate"}`, "Idempotency-Key", e.key(), "If-Match", rev(1)), 412, "precondition-failed")
	// A failure may be made silent; it then departs from the seeded table.
	var f struct {
		Silent     bool
		Departures []string
	}
	e.ok(e.do("PATCH", "/api/notification-rules/ntr_failure", `{"silent":true}`, "Idempotency-Key", e.key(), "If-Match", rev(1)), 200, &f)
	if !f.Silent || strings.Join(f.Departures, ",") != "silent" {
		t.Fatalf("silent failure rule %+v", f)
	}
	expectProblem(t, e.do("PATCH", "/api/notification-rules/ntr_failure", `{"timing":"daily"}`, "Idempotency-Key", e.key(), "If-Match", rev(2)), 422, "validation-failed")
	expectProblem(t, e.do("PATCH", "/api/notification-rules/ntr_progress", `{"channels":{"telegram":true},"timing":"immediate"}`, "Idempotency-Key", e.key(), "If-Match", rev(1)), 422, "validation-failed")
	// An agent session never changes routing or takes backups (the preset's admin-only rule).
	expectProblem(t, e.agent("PATCH", "/api/notification-rules/ntr_failure", `{"channels":{"telegram":false}}`, "Idempotency-Key", e.key(), "If-Match", rev(2)), 403, "policy-denied")
	expectProblem(t, e.agent("POST", "/api/backups", "", "Idempotency-Key", e.key()), 403, "policy-denied")

	s := e.settings()
	if s.Timezone != "UTC" || s.DigestTime != "09:00" || s.QuietHours.Enabled || s.Telegram.TokenSet {
		t.Fatalf("default settings %+v", s)
	}
	e.ok(e.do("PATCH", "/api/notification-settings", `{"quietHours":{"enabled":true,"start":"23:00","end":"07:30"},"digestTime":"08:45"}`,
		"Idempotency-Key", e.key(), "If-Match", rev(s.Rev)), 200, &s)
	if !s.QuietHours.Enabled || s.QuietHours.Start != "23:00" || s.QuietHours.End != "07:30" || s.DigestTime != "08:45" {
		t.Fatalf("edited settings %+v", s)
	}
	expectProblem(t, e.do("PATCH", "/api/notification-settings", `{"telegramChats":[0]}`, "Idempotency-Key", e.key(), "If-Match", rev(s.Rev)), 422, "validation-failed")

	// The timezone is a policy; the settings follow it.
	var p struct {
		Timezone   string
		Rev        int
		Departures []string
	}
	e.ok(e.do("GET", "/api/policies", ""), 200, &p)
	e.ok(e.do("PATCH", "/api/policies", `{"timezone":"Europe/Berlin"}`, "Idempotency-Key", e.key(), "If-Match", rev(p.Rev)), 200, &p)
	if p.Timezone != "Europe/Berlin" || !strings.Contains(strings.Join(p.Departures, ","), "timezone") {
		t.Fatalf("policies %+v", p)
	}
	expectProblem(t, e.do("PATCH", "/api/policies", `{"timezone":"Mars/Olympus"}`, "Idempotency-Key", e.key(), "If-Match", rev(p.Rev)), 422, "validation-failed")
	if s := e.settings(); s.Timezone != "Europe/Berlin" {
		t.Fatalf("settings timezone %q", s.Timezone)
	}
}

func TestTelegramBotAndApprovalFromThePhone(t *testing.T) {
	fake := newFakeTelegram(t)
	e := startWith(t, func(c *Config) { c.Telegram = notify.Bot{BaseURL: fake.URL} })
	loops := e.notifyLoops(fake)
	ctx := context.Background()

	// Without a token, verify says so and nothing reaches Telegram.
	var v struct {
		Ok          bool
		Error       string
		BotUsername string
		Chats       []struct {
			ID        int64
			Delivered bool
		}
	}
	e.ok(e.do("POST", "/api/telegram-bot:verify", "", "Idempotency-Key", e.key()), 200, &v)
	if v.Ok || !strings.Contains(v.Error, "no bot token") {
		t.Fatalf("verify without token %+v", v)
	}
	e.setUpBot()
	// Replacing the token needs the settings revision.
	expectProblem(t, e.do("PUT", "/api/telegram-bot", `{"token":"123456:ABCdefGHIjklMNOpqrSTU"}`, "Idempotency-Key", e.key()), 428, "precondition-required")
	e.ok(e.do("PUT", "/api/telegram-bot", `{"token":"654321:ZYXwvuTSRqpoNMLkjiHGF"}`, "Idempotency-Key", e.key(), "If-Match", rev(e.settings().Rev)), 200, nil)
	if n := e.count(`SELECT count(*) FROM secrets WHERE name = 'telegram-bot-token' AND kind = 'telegram' AND rev = 2`); n != 1 {
		t.Fatalf("token secret rows %d", n)
	}

	e.ok(e.do("POST", "/api/telegram-bot:verify", "", "Idempotency-Key", e.key()), 200, &v)
	if !v.Ok || v.BotUsername != "cadence_test_bot" || len(v.Chats) != 1 || !v.Chats[0].Delivered {
		t.Fatalf("verify %+v", v)
	}
	if fake.token != "654321:ZYXwvuTSRqpoNMLkjiHGF" {
		t.Fatalf("the bot used token %q", fake.token)
	}
	if msgs := fake.take("sendMessage"); len(msgs) != 1 || msgs[0].Body["chat_id"].(float64) != 1001 {
		t.Fatalf("test message %+v", msgs)
	}
	fake.take("getMe")

	// An agent's gated command: the router queues the approval, the sender sends it with Approve / Deny once the
	// batching window (notifications.approval_batch_s) has closed.
	e.newProject("demo")
	id := e.gateArchive("demo", e.key(), 2)
	if _, err := loops.router.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	loops.sender.Now = func() time.Time { return time.Now().Add(3 * time.Minute) }
	if _, err := loops.sender.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := fake.take("sendMessage")
	if len(msgs) != 1 {
		t.Fatalf("messages %+v", msgs)
	}
	text := msgs[0].Body["text"].(string)
	if !strings.HasPrefix(text, "Approval requested: projects.archive") {
		t.Fatalf("text %q", text)
	}
	row := msgs[0].Body["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)
	approveData := row[0].(map[string]any)["callback_data"].(string)
	denyData := row[1].(map[string]any)["callback_data"].(string)
	if len(approveData) > 64 || !strings.HasPrefix(approveData, "a.") || !strings.HasPrefix(denyData, "d.") {
		t.Fatalf("buttons %q %q", approveData, denyData)
	}

	// A tampered button, and a press from a chat that is not allowed, decide nothing.
	fake.push(press(1001, 101, text, "a."+approveData[2:18]+".AAAAAAAAAAAAAAAAAAAAAA"))
	fake.push(press(2002, 55, text, approveData))
	if err := loops.poller.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	answers := fake.take("answerCallbackQuery")
	if len(answers) != 1 || answers[0].Body["text"] != "This button is not valid." {
		t.Fatalf("answers %+v (the other chat must get none)", answers)
	}
	if a := e.approval(id); a.State != "pending" {
		t.Fatalf("approval decided by a bad press: %+v", a)
	}
	if s := e.settings(); len(s.Telegram.PendingChats) != 1 || s.Telegram.PendingChats[0].ID != 2002 {
		t.Fatalf("pending chats %+v", s.Telegram.PendingChats)
	}

	// The real press approves as the admin through Telegram: the approval, the audit log and the replay agree.
	fake.push(press(1001, 101, text, approveData))
	if err := loops.poller.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	a := e.approval(id)
	if a.State != "approved" || a.DecidedBy == nil || a.DecidedBy.ID != "usr_admin" || a.DecidedBy.Channel != "telegram" ||
		a.Result == nil || a.Result.Status != 200 {
		t.Fatalf("approval after the press %+v", a)
	}
	au := e.audit("operation=approvals.approve")
	if len(au.Items) != 1 || au.Items[0].Actor.Channel != "telegram" || au.Items[0].Actor.ID != "usr_admin" {
		t.Fatalf("audit %+v", au.Items)
	}
	answers, edits := fake.take("answerCallbackQuery"), fake.take("editMessageText")
	if len(answers) != 1 || answers[0].Body["text"] != "Approved." || len(edits) != 1 ||
		!strings.HasSuffix(edits[0].Body["text"].(string), "Approved from Telegram by @anton.") {
		t.Fatalf("answers %+v edits %+v", answers, edits)
	}

	// Single use: the same button, or its Deny sibling, only says it is decided.
	fake.push(press(1001, 101, text, denyData))
	if err := loops.poller.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if answers := fake.take("answerCallbackQuery"); len(answers) != 1 || answers[0].Body["text"] != "Already decided." {
		t.Fatalf("second press %+v", answers)
	}
	if a := e.approval(id); a.State != "approved" {
		t.Fatalf("second press changed the approval: %+v", a)
	}
}

func TestButtonTokensExpire(t *testing.T) {
	e := start(t)
	ctx := context.Background()
	e.newProject("demo")
	id := e.gateArchive("demo", e.key(), 2)
	signer := notify.NewSigner([]byte("k"))
	a, _ := approvals.Get(ctx, e.pool, id)
	approveData, _, err := signer.Mint(ctx, e.pool, id, a.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	tokenID, action, err := signer.Parse(approveData)
	if err != nil {
		t.Fatal(err)
	}
	signer.Now = func() time.Time { return a.ExpiresAt.Add(time.Second) }
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := signer.Spend(ctx, tx, tokenID, action, notify.Presser{ChatID: 1})
		return err
	})
	if !errors.Is(err, notify.ErrTokenExpired) {
		t.Fatalf("expired token: %v", err)
	}
	// A token id with the other action's signature is refused even though the id exists.
	err = pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := signer.Spend(ctx, tx, tokenID, notify.ActionDeny, notify.Presser{ChatID: 1})
		return err
	})
	if !errors.Is(err, notify.ErrTokenInvalid) {
		t.Fatalf("mismatched action: %v", err)
	}
}

func TestQuietHoursAndDigest(t *testing.T) {
	fake := newFakeTelegram(t)
	e := startWith(t, func(c *Config) { c.Telegram = notify.Bot{BaseURL: fake.URL} })
	loops := e.notifyLoops(fake)
	ctx := context.Background()
	e.setUpBot()
	s := e.settings()
	e.ok(e.do("PATCH", "/api/notification-settings", `{"quietHours":{"enabled":true,"start":"22:00","end":"08:00"}}`,
		"Idempotency-Key", e.key(), "If-Match", rev(s.Rev)), 200, nil)
	night := time.Date(2026, 10, 1, 23, 30, 0, 0, time.UTC)
	loops.router.Now = func() time.Time { return night }

	e.newProject("demo")
	id := e.gateArchive("demo", e.key(), 2)
	// A failure during quiet hours still goes out.
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return events.Append(ctx, tx, backups.System, nil, []events.Draft{{Topic: backups.Topic, Type: backups.EventFailed,
			Payload: map[string]any{"backup": map[string]any{"id": "bkp_x", "error": "disk full"}}}})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := loops.router.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	rows, _ := e.pool.Query(ctx, `SELECT event_class, state FROM notification_deliveries`)
	for rows.Next() {
		var c, st string
		_ = rows.Scan(&c, &st)
		states[c] = st
	}
	rows.Close()
	if states["approval_requested"] != "suppressed" || states["failure"] != "queued" || len(states) != 2 {
		t.Fatalf("deliveries at night %v", states)
	}
	if _, err := loops.sender.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if msgs := fake.take("sendMessage"); len(msgs) != 1 || !strings.Contains(msgs[0].Body["text"].(string), "disk full") {
		t.Fatalf("night messages %+v", msgs)
	}

	// The digest goes out once at 09:00 local, with the open approval, in-app and on Telegram.
	morning := time.Date(2026, 10, 2, 9, 2, 0, 0, time.UTC)
	dg := &notify.Digester{Pool: e.pool, Defaults: defaults.Get, Now: func() time.Time { return morning.Add(-5 * time.Minute) }}
	if sent, err := dg.Tick(ctx); err != nil || sent {
		t.Fatalf("digest before 09:00: %v %v", sent, err)
	}
	dg.Now = func() time.Time { return morning }
	if sent, err := dg.Tick(ctx); err != nil || !sent {
		t.Fatalf("digest at 09:02: %v %v", sent, err)
	}
	if sent, _ := dg.Tick(ctx); sent {
		t.Fatal("a second digest the same day")
	}
	digests := e.events("notifications")
	if len(digests) != 1 || digests[0].Type != notify.EventDigest {
		t.Fatalf("in-app digest %+v", digests)
	}
	var payload struct {
		Digest notify.Digest
		Text   string
	}
	_ = json.Unmarshal(digests[0].Payload, &payload)
	if payload.Digest.OpenApprovals != 1 || payload.Digest.Approvals[0] != "projects.archive" || !strings.Contains(payload.Text, "• demo: budget") {
		t.Fatalf("digest %+v", payload)
	}
	if _, err := loops.sender.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if msgs := fake.take("sendMessage"); len(msgs) != 1 || !strings.HasPrefix(msgs[0].Body["text"].(string), "Cadence daily digest") {
		t.Fatalf("digest message %+v", msgs)
	}
	_ = id
}

// TestQuieterTelegram: approvals close together share one message whose rows decide each approval on its own;
// outcomes arrive silently; a pipeline step's failure is not a failure notice (its run's end is, once), and an agent
// session's failure is told once however often its end is repeated.
func TestQuieterTelegram(t *testing.T) {
	fake := newFakeTelegram(t)
	e := startWith(t, func(c *Config) { c.Telegram = notify.Bot{BaseURL: fake.URL} })
	loops := e.notifyLoops(fake)
	ctx := context.Background()
	e.setUpBot()
	t0 := time.Now()
	loops.router.Now = func() time.Time { return t0 }
	loops.sender.Now = func() time.Time { return t0 }

	ids := []string{}
	for _, slug := range []string{"one", "two", "three"} {
		e.newProject(slug)
		ids = append(ids, e.gateArchive(slug, e.key(), 2))
	}
	if _, err := loops.router.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(DISTINCT next_at) FROM notification_deliveries WHERE event_class = 'approval_requested' AND state = 'queued'`); n != 1 {
		t.Fatalf("the approvals are due at %d different times, want one window", n)
	}
	// Held for the window: nothing rings yet.
	if _, err := loops.sender.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if msgs := fake.take("sendMessage"); len(msgs) != 0 {
		t.Fatalf("sent inside the window: %+v", msgs)
	}
	loops.sender.Now = func() time.Time { return t0.Add(121 * time.Second) }
	if _, err := loops.sender.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	msgs := fake.take("sendMessage")
	if len(msgs) != 1 {
		t.Fatalf("approval messages %d, want 1", len(msgs))
	}
	text := msgs[0].Body["text"].(string)
	if !strings.HasPrefix(text, "3 approvals requested") || !strings.Contains(text, "[3] Approval requested: projects.archive") ||
		msgs[0].Body["disable_notification"] != nil {
		t.Fatalf("batched message %q %+v", text, msgs[0].Body)
	}
	var kb telegram.Keyboard
	raw, _ := json.Marshal(msgs[0].Body["reply_markup"])
	_ = json.Unmarshal(raw, &kb)
	if len(kb.InlineKeyboard) != 3 || kb.InlineKeyboard[1][1].Text != "Deny 2" {
		t.Fatalf("keyboard %+v", kb)
	}

	// Deny the second: only that approval is decided, and the message keeps the other two rows.
	up := press(1001, 101, text, kb.InlineKeyboard[1][1].CallbackData)
	up.CallbackQuery.Message.ReplyMarkup = &kb
	fake.push(up)
	if err := loops.poller.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	if a := e.approval(ids[1]); a.State != "denied" {
		t.Fatalf("second approval %+v", a)
	}
	if a, b := e.approval(ids[0]), e.approval(ids[2]); a.State != "pending" || b.State != "pending" {
		t.Fatalf("the other approvals were decided: %s %s", a.State, b.State)
	}
	edits := fake.take("editMessageText")
	if len(edits) != 1 || !strings.HasSuffix(edits[0].Body["text"].(string), "[2] Denied from Telegram by @anton.") {
		t.Fatalf("edits %+v", edits)
	}
	raw, _ = json.Marshal(edits[0].Body["reply_markup"])
	var rest telegram.Keyboard
	_ = json.Unmarshal(raw, &rest)
	if len(rest.InlineKeyboard) != 2 || rest.InlineKeyboard[0][0].Text != "Approve 1" || rest.InlineKeyboard[1][0].Text != "Approve 3" {
		t.Fatalf("keyboard after the press %+v", rest)
	}
	fake.take("answerCallbackQuery")

	// An outcome, a failed optional step whose run goes on, a failed run, and an agent session's end told twice.
	later := t0.Add(5 * time.Minute)
	loops.router.Now = func() time.Time { return later }
	loops.sender.Now = func() time.Time { return later }
	session := map[string]any{"session": map[string]any{"id": "ses_x", "number": 9, "project": "one", "state": "failed", "error": "the agent exited"}}
	drafts := []events.Draft{
		{Topic: "branches", Type: "branch.waiting", Payload: map[string]any{"project": "one", "branch": "sync/x", "files": 2, "reason": "a sync"}},
		{Topic: "pipeline_run.plr_x", Type: "pipeline_run.step_changed", Payload: map[string]any{"pipelineRunId": "plr_x", "runState": "running",
			"step": map[string]any{"id": "pls_x", "step": "oasis", "kind": "sdp_ingest", "state": "failed", "error": map[string]any{"type": "step", "message": "no manifest"}}}},
		{Topic: "pipeline_run.plr_y", Type: "pipeline_run.step_changed", Payload: map[string]any{"pipelineRunId": "plr_y", "runState": "running",
			"step": map[string]any{"id": "pls_y", "step": "train", "kind": "toy_train", "state": "failed"}}},
		{Topic: "pipeline_run.plr_y", Type: "pipeline_run.state_changed", Payload: map[string]any{"pipelineRun": map[string]any{"id": "plr_y",
			"pipeline": "train", "runId": "run_y", "state": "failed", "error": "step train failed (step): boom"}}},
		{Topic: "agent.sessions", Type: "agent_session.changed", Payload: session},
		{Topic: "agent.session.ses_x", Type: "agent_session.changed", Payload: session},
		{Topic: "agent.sessions", Type: "agent_session.changed", Payload: session},
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		return events.Append(ctx, tx, backups.System, nil, drafts)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := loops.router.CatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := loops.sender.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{} // title → silent
	for _, m := range fake.take("sendMessage") {
		title, _, _ := strings.Cut(m.Body["text"].(string), "\n")
		got[title] = m.Body["disable_notification"] == true
	}
	want := map[string]bool{
		"Branch waiting for review: sync/x (one)": true,
		"Run failed: run_y":                       false,
		"Agent session 9 failed (one)":            false,
	}
	if len(got) != len(want) {
		t.Fatalf("messages %v, want %v", got, want)
	}
	for title, silent := range want {
		if s, ok := got[title]; !ok || s != silent {
			t.Fatalf("messages %v, want %v", got, want)
		}
	}
}

func TestBackupsAPI(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is needed to run pg_dump inside the Postgres container:", err)
	}
	var svc *backups.Service
	dir := t.TempDir()
	e := startWith(t, func(c *Config) { c.Backups = svc }, func(pool *pgxpool.Pool, js *jobs.Service) {
		docker := []string{"docker", "exec", "-i", "-e", "PGPASSWORD", testdb.ContainerID()}
		dsn := pool.Config().ConnString()
		svc = &backups.Service{Pool: pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Jobs: js, Defaults: defaults.Get,
			Config: backups.Config{Dir: filepath.Join(dir, "backups"), DSN: testdb.InContainer(dsn), PoolDSN: dsn,
				PgDump: append(append([]string{}, docker...), "pg_dump"), PgRestore: append(append([]string{}, docker...), "pg_restore")}}
		svc.Register(js)
	})
	var dry struct{ ID, State, Trigger string }
	e.ok(e.do("POST", "/api/backups?dryRun=true", "", "Idempotency-Key", e.key()), 200, &dry)
	if dry.State != "queued" || dry.Trigger != "manual" || e.count(`SELECT count(*) FROM backups`) != 0 {
		t.Fatalf("dry run %+v", dry)
	}
	var acc struct{ JobID string }
	e.ok(e.do("POST", "/api/backups", "", "Idempotency-Key", e.key()), 202, &acc)
	e.waitJob(acc.JobID, "done")
	var list struct {
		Items []struct {
			ID, State, Trigger string
			Rev                int
			DumpBytes          int64
		}
		Schedule struct {
			NightlyAt, RestoreTestWeekday string
			KeepNightly, KeepWeekly       int
			NextBackupAt                  *time.Time
		}
		LastRestoreTest *struct{ BackupID string }
	}
	e.ok(e.do("GET", "/api/backups", ""), 200, &list)
	if len(list.Items) != 1 || list.Items[0].State != "succeeded" || list.Items[0].DumpBytes == 0 ||
		list.Schedule.NightlyAt != "03:00" || list.Schedule.KeepNightly != 7 || list.Schedule.KeepWeekly != 4 ||
		list.Schedule.NextBackupAt == nil || list.LastRestoreTest != nil {
		t.Fatalf("list %+v", list)
	}
	b := list.Items[0]
	e.ok(e.do("POST", "/api/backups/"+b.ID+":verify", "", "Idempotency-Key", e.key(), "If-Match", rev(b.Rev)), 202, &acc)
	e.waitJob(acc.JobID, "done")
	var got struct {
		RestoreTest struct {
			State  string
			Tables []struct {
				Name               string
				BackedUp, Restored int64
			}
		}
	}
	e.ok(e.do("GET", "/api/backups/"+b.ID, ""), 200, &got)
	if got.RestoreTest.State != "passed" || len(got.RestoreTest.Tables) == 0 {
		t.Fatalf("restore test %+v", got.RestoreTest)
	}
	e.ok(e.do("GET", "/api/backups", ""), 200, &list)
	if list.LastRestoreTest == nil || list.LastRestoreTest.BackupID != b.ID {
		t.Fatalf("last restore test %+v", list.LastRestoreTest)
	}
	expectProblem(t, e.do("POST", "/api/backups/"+b.ID+":verify", "", "Idempotency-Key", e.key(), "If-Match", rev(b.Rev)), 412, "precondition-failed")
}
