// Package mounts holds the storage locations Cadence reads audio from and writes exports to (docs/spec/02 "Storage
// and mounts"; docs/review/2026-10-03-phase-4-plan.md decisions 1–3): a local path, an NFS or SMB share the OS
// mounted at a path (one driver), an S3-compatible bucket, or a Hugging Face Hub repository pinned to a revision.
//
// A mount is registry data, registered by an approved mounts.new and never edited afterwards (a new location is a
// new mount). Its name is the authority of every URI on it (uri.go). Workers resolve URIs themselves
// (cadence_worker.mounts) from the mounts their lease carries (LeaseMounts); the control plane reads a mount only to
// scan it (scan.go) and to copy content-store blobs back from it (internal/cache). A worker checks health
// (health.go, step kind mount_check@1); a step job that names an unhealthy mount fails at once (CheckJob).
package mounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind in references and topics.
const Kind = "mount"

// Mount kinds (the contract's MountKind).
const (
	KindLocal = "local"
	KindNFS   = "nfs"
	KindSMB   = "smb"
	KindS3    = "s3"
	KindHF    = "hf"
)

// Kinds lists the mount kinds.
var Kinds = []string{KindLocal, KindNFS, KindSMB, KindS3, KindHF}

// Health states.
const (
	HealthUnknown   = "unknown"
	HealthChecking  = "checking"
	HealthHealthy   = "healthy"
	HealthUnhealthy = "unhealthy"
)

// Event types on mount.{id} (docs/spec/06-platform.md "Topic scheme": mount health).
const (
	EventCreated = "mount.created"
	EventHealth  = "mount.health"
	EventScanned = "mount.scanned"
)

// Operation is the command that registers a mount.
const Operation = "mounts.new"

// Topic is a mount's topic: mount.{id}.
func Topic(id string) string { return "mount." + id }

var (
	nameRe     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	bucketRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	hfRepoRe   = regexp.MustCompile(`^(datasets/|spaces/)?[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	revisionRe = regexp.MustCompile(`^[0-9a-f]{40}$`)
	secretRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,98}$`)
)

// PathKind reports whether kind is read through a path the OS mounted (local, nfs, smb).
func PathKind(kind string) bool { return kind == KindLocal || kind == KindNFS || kind == KindSMB }

// Health is the last health check (the contract's MountHealth).
type Health struct {
	State          string     `json:"state"`
	CheckedAt      *time.Time `json:"checkedAt,omitempty"`
	Host           string     `json:"host,omitempty"`
	Reachable      *bool      `json:"reachable,omitempty"`
	Writable       *bool      `json:"writable,omitempty"`
	FreeBytes      *int64     `json:"freeBytes,omitempty"`
	TotalBytes     *int64     `json:"totalBytes,omitempty"`
	ThroughputMBps *float64   `json:"throughputMBps,omitempty"`
	SampledBytes   *int64     `json:"sampledBytes,omitempty"`
	Detail         string     `json:"detail,omitempty"`
	JobID          string     `json:"jobId,omitempty"`
}

// Entry is a top-level entry of a scan.
type Entry struct {
	Path  string `json:"path"`
	Files int64  `json:"files"`
	Bytes int64  `json:"bytes"`
}

// Inventory is the last scan (the contract's MountInventory).
type Inventory struct {
	ScannedAt time.Time `json:"scannedAt"`
	Path      string    `json:"path,omitempty"`
	Files     int64     `json:"files"`
	Bytes     int64     `json:"bytes"`
	Truncated bool      `json:"truncated,omitempty"`
	Entries   []Entry   `json:"entries"`
	Blobs     int64     `json:"blobs"`
	BlobBytes int64     `json:"blobBytes"`
	// BlobsMismatched are files named like a blob whose size is not the blob's: not recorded as copies.
	BlobsMismatched int64  `json:"blobsMismatched,omitempty"`
	JobID           string `json:"jobId,omitempty"`
}

// Mount is one registered mount.
type Mount struct {
	ID          string
	Name        string
	Kind        string
	Root        string
	Endpoint    string
	Region      string
	Revision    string
	Credentials string // a secret's name
	ReadOnly    bool
	LicenceHint string
	Description string
	Health      Health
	Inventory   *Inventory
	Rev         int
	CreatedBy   auth.Actor
	ApprovalID  string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Utterances  int64
	Copies      int64
	CopyBytes   int64
}

// URIPrefix is mount://<name>/.
func (m Mount) URIPrefix() string { return Scheme + m.Name + "/" }

