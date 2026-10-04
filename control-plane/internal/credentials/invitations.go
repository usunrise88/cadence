package credentials

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Reviewer invitations (phase 4 · stream A; docs/spec/06-platform.md "Authentication and access"). A reviewer is a
// user with role reviewer and no password. An invitation is a credential of kind invitation (token cri_…, subject the
// batch) that auth.accept redeems for a browser session whose scope reaches that one batch; the session ends with the
// invitation and never slides. Freezing the batch revokes both.

// Reviewer roles in a batch.
const (
	RoleAnnotator   = "annotator"
	RoleAdjudicator = "adjudicator"
)

// UserRoleReviewer is users.role of an invited reviewer.
const UserRoleReviewer = "reviewer"

// EnsureReviewer returns the reviewer named name, creating it (role reviewer, no password) when no user has the name;
// the admin's or another non-reviewer account's name is refused.
func EnsureReviewer(ctx context.Context, tx pgx.Tx, name string) (User, error) {
	var (
		u    User
		role string
	)
	err := tx.QueryRow(ctx, "SELECT id, name, role FROM users WHERE name = $1", name).Scan(&u.ID, &u.Name, &role)
	switch {
	case err == nil && role != UserRoleReviewer:
		return User{}, problems.Conflict.New("%q is the name of an account that is not a reviewer; choose another name for the reviewer", name)
	case err == nil:
		return u, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return User{}, fmt.Errorf("find reviewer %s: %w", name, err)
	}
	u = User{ID: "usr_" + uuid.Must(uuid.NewV7()).String(), Name: name}
	if _, err := tx.Exec(ctx, "INSERT INTO users (id, name, role) VALUES ($1, $2, $3)", u.ID, u.Name, UserRoleReviewer); err != nil {
		return User{}, fmt.Errorf("create reviewer %s: %w", name, err)
	}
	return u, nil
}

// NewInvitation issues an invitation (cri_…) for reviewer userID to batchID with role, expiring at exp; the token is
// shown once.
func NewInvitation(ctx context.Context, tx pgx.Tx, userID, userName, batchID, role string, exp time.Time) (string, Credential, error) {
	return issue(ctx, tx, auth.PrefixInvitation, Credential{
		Kind: KindInvitation, UserID: userID, Subject: batchID, Name: "reviewer " + userName + " · " + batchID,
		Scope: auth.Scope{Batch: batchID, BatchRole: role}, ExpiresAt: &exp,
	})
}

// Invitations lists the invitations of batchID, newest first, revoked and expired ones included.
func Invitations(ctx context.Context, q storage.Querier, batchID string) ([]Credential, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM credentials WHERE kind = $1 AND subject = $2 ORDER BY created_at DESC, id DESC",
		KindInvitation, batchID)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list invitations: %w", err)
	}
	return out, nil
}

// Redeem starts a reviewer session from an invitation token: the session reaches the invitation's batch only and
// expires with the invitation. An unknown, revoked or expired token is invitation-invalid.
func Redeem(ctx context.Context, tx pgx.Tx, token, name string) (string, Credential, User, error) {
	var (
		inv      Credential
		userName string
	)
	rows, err := tx.Query(ctx, "SELECT "+cols+" FROM credentials WHERE token_hash = $1 AND kind = $2 FOR UPDATE",
		auth.HashToken(token), KindInvitation)
	if err != nil {
		return "", Credential{}, User{}, fmt.Errorf("find invitation: %w", err)
	}
	inv, err = pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", Credential{}, User{}, problems.InvitationInvalid.New("this invitation link is not known; ask for a new one")
	}
	if err != nil {
		return "", Credential{}, User{}, fmt.Errorf("read invitation: %w", err)
	}
	if !inv.Active(time.Now()) || inv.Scope.Batch == "" || inv.UserID == "" {
		return "", Credential{}, User{}, problems.InvitationInvalid.New("this invitation has expired or was revoked; ask the admin for a new one")
	}
	if err := tx.QueryRow(ctx, "SELECT name FROM users WHERE id = $1", inv.UserID).Scan(&userName); err != nil {
		return "", Credential{}, User{}, fmt.Errorf("read reviewer %s: %w", inv.UserID, err)
	}
	if _, err := tx.Exec(ctx, "UPDATE credentials SET last_used_at = now() WHERE id = $1", inv.ID); err != nil {
		return "", Credential{}, User{}, fmt.Errorf("touch invitation %s: %w", inv.ID, err)
	}
	tok, c, err := issue(ctx, tx, auth.PrefixSession, Credential{
		Kind: KindSession, UserID: inv.UserID, Subject: inv.ID, Name: name, Scope: inv.Scope, ExpiresAt: inv.ExpiresAt,
	})
	if err != nil {
		return "", Credential{}, User{}, err
	}
	return tok, c, User{ID: inv.UserID, Name: userName}, nil
}

// RevokeBatch revokes every invitation of batchID and every session they started (the batch closed).
func RevokeBatch(ctx context.Context, tx pgx.Tx, batchID string) (int64, error) {
	tag, err := tx.Exec(ctx, `UPDATE credentials SET revoked_at = now(), rev = rev + 1
		WHERE revoked_at IS NULL AND ((kind = 'invitation' AND subject = $1) OR (kind = 'session' AND scope->>'batch' = $1))`, batchID)
	if err != nil {
		return 0, fmt.Errorf("revoke the invitations of %s: %w", batchID, err)
	}
	return tag.RowsAffected(), nil
}

// InvitationExpiry is when an invitation made now for a batch due at due expires: maxDays from now, never after the
// due date (a due date in the past leaves maxDays).
func InvitationExpiry(now time.Time, due *time.Time, maxDays int, asked *time.Time) time.Time {
	exp := now.Add(time.Duration(maxDays) * 24 * time.Hour)
	if due != nil && due.After(now) && due.Before(exp) {
		exp = *due
	}
	if asked != nil && asked.After(now) && asked.Before(exp) {
		exp = *asked
	}
	return exp.UTC().Truncate(time.Second)
}
