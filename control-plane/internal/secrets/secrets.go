// Package secrets stores named credentials (docs/spec/08-resolutions.md R9). Postgres holds the name, kind, scope
// and last use; the value lives in an encrypted file store in the control plane's data directory
// (<data dir>/secrets/<id>, NaCl secretbox under the master key, files 0600) and never enters the database, a
// response, a log line or an agent context. Server-side consumers read a value with Store.Read.
package secrets

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/nacl/secretbox"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the EntityKind of a secret.
const Kind = "secret"

// Kinds are the credential kinds (the contract's SecretKind).
var Kinds = []string{"huggingface", "ngc", "github", "s3", "judge-api", "other"}

// ScopeInstance is the scope of a secret every server-side consumer may read.
const ScopeInstance = "instance"

var idPattern = regexp.MustCompile(`^sec_[0-9a-f-]{36}$`)

// Secret is a credential's metadata. It has no value field: values never leave Store.
type Secret struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	Scope      string     `json:"scope"`
	Rev        int        `json:"rev"`
	Actor      auth.Actor `json:"actor"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
}

const secretCols = "id, name, kind, scope, rev, created_by, created_at, last_used_at" //nolint:gosec // column names, not a credential

func scanSecret(row pgx.CollectableRow) (Secret, error) {
	var s Secret
	err := row.Scan(&s.ID, &s.Name, &s.Kind, &s.Scope, &s.Rev, &s.Actor, &s.CreatedAt, &s.LastUsedAt)
	return s, err
}

// List returns secrets by name, without values.
func List(ctx context.Context, q storage.Querier) ([]Secret, error) {
	rows, err := q.Query(ctx, "SELECT "+secretCols+" FROM secrets ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanSecret)
	if err != nil {
		return nil, fmt.Errorf("list secrets: %w", err)
	}
	return out, nil
}

// Store encrypts values into files and reads them back for server-side consumers.
type Store struct {
	pool *pgxpool.Pool
	dir  string
	key  *[KeySize]byte
}

// NewStore returns a store keeping values under dir (created 0700 when missing), encrypted with key.
func NewStore(pool *pgxpool.Pool, dir string, key *[KeySize]byte) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create secret store %s: %w", dir, err)
	}
	return &Store{pool: pool, dir: dir, key: key}, nil
}

// Dir is the directory holding the encrypted values.
func (s *Store) Dir() string { return s.dir }

// NewInput is the body of secrets.new.
type NewInput struct {
	Name, Kind, Scope string
	Value             []byte
}

// Create records a secret's metadata in tx and, unless dryRun, writes its encrypted value. A taken name is a
// conflict; a project scope must name an existing project. The value file is written before the transaction
// commits; if the commit then fails the file is an orphan nobody references (its id is never reused).
func (s *Store) Create(ctx context.Context, tx pgx.Tx, in NewInput, actor auth.Actor, dryRun bool) (Secret, []events.Draft, error) {
	if in.Scope == "" {
		in.Scope = ScopeInstance
	}
	if slug, ok := strings.CutPrefix(in.Scope, "project:"); ok {
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM projects WHERE slug = $1)", slug).Scan(&exists); err != nil {
			return Secret{}, nil, fmt.Errorf("check secret scope: %w", err)
		}
		if !exists {
			return Secret{}, nil, problems.NotFound.New("scope %q names no project", in.Scope)
		}
	}
	id := "sec_" + uuid.Must(uuid.NewV7()).String()
	rows, err := tx.Query(ctx, `INSERT INTO secrets (id, name, kind, scope, created_by) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (name) DO NOTHING RETURNING `+secretCols, id, in.Name, in.Kind, in.Scope, actor)
	if err != nil {
		return Secret{}, nil, fmt.Errorf("insert secret: %w", err)
	}
	sec, err := pgx.CollectExactlyOneRow(rows, scanSecret)
	if errors.Is(err, pgx.ErrNoRows) {
		return Secret{}, nil, problems.Conflict.New("a secret named %q already exists; pick another name", in.Name)
	}
	if err != nil {
		return Secret{}, nil, fmt.Errorf("insert secret: %w", err)
	}
	if !dryRun {
		if err := s.write(sec.ID, in.Value); err != nil {
			return Secret{}, nil, err
		}
	}
	return sec, []events.Draft{{
		Topic:   events.EntityTopic(Kind, sec.ID),
		Type:    "secret.created",
		Entity:  &events.EntityRef{Kind: Kind, ID: sec.ID, Rev: sec.Rev},
		Payload: map[string]any{"secret": sec},
	}}, nil
}

// Read returns the value of the secret named name and records the use. It is for server-side consumers only
// (bootstrap, the worker lease); never hand the value to an agent or put it in a response or log line.
func (s *Store) Read(ctx context.Context, name string) ([]byte, error) {
	var id string
	err := s.pool.QueryRow(ctx, "UPDATE secrets SET last_used_at = now() WHERE name = $1 RETURNING id", name).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, problems.NotFound.New("no secret named %q", name)
	}
	if err != nil {
		return nil, fmt.Errorf("look up secret %q: %w", name, err)
	}
	return s.read(id)
}

// RequestMAC keys a request fingerprint on a secret value with the master key, so the idempotency record of
// secrets.new cannot be used to test guesses of the value without the key.
func (s *Store) RequestMAC(value []byte) string {
	m := hmac.New(sha256.New, s.key[:])
	_, _ = m.Write(value)
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Store) path(id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", fmt.Errorf("secret id %q is malformed", id)
	}
	return filepath.Join(s.dir, id), nil
}

// write seals value (a fresh random nonce, then secretbox) into <dir>/<id>, 0600, atomically.
func (s *Store) write(id string, value []byte) error {
	p, err := s.path(id)
	if err != nil {
		return err
	}
	return s.seal(s.dir, p, id, value)
}

// seal writes value sealed into p (a temporary file in dir, renamed), 0600.
func (s *Store) seal(dir, p, id string, value []byte) error {
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("secret nonce: %w", err)
	}
	sealed := secretbox.Seal(nonce[:], value, &nonce, s.key)
	tmp, err := os.CreateTemp(dir, ".tmp-"+id+"-*")
	if err != nil {
		return fmt.Errorf("write secret: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write secret: %w", err)
	}
	if _, err := tmp.Write(sealed); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write secret: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write secret: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write secret: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("write secret: %w", err)
	}
	return nil
}

func (s *Store) read(id string) ([]byte, error) {
	p, err := s.path(id)
	if err != nil {
		return nil, err
	}
	v, err := s.open(p, id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("secret %s has no value file in %s (restored database without the data directory?)", id, s.dir)
	}
	return v, err
}

// open reads and decrypts the sealed file p; a missing file is fs.ErrNotExist (wrapped).
func (s *Store) open(p, id string) ([]byte, error) {
	sealed, err := os.ReadFile(p) //nolint:gosec // p is <store dir>/[transit/]<validated id>
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("secret %s: %w", id, err)
	}
	if err != nil {
		return nil, fmt.Errorf("read secret %s: %w", id, err)
	}
	if len(sealed) < 24+secretbox.Overhead {
		return nil, fmt.Errorf("secret %s: value file is truncated", id)
	}
	var nonce [24]byte
	copy(nonce[:], sealed[:24])
	value, ok := secretbox.Open(nil, sealed[24:], &nonce, s.key)
	if !ok {
		return nil, fmt.Errorf("secret %s: cannot decrypt (wrong master key?)", id)
	}
	return value, nil
}