const mountCols = `m.id, m.name, m.kind, m.root, m.endpoint, m.region, m.revision, m.credentials, m.read_only,
	m.licence_hint, m.description, m.health, m.inventory, m.rev, m.created_by, coalesce(m.approval_id, ''), m.created_at,
	m.updated_at,
	(SELECT count(*) FROM utterance_uris u WHERE u.mount_id = m.id),
	(SELECT count(*) FROM blob_copies b WHERE b.mount_id = m.id),
	coalesce((SELECT sum(b.size) FROM blob_copies b WHERE b.mount_id = m.id), 0)::bigint`

func scan(row pgx.CollectableRow) (Mount, error) {
	var (
		m      Mount
		health []byte
		inv    []byte
	)
	err := row.Scan(&m.ID, &m.Name, &m.Kind, &m.Root, &m.Endpoint, &m.Region, &m.Revision, &m.Credentials, &m.ReadOnly,
		&m.LicenceHint, &m.Description, &health, &inv, &m.Rev, &m.CreatedBy, &m.ApprovalID, &m.CreatedAt, &m.UpdatedAt,
		&m.Utterances, &m.Copies, &m.CopyBytes)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(health, &m.Health); err != nil {
		return m, fmt.Errorf("decode mount health: %w", err)
	}
	if m.Health.State == "" {
		m.Health.State = HealthUnknown
	}
	if len(inv) > 0 {
		m.Inventory = &Inventory{}
		if err := json.Unmarshal(inv, m.Inventory); err != nil {
			return m, fmt.Errorf("decode mount inventory: %w", err)
		}
	}
	return m, nil
}

// List returns every mount by name.
func List(ctx context.Context, q storage.Querier) ([]Mount, error) {
	rows, err := q.Query(ctx, "SELECT "+mountCols+" FROM mounts m ORDER BY m.name")
	if err != nil {
		return nil, fmt.Errorf("list mounts: %w", err)
	}
	out, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, fmt.Errorf("list mounts: %w", err)
	}
	return out, nil
}

// Get reads a mount by id (mnt_…) or name.
func Get(ctx context.Context, q storage.Querier, ref string) (Mount, error) {
	rows, err := q.Query(ctx, "SELECT "+mountCols+" FROM mounts m WHERE m.id = $1 OR m.name = $1", ref)
	if err != nil {
		return Mount{}, fmt.Errorf("read mount: %w", err)
	}
	m, err := pgx.CollectOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mount{}, problems.NotFound.New("no mount %q (mounts.list names them)", ref)
	}
	if err != nil {
		return Mount{}, fmt.Errorf("read mount: %w", err)
	}
	return m, nil
}

// NewInput is a mounts.new request.
type NewInput struct {
	Name, Kind, Root, Endpoint, Region, Revision, Credentials, LicenceHint, Description string
	ReadOnly                                                                            *bool
}

