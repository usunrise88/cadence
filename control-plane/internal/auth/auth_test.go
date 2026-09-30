package auth

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"testing"
	"time"
)

// fastParams keep the tests quick; production uses DefaultPasswordParams.
var fastParams = PasswordParams{Time: 1, Memory: 1024, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestPasswordHash(t *testing.T) {
	h, err := HashPassword("correct horse battery", fastParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("hash %q is not an argon2id PHC string", h)
	}
	h2, _ := HashPassword("correct horse battery", fastParams)
	if h == h2 {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
	tests := []struct {
		name     string
		encoded  string
		password string
		ok       bool
		err      error
	}{
		{"right password", h, "correct horse battery", true, nil},
		{"wrong password", h, "correct horse batterY", false, nil},
		{"empty password", h, "", false, nil},
		{"default params verify too", mustHash(t, "pw", DefaultPasswordParams()), "pw", true, nil},
		{"not argon2id", "$argon2i$v=19$m=1024,t=1,p=1$c2FsdA$a2V5", "x", false, ErrMalformedHash},
		{"wrong version", "$argon2id$v=16$m=1024,t=1,p=1$c2FsdA$a2V5", "x", false, ErrMalformedHash},
		{"bad params", "$argon2id$v=19$m=0,t=1,p=1$c2FsdA$a2V5", "x", false, ErrMalformedHash},
		{"bad salt", "$argon2id$v=19$m=1024,t=1,p=1$!!$a2V5", "x", false, ErrMalformedHash},
		{"truncated", "$argon2id$v=19$m=1024,t=1,p=1", "x", false, ErrMalformedHash},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := VerifyPassword(tt.encoded, tt.password)
			if ok != tt.ok || !errors.Is(err, tt.err) {
				t.Errorf("VerifyPassword = %v, %v; want %v, %v", ok, err, tt.ok, tt.err)
			}
		})
	}
}

func mustHash(t *testing.T, pw string, p PasswordParams) string {
	t.Helper()
	h, err := HashPassword(pw, p)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// RFC 6238 Appendix B vectors for SHA-1 (secret "12345678901234567890"), truncated to six digits.
func TestTOTPCodeRFC6238(t *testing.T) {
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte("12345678901234567890"))
	tests := []struct {
		unix int64
		want string
	}{
		{59, "287082"},
		{1111111109, "081804"},
		{1111111111, "050471"},
		{1234567890, "005924"},
		{2000000000, "279037"},
		{20000000000, "353130"},
	}
	for _, tt := range tests {
		got, err := TOTPCode(secret, TOTPStep(time.Unix(tt.unix, 0)))
		if err != nil || got != tt.want {
			t.Errorf("T=%d: code %q, %v; want %q", tt.unix, got, err, tt.want)
		}
	}
}

func TestVerifyTOTP(t *testing.T) {
	secret, err := NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	step := TOTPStep(now)
	code := func(s int64) string {
		c, err := TOTPCode(secret, s)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	tests := []struct {
		name     string
		code     string
		lastStep int64
		ok       bool
		matched  int64
	}{
		{"current step", code(step), 0, true, step},
		{"previous step (drift)", code(step - 1), 0, true, step - 1},
		{"next step (drift)", code(step + 1), 0, true, step + 1},
		{"two steps old", code(step - 2), 0, false, 0},
		{"replay of the used step", code(step), step, false, 0},
		{"newer step after a used one", code(step + 1), step, true, step + 1},
		{"wrong length", "12345", 0, false, 0},
		{"garbage", "abcdef", 0, false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := VerifyTOTP(secret, tt.code, now, tt.lastStep)
			if err != nil || ok != tt.ok || got != tt.matched {
				t.Errorf("VerifyTOTP = %d, %v, %v; want %d, %v", got, ok, err, tt.matched, tt.ok)
			}
		})
	}
	if _, _, err := VerifyTOTP("not base32!", "123456", now, 0); err == nil {
		t.Error("a malformed secret should fail")
	}
	uri := TOTPURI(secret, "admin")
	if !strings.HasPrefix(uri, "otpauth://totp/Cadence:admin?") || !strings.Contains(uri, "secret="+secret) ||
		!strings.Contains(uri, "issuer=Cadence") {
		t.Errorf("uri %s", uri)
	}
}

func TestTokens(t *testing.T) {
	a, err := NewToken(PrefixAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken(PrefixAPIKey)
	if !strings.HasPrefix(a, "cdk_") || len(a) != 4+52 || a == b || strings.ToLower(a) != a {
		t.Errorf("tokens %q %q", a, b)
	}
	if h := HashToken(a); len(h) != 64 || h != HashToken(a) || h == HashToken(b) {
		t.Errorf("hash %q", h)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(func() time.Time { return now }, DefaultLoginLimits()...)
	tick := func(d time.Duration) { now = now.Add(d) }

	for i := range 5 {
		if _, ok := l.Check("ip:a"); !ok {
			t.Fatalf("attempt %d refused", i+1)
		}
		l.Record("ip:a")
		tick(time.Second)
	}
	retry, ok := l.Check("ip:a")
	if ok || retry != 55*time.Second {
		t.Fatalf("6th attempt within a minute: ok=%v retry=%v, want refused, 55s", ok, retry)
	}
	if _, ok := l.Check("ip:b"); !ok {
		t.Error("another key is limited too")
	}
	tick(55 * time.Second) // the first hit leaves the minute window
	if _, ok := l.Check("ip:a"); !ok {
		t.Fatal("still refused after the minute window moved")
	}

	// The hour limit: 20 failures spread so the minute limit never trips.
	l2 := NewLimiter(func() time.Time { return now }, DefaultLoginLimits()...)
	start := now
	for range 20 {
		l2.Record("user:admin")
		tick(15 * time.Second)
	}
	retry, ok = l2.Check("user:admin")
	if ok {
		t.Fatal("21st attempt within the hour accepted")
	}
	if want := time.Hour - now.Sub(start); retry != want {
		t.Errorf("retry = %v, want %v", retry, want)
	}
	tick(retry)
	if _, ok := l2.Check("user:admin"); !ok {
		t.Error("still refused after Retry-After")
	}
}

func TestScope(t *testing.T) {
	tests := []struct {
		name     string
		scope    Scope
		project  string
		reaches  bool
		registry bool
	}{
		{"full", FullScope(), "prj_1", true, true},
		{"one project", Scope{ProjectID: "prj_1"}, "prj_1", true, false},
		{"another project", Scope{ProjectID: "prj_1"}, "prj_2", false, false},
		{"registry only", Scope{RegistryRead: true}, "prj_1", false, true},
		{"empty reaches nothing", Scope{}, "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.scope.AllowsProject(tt.project); got != tt.reaches {
				t.Errorf("AllowsProject = %v", got)
			}
			if got := tt.scope.AllowsRegistryRead(); got != tt.registry {
				t.Errorf("AllowsRegistryRead = %v", got)
			}
			ctx := WithScope(context.Background(), tt.scope)
			if err := CheckProject(ctx, tt.project); (err == nil) != tt.reaches {
				t.Errorf("CheckProject = %v", err)
			}
			if err := CheckRegistryRead(ctx); (err == nil) != tt.registry {
				t.Errorf("CheckRegistryRead = %v", err)
			}
			if err := CheckAll(ctx); (err == nil) != tt.scope.All {
				t.Errorf("CheckAll = %v", err)
			}
		})
	}
	if err := CheckProject(context.Background(), "prj_1"); err == nil || !strings.Contains(err.Error(), "unauthenticated") {
		t.Errorf("no scope in context: %v", err)
	}
}
