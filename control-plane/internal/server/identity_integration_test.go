//go:build integration

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
)

const adminPassword = "correct horse battery staple"

// clock is a settable time source for TOTP checks.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// startAuth runs the control plane with real authentication: no fixed actor, cheap password hashing, and a
// settable clock for TOTP.
func startAuth(t *testing.T) (*env, *clock) {
	t.Helper()
	clk := &clock{now: time.Now()}
	e := startWith(t, func(s *Server) {
		s.Actor = auth.Actor{}
		s.Credentials = credentials.NewStore(s.Pool, clk.Now,
			auth.PasswordParams{Time: 1, Memory: 1024, Threads: 1, SaltLen: 16, KeyLen: 32})
		s.LoginLimiter = auth.NewLimiter(time.Now, auth.DefaultLoginLimits()...)
	})
	return e, clk
}

// web headers: the session cookie plus the CSRF header, as the SPA sends them.
func web(cookie string, hdr ...string) []string {
	return append([]string{"Cookie", auth.CookieName + "=" + cookie, auth.ClientHeader, auth.ClientWeb}, hdr...)
}

func bearer(token string, hdr ...string) []string {
	return append([]string{"Authorization", "Bearer " + token}, hdr...)
}

// sessionCookie reads the cadence_session cookie a response set.
func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == auth.CookieName {
			return c
		}
	}
	t.Fatalf("%s %s set no session cookie", resp.Request.Method, resp.Request.URL.Path)
	return nil
}

type authStatus struct {
	SetupRequired bool `json:"setupRequired"`
	Actor         *struct {
		Kind, ID, Name, SessionID string
	} `json:"actor"`
	TotpEnabled *bool `json:"totpEnabled"`
}

// setup runs first start and returns the admin's session cookie value.
func (e *env) setup() string {
	e.t.Helper()
	resp := e.ok(e.do("POST", "/api/auth:setup", `{"password":"`+adminPassword+`"}`, auth.ClientHeader, auth.ClientWeb), 200, nil)
	return sessionCookie(e.t, resp).Value
}

