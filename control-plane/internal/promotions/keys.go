package promotions

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/secrets"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Signing key states.
const (
	KeyCurrent = "current"
	KeyRetired = "retired"
)

// SecretKind is the secret kind holding a signing key's private seed (sealed with the master key like every secret,
// so backups carry it sealed).
const SecretKind = "signing"

// SecretName is the secret holding the first instance signing key; later keys take SecretName-<n>.
const SecretName = "instance-signing-key"

// keyLockClass serialises key creation and rotation (two-key advisory lock, see commands.idempotencyLockClass).
const keyLockClass int32 = 0x63646e50

// Key is an instance signing key. Private is set only when the key was loaded for signing.
type Key struct {
	ID         string
	Public     ed25519.PublicKey
	Private    ed25519.PrivateKey
	SecretName string
	State      string
	CreatedAt  time.Time
	RetiredAt  *time.Time
}

// KeyID names a public key: "ed25519:" and the hex of the first 16 bytes of the SHA-256 of its 32 raw bytes. The
// delivery script computes the same id from the pinned PEM file (openssl pkey … -outform DER | tail -c 32 | sha256sum).
func KeyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "ed25519:" + hex.EncodeToString(sum[:16])
}

// PublicPEM is the SubjectPublicKeyInfo PEM of pub: the file a person pins as /etc/cadence/instance.pub.
func PublicPEM(pub ed25519.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		panic(fmt.Sprintf("promotions: marshal an ed25519 public key: %v", err)) // cannot fail for a 32-byte key
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

// ParsePublicPEM reads an Ed25519 SubjectPublicKeyInfo PEM.
func ParsePublicPEM(s string) (ed25519.PublicKey, error) {
	b, _ := pem.Decode([]byte(s))
	if b == nil || b.Type != "PUBLIC KEY" {
		return nil, errors.New("not a PUBLIC KEY PEM block")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("the public key is %T, not Ed25519", k)
	}
	return pub, nil
}

// SecretStore is the sealed secret store (secrets.Store): signing seeds are written once and read for each signature.
type SecretStore interface {
	Read(ctx context.Context, name string) ([]byte, error)
	Create(ctx context.Context, tx pgx.Tx, in secrets.NewInput, actor auth.Actor, dryRun bool) (secrets.Secret, []events.Draft, error)
}

// Keyring loads and creates the instance's signing keys. The private seed lives only in the secret store; it never
// appears in an API answer, a job, a log line or an agent context.
type Keyring struct {
	Secrets SecretStore
	// Rand is the entropy source of new keys (crypto/rand when nil).
	Rand io.Reader
}

func (k *Keyring) rand() io.Reader {
	if k.Rand != nil {
		return k.Rand
	}
	return rand.Reader
}

const keyCols = "id, public_key, secret_name, state, created_at, retired_at"

func scanKey(row pgx.CollectableRow) (Key, error) {
	var (
		k   Key
		pub []byte
	)
	err := row.Scan(&k.ID, &pub, &k.SecretName, &k.State, &k.CreatedAt, &k.RetiredAt)
	k.Public = ed25519.PublicKey(pub)
	return k, err
}

// Keys lists the signing keys, public halves only: the current one first, then retired ones, newest first.
func Keys(ctx context.Context, q storage.Querier) ([]Key, error) {
	rows, err := q.Query(ctx, "SELECT "+keyCols+" FROM signing_keys ORDER BY state = 'current' DESC, created_at DESC")
	if err != nil {
		return nil, fmt.Errorf("list signing keys: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanKey)
	if err != nil {
		return nil, fmt.Errorf("list signing keys: %w", err)
	}
	return out, nil
}

// KeyByID reads one key's public half.
func KeyByID(ctx context.Context, q storage.Querier, id string) (Key, error) {
	rows, err := q.Query(ctx, "SELECT "+keyCols+" FROM signing_keys WHERE id = $1", id)
	if err != nil {
		return Key{}, fmt.Errorf("read signing key: %w", err)
	}
	k, err := pgx.CollectExactlyOneRow(rows, scanKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, problems.NotFound.New("no signing key %s", id)
	}
	if err != nil {
		return Key{}, fmt.Errorf("read signing key: %w", err)
	}
	return k, nil
}

// errNoKey is current's answer when the instance has no signing key yet.
var errNoKey = errors.New("this instance has no signing key yet")

func current(ctx context.Context, q storage.Querier) (Key, error) {
	rows, err := q.Query(ctx, "SELECT "+keyCols+" FROM signing_keys WHERE state = 'current'")
	if err != nil {
		return Key{}, fmt.Errorf("read the current signing key: %w", err)
	}
	k, err := pgx.CollectExactlyOneRow(rows, scanKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Key{}, errNoKey
	}
	if err != nil {
		return Key{}, fmt.Errorf("read the current signing key: %w", err)
	}
	return k, nil
}

// Current returns the current key with its private half, read from the secret store. In a transaction that will
// append a record, call it after the chain is locked, so a rotation that committed meanwhile is seen.
func (k *Keyring) Current(ctx context.Context, q storage.Querier) (Key, error) {
	key, err := current(ctx, q)
	if errors.Is(err, errNoKey) {
		return Key{}, problems.Internal.New("this instance has no signing key; the control plane creates one at start (restart it)")
	}
	if err != nil {
		return Key{}, err
	}
	return k.withPrivate(ctx, key)
}

func (k *Keyring) withPrivate(ctx context.Context, key Key) (Key, error) {
	if k == nil || k.Secrets == nil {
		return Key{}, problems.Internal.New("this control plane has no secret store, so it cannot sign promotion records")
	}
	raw, err := k.Secrets.Read(ctx, key.SecretName)
	if err != nil {
		return Key{}, fmt.Errorf("read signing key %s: %w", key.ID, err)
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return Key{}, fmt.Errorf("signing key %s: the sealed seed is not %d base64 bytes", key.ID, ed25519.SeedSize)
	}
	priv := ed25519.NewKeyFromSeed(seed)
	if !priv.Public().(ed25519.PublicKey).Equal(key.Public) {
		return Key{}, fmt.Errorf("signing key %s: the sealed seed does not belong to the recorded public key", key.ID)
	}
	key.Private = priv
	return key, nil
}

// lock takes the transaction-scoped key lock.
func lock(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, 0)", keyLockClass); err != nil {
		return fmt.Errorf("lock signing keys: %w", err)
	}
	return nil
}

// create generates a key, seals its seed under a new secret and records it as current (the caller retires the old
// one first). Secret names: instance-signing-key, then instance-signing-key-2, -3, ….
func (k *Keyring) create(ctx context.Context, tx pgx.Tx, actor auth.Actor, now time.Time) (Key, error) {
	if k == nil || k.Secrets == nil {
		return Key{}, errors.New("no secret store: cannot create a signing key")
	}
	pub, priv, err := ed25519.GenerateKey(k.rand())
	if err != nil {
		return Key{}, fmt.Errorf("generate signing key: %w", err)
	}
	var n int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM signing_keys").Scan(&n); err != nil {
		return Key{}, fmt.Errorf("count signing keys: %w", err)
	}
	name := SecretName
	if n > 0 {
		name = fmt.Sprintf("%s-%d", SecretName, n+1)
	}
	seed := []byte(base64.StdEncoding.EncodeToString(priv.Seed()))
	if _, _, err := k.Secrets.Create(ctx, tx, secrets.NewInput{Name: name, Kind: SecretKind, Scope: secrets.ScopeInstance, Value: seed}, actor, false); err != nil {
		return Key{}, fmt.Errorf("seal signing key: %w", err)
	}
	key := Key{ID: KeyID(pub), Public: pub, Private: priv, SecretName: name, State: KeyCurrent, CreatedAt: now}
	if _, err := tx.Exec(ctx, `INSERT INTO signing_keys (id, public_key, secret_name, state, created_at) VALUES ($1, $2, $3, 'current', $4)`,
		key.ID, []byte(pub), name, now); err != nil {
		return Key{}, fmt.Errorf("record signing key: %w", err)
	}
	return key, nil
}

