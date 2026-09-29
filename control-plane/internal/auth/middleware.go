package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Session cookie and CSRF header (docs/spec/06-platform.md "Authentication and access").
const (
	CookieName = "cadence_session"
	// SessionLifetime is the sliding lifetime of a browser session: each use (at most once a minute) extends it.
	SessionLifetime = 30 * 24 * time.Hour
	// ClientHeader must be sent with value ClientWeb on every mutation not authenticated by a Bearer token. A
	// cross-site form cannot set a custom header, and a cross-site fetch that sets one needs a CORS preflight the
	// server never grants; together with SameSite=Lax this is the CSRF defence.
	ClientHeader = "Cadence-Client"
	ClientWeb    = "web"
)

// Via says how a token was presented.
type Via int

// Ways a token reaches the server.
const (
	ViaCookie Via = iota // the session cookie
	ViaBearer            // Authorization: Bearer
)

// ErrUnknownToken is what a Resolver returns for a token that is unknown, revoked, expired or of the wrong kind.
var ErrUnknownToken = errors.New("unknown, revoked or expired token")

// Resolver turns a presented token into a principal. Refresh reports that the session was extended and its
// cookie should be re-issued with a new expiry.
type Resolver interface {
	Resolve(ctx context.Context, token string, via Via) (p Principal, refresh bool, err error)
}

// Authenticator is the API's authentication middleware.
type Authenticator struct {
	// Resolver looks tokens up; required unless Fixed is set.
	Resolver Resolver
	// Fixed, when set, attributes every request to this actor with full scope and no authentication at all
	// (tests; development). The CSRF rule does not apply either.
	Fixed *Actor
	// Public reports requests that may proceed without a principal (sign-in, first start, help).
	Public func(*http.Request) bool
	// OnError writes a problem.
	OnError func(http.ResponseWriter, *http.Request, error)
}

// Middleware authenticates every request:
//
//   - A Bearer token (cdk_, cst_) must resolve, or the request is 401.
//   - Otherwise the session cookie is used when it resolves; a stale cookie is cleared.
//   - Without a principal only Public requests proceed; the rest are 401.
//   - A mutation (POST, PUT, PATCH) not authenticated by Bearer must carry Cadence-Client: web, or it is 403.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := WithClient(r.Context(), ClientOf(r))
		if a.Fixed != nil {
			p := Principal{Actor: *a.Fixed, Scope: FullScope(), UserID: fixedUser(*a.Fixed)}
			next.ServeHTTP(w, r.WithContext(WithPrincipal(ctx, p)))
			return
		}
		secure := ClientFrom(ctx).Secure
		var (
			p      Principal
			authed bool
			bearer bool
		)
		if token, ok := BearerToken(r); ok {
			bearer = true
			var err error
			p, _, err = a.Resolver.Resolve(ctx, token, ViaBearer)
			if err != nil {
				a.OnError(w, r, unauthenticated(err, "the Bearer token is unknown, revoked or expired"))
				return
			}
			authed = true
		} else if c, err := r.Cookie(CookieName); err == nil && c.Value != "" {
			var refresh bool
			p, refresh, err = a.Resolver.Resolve(ctx, c.Value, ViaCookie)
			switch {
			case err == nil:
				authed = true
				if refresh {
					http.SetCookie(w, SessionCookie(c.Value, secure))
				}
			case errors.Is(err, ErrUnknownToken):
				http.SetCookie(w, ClearSessionCookie(secure))
			default:
				a.OnError(w, r, err)
				return
			}
		}
		if !authed && (a.Public == nil || !a.Public(r)) {
			a.OnError(w, r, problems.Unauthenticated.New("sign in (session cookie) or send Authorization: Bearer <token>"))
			return
		}
		if NeedsClientHeader(r.Method, bearer) && r.Header.Get(ClientHeader) != ClientWeb {
			a.OnError(w, r, problems.Forbidden.New("browser requests that change state must send the header %s: %s", ClientHeader, ClientWeb))
			return
		}
		if authed {
			ctx = WithPrincipal(ctx, p)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func unauthenticated(err error, detail string) error {
	if errors.Is(err, ErrUnknownToken) {
		return problems.Unauthenticated.New("%s", detail)
	}
	return err
}

func fixedUser(a Actor) string {
	if a.Kind == KindUser {
		return a.ID
	}
	return ""
}

// NeedsClientHeader is the CSRF rule: a state-changing request that is not authenticated by a Bearer token (so
// by the cookie, or not at all, like sign-in) must carry Cadence-Client: web.
func NeedsClientHeader(method string, bearer bool) bool {
	if bearer {
		return false
	}
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// BearerToken returns the token of an Authorization: Bearer header.
func BearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}

// SessionCookie is the session cookie carrying token: HttpOnly, SameSite=Lax, Secure behind TLS, 30 days.
func SessionCookie(token string, secure bool) *http.Cookie {
	//nolint:gosec // Secure is set whenever the client reached us over TLS (directly or at the proxy); plain HTTP is local only
	return &http.Cookie{
		Name: CookieName, Value: token, Path: "/", MaxAge: int(SessionLifetime / time.Second),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	}
}

// ClearSessionCookie deletes the session cookie.
func ClearSessionCookie(secure bool) *http.Cookie {
	//nolint:gosec // as SessionCookie
	return &http.Cookie{Name: CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode}
}

// Client is what the server knows about the caller's connection.
type Client struct {
	// IP is the client address: the connection's peer, or, when the peer is a proxy on loopback or a private
	// network (the host's Caddy, R39; Docker's gateway), the address it appended to X-Forwarded-For.
	IP string
	// Secure is true when the client reached us over TLS (directly or at the proxy).
	Secure bool
}

type clientKey struct{}

// WithClient returns ctx carrying c.
func WithClient(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// ClientFrom returns the client of the request (zero when the middleware did not run).
func ClientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(clientKey{}).(Client)
	return c
}

// ClientOf reads the client of r. Forwarded headers are trusted only from a loopback or private peer: the control
// plane is published on localhost only, behind the host's proxy (docs/spec/08-resolutions.md R39).
func ClientOf(r *http.Request) Client {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	c := Client{IP: host, Secure: r.TLS != nil}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
				c.IP = last
			}
		}
		if strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
			c.Secure = true
		}
	}
	return c
}
