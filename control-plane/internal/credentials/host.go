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
	return ensureTokenFile(ctx, pool, path, KindAgentHost, auth.PrefixHost, func(q storage.Querier) (string, error) {
		tok, _, err := NewHostToken(ctx, q, HostName, true)
		return tok, err
	})
}

// NewWorkerToken issues a worker token (cwk_) for the compute host named host (its subject): it reaches the worker
// protocol for that host and nothing else. With others it also revokes the host's other worker tokens, so a
// re-issued token replaces the old one (one per host, R14).
func NewWorkerToken(ctx context.Context, q storage.Querier, host string, others bool) (string, Credential, error) {
	if host == "" {
		return "", Credential{}, errors.New("worker token: name the compute host")
	}
	if others {
		if _, err := q.Exec(ctx, `UPDATE credentials SET revoked_at = now(), rev = rev + 1
			WHERE kind = 'worker' AND subject = $1 AND revoked_at IS NULL`, host); err != nil {
			return "", Credential{}, fmt.Errorf("revoke old worker tokens: %w", err)
		}
	}
	return issue(ctx, q, auth.PrefixWorker, Credential{Kind: KindWorker, Subject: host, Name: "worker on " + host, Scope: auth.Scope{}})
}

// EnsureWorkerTokenFile keeps a working worker token for host in path (a volume the control plane writes and the
// worker reads), like EnsureHostTokenFile: a token there that still resolves for this host is kept; otherwise a new
// one is issued and the host's older worker tokens are revoked.
func EnsureWorkerTokenFile(ctx context.Context, pool *pgxpool.Pool, path, host string) (bool, error) {
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the path is the operator's CADENCE_WORKER_TOKEN_FILE
		tok := strings.TrimSpace(string(b))
		var id string
		err := pool.QueryRow(ctx, `SELECT id FROM credentials WHERE token_hash = $1 AND kind = 'worker' AND subject = $2
			AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, auth.HashToken(tok), host).Scan(&id)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("check the worker token: %w", err)
		}
		_ = os.Remove(path) // a token of another host or a revoked one: issue a new one below
	}
	return ensureTokenFile(ctx, pool, path, KindWorker, auth.PrefixWorker, func(q storage.Querier) (string, error) {
		tok, _, err := NewWorkerToken(ctx, q, host, true)
		return tok, err
	})
}

// EgressName is the name of the egress proxy credential in the credentials list.
const EgressName = "egress proxy"

// NewEgressToken issues an egress proxy token (cep_): it reads the egress allowlist (egressHosts.list) and nothing
// else. It revokes every other active egress proxy token.
func NewEgressToken(ctx context.Context, q storage.Querier) (string, Credential, error) {
	if _, err := q.Exec(ctx, `UPDATE credentials SET revoked_at = now(), rev = rev + 1
		WHERE kind = 'egress_proxy' AND revoked_at IS NULL`); err != nil {
		return "", Credential{}, fmt.Errorf("revoke old egress tokens: %w", err)
	}
	return issue(ctx, q, auth.PrefixEgress, Credential{Kind: KindEgressProxy, Name: EgressName, Scope: auth.Scope{}})
}

// EnsureEgressTokenFile keeps a working egress proxy token in path (a volume the control plane writes and the proxy
// reads), like EnsureHostTokenFile. The proxy has a credential of its own so that it never holds the host token,
// which claims agent credential values.
func EnsureEgressTokenFile(ctx context.Context, pool *pgxpool.Pool, path string) (bool, error) {
	return ensureTokenFile(ctx, pool, path, KindEgressProxy, auth.PrefixEgress, func(q storage.Querier) (string, error) {
		tok, _, err := NewEgressToken(ctx, q)
		return tok, err
	})
}

func ensureTokenFile(ctx context.Context, pool *pgxpool.Pool, path, kind, prefix string,
	issueFn func(storage.Querier) (string, error)) (bool, error) {
	if b, err := os.ReadFile(path); err == nil { //nolint:gosec // the path is the operator's CADENCE_*_TOKEN_FILE
		tok := strings.TrimSpace(string(b))
		if strings.HasPrefix(tok, prefix) {
			var id string
			err := pool.QueryRow(ctx, `SELECT id FROM credentials WHERE token_hash = $1 AND kind = $2
				AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, auth.HashToken(tok), kind).Scan(&id)
			if err == nil {
				return false, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return false, fmt.Errorf("check the %s token: %w", kind, err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	var tok string
	if err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var err error
		tok, err = issueFn(tx)
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
