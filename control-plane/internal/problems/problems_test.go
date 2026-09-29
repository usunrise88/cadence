package problems

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	rev := 4
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
		wantJSON   []string // substrings of the body
		notJSON    []string
	}{
		{"not found", NotFound.New("no project with slug %q", "x"), 404, TypeBase + "not-found",
			[]string{`"title":"Not found"`, `"detail":"no project with slug \"x\""`, `"instance":"/api/projects/x"`}, nil},
		{"stale revision carries currentRev", Stale(rev, "stale"), 412, TypeBase + "precondition-failed",
			[]string{`"currentRev":4`}, nil},
		{"validation lists fields", Validation([]FieldError{{Path: "/slug", Message: "bad"}}), 422,
			TypeBase + "validation-failed", []string{`"errors":[{"message":"bad","path":"/slug"}]`}, nil},
		{"wrapped problem keeps its type", fmt.Errorf("ctx: %w", Conflict.New("taken")), 409, TypeBase + "conflict", nil, nil},
		{"unknown errors become internal without leaking", errors.New("pq: password=secret"), 500, TypeBase + "internal",
			nil, []string{"secret", "pq:"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Write(rec, httptest.NewRequest(http.MethodGet, "/api/projects/x", nil), slog.New(slog.NewTextHandler(io.Discard, nil)), tt.err)
			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != ContentType {
				t.Errorf("Content-Type = %q", ct)
			}
			body := rec.Body.String()
			var p struct {
				Type   string `json:"type"`
				Status int    `json:"status"`
			}
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				t.Fatal(err)
			}
			if p.Type != tt.wantType || p.Status != tt.wantStatus {
				t.Errorf("type/status = %s/%d, body %s", p.Type, p.Status, body)
			}
			for _, s := range tt.wantJSON {
				if !strings.Contains(body, s) {
					t.Errorf("body %s lacks %s", body, s)
				}
			}
			for _, s := range tt.notJSON {
				if strings.Contains(body, s) {
					t.Errorf("body %s leaks %s", body, s)
				}
			}
		})
	}
}

func TestRegistryIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, ty := range Types() {
		if seen[ty.Slug] {
			t.Errorf("duplicate slug %s", ty.Slug)
		}
		seen[ty.Slug] = true
		if ty.Status < 400 || ty.Status > 599 || ty.Title == "" {
			t.Errorf("bad type %+v", ty)
		}
	}
}