// Validate checks a request and returns the mount as it would be registered (no id yet).
func Validate(ctx context.Context, q storage.Querier, in NewInput) (Mount, error) {
	m := Mount{Name: in.Name, Kind: in.Kind, Root: strings.TrimSpace(in.Root), Endpoint: strings.TrimSpace(in.Endpoint),
		Region: in.Region, Revision: in.Revision, Credentials: in.Credentials, ReadOnly: true,
		LicenceHint: in.LicenceHint, Description: in.Description, Health: Health{State: HealthUnknown}}
	if in.ReadOnly != nil {
		m.ReadOnly = *in.ReadOnly
	}
	var fields []problems.FieldError
	bad := func(p, msg string, args ...any) {
		fields = append(fields, problems.FieldError{Path: p, Message: fmt.Sprintf(msg, args...)})
	}
	if !nameRe.MatchString(m.Name) {
		bad("/name", "lower-case letters, digits and dashes, at most 40, starting and ending with a letter or digit")
	}
	switch m.Kind {
	case KindLocal, KindNFS, KindSMB:
		if !path.IsAbs(m.Root) || path.Clean(m.Root) != m.Root || m.Root == "/" {
			bad("/root", "an absolute, clean path other than / (where workers and the control plane see the share)")
		}
		if m.Endpoint != "" || m.Revision != "" || m.Credentials != "" || m.Region != "" {
			bad("/kind", "a %s mount takes only root (the OS mounts the share; Cadence holds no credentials for it)", m.Kind)
		}
	case KindS3:
		bucket, prefix, _ := strings.Cut(m.Root, "/")
		if !bucketRe.MatchString(bucket) {
			bad("/root", "bucket[/prefix] with a valid bucket name")
		}
		if prefix != "" && (strings.HasSuffix(prefix, "/") || checkPath(prefix) != nil) {
			bad("/root", "the prefix is a clean relative path without a trailing /")
		}
		if !strings.HasPrefix(m.Endpoint, "https://") && !strings.HasPrefix(m.Endpoint, "http://") {
			bad("/endpoint", "an s3 mount needs the endpoint URL (https://host[:port])")
		}
		if m.Credentials == "" {
			bad("/credentials", "an s3 mount needs the name of a secret holding <accessKeyId>:<secretAccessKey>")
		}
		if m.Region == "" {
			m.Region = "us-east-1"
		}
		if m.Revision != "" {
			bad("/revision", "only hf mounts are pinned to a revision")
		}
	case KindHF:
		if !hfRepoRe.MatchString(m.Root) {
			bad("/root", "datasets/<org>/<name> or <org>/<model>")
		}
		if !revisionRe.MatchString(m.Revision) {
			bad("/revision", "an hf mount is pinned to a commit SHA (40 hex digits): reads never move with the branch")
		}
		if !m.ReadOnly {
			bad("/readOnly", "a Hub mount is read-only")
		}
		if m.Endpoint != "" || m.Region != "" {
			bad("/endpoint", "an hf mount takes no endpoint or region")
		}
	default:
		bad("/kind", "one of %s", strings.Join(Kinds, ", "))
	}
	if m.Credentials != "" && !secretRe.MatchString(m.Credentials) {
		bad("/credentials", "a secret's name")
	}
	if len(fields) > 0 {
		return Mount{}, problems.Validation(fields)
	}
	if m.Credentials != "" {
		var exists bool
		if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM secrets WHERE name = $1)", m.Credentials).Scan(&exists); err != nil {
			return Mount{}, fmt.Errorf("look up secret: %w", err)
		}
		if !exists {
			return Mount{}, problems.Validation([]problems.FieldError{{Path: "/credentials",
				Message: fmt.Sprintf("no secret %q (secrets.new stores it; the admin's)", m.Credentials)}})
		}
	}
	var taken bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM mounts WHERE name = $1)", m.Name).Scan(&taken); err != nil {
		return Mount{}, fmt.Errorf("look up mount: %w", err)
	}
	if taken {
		return Mount{}, problems.Conflict.New("a mount named %q exists; mounts are never edited, register the new location under another name", m.Name)
	}
	return m, nil
}

// Create registers a validated mount (the approved replay of mounts.new) and emits mount.created.
func Create(ctx context.Context, tx pgx.Tx, in NewInput, actor auth.Actor, approvalID string, now time.Time) (Mount, []events.Draft, error) {
	m, err := Validate(ctx, tx, in)
	if err != nil {
		return Mount{}, nil, err
	}
	id := "mnt_" + uuid.Must(uuid.NewV7()).String()
	if _, err := tx.Exec(ctx, `INSERT INTO mounts (id, name, kind, root, endpoint, region, revision, credentials, read_only,
			licence_hint, description, created_by, approval_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, NULLIF($13, ''), $14, $14)`,
		id, m.Name, m.Kind, m.Root, m.Endpoint, m.Region, m.Revision, m.Credentials, m.ReadOnly, m.LicenceHint,
		m.Description, actor, approvalID, now); err != nil {
		return Mount{}, nil, fmt.Errorf("register mount: %w", err)
	}
	m, err = Get(ctx, tx, id)
	if err != nil {
		return Mount{}, nil, err
	}
	return m, []events.Draft{{Topic: Topic(id), Type: EventCreated, Entity: &events.EntityRef{Kind: Kind, ID: id, Rev: m.Rev},
		Payload: map[string]any{"id": id, "name": m.Name, "kind": m.Kind, "readOnly": m.ReadOnly}}}, nil
}

// SetHealth records a health check and emits mount.health.
func SetHealth(ctx context.Context, tx pgx.Tx, id string, h Health) ([]events.Draft, error) {
	if h.State == "" {
		h.State = HealthUnknown
	}
	tag, err := tx.Exec(ctx, "UPDATE mounts SET health = $2, updated_at = now() WHERE id = $1", id, h)
	if err != nil {
		return nil, fmt.Errorf("record mount health: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, problems.NotFound.New("no mount %q", id)
	}
	return []events.Draft{{Topic: Topic(id), Type: EventHealth, Entity: &events.EntityRef{Kind: Kind, ID: id},
		Payload: map[string]any{"id": id, "health": h}}}, nil
}

// RecordURIs links an utterance to where its audio also lives (stream D's ingest). Every URI must parse and name a
// registered mount; recording one twice is a no-op.
func RecordURIs(ctx context.Context, tx pgx.Tx, utteranceID string, uris []string) error {
	ids := map[string]string{}
	for _, s := range uris {
		u, err := ParseURI(s)
		if err != nil {
			return problems.ValidationFailed.New("%v", err)
		}
		id, ok := ids[u.Mount]
		if !ok {
			if err := tx.QueryRow(ctx, "SELECT id FROM mounts WHERE name = $1", u.Mount).Scan(&id); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return problems.ValidationFailed.New("%s names no registered mount %q", s, u.Mount)
				}
				return fmt.Errorf("look up mount: %w", err)
			}
			ids[u.Mount] = id
		}
		if _, err := tx.Exec(ctx, `INSERT INTO utterance_uris (utterance_id, uri, mount_id) VALUES ($1, $2, $3)
			ON CONFLICT DO NOTHING`, utteranceID, u.String(), id); err != nil {
			return fmt.Errorf("record utterance uri: %w", err)
		}
	}
	return nil
}

