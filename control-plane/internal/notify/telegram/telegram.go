// Package telegram is a minimal Telegram Bot API client (https://core.telegram.org/bots/api): the calls the
// notification channel needs — getMe, sendMessage with an inline keyboard, editMessageText, answerCallbackQuery and
// getUpdates (long polling, so no public webhook is needed). The base URL is configurable so tests run against a
// fake server; nothing in this package logs, and errors never contain the token (it is part of every URL).
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is Telegram's Bot API endpoint.
const DefaultBaseURL = "https://api.telegram.org"

// Client calls the Bot API with one bot token.
type Client struct {
	BaseURL string // DefaultBaseURL when empty
	Token   string
	HTTP    *http.Client // a client with a 60 s timeout when nil (long polls wait up to 50 s)
}

// User is a Telegram user or bot.
type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username,omitempty"`
}

// Chat is a private chat, group or channel.
type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
}

// Label names the chat as a person would: its title or @username.
func (c Chat) Label() string {
	switch {
	case c.Title != "":
		return c.Title
	case c.Username != "":
		return "@" + c.Username
	}
	return c.Type
}

// Message is a sent or received message.
type Message struct {
	MessageID int64  `json:"message_id"`
	Chat      Chat   `json:"chat"`
	From      *User  `json:"from,omitempty"`
	Text      string `json:"text,omitempty"`
	// ReplyMarkup is the message's inline keyboard (a pressed button's message carries it: a batched approval
	// message keeps the rows not decided yet).
	ReplyMarkup *Keyboard `json:"reply_markup,omitempty"`
}

// CallbackQuery is the press of an inline button.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
}

// Update is one getUpdates item.
type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Button is an inline keyboard button whose press sends CallbackData back to the bot (at most 64 bytes).
type Button struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

// Keyboard is an inline keyboard: rows of buttons.
type Keyboard struct {
	InlineKeyboard [][]Button `json:"inline_keyboard"`
}

// Error is a Bot API error answer (ok: false).
type Error struct {
	Method      string
	Code        int
	Description string
}

func (e *Error) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

// Unauthorized reports whether err is Telegram refusing the token.
func Unauthorized(err error) bool {
	var e *Error
	return errors.As(err, &e) && (e.Code == http.StatusUnauthorized || e.Code == http.StatusNotFound)
}

type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
}

func (c *Client) call(ctx context.Context, method string, body, result any) error {
	if c.Token == "" {
		return fmt.Errorf("telegram %s: no bot token is stored", method)
	}
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	b, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("telegram %s: encode: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/bot"+c.Token+"/"+method, bytes.NewReader(b))
	if err != nil {
		return fmt.Errorf("telegram %s: %s", method, c.redact(err.Error()))
	}
	req.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("telegram %s: %s", method, c.redact(err.Error())) // *url.Error carries the URL: redact
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("telegram %s: read answer: %s", method, c.redact(err.Error()))
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return &Error{Method: method, Code: resp.StatusCode, Description: "answer is not JSON"}
	}
	if !env.OK {
		code := env.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &Error{Method: method, Code: code, Description: c.redact(env.Description)}
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}

func (c *Client) redact(s string) string {
	if c.Token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.Token, "<token>")
}

// GetMe checks the token and returns the bot.
func (c *Client) GetMe(ctx context.Context) (User, error) {
	var u User
	err := c.call(ctx, "getMe", struct{}{}, &u)
	return u, err
}

// SendMessage sends plain text (no parse mode, so nothing in it is markup) with an optional inline keyboard.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string, kb *Keyboard) (Message, error) {
	return c.Send(ctx, chatID, text, kb, false)
}

// Send is SendMessage that may be silent: the message arrives without sound (disable_notification).
func (c *Client) Send(ctx context.Context, chatID int64, text string, kb *Keyboard, silent bool) (Message, error) {
	body := map[string]any{"chat_id": chatID, "text": text, "disable_web_page_preview": true}
	if kb != nil {
		body["reply_markup"] = kb
	}
	if silent {
		body["disable_notification"] = true
	}
	var m Message
	err := c.call(ctx, "sendMessage", body, &m)
	return m, err
}

// EditMessageText replaces a message's text and drops its keyboard (the buttons of a decided approval).
func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string) error {
	return c.EditMessage(ctx, chatID, messageID, text, nil)
}

// EditMessage replaces a message's text and its keyboard with kb (nil or empty: no buttons left).
func (c *Client) EditMessage(ctx context.Context, chatID, messageID int64, text string, kb *Keyboard) error {
	rows := [][]Button{}
	if kb != nil && kb.InlineKeyboard != nil {
		rows = kb.InlineKeyboard
	}
	return c.call(ctx, "editMessageText", map[string]any{
		"chat_id": chatID, "message_id": messageID, "text": text, "reply_markup": Keyboard{InlineKeyboard: rows},
	}, nil)
}

// AnswerCallbackQuery acknowledges a button press with a short toast.
func (c *Client) AnswerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// GetUpdates long-polls for updates after offset-1 (offset confirms everything before it), waiting up to timeout
// seconds.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var out []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeout, "allowed_updates": []string{"message", "callback_query"},
	}, &out)
	return out, err
}
