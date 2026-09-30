package notify

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Token actions.
const (
	ActionApprove = "approve"
	ActionDeny    = "deny"
)

// Token errors: a press whose data was altered (or signed with another key), one already used, one past its
// approval's expiry.
var (
	ErrTokenInvalid = errors.New("the button is not one Cadence signed")
	ErrTokenSpent   = errors.New("this approval was already decided from a button")
	ErrTokenExpired = errors.New("the approval expired")
)

var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// Signer mints and checks the callback data of inline buttons: "<a|d>.<id>.<mac>", where id is 16 random base32
// characters and mac the first 16 bytes of HMAC-SHA256(key, action "." id), base64url — 41 bytes, inside Telegram's
// 64-byte limit. The key comes from the master key (secrets.Store.DeriveKey), so a database dump alone cannot forge
// a button; the token's row makes it single-use and bounds it by the approval's expiry.
type Signer struct {
	key []byte
	Now func() time.Time
}

// NewSigner returns a signer keyed by key.
func NewSigner(key []byte) *Signer { return &Signer{key: key, Now: time.Now} }

func (s *Signer) mac(action, id string) string {
	m := hmac.New(sha256.New, s.key)
	_, _ = m.Write([]byte(action + "." + id))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil)[:16])
}

func actionCode(action string) string {
	if action == ActionApprove {
		return "a"
	}
	return "d"
}

// Mint stores an approve and a deny token for the approval, valid until expires, and returns their callback data.
func (s *Signer) Mint(ctx context.Context, q storage.Querier, approvalID string, expires time.Time) (approve, deny string, err error) {
	out := map[string]string{}
	for _, action := range []string{ActionApprove, ActionDeny} {
		var raw [10]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", "", fmt.Errorf("token id: %w", err)
		}
		id := strings.ToLower(tokenEncoding.EncodeToString(raw[:]))
		if _, err := q.Exec(ctx, `INSERT INTO notification_tokens (id, approval_id, action, expires_at) VALUES ($1, $2, $3, $4)`,
			id, approvalID, action, expires); err != nil {
			return "", "", fmt.Errorf("store button token: %w", err)
		}
		out[action] = actionCode(action) + "." + id + "." + s.mac(action, id)
	}
	return out[ActionApprove], out[ActionDeny], nil
}

// Parse checks the signature of callback data and returns the token id and action.
func (s *Signer) Parse(data string) (id, action string, err error) {
	parts := strings.Split(data, ".")
	if len(parts) != 3 || len(parts[1]) != 16 {
		return "", "", ErrTokenInvalid
	}
	switch parts[0] {
	case "a":
		action = ActionApprove
	case "d":
		action = ActionDeny
	default:
		return "", "", ErrTokenInvalid
	}
	if !hmac.Equal([]byte(parts[2]), []byte(s.mac(action, parts[1]))) {
		return "", "", ErrTokenInvalid
	}
	return parts[1], action, nil
}

// Presser is who pressed a button.
type Presser struct {
	ChatID   int64  `json:"chatId"`
	UserID   int64  `json:"userId"`
	Username string `json:"username,omitempty"`
}

// Spend marks the token id with action used by p, together with every other token of its approval, and returns the
// approval id. A token already used is ErrTokenSpent, one past its expiry ErrTokenExpired, an unknown one (or a
// signed id whose stored action differs) ErrTokenInvalid.
func (s *Signer) Spend(ctx context.Context, tx pgx.Tx, id, action string, p Presser) (string, error) {
	var (
		approvalID, stored string
		expires            time.Time
		used               *time.Time
	)
	err := tx.QueryRow(ctx, `SELECT approval_id, action, expires_at, used_at FROM notification_tokens WHERE id = $1 FOR UPDATE`,
		id).Scan(&approvalID, &stored, &expires, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTokenInvalid
	}
	if err != nil {
		return "", fmt.Errorf("read button token: %w", err)
	}
	switch {
	case stored != action:
		return "", ErrTokenInvalid
	case used != nil:
		return "", ErrTokenSpent
	case !s.Now().Before(expires):
		return "", ErrTokenExpired
	}
	by, _ := json.Marshal(p)
	if _, err := tx.Exec(ctx, `UPDATE notification_tokens SET used_at = $2, used_by = $3 WHERE approval_id = $1 AND used_at IS NULL`,
		approvalID, s.Now(), by); err != nil {
		return "", fmt.Errorf("spend button token: %w", err)
	}
	return approvalID, nil
}