// Ensure creates the instance signing key when there is none (first start) and reports whether it did.
func (k *Keyring) Ensure(ctx context.Context, pool *pgxpool.Pool, now time.Time) (Key, bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Key{}, false, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := lock(ctx, tx); err != nil {
		return Key{}, false, err
	}
	if key, err := current(ctx, tx); err == nil {
		return key, false, nil
	} else if !errors.Is(err, errNoKey) {
		return Key{}, false, err
	}
	key, err := k.create(ctx, tx, System, now)
	if err != nil {
		return Key{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Key{}, false, fmt.Errorf("commit signing key: %w", err)
	}
	key.Private = nil
	return key, true, nil
}

// System is the actor of records Cadence appends on its own (withdrawals) and of the first key.
var System = auth.Actor{Kind: auth.KindAutomation, ID: "cadence", Name: "Cadence"}

// Rotate replaces the current key (cadence admin rotate-signing-key): every delivery target's chain gets a
// key-rotation record signed by the old key that names the new one, then the old key is retired. Production hosts
// keep refusing records of the new key until a person installs its public key as the new pin.
func (s *Service) Rotate(ctx context.Context, tx pgx.Tx, actor auth.Actor, reason string, now time.Time) (Key, Key, []Record, []events.Draft, error) {
	if err := lock(ctx, tx); err != nil {
		return Key{}, Key{}, nil, nil, err
	}
	old, err := s.Keys.Current(ctx, tx)
	if err != nil {
		return Key{}, Key{}, nil, nil, err
	}
	if _, err := tx.Exec(ctx, "UPDATE signing_keys SET state = 'retired', retired_at = $2 WHERE id = $1", old.ID, now); err != nil {
		return Key{}, Key{}, nil, nil, fmt.Errorf("retire signing key: %w", err)
	}
	next, err := s.Keys.create(ctx, tx, actor, now)
	if err != nil {
		return Key{}, Key{}, nil, nil, err
	}
	rows, err := tx.Query(ctx, "SELECT id FROM deployment_targets WHERE kind = 'delivery' ORDER BY created_at")
	if err != nil {
		return Key{}, Key{}, nil, nil, fmt.Errorf("list delivery targets: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return Key{}, Key{}, nil, nil, fmt.Errorf("list delivery targets: %w", err)
	}
	var (
		recs   []Record
		drafts []events.Draft
	)
	for _, id := range ids {
		rec, d, err := s.append(ctx, tx, AppendInput{
			Kind: KindKeyRotation, TargetID: id, Actor: actor, Reason: reason,
			Body: map[string]any{"newKey": map[string]any{"id": next.ID, "alg": "Ed25519", "publicKeyPem": PublicPEM(next.Public)}},
		}, &old, now)
		if err != nil {
			return Key{}, Key{}, nil, nil, err
		}
		recs, drafts = append(recs, rec), append(drafts, d...)
	}
	old.Private, next.Private = nil, nil
	return old, next, recs, drafts, nil
}
