package credentials

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// AdminID is the admin account seeded by migration 0001 (the phase-0 development actor).
const AdminID = "usr_admin"

// User is a person who signs in. v1 has one: the admin.
type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TOTPEnabled bool   `json:"totpEnabled"`
}

// Actor is the user as the actor of its commands.
func (u User) Actor() auth.Actor { return auth.Actor{Kind: auth.KindUser, ID: u.ID, Name: u.Name} }

type account struct {
	User
	passwordHash *string
	totpSecret   *string
	totpLastStep int64
}

const userCols = "id, name, totp_enabled, password_hash, totp_secret, totp_last_step"

func scanAccount(row pgx.CollectableRow) (account, error) {
	var a account
	err := row.Scan(&a.ID, &a.Name, &a.TOTPEnabled, &a.passwordHash, &a.totpSecret, &a.totpLastStep)
	return a, err
}

func oneAccount(rows pgx.Rows, err error) (account, bool, error) {
	if err != nil {
		return account{}, false, fmt.Errorf("query user: %w", err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanAccount)
	if errors.Is(err, pgx.ErrNoRows) {
		return account{}, false, nil
	}
	if err != nil {
		return account{}, false, fmt.Errorf("read user: %w", err)
	}
	return a, true, nil
}

// GetUser returns the user with id, or not-found.
func GetUser(ctx context.Context, q storage.Querier, id string) (User, error) {
	rows, err := q.Query(ctx, "SELECT "+userCols+" FROM users WHERE id = $1", id)
	a, found, err := oneAccount(rows, err)
	if err == nil && !found {
		err = problems.NotFound.New("no user %q", id)
	}
	return a.User, err
}

// SetupRequired reports first start: the admin has no password yet.
func SetupRequired(ctx context.Context, q storage.Querier) (bool, error) {
	var required bool
	err := q.QueryRow(ctx, "SELECT password_hash IS NULL FROM users WHERE id = $1", AdminID).Scan(&required)
	if err != nil {
		return false, fmt.Errorf("read first-start state: %w", err)
	}
	return required, nil
}

// Setup sets the admin's password at first start (and, optionally, renames the account). Once a password is set
// it refuses with conflict: from then on only `cadence admin reset-password`, run on the host, changes it.
func (s *Store) Setup(ctx context.Context, tx pgx.Tx, username, password string) (User, []events.Draft, error) {
	hash, err := auth.HashPassword(password, s.params)
	if err != nil {
		return User{}, nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE users SET password_hash = $2, password_changed_at = now(),
			name = coalesce(nullif($3, ''), name)
		WHERE id = $1 AND password_hash IS NULL RETURNING `+userCols, AdminID, hash, username)
	a, found, err := oneAccount(rows, err)
	if err != nil {
		return User{}, nil, err
	}
	if !found {
		return User{}, nil, problems.Conflict.New("the admin account is already set up; sign in, or reset the password from the host shell with `cadence admin reset-password`")
	}
	return a.User, userEvent(a.User, "user.password_set"), nil
}

// Login verifies a sign-in: username, password and, when the user has TOTP on, a current code. Every failure is
// unauthenticated with the same detail, except a missing code after a correct password (totp-required), which the
// sign-in form answers by asking for it. The TOTP step is recorded so a code cannot be used twice.
func (s *Store) Login(ctx context.Context, tx pgx.Tx, username, password, code string) (User, error) {
	bad := problems.Unauthenticated.New("wrong username, password or code")
	rows, err := tx.Query(ctx, "SELECT "+userCols+" FROM users WHERE name = $1 FOR UPDATE", username)
	a, found, err := oneAccount(rows, err)
	if err != nil {
		return User{}, err
	}
	if !found || a.passwordHash == nil {
		_, _ = auth.VerifyPassword(s.dummyHash(), password) // same cost as a real check
		return User{}, bad
	}
	ok, err := auth.VerifyPassword(*a.passwordHash, password)
	if err != nil {
		return User{}, fmt.Errorf("verify password of %s: %w", a.ID, err)
	}
	if !ok {
		return User{}, bad
	}
	if !a.TOTPEnabled {
		return a.User, nil
	}
	if code == "" {
		return User{}, problems.TOTPRequired.New("this account signs in with a code from its authenticator app")
	}
	if err := s.checkTOTP(ctx, tx, a, code); err != nil {
		if errors.Is(err, errBadCode) {
			return User{}, bad
		}
		return User{}, err
	}
	return a.User, nil
}

var errBadCode = errors.New("bad TOTP code")

// checkTOTP verifies code against the account's secret and stores the step it matched.
func (s *Store) checkTOTP(ctx context.Context, tx pgx.Tx, a account, code string) error {
	if a.totpSecret == nil {
		return errBadCode
	}
	step, ok, err := auth.VerifyTOTP(*a.totpSecret, code, s.now(), a.totpLastStep)
	if err != nil {
		return err
	}
	if !ok {
		return errBadCode
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET totp_last_step = $2 WHERE id = $1", a.ID, step); err != nil {
		return fmt.Errorf("record TOTP step: %w", err)
	}
	return nil
}

func lockAccount(ctx context.Context, tx pgx.Tx, userID string) (account, error) {
	rows, err := tx.Query(ctx, "SELECT "+userCols+" FROM users WHERE id = $1 FOR UPDATE", userID)
	a, found, err := oneAccount(rows, err)
	if err == nil && !found {
		err = problems.NotFound.New("no user %q", userID)
	}
	return a, err
}

// EnrollTOTP stores a new pending secret for userID and returns it with its otpauth URI. It is refused while TOTP
// is on (turn it off first); a second enrollment replaces a pending secret.
func (s *Store) EnrollTOTP(ctx context.Context, tx pgx.Tx, userID string) (secret, uri string, err error) {
	a, err := lockAccount(ctx, tx, userID)
	if err != nil {
		return "", "", err
	}
	if a.TOTPEnabled {
		return "", "", problems.Conflict.New("TOTP is already on; turn it off with a current code before enrolling again")
	}
	secret, err = auth.NewTOTPSecret()
	if err != nil {
		return "", "", err
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET totp_secret = $2, totp_last_step = 0 WHERE id = $1", userID, secret); err != nil {
		return "", "", fmt.Errorf("store TOTP secret: %w", err)
	}
	return secret, auth.TOTPURI(secret, a.Name), nil
}

// ConfirmTOTP turns TOTP on once code matches the pending secret.
func (s *Store) ConfirmTOTP(ctx context.Context, tx pgx.Tx, userID, code string) (User, []events.Draft, error) {
	a, err := lockAccount(ctx, tx, userID)
	if err != nil {
		return User{}, nil, err
	}
	if a.TOTPEnabled {
		return User{}, nil, problems.Conflict.New("TOTP is already on")
	}
	if a.totpSecret == nil {
		return User{}, nil, problems.Conflict.New("no TOTP enrollment is pending; start one with totp.enroll")
	}
	if err := s.checkTOTP(ctx, tx, a, code); err != nil {
		return User{}, nil, codeError(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET totp_enabled = true WHERE id = $1", userID); err != nil {
		return User{}, nil, fmt.Errorf("enable TOTP: %w", err)
	}
	a.TOTPEnabled = true
	return a.User, userEvent(a.User, "user.totp_enabled"), nil
}

// DisableTOTP turns TOTP off when code is current.
func (s *Store) DisableTOTP(ctx context.Context, tx pgx.Tx, userID, code string) (User, []events.Draft, error) {
	a, err := lockAccount(ctx, tx, userID)
	if err != nil {
		return User{}, nil, err
	}
	if !a.TOTPEnabled {
		return User{}, nil, problems.Conflict.New("TOTP is not on")
	}
	if err := s.checkTOTP(ctx, tx, a, code); err != nil {
		return User{}, nil, codeError(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE users SET totp_enabled = false, totp_secret = NULL, totp_last_step = 0 WHERE id = $1", userID); err != nil {
		return User{}, nil, fmt.Errorf("disable TOTP: %w", err)
	}
	a.TOTPEnabled = false
	return a.User, userEvent(a.User, "user.totp_disabled"), nil
}

func codeError(err error) error {
	if errors.Is(err, errBadCode) {
		return problems.ValidationFailed.New("the code does not match; check the authenticator app's clock and use the newest code")
	}
	return err
}

// ResetPassword sets userName's password from the host shell, revokes the user's sessions and, when asked, turns
// TOTP off (a lost phone). It works before first start too.
func (s *Store) ResetPassword(ctx context.Context, tx pgx.Tx, userName, password string, disableTOTP bool) (User, int64, error) {
	hash, err := auth.HashPassword(password, s.params)
	if err != nil {
		return User{}, 0, err
	}
	rows, err := tx.Query(ctx, `UPDATE users SET password_hash = $2, password_changed_at = now(),
			totp_enabled = totp_enabled AND NOT $3, totp_secret = CASE WHEN $3 THEN NULL ELSE totp_secret END
		WHERE name = $1 RETURNING `+userCols, userName, hash, disableTOTP)
	a, found, err := oneAccount(rows, err)
	if err != nil {
		return User{}, 0, err
	}
	if !found {
		return User{}, 0, problems.NotFound.New("no user named %q", userName)
	}
	n, err := RevokeUserSessions(ctx, tx, a.ID)
	if err != nil {
		return User{}, 0, err
	}
	if err := events.Append(ctx, tx, a.Actor(), nil, userEvent(a.User, "user.password_set")); err != nil {
		return User{}, 0, err
	}
	return a.User, n, nil
}

// User events are security-relevant account changes; they carry the account's public fields only.
func userEvent(u User, typ string) []events.Draft {
	return []events.Draft{{
		Topic:   events.EntityTopic("user", u.ID),
		Type:    typ,
		Payload: map[string]any{"user": u},
	}}
}
