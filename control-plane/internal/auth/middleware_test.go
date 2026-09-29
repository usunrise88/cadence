package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

type fakeResolver map[string]Principal

func (f fakeResolver) Resolve(_ context.Context, token string, via Via) (Principal, bool, error) {
	if token == "boom" {
		return Principal{}, false, errors.New("database down")
	}
	p, ok := f[token]
	if !ok || (via == ViaCookie) != strings.HasPrefix(token, PrefixSession) {
		return Principal{}, false, ErrUnknownToken
	}
	return p, token == "cws_refresh", nil
}

func TestNeedsClientHeader(t *testing.T) {
	tests := []struct {
		method string
		bearer bool
		want   bool
	}{
		{http.MethodGet, false, false},
		{http.MethodHead, false, false},
		{http.MethodPost, false, true},
		{http.MethodPut, false, true},
		{http.MethodPatch, false, true},
		{http.MethodPost, true, false},
		{http.MethodPatch, true, false},
	}
	for _, tt := range tests {
		if got := NeedsClientHeader(tt.method, tt.bearer); got != tt.want {
			t.Errorf("NeedsClientHeader(%s, bearer=%v) = %v", tt.method, tt.bearer, got)
		}
	}
}

func TestMiddleware(t *testing.T) {
	admin := Principal{Actor: DevActor(), Scope: FullScope(), CredentialID: "crd_s", CredentialKind: "session", UserID: "usr_admin"}
	key := Principal{Actor: Actor{Kind: KindAutomation, ID: "crd_k"}, Scope: Scope{ProjectID: "prj_1"}, CredentialID: "crd_k", CredentialKind: "api_key"}
	res := fakeResolver{"cws_ok": admin, "cws_refresh": admin, "cdk_ok": key}
	a := &Authenticator{
		Resolver: res,
		Public:   func(r *http.Request) bool { return r.URL.Path == "/auth:login" || r.URL.Path == "/auth" },
		OnError: func(w http.ResponseWriter, r *http.Request, err error) {
			pe, _ := problems.As(err)
			w.WriteHeader(pe.Type.Status)
			_, _ = w.Write([]byte(pe.Type.Slug))
		},
	}
	var seen Actor
	h := a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name       string
		method     string
		path       string
		cookie     string
		bearer     string
		client     bool // send Cadence-Client: web
		wantStatus int
		wantSlug   string
		wantActor  string
		wantCookie string // substring of Set-Cookie
	}{
		{"no credentials", "GET", "/projects", "", "", false, 401, "unauthenticated", "", ""},
		{"public without credentials", "GET", "/auth", "", "", false, 204, "", "", ""},
		{"cookie read", "GET", "/projects", "cws_ok", "", false, 204, "", "usr_admin", ""},
		{"cookie refresh slides the cookie", "GET", "/projects", "cws_refresh", "", false, 204, "", "usr_admin", "Max-Age=2592000"},
		{"cookie mutation without the header", "POST", "/projects", "cws_ok", "", false, 403, "forbidden", "", ""},
		{"cookie mutation with the header", "POST", "/projects", "cws_ok", "", true, 204, "", "usr_admin", ""},
		{"stale cookie is cleared and refused", "GET", "/projects", "cws_gone", "", false, 401, "unauthenticated", "", "Max-Age=0"},
		{"stale cookie on a public request", "GET", "/auth", "cws_gone", "", false, 204, "", "", "Max-Age=0"},
		{"login without the header", "POST", "/auth:login", "", "", false, 403, "forbidden", "", ""},
		{"login with the header", "POST", "/auth:login", "", "", true, 204, "", "", ""},
		{"bearer mutation needs no header", "POST", "/projects", "", "cdk_ok", false, 204, "", "crd_k", ""},
		{"bad bearer", "GET", "/auth", "", "cdk_nope", false, 401, "unauthenticated", "", ""},
		{"bearer wins over cookie", "GET", "/projects", "cws_ok", "cdk_ok", false, 204, "", "crd_k", ""},
		{"session token as bearer", "GET", "/projects", "", "cws_ok", false, 401, "unauthenticated", "", ""},
		{"resolver failure is internal", "GET", "/projects", "", "boom", false, 500, "internal", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen = Actor{}
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.cookie != "" {
				req.AddCookie(&http.Cookie{Name: CookieName, Value: tt.cookie})
			}
			if tt.bearer != "" {
				req.Header.Set("Authorization", "Bearer "+tt.bearer)
			}
			if tt.client {
				req.Header.Set(ClientHeader, ClientWeb)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus || (tt.wantSlug != "" && rec.Body.String() != tt.wantSlug) {
				t.Fatalf("got %d %q, want %d %q", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantSlug)
			}
			if seen.ID != tt.wantActor {
				t.Errorf("actor %q, want %q", seen.ID, tt.wantActor)
			}
			sc := rec.Header().Get("Set-Cookie")
			if (tt.wantCookie == "") != (sc == "") || !strings.Contains(sc, tt.wantCookie) {
				t.Errorf("Set-Cookie %q, want %q", sc, tt.wantCookie)
			}
		})
	}

	fixed := &Authenticator{Fixed: &Actor{Kind: KindUser, ID: "usr_test"}}
	var scope Scope
	fh := fixed.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = FromContext(r.Context())
		scope, _ = ScopeFromContext(r.Context())
	}))
	fh.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/projects", nil))
	if seen.ID != "usr_test" || !scope.All {
		t.Errorf("fixed actor: %+v %+v", seen, scope)
	}
}

func TestSessionCookie(t *testing.T) {
	c := SessionCookie("cws_x", true).String()
	for _, want := range []string{"cadence_session=cws_x", "HttpOnly", "Secure", "SameSite=Lax", "Path=/", "Max-Age=2592000"} {
		if !strings.Contains(c, want) {
			t.Errorf("%s lacks %s", c, want)
		}
	}
	if strings.Contains(SessionCookie("cws_x", false).String(), "Secure") {
		t.Error("plain HTTP cookie is Secure")
	}
}

func TestClientOf(t *testing.T) {
	tests := []struct {
		name       string
		remote     string
		xff, proto string
		tls        bool
		wantIP     string
		wantSecure bool
	}{
		{"direct", "203.0.113.5:4000", "", "", false, "203.0.113.5", false},
		{"direct TLS", "203.0.113.5:4000", "", "", true, "203.0.113.5", true},
		{"proxy on loopback", "127.0.0.1:4000", "198.51.100.1, 203.0.113.9", "https", false, "203.0.113.9", true},
		{"proxy on a private network", "172.17.0.1:4000", "203.0.113.9", "https", false, "203.0.113.9", true},
		{"forwarded headers from a public peer are ignored", "203.0.113.5:4000", "10.0.0.1", "https", false, "203.0.113.5", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tt.remote
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			if tt.proto != "" {
				r.Header.Set("X-Forwarded-Proto", tt.proto)
			}
			if tt.tls {
				r.TLS = &tls.ConnectionState{}
			}
			c := ClientOf(r)
			if c.IP != tt.wantIP || c.Secure != tt.wantSecure {
				t.Errorf("ClientOf = %+v", c)
			}
		})
	}
}
