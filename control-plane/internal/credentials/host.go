package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// HostName is the name of the agent host credential in the credentials list.
const HostName = "agent host"

// NewHostToken issues an agent host token (cah_): it reaches the host protocol (claim, report, ask) and nothing
// else — no project, no registry. With others it also revokes every other active host token, so a re-issued token
// replaces the old one.
func NewHostToken(ctx context.Context, q storage.Querier, name string, others bool) (string, Credential, error) {
	if others {
		if _, err := q.Exec(ctx, `UPDATE credentials SET revoked_at = now(), rev = rev + 1
			WHERE kind = 'agent_host' AND revoked_at IS NULL`); err != nil {
			return "", Credential{}, fmt.Errorf("revoke old host tokens: %w", err)
		}
	}
	if name == "" {
		name = HostName
	}
	return issue(ctx, q, auth.PrefixHost, Credential{Kind: KindAgentHost, Name: name, Scope: auth.Scope{}})
}

// EnsureHostTokenFile keeps a working agent host token in path, the file the agent host reads (a volume both
// containers mount): a token there that still resolves is kept; otherwise a new one is issued, every older host
// token is revoked, and the file is written 0600. It reports whether it issued a new token.
func EnsureHostTokenFile(ctx context.Context, pool *pgxpool.Pool, path string) (bool, error) {
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the path is the operator's CADENCE_HOST_TOKEN_FILE
		tok := strings.TrimSpace(string(b))
		if strings.HasPrefix(tok, auth.PrefixHost) {
			var id string
			err := pool.QueryRow(ctx, `SELECT id FROM credentials WHERE token_hash = $1 AND kind = 'agent_host'
				AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, auth.HashToken(tok)).Scan(&id)
			if err == nil {
				return false, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return false, fmt.Errorf("check the host token: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var tok string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		tok, _, err = NewHostToken(ctx, tx, HostName, true)
		return err
	}); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(tok+"\n"), 0o600); err != nil {
		return false, fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, fmt.Errorf("replace %s: %w", path, err)
	}
	return true, nil
}
