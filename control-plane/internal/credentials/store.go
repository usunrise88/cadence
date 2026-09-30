package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
)

// Store resolves presented tokens and verifies sign-ins against the database.
type Store struct {
	pool   *pgxpool.Pool
	now    func() time.Time
	params auth.PasswordParams

	dummyOnce sync.Once
	dummy     string // a hash verified when the user does not exist, so both paths cost the same
}

// NewStore returns a store on pool; now is the clock of TOTP checks (time.Now in production) and params the
// Argon2id cost of new password hashes.
func NewStore(pool *pgxpool.Pool, now func() time.Time, params auth.PasswordParams) *Store {
	return &Store{pool: pool, now: now, params: params}
}

// Pool is the store's database.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Now is the store's clock.
func (s *Store) Now() time.Time { return s.now() }

// PasswordParams are the Argon2id costs of new hashes.
func (s *Store) PasswordParams() auth.PasswordParams { return s.params }

var _ auth.Resolver = (*Store)(nil)

// kindFor maps a presented token to the credential kind it must be: sessions only in the cookie, API keys and
// agent tokens only as Bearer.
func kindFor(token string, via auth.Via) (string, bool) {
	switch {
	case via == auth.ViaCookie && strings.HasPrefix(token, auth.PrefixSession):
		return KindSession, true
	case via == auth.ViaBearer && strings.HasPrefix(token, auth.PrefixAPIKey):
		return KindAPIKey, true
	case via == auth.ViaBearer && strings.HasPrefix(token, auth.PrefixAgent):
		return KindAgent, true
	case via == auth.ViaBearer && strings.HasPrefix(token, auth.PrefixHost):
		return KindAgentHost, true
	case via == auth.ViaBearer && strings.HasPrefix(token, auth.PrefixEgress):
		return KindEgressProxy, true
	}
	return "", false
}

// Resolve implements auth.Resolver. It records last use at most once per TouchInterval; for a session that write
// also slides its expiry, and refresh tells the middleware to re-issue the cookie.
func (s *Store) Resolve(ctx context.Context, token string, via auth.Via) (auth.Principal, bool, error) {
	kind, ok := kindFor(token, via)
	if !ok {
		return auth.Principal{}, false, auth.ErrUnknownToken
	}
	var (
		c        Credential
		scope    []byte
		userName string
		dbNow    time.Time
	)
	err := s.pool.QueryRow(ctx, `SELECT c.id, c.kind, coalesce(c.user_id, ''), coalesce(c.subject, ''), c.name, c.scope,
			c.last_used_at, coalesce(u.name, ''), now()
		FROM credentials c LEFT JOIN users u ON u.id = c.user_id
		WHERE c.token_hash = $1 AND c.kind = $2 AND c.revoked_at IS NULL AND (c.expires_at IS NULL OR c.expires_at > now())`,
		auth.HashToken(token), kind).
		Scan(&c.ID, &c.Kind, &c.UserID, &c.Subject, &c.Name, &scope, &c.LastUsedAt, &userName, &dbNow)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Principal{}, false, auth.ErrUnknownToken
	}
	if err != nil {
		return auth.Principal{}, false, fmt.Errorf("resolve credential: %w", err)
	}
	if err := json.Unmarshal(scope, &c.Scope); err != nil {
		return auth.Principal{}, false, fmt.Errorf("decode scope of %s: %w", c.ID, err)
	}
	refresh := false
	if c.LastUsedAt == nil || dbNow.Sub(*c.LastUsedAt) >= TouchInterval {
		if _, err := s.pool.Exec(ctx, `UPDATE credentials SET last_used_at = now(),
				expires_at = CASE WHEN kind = 'session' THEN now() + $2::interval ELSE expires_at END
			WHERE id = $1`, c.ID, fmt.Sprintf("%d seconds", int(auth.SessionLifetime/time.Second))); err != nil {
			return auth.Principal{}, false, fmt.Errorf("touch credential %s: %w", c.ID, err)
		}
		refresh = c.Kind == KindSession
	}
	return principal(c, userName), refresh, nil
}

// principal is who a credential acts as: a session is its user; an API key is an automation actor named after the
// key; an agent token is the agent of its session. Actor ids of keys and tokens are the credential ids, so
// idempotency keys and audit rows stay apart per credential.
func principal(c Credential, userName string) auth.Principal {
	p := auth.Principal{Scope: c.Scope, CredentialID: c.ID, CredentialKind: c.Kind, UserID: c.UserID}
	switch c.Kind {
	case KindSession:
		p.Actor = auth.Actor{Kind: auth.KindUser, ID: c.UserID, Name: userName}
	case KindAgent:
		p.Actor = auth.Actor{Kind: auth.KindAgent, ID: c.ID, Name: "agent", SessionID: c.Subject}
	default:
		p.Actor = auth.Actor{Kind: auth.KindAutomation, ID: c.ID, Name: c.Name}
	}
	return p
}

func (s *Store) dummyHash() string {
	s.dummyOnce.Do(func() {
		h, err := auth.HashPassword("cadence-dummy-password", s.params)
		if err == nil {
			s.dummy = h
		}
	})
	return s.dummy
}