func TestFirstStartSignInAndOut(t *testing.T) {
	e, _ := startAuth(t)
	var st authStatus
	e.ok(e.do("GET", "/api/auth", ""), 200, &st)
	if !st.SetupRequired || st.Actor != nil {
		t.Fatalf("fresh instance: %+v", st)
	}
	expectProblem(t, e.do("GET", "/api/projects", ""), 401, "unauthenticated")
	expectProblem(t, e.do("GET", "/api/me", ""), 401, "unauthenticated")
	expectProblem(t, e.do("POST", "/api/auth:login", `{"username":"admin","password":"`+adminPassword+`"}`,
		auth.ClientHeader, auth.ClientWeb), 401, "unauthenticated")

	// First start: the CSRF header is required, the password has a minimum length, and setup runs once.
	expectProblem(t, e.do("POST", "/api/auth:setup", `{"password":"`+adminPassword+`"}`), 403, "forbidden")
	expectProblem(t, e.do("POST", "/api/auth:setup", `{"password":"short"}`, auth.ClientHeader, auth.ClientWeb), 422, "validation-failed")
	resp := e.ok(e.do("POST", "/api/auth:setup", `{"password":"`+adminPassword+`"}`, auth.ClientHeader, auth.ClientWeb), 200, &st)
	c := sessionCookie(t, resp)
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.MaxAge != 30*24*3600 || c.Path != "/" ||
		!strings.HasPrefix(c.Value, auth.PrefixSession) {
		t.Errorf("session cookie %+v", c)
	}
	if st.SetupRequired || st.Actor == nil || st.Actor.ID != "usr_admin" {
		t.Errorf("after setup: %+v", st)
	}
	expectProblem(t, e.do("POST", "/api/auth:setup", `{"password":"another long password"}`, auth.ClientHeader, auth.ClientWeb), 409, "conflict")
	cookie := c.Value

	// The cookie authenticates reads; mutations also need the CSRF header.
	var me struct{ Kind, ID, Name string }
	e.ok(e.do("GET", "/api/me", "", web(cookie)...), 200, &me)
	if me.ID != "usr_admin" || me.Kind != "user" || me.Name != "admin" {
		t.Errorf("me.get = %+v", me)
	}
	e.ok(e.do("GET", "/api/auth", "", web(cookie)...), 200, &st)
	if st.SetupRequired || st.Actor == nil || st.TotpEnabled == nil || *st.TotpEnabled {
		t.Errorf("auth.get signed in: %+v", st)
	}
	expectProblem(t, e.do("POST", "/api/projects", `{"slug":"csrf","name":"x"}`, "Cookie", auth.CookieName+"="+cookie,
		"Idempotency-Key", e.key()), 403, "forbidden")
	e.ok(e.do("POST", "/api/projects", `{"slug":"csrf","name":"x"}`, web(cookie, "Idempotency-Key", e.key())...), 201, nil)

	// Sign in again (behind TLS at the proxy: the cookie is Secure), sign out, and the old session is gone.
	expectProblem(t, e.do("POST", "/api/auth:login", `{"username":"admin","password":"wrong password"}`,
		auth.ClientHeader, auth.ClientWeb), 401, "unauthenticated")
	resp = e.ok(e.do("POST", "/api/auth:login", `{"username":"admin","password":"`+adminPassword+`"}`,
		auth.ClientHeader, auth.ClientWeb, "X-Forwarded-Proto", "https"), 200, nil)
	second := sessionCookie(t, resp)
	if !second.Secure || second.Value == cookie {
		t.Errorf("second session cookie %+v", second)
	}
	resp = e.ok(e.do("POST", "/api/auth:logout", "", web(cookie)...), 204, nil)
	if c := sessionCookie(t, resp); c.MaxAge >= 0 || c.Value != "" {
		t.Errorf("logout cookie %+v", c)
	}
	resp = e.do("GET", "/api/projects", "", web(cookie)...)
	if c := sessionCookie(t, resp); c.MaxAge >= 0 {
		t.Errorf("a revoked session's cookie is not cleared: %+v", c)
	}
	expectProblem(t, resp, 401, "unauthenticated")
	e.ok(e.do("GET", "/api/projects", "", web(second.Value)...), 200, nil)
	if n := e.count("SELECT count(*) FROM credentials WHERE kind = 'session' AND revoked_at IS NOT NULL"); n != 1 {
		t.Errorf("%d revoked sessions, want 1", n)
	}
	if n := e.count("SELECT count(*) FROM events WHERE type = 'user.password_set'"); n != 1 {
		t.Errorf("%d user.password_set events", n)
	}
	// Help stays readable without a session (the sign-in screen links error types to their articles).
	e.ok(e.do("GET", "/api/help/errors.unauthenticated", ""), 200, nil)
}

func TestSessionSlidesAndExpires(t *testing.T) {
	e, _ := startAuth(t)
	cookie := e.setup()
	ctx := context.Background()
	// Used within the minute: no write, no new cookie.
	resp := e.ok(e.do("GET", "/api/me", "", web(cookie)...), 200, nil)
	if len(resp.Cookies()) != 0 {
		t.Errorf("cookie re-issued within the touch interval: %v", resp.Cookies())
	}
	// Last used long ago and close to expiry: the next use slides it 30 days and re-issues the cookie.
	if _, err := e.pool.Exec(ctx, `UPDATE credentials SET last_used_at = now() - interval '2 minutes',
		expires_at = now() + interval '1 hour' WHERE kind = 'session'`); err != nil {
		t.Fatal(err)
	}
	resp = e.ok(e.do("GET", "/api/me", "", web(cookie)...), 200, nil)
	if c := sessionCookie(t, resp); c.Value != cookie || c.MaxAge != 30*24*3600 {
		t.Errorf("slid cookie %+v", c)
	}
	if n := e.count("SELECT count(*) FROM credentials WHERE kind = 'session' AND expires_at > now() + interval '29 days'"); n != 1 {
		t.Errorf("expiry did not slide")
	}
	// Expired: refused and cleared.
	if _, err := e.pool.Exec(ctx, "UPDATE credentials SET expires_at = now() - interval '1 second' WHERE kind = 'session'"); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do("GET", "/api/me", "", web(cookie)...), 401, "unauthenticated")
}

