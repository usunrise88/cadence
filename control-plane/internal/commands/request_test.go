package commands

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

func TestParseIfMatch(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{`"3"`, 3, false},
		{`3`, 3, false},
		{`W/"3"`, 3, false},
		{` "12" `, 12, false},
		{`"0"`, 0, true},
		{`"-1"`, 0, true},
		{`*`, 0, true},
		{`"abc"`, 0, true},
		{``, 0, true},
		{`"3`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseIfMatch(tt.in)
			if tt.wantErr {
				var pe *problems.Error
				if !errors.As(err, &pe) || pe.Type != problems.BadRequest {
					t.Fatalf("ParseIfMatch(%q) err = %v, want bad-request", tt.in, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("ParseIfMatch(%q) = %d, %v; want %d", tt.in, got, err, tt.want)
			}
		})
	}
	if ETag(3) != `"3"` {
		t.Errorf("ETag(3) = %s", ETag(3))
	}
}

func TestCheckRev(t *testing.T) {
	if err := CheckRev("project", 2, 2); err != nil {
		t.Fatalf("same revision: %v", err)
	}
	var pe *problems.Error
	if err := CheckRev("project", 1, 2); !errors.As(err, &pe) || pe.Type != problems.PreconditionFailed || *pe.CurrentRev != 2 {
		t.Fatalf("stale revision: %v", err)
	}
}

func TestHashRequest(t *testing.T) {
	base := HashRequest("POST", "/api/projects", url.Values{}, "", []byte(`{"slug":"demo","name":"Demo"}`))
	tests := []struct {
		name  string
		hash  string
		equal bool
	}{
		{"key order and whitespace ignored",
			HashRequest("POST", "/api/projects", url.Values{}, "", []byte("{ \"name\": \"Demo\",\n \"slug\": \"demo\" }")), true},
		{"dryRun ignored",
			HashRequest("POST", "/api/projects", url.Values{"dryRun": {"true"}}, "", []byte(`{"slug":"demo","name":"Demo"}`)), true},
		{"different body",
			HashRequest("POST", "/api/projects", url.Values{}, "", []byte(`{"slug":"demo","name":"Other"}`)), false},
		{"different path",
			HashRequest("POST", "/api/projects/demo:archive", url.Values{}, "", []byte(`{"slug":"demo","name":"Demo"}`)), false},
		{"different If-Match",
			HashRequest("POST", "/api/projects", url.Values{}, `"2"`, []byte(`{"slug":"demo","name":"Demo"}`)), false},
		{"different method",
			HashRequest("PUT", "/api/projects", url.Values{}, "", []byte(`{"slug":"demo","name":"Demo"}`)), false},
		{"other query parameter counts",
			HashRequest("POST", "/api/projects", url.Values{"x": {"1"}}, "", []byte(`{"slug":"demo","name":"Demo"}`)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if (tt.hash == base) != tt.equal {
				t.Errorf("equal = %v, want %v", tt.hash == base, tt.equal)
			}
		})
	}
	if got := HashRequest("POST", "/x", nil, "", []byte("not json")); got == "" {
		t.Error("non-JSON bodies still hash")
	}
}

func TestHashMiddleware(t *testing.T) {
	var seen, body string
	h := HashMiddleware(func(w http.ResponseWriter, _ *http.Request, err error) {
		t.Fatalf("unexpected error: %v", err)
	})(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = RequestHash(r.Context())
		b, _ := io.ReadAll(r.Body)
		body = string(b)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"a":1}`))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if seen != HashRequest("POST", "/api/projects", url.Values{}, "", []byte(`{"a":1}`)) {
		t.Errorf("hash not stored in context")
	}
	if body != `{"a":1}` {
		t.Errorf("body not restored: %q", body)
	}
	seen = "unset"
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/projects", nil))
	if seen != "" {
		t.Errorf("GET should carry no hash, got %q", seen)
	}
}
