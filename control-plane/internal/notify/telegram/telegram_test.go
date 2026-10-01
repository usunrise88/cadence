package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const token = "123456:SECRET-token_value"

func TestClientCallsAndRedaction(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, `{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"C","username":"cadence_bot"}}`)
		case strings.HasSuffix(r.URL.Path, "/sendMessage"):
			var m map[string]any
			_ = json.Unmarshal(body, &m)
			if m["chat_id"].(float64) == 42 {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user `+token+`"}`)
				return
			}
			kb := m["reply_markup"].(map[string]any)["inline_keyboard"].([]any)
			if len(kb) != 1 {
				t.Errorf("keyboard %v", kb)
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":7,"chat":{"id":1,"type":"private"}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"ok":false,"error_code":404,"description":"Not Found"}`)
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Token: token}
	ctx := context.Background()
	me, err := c.GetMe(ctx)
	if err != nil || me.Username != "cadence_bot" {
		t.Fatalf("GetMe = %+v, %v", me, err)
	}
	kb := &Keyboard{InlineKeyboard: [][]Button{{{Text: "Approve", CallbackData: "a.x.y"}}}}
	m, err := c.SendMessage(ctx, 1, "hello", kb)
	if err != nil || m.MessageID != 7 {
		t.Fatalf("SendMessage = %+v, %v", m, err)
	}
	_, err = c.SendMessage(ctx, 42, "hello", kb)
	if err == nil || strings.Contains(err.Error(), token) || !strings.Contains(err.Error(), "<token>") {
		t.Fatalf("error leaks or lacks redaction: %v", err)
	}
	if err := c.AnswerCallbackQuery(ctx, "q", "ok"); !Unauthorized(err) {
		t.Fatalf("404 should read as a refused token: %v", err)
	}
	if got[0] != "/bot"+token+"/getMe" {
		t.Fatalf("path %q", got[0])
	}
	// A transport error names the URL, which carries the token: it must be redacted.
	dead := &Client{BaseURL: "http://127.0.0.1:1", Token: token}
	if _, err := dead.GetMe(ctx); err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("transport error leaks the token: %v", err)
	}
	if _, err := (&Client{BaseURL: srv.URL}).GetMe(ctx); err == nil {
		t.Fatal("no token accepted")
	}
}