func TestLoginRateLimit(t *testing.T) {
	e, _ := startAuth(t)
	e.setup()
	login := func(user, password, ip string) *http.Response {
		return e.do("POST", "/api/auth:login", `{"username":"`+user+`","password":"`+password+`"}`,
			auth.ClientHeader, auth.ClientWeb, "X-Forwarded-For", ip)
	}
	for range 5 {
		expectProblem(t, login("admin", "wrong password", "203.0.113.1"), 401, "unauthenticated")
	}
	resp := login("admin", adminPassword, "203.0.113.1")
	if ra := resp.Header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q", ra)
	}
	expectProblem(t, resp, 429, "rate-limited")
	// Per username: another address is refused for admin too; per address: another username from the first
	// address is refused as well; an unrelated address and username are not.
	expectProblem(t, login("admin", adminPassword, "203.0.113.2"), 429, "rate-limited")
	expectProblem(t, login("someone", "x", "203.0.113.1"), 429, "rate-limited")
	expectProblem(t, login("someone", "x", "203.0.113.3"), 401, "unauthenticated")
}

func TestTOTP(t *testing.T) {
	e, clk := startAuth(t)
	cookie := e.setup()
	var enr struct{ Secret, URI string }
	e.ok(e.do("POST", "/api/auth/totp:enroll", "", web(cookie)...), 200, &enr)
	if enr.Secret == "" || !strings.HasPrefix(enr.URI, "otpauth://totp/Cadence:admin?") {
		t.Fatalf("enroll: %+v", enr)
	}
	code := func() string {
		c, err := auth.TOTPCode(enr.Secret, auth.TOTPStep(clk.Now()))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	wrong := "000000"
	if code() == wrong {
		wrong = "111111"
	}
	expectProblem(t, e.do("POST", "/api/auth/totp:confirm", `{"code":"`+wrong+`"}`, web(cookie)...), 422, "validation-failed")
	var st authStatus
	e.ok(e.do("POST", "/api/auth/totp:confirm", `{"code":"`+code()+`"}`, web(cookie)...), 200, &st)
	if st.TotpEnabled == nil || !*st.TotpEnabled {
		t.Fatalf("confirm: %+v", st)
	}
	expectProblem(t, e.do("POST", "/api/auth/totp:enroll", "", web(cookie)...), 409, "conflict")

	body := func(totp string) string {
		b, _ := json.Marshal(map[string]string{"username": "admin", "password": adminPassword, "totpCode": totp})
		return string(b)
	}
	noCode := `{"username":"admin","password":"` + adminPassword + `"}`
	expectProblem(t, e.do("POST", "/api/auth:login", noCode, auth.ClientHeader, auth.ClientWeb), 401, "totp-required")
	// The code used to confirm cannot be replayed; the next step's code signs in.
	expectProblem(t, e.do("POST", "/api/auth:login", body(code()), auth.ClientHeader, auth.ClientWeb), 401, "unauthenticated")
	clk.Add(auth.TOTPPeriod)
	e.ok(e.do("POST", "/api/auth:login", body(code()), auth.ClientHeader, auth.ClientWeb), 200, nil)
	// A wrong password with a right code is still refused; totp-required never leaks for a wrong password.
	expectProblem(t, e.do("POST", "/api/auth:login", `{"username":"admin","password":"nope nope nope"}`,
		auth.ClientHeader, auth.ClientWeb), 401, "unauthenticated")

	clk.Add(auth.TOTPPeriod)
	e.ok(e.do("POST", "/api/auth/totp:disable", `{"code":"`+code()+`"}`, web(cookie)...), 200, &st)
	if st.TotpEnabled == nil || *st.TotpEnabled {
		t.Fatalf("disable: %+v", st)
	}
	e.ok(e.do("POST", "/api/auth:login", noCode, auth.ClientHeader, auth.ClientWeb), 200, nil)
	if n := e.count("SELECT count(*) FROM events WHERE type IN ('user.totp_enabled', 'user.totp_disabled')"); n != 2 {
		t.Errorf("%d TOTP events, want 2", n)
	}
}

type credential struct {
	ID, Kind, Name string
	Scope          struct {
		All          bool
		ProjectID    string `json:"projectId"`
		Project      string
		RegistryRead bool
		Preset       string
	}
	Current    bool
	Rev        int
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

type created struct {
	Credential credential
	Token      string
}

func TestAPIKeysAndScope(t *testing.T) {
	e, _ := startAuth(t)
	cookie := e.setup()
	var a, b project
	e.ok(e.do("POST", "/api/projects", `{"slug":"alpha","name":"A"}`, web(cookie, "Idempotency-Key", e.key())...), 201, &a)
	e.ok(e.do("POST", "/api/projects", `{"slug":"beta","name":"B"}`, web(cookie, "Idempotency-Key", e.key())...), 201, &b)

	expectProblem(t, e.do("POST", "/api/credentials", `{"name":"none","scope":{}}`, web(cookie, "Idempotency-Key", e.key())...),
		422, "validation-failed")
	var dry created
	e.ok(e.do("POST", "/api/credentials?dryRun=true", `{"name":"ci","scope":{"project":"alpha"}}`,
		web(cookie, "Idempotency-Key", e.key())...), 200, &dry)
	if dry.Token != "" || dry.Credential.Kind != "api_key" {
		t.Errorf("dry run: %+v", dry)
	}

	// Created once with a token; the replay of the same key has no token, and neither does the stored response.
	key := e.key()
	var k created
	resp := e.ok(e.do("POST", "/api/credentials", `{"name":"ci","scope":{"project":"alpha"}}`, web(cookie, "Idempotency-Key", key)...), 201, &k)
	if !strings.HasPrefix(k.Token, auth.PrefixAPIKey) || k.Credential.Scope.Project != "alpha" ||
		k.Credential.Scope.ProjectID != a.ID || k.Credential.Rev != 1 || resp.Header.Get("ETag") != `"1"` {
		t.Fatalf("created %+v", k)
	}
	var replay created
	e.ok(e.do("POST", "/api/credentials", `{"name":"ci","scope":{"project":"alpha"}}`, web(cookie, "Idempotency-Key", key)...), 201, &replay)
	if replay.Token != "" || replay.Credential.ID != k.Credential.ID {
		t.Errorf("replay: %+v", replay)
	}
	if n := e.count("SELECT count(*) FROM idempotency_keys WHERE position('" + k.Token + "' in convert_from(body, 'UTF8')) > 0"); n != 0 {
		t.Error("the token is stored in the idempotency table")
	}
	if n := e.count("SELECT count(*) FROM credentials WHERE token_hash = '" + auth.HashToken(k.Token) + "'"); n != 1 {
		t.Error("the key's hash is not stored")
	}

	// The key reaches its project only, needs no CSRF header, and cannot do instance-wide things.
	tok := k.Token
	var me struct{ Kind, ID, Name string }
	e.ok(e.do("GET", "/api/me", "", bearer(tok)...), 200, &me)
	if me.Kind != "automation" || me.ID != k.Credential.ID || me.Name != "ci" {
		t.Errorf("me.get with a key = %+v", me)
	}
	var list struct{ Items []project }
	e.ok(e.do("GET", "/api/projects", "", bearer(tok)...), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Slug != "alpha" {
		t.Errorf("projects.list with a key: %+v", list.Items)
	}
	e.ok(e.do("GET", "/api/projects/alpha", "", bearer(tok)...), 200, nil)
	expectProblem(t, e.do("GET", "/api/projects/beta", "", bearer(tok)...), 403, "forbidden")
	e.ok(e.do("PATCH", "/api/projects/alpha", `{"name":"A2"}`, bearer(tok, "Idempotency-Key", e.key(), "If-Match", `"1"`)...), 200, nil)
	expectProblem(t, e.do("PATCH", "/api/projects/beta", `{"name":"B2"}`, bearer(tok, "Idempotency-Key", e.key(), "If-Match", `"1"`)...), 403, "forbidden")
	expectProblem(t, e.do("POST", "/api/projects", `{"slug":"gamma","name":"G"}`, bearer(tok, "Idempotency-Key", e.key())...), 403, "forbidden")
	expectProblem(t, e.do("GET", "/api/credentials", "", bearer(tok)...), 403, "forbidden")
	expectProblem(t, e.do("GET", "/api/registry", "", bearer(tok)...), 403, "forbidden")
	expectProblem(t, e.do("POST", "/api/auth/totp:enroll", "", bearer(tok)...), 403, "forbidden")
	expectProblem(t, e.do("GET", "/api/events?project=beta", "", bearer(tok)...), 403, "forbidden")
	var evs struct {
		Items []struct {
			ProjectID string `json:"projectId"`
			Type      string
		}
	}
	e.ok(e.do("GET", "/api/events", "", bearer(tok)...), 200, &evs)
	for _, ev := range evs.Items {
		if ev.ProjectID == b.ID {
			t.Errorf("a key scoped to alpha sees beta's event %+v", ev)
		}
	}
	if n := e.count("SELECT count(*) FROM credentials WHERE kind = 'api_key' AND last_used_at IS NOT NULL"); n != 1 {
		t.Errorf("last_used_at not recorded")
	}

	// A registry-read key reads the registry and nothing else.
	var reg created
	e.ok(e.do("POST", "/api/credentials", `{"name":"reader","scope":{"registryRead":true}}`, web(cookie, "Idempotency-Key", e.key())...), 201, &reg)
	e.ok(e.do("GET", "/api/registry", "", bearer(reg.Token)...), 200, nil)
	e.ok(e.do("GET", "/api/projects", "", bearer(reg.Token)...), 200, &list)
	if len(list.Items) != 0 {
		t.Errorf("registry key lists projects: %+v", list.Items)
	}
	expectProblem(t, e.do("GET", "/api/events", "", bearer(reg.Token)...), 403, "forbidden")

	// credentials.list never carries secrets and marks the caller's own credential.
	listResp := e.do("GET", "/api/credentials", "", web(cookie)...)
	var creds struct{ Items []credential }
	e.ok(listResp, 200, &creds)
	var sawSession bool
	for _, c := range creds.Items {
		if c.Kind == "session" && c.Current {
			sawSession = true
		}
	}
	if len(creds.Items) != 3 || !sawSession {
		t.Errorf("credentials.list: %+v", creds.Items)
	}
	raw := e.do("GET", "/api/credentials?kind=api_key", "", web(cookie)...)
	var rawBody bytes.Buffer
	_, _ = rawBody.ReadFrom(raw.Body)
	_ = raw.Body.Close()
	if strings.Contains(rawBody.String(), tok) || strings.Contains(rawBody.String(), auth.HashToken(tok)) || strings.Contains(rawBody.String(), "session") {
		t.Errorf("credentials.list?kind=api_key leaks or mis-filters: %s", rawBody.String())
	}

	// Revoke is a command with If-Match; the key stops working at once.
	expectProblem(t, e.do("POST", "/api/credentials/"+k.Credential.ID+":revoke", "", web(cookie, "Idempotency-Key", e.key())...), 428, "precondition-required")
	var revoked credential
	e.ok(e.do("POST", "/api/credentials/"+k.Credential.ID+":revoke", "", web(cookie, "Idempotency-Key", e.key(), "If-Match", `"1"`)...), 200, &revoked)
	if revoked.RevokedAt == nil || revoked.Rev != 2 {
		t.Errorf("revoked %+v", revoked)
	}
	expectProblem(t, e.do("GET", "/api/me", "", bearer(tok)...), 401, "unauthenticated")
	expectProblem(t, e.do("POST", "/api/credentials/"+k.Credential.ID+":revoke", "", web(cookie, "Idempotency-Key", e.key(), "If-Match", `"2"`)...), 409, "conflict")
	expectProblem(t, e.do("POST", "/api/credentials/crd_nope:revoke", "", web(cookie, "Idempotency-Key", e.key(), "If-Match", `"1"`)...), 404, "not-found")
	if n := e.count("SELECT count(*) FROM events WHERE type IN ('credential.created', 'credential.revoked') AND project_id IS NULL"); n != 3 {
		t.Errorf("%d credential events, want 3", n)
	}
	e.ok(e.do("GET", "/api/credentials?revoked=true&kind=api_key", "", web(cookie)...), 200, &creds)
	if len(creds.Items) != 2 {
		t.Errorf("revoked=true: %+v", creds.Items)
	}
	// An expired key is refused.
	if _, err := e.pool.Exec(context.Background(), "UPDATE credentials SET expires_at = now() - interval '1 second' WHERE id = $1", reg.Credential.ID); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do("GET", "/api/registry", "", bearer(reg.Token)...), 401, "unauthenticated")
}

func TestAgentToken(t *testing.T) {
	e, _ := startAuth(t)
	cookie := e.setup()
	var a project
	e.ok(e.do("POST", "/api/projects", `{"slug":"alpha","name":"A"}`, web(cookie, "Idempotency-Key", e.key())...), 201, &a)
	ctx := context.Background()
	var token, id string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		token, id, err = credentials.MintAgentToken(ctx, tx, "ses_1", a.ID, "guardrails-default")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(token, auth.PrefixAgent) || !strings.HasPrefix(id, "crd_") {
		t.Fatalf("minted %q %q", token, id)
	}
	var me struct{ Kind, ID, SessionID string }
	e.ok(e.do("GET", "/api/me", "", bearer(token)...), 200, &me)
	if me.Kind != "agent" || me.ID != id || me.SessionID != "ses_1" {
		t.Errorf("me.get with an agent token = %+v", me)
	}
	e.ok(e.do("GET", "/api/projects/alpha", "", bearer(token)...), 200, nil)
	e.ok(e.do("GET", "/api/registry", "", bearer(token)...), 200, nil)
	// A session token is not accepted as Bearer, nor an agent token as a cookie.
	expectProblem(t, e.do("GET", "/api/me", "", bearer(cookie)...), 401, "unauthenticated")
	expectProblem(t, e.do("GET", "/api/me", "", "Cookie", auth.CookieName+"="+token), 401, "unauthenticated")

	var creds struct{ Items []credential }
	e.ok(e.do("GET", "/api/credentials?kind=agent", "", web(cookie)...), 200, &creds)
	if len(creds.Items) != 1 || creds.Items[0].Scope.Preset != "guardrails-default" || !creds.Items[0].Scope.RegistryRead {
		t.Errorf("agent credential %+v", creds.Items)
	}

	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return credentials.Revoke(ctx, tx, id) }); err != nil {
		t.Fatal(err)
	}
	expectProblem(t, e.do("GET", "/api/me", "", bearer(token)...), 401, "unauthenticated")
	// Revoking twice is a no-op; an unknown id is not-found.
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return credentials.Revoke(ctx, tx, id) }); err != nil {
		t.Errorf("second revoke: %v", err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return credentials.Revoke(ctx, tx, "crd_nope") }); err == nil {
		t.Error("revoking an unknown credential succeeded")
	}
}

func TestResetPassword(t *testing.T) {
	e, _ := startAuth(t)
	cookie := e.setup()
	store := credentials.NewStore(e.pool, time.Now, auth.PasswordParams{Time: 1, Memory: 1024, Threads: 1, SaltLen: 16, KeyLen: 32})
	ctx := context.Background()
	var n int64
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		var err error
		_, n, err = store.ResetPassword(ctx, tx, "admin", "a brand new password", true)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d sessions revoked, want 1", n)
	}
	expectProblem(t, e.do("GET", "/api/me", "", web(cookie)...), 401, "unauthenticated")
	expectProblem(t, e.do("POST", "/api/auth:login", `{"username":"admin","password":"`+adminPassword+`"}`,
		auth.ClientHeader, auth.ClientWeb), 401, "unauthenticated")
	e.ok(e.do("POST", "/api/auth:login", `{"username":"admin","password":"a brand new password"}`,
		auth.ClientHeader, auth.ClientWeb), 200, nil)
}
