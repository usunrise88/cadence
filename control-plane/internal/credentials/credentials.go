// Package credentials is the one table of credentials — browser sessions, personal API keys (cdk_), agent session
// tokens (cst_), and later reviewer invitations and worker leases — plus the user account secrets behind sign-in
// (password, TOTP). Tokens are stored as SHA-256 hashes and shown once; revocation is a command.
//
// Mutations take a transaction and return the events they emit; reads take any storage.Querier. Store resolves
// presented tokens for the auth middleware (auth.Resolver).
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// EntityKind is the snake-singular entity kind used in topics and entity references.
const EntityKind = "credential"

// Credential kinds.
const (
	KindSession    = "session"
	KindAPIKey     = "api_key"
	KindAgent      = "agent"
	KindInvitation = "invitation"
	KindWorker     = "worker"
)

// TouchInterval is how often last_used_at (and a session's sliding expiry) is written at most.
const TouchInterval = time.Minute

// Credential is one row, without its hash. Its JSON form is the contract's Credential (minus the derived fields
// the server adds: project slug, current).
type Credential struct {
	ID         string     `json:"id"`
	Kind       string     `json:"kind"`
	UserID     string     `json:"userId,omitempty"`
	Subject    string     `json:"subject,omitempty"`
	Name       string     `json:"name"`
	Scope      auth.Scope `json:"scope"`
	Rev        int        `json:"rev"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
}

// Active reports whether c is neither revoked nor expired at now.
func (c Credential) Active(now time.Time) bool {
	return c.RevokedAt == nil && (c.ExpiresAt == nil || c.ExpiresAt.After(now))
}

const cols = "id, kind, coalesce(user_id, ''), coalesce(subject, ''), name, scope, rev, created_at, expires_at, last_used_at, revoked_at"

func scan(row pgx.CollectableRow) (Credential, error) {
	var (
		c     Credential
		scope []byte
	)
	if err := row.Scan(&c.ID, &c.Kind, &c.UserID, &c.Subject, &c.Name, &scope, &c.Rev, &c.CreatedAt, &c.ExpiresAt,
		&c.LastUsedAt, &c.RevokedAt); err != nil {
		return Credential{}, err
	}
	if err := json.Unmarshal(scope, &c.Scope); err != nil {
		return Credential{}, fmt.Errorf("decode scope of %s: %w", c.ID, err)
	}
	return c, nil
}

func one(rows pgx.Rows, err error, id string) (Credential, error) {
	if err != nil {
		return Credential{}, fmt.Errorf("query credential: %w", err)
	}
	c, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Credential{}, problems.NotFound.New("no credential %q", id)
	}
	if err != nil {
		return Credential{}, fmt.Errorf("read credential: %w", err)
	}
	return c, nil
}

// Get returns the credential with id, or not-found.
func Get(ctx context.Context, q storage.Querier, id string) (Credential, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM credentials WHERE id = $1", id)
	return one(rows, err, id)
}

// List returns credentials newest first; kind filters when not empty; revoked and expired ones only when asked.
func List(ctx context.Context, q storage.Querier, kind string, inactive bool) ([]Credential, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+` FROM credentials
		WHERE ($1 = '' OR kind = $1) AND ($2 OR (revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())))
		ORDER BY created_at DESC, id DESC`, kind, inactive)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	return out, nil
}

// issue inserts a credential of kind with a fresh token of prefix and returns the token (shown once).
func issue(ctx context.Context, q storage.Querier, prefix string, c Credential) (string, Credential, error) {
	token, err := auth.NewToken(prefix)
	if err != nil {
		return "", Credential{}, err
	}
	scope, err := json.Marshal(c.Scope)
	if err != nil {
		return "", Credential{}, fmt.Errorf("marshal scope: %w", err)
	}
	id := "crd_" + uuid.Must(uuid.NewV7()).String()
	// Signing in is a session's first use; keys and tokens show "never used" until they are.
	rows, err := q.Query(ctx, `INSERT INTO credentials (id, kind, user_id, subject, name, scope, token_hash, expires_at, last_used_at)
		VALUES ($1, $2, nullif($3, ''), nullif($4, ''), $5, $6, $7, $8, CASE WHEN $2 = 'session' THEN now() END)
		RETURNING `+cols,
		id, c.Kind, c.UserID, c.Subject, c.Name, scope, auth.HashToken(token), c.ExpiresAt)
	out, err := one(rows, err, id)
	if err != nil {
		return "", Credential{}, err
	}
	return token, out, nil
}

// NewSession starts a browser session for userID with full scope; it expires after auth.SessionLifetime unless
// used (sliding).
func NewSession(ctx context.Context, q storage.Querier, userID, name string) (token string, c Credential, err error) {
	exp := time.Now().Add(auth.SessionLifetime)
	return issue(ctx, q, auth.PrefixSession, Credential{
		Kind: KindSession, UserID: userID, Name: name, Scope: auth.FullScope(), ExpiresAt: &exp,
	})
}

// NewAPIKeyInput describes a personal API key.
type NewAPIKeyInput struct {
	UserID    string
	Name      string
	ProjectID string // the one project it reaches; empty for none
	Registry  bool   // may read the registry
	ExpiresAt *time.Time
}

// NewAPIKey creates a personal API key (cdk_) and returns its token (shown once) and the credential.created event.
func NewAPIKey(ctx context.Context, tx pgx.Tx, in NewAPIKeyInput) (string, Credential, []events.Draft, error) {
	if in.ProjectID == "" && !in.Registry {
		return "", Credential{}, nil, problems.Validation([]problems.FieldError{{
			Path: "/scope", Message: "name a project, allow registry read, or both",
		}})
	}
	if in.ExpiresAt != nil && !in.ExpiresAt.After(time.Now()) {
		return "", Credential{}, nil, problems.Validation([]problems.FieldError{{
			Path: "/expiresAt", Message: "must be in the future",
		}})
	}
	token, c, err := issue(ctx, tx, auth.PrefixAPIKey, Credential{
		Kind: KindAPIKey, UserID: in.UserID, Name: in.Name, ExpiresAt: in.ExpiresAt,
		Scope: auth.Scope{ProjectID: in.ProjectID, RegistryRead: in.Registry},
	})
	if err != nil {
		return "", Credential{}, nil, err
	}
	return token, c, event(c, "credential.created"), nil
}

// MintAgentToken issues the agent session token (cst_) for session sessionID: one project, registry read, the
// permission preset's verbs. It dies with the session (Revoke) and never expires on its own. It emits no event;
// the session's own events carry the change.
func MintAgentToken(ctx context.Context, tx pgx.Tx, sessionID, projectID, preset string) (token, id string, err error) {
	if sessionID == "" || projectID == "" {
		return "", "", fmt.Errorf("mint agent token: session and project are required")
	}
	token, c, err := issue(ctx, tx, auth.PrefixAgent, Credential{
		Kind: KindAgent, Subject: sessionID, Name: "agent session " + sessionID,
		Scope: auth.Scope{ProjectID: projectID, RegistryRead: true, Preset: preset},
	})
	if err != nil {
		return "", "", fmt.Errorf("mint agent token: %w", err)
	}
	return token, c.ID, nil
}

// Revoke revokes credential id at whatever revision it is (session end, sign-out). Revoking a revoked credential
// is a no-op; an unknown id is not-found. It emits no event; use RevokeAt for the audited command.
func Revoke(ctx context.Context, tx pgx.Tx, id string) error {
	tag, err := tx.Exec(ctx, `UPDATE credentials SET revoked_at = coalesce(revoked_at, now()),
		rev = CASE WHEN revoked_at IS NULL THEN rev + 1 ELSE rev END WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("revoke credential %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return problems.NotFound.New("no credential %q", id)
	}
	return nil
}