// RecordURIsBatch is RecordURIs for many utterances at once (an ingest's draft: one URI per segment), in one batch.
// uris[i] belongs to utteranceIDs[i]; an empty URI is skipped.
func RecordURIsBatch(ctx context.Context, tx pgx.Tx, utteranceIDs, uris []string) error {
	if len(utteranceIDs) != len(uris) {
		return fmt.Errorf("record utterance uris: %d utterances, %d uris", len(utteranceIDs), len(uris))
	}
	ids := map[string]string{}
	b := &pgx.Batch{}
	for i, s := range uris {
		if s == "" {
			continue
		}
		u, err := ParseURI(s)
		if err != nil {
			return problems.ValidationFailed.New("%v", err)
		}
		id, ok := ids[u.Mount]
		if !ok {
			if err := tx.QueryRow(ctx, "SELECT id FROM mounts WHERE name = $1", u.Mount).Scan(&id); err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					return problems.ValidationFailed.New("%s names no registered mount %q", s, u.Mount)
				}
				return fmt.Errorf("look up mount: %w", err)
			}
			ids[u.Mount] = id
		}
		b.Queue(`INSERT INTO utterance_uris (utterance_id, uri, mount_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			utteranceIDs[i], u.String(), id)
	}
	if b.Len() == 0 {
		return nil
	}
	if err := tx.SendBatch(ctx, b).Close(); err != nil {
		return fmt.Errorf("record utterance uris: %w", err)
	}
	return nil
}

// URIsOf lists the URIs of an utterance, sorted.
func URIsOf(ctx context.Context, q storage.Querier, utteranceID string) ([]string, error) {
	rows, err := q.Query(ctx, "SELECT uri FROM utterance_uris WHERE utterance_id = $1 ORDER BY uri", utteranceID)
	if err != nil {
		return nil, fmt.Errorf("read utterance uris: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read utterance uris: %w", err)
	}
	return out, nil
}

// RecordCopy records that blob hash also lives on mount mountID at rel (relative to the root): an export, a backup
// mirror on a mount, or a scan that found it. The blob may then be evicted from the local cache.
func RecordCopy(ctx context.Context, tx pgx.Tx, mountID, hash, rel string, size int64) error {
	if _, err := tx.Exec(ctx, `INSERT INTO blob_copies (hash, mount_id, path, size, seen_at) VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (hash, mount_id) DO UPDATE SET path = excluded.path, size = excluded.size, seen_at = excluded.seen_at`,
		hash, mountID, rel, size); err != nil {
		return fmt.Errorf("record blob copy: %w", err)
	}
	return nil
}

// Copy is one blob copy on a mount (RecordCopies).
type Copy struct {
	Hash string
	Path string // relative to the mount's root
	Size int64
}

// Batcher sends a batch: a transaction or the pool.
type Batcher interface {
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// RecordCopies is RecordCopy for many blobs on one mount, in batches of a thousand (an export's audio, the backup
// mirror on a mount).
func RecordCopies(ctx context.Context, q Batcher, mountID string, copies []Copy) error {
	const per = 1000
	for start := 0; start < len(copies); start += per {
		b := &pgx.Batch{}
		for _, c := range copies[start:min(len(copies), start+per)] {
			b.Queue(`INSERT INTO blob_copies (hash, mount_id, path, size, seen_at) VALUES ($1, $2, $3, $4, now())
				ON CONFLICT (hash, mount_id) DO UPDATE SET path = excluded.path, size = excluded.size, seen_at = excluded.seen_at`,
				c.Hash, mountID, c.Path, c.Size)
		}
		if err := q.SendBatch(ctx, b).Close(); err != nil {
			return fmt.Errorf("record blob copies: %w", err)
		}
	}
	return nil
}