// RevokeAt is the credentials.revoke command: it revokes credential id at revision rev and emits
// credential.revoked. Revoking a revoked credential is a conflict.
func RevokeAt(ctx context.Context, tx pgx.Tx, id string, rev int) (Credential, []events.Draft, error) {
	rows, err := tx.Query(ctx, "SELECT "+cols+" FROM credentials WHERE id = $1 FOR UPDATE", id)
	cur, err := one(rows, err, id)
	if err != nil {
		return Credential{}, nil, err
	}
	if err := commands.CheckRev(EntityKind, rev, cur.Rev); err != nil {
		return Credential{}, nil, err
	}
	if cur.RevokedAt != nil {
		return Credential{}, nil, problems.Conflict.New("credential %s is already revoked", id)
	}
	rows, err = tx.Query(ctx, "UPDATE credentials SET revoked_at = now(), rev = rev + 1 WHERE id = $1 RETURNING "+cols, id)
	c, err := one(rows, err, id)
	if err != nil {
		return Credential{}, nil, err
	}
	return c, event(c, "credential.revoked"), nil
}

// RevokeUserSessions revokes every active session of userID (password reset).
func RevokeUserSessions(ctx context.Context, q storage.Querier, userID string) (int64, error) {
	tag, err := q.Exec(ctx, `UPDATE credentials SET revoked_at = now(), rev = rev + 1
		WHERE user_id = $1 AND kind = 'session' AND revoked_at IS NULL`, userID)
	if err != nil {
		return 0, fmt.Errorf("revoke sessions of %s: %w", userID, err)
	}
	return tag.RowsAffected(), nil
}

// Credential events carry the credential (never a token or hash) and no projectId: credentials are instance-wide
// configuration.
func event(c Credential, typ string) []events.Draft {
	return []events.Draft{{
		Topic:   events.EntityTopic(EntityKind, c.ID),
		Type:    typ,
		Entity:  &events.EntityRef{Kind: EntityKind, ID: c.ID, Rev: c.Rev},
		Payload: map[string]any{"credential": c},
	}}
}
