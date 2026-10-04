package registry

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
)

var (
	collectionName = regexp.MustCompile(`^[a-z][a-z-]*/[a-z0-9][a-z0-9._-]{0,98}[a-z0-9]$`)
	fingerprintRe  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidName reports whether name is a collection name for kind: <kind prefix><lowercase name>.
func ValidName(kind, name string) bool {
	k, ok := kinds[kind]
	return ok && strings.HasPrefix(name, k.prefix) && collectionName.MatchString(name)
}

// RegisterInput describes a version to register.
type RegisterInput struct {
	Kind        string
	Name        string // collection name, <kind prefix>/<name>
	Description string // the collection's, used when the collection is new
	Tags        []string
	Licence     string
	Payload     []byte // JSON; stored canonical
	Actor       auth.Actor
	Freeze      bool // register frozen (bundled and fixture versions) instead of as a draft
	// Fingerprint, when set (64 hex digits), replaces the sha256 of the payload as the version's identity: a kind
	// whose content is defined apart from its descriptive payload (a dataset version is its utterances, splits and
	// transcripts, R18) registers once however its payload's lineage differs.
	Fingerprint string
	// On, when set, is the date the version string carries instead of the registration date: an import from a
	// project bundle (internal/bundles) keeps the version string the bundle's instance gave the same content.
	On time.Time
}

func (in RegisterInput) on(now time.Time) time.Time {
	if in.On.IsZero() {
		return now
	}
	return in.On
}

// Register adds a version to a collection, creating the collection on first use. Content that the collection
// already holds is not registered twice: the existing version comes back with created false.
func Register(ctx context.Context, tx pgx.Tx, in RegisterInput, now time.Time) (Version, bool, []events.Draft, error) {
	k, ok := kinds[in.Kind]
	if !ok {
		return Version{}, false, nil, fmt.Errorf("register: unknown registry kind %q", in.Kind)
	}
	if !strings.HasPrefix(in.Name, k.prefix) || !collectionName.MatchString(in.Name) {
		return Version{}, false, nil, fmt.Errorf("register: collection name %q must look like %s<name>", in.Name, k.prefix)
	}
	canonical, err := Canonical(in.Payload)
	if err != nil {
		return Version{}, false, nil, err
	}
	fp := Fingerprint(canonical)
	if in.Fingerprint != "" {
		if !fingerprintRe.MatchString(in.Fingerprint) {
			return Version{}, false, nil, fmt.Errorf("register: fingerprint %q must be 64 lowercase hex digits", in.Fingerprint)
		}
		fp = in.Fingerprint
	}

	collectionID, err := ensureCollection(ctx, tx, in)
	if err != nil {
		return Version{}, false, nil, err
	}
	if v, found, err := byFingerprint(ctx, tx, collectionID, fp); err != nil || found {
		return v, false, nil, err
	}

	id := "ver_" + uuid.Must(uuid.NewV7()).String()
	state, frozenAt := StateDraft, (*time.Time)(nil)
	if in.Freeze {
		state, frozenAt = StateFrozen, &now
	}
	tag, err := tx.Exec(ctx, `INSERT INTO registry_versions (id, collection_id, version, fingerprint, state, payload, created_by,
		created_at, frozen_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (collection_id, fingerprint) DO NOTHING`,
		id, collectionID, VersionString(in.on(now), fp), fp, state, canonical, in.Actor, now, frozenAt)
	if err != nil {
		return Version{}, false, nil, fmt.Errorf("insert registry version: %w", err)
	}
	v, found, err := byFingerprint(ctx, tx, collectionID, fp)
	switch {
	case err != nil:
		return Version{}, false, nil, err
	case !found:
		return Version{}, false, nil, errors.New("register: version vanished after insert")
	case tag.RowsAffected() == 0: // registered concurrently by another start
		return v, false, nil, nil
	}
	return v, true, []events.Draft{VersionEvent(v, v.Kind+".registered")}, nil
}

func byFingerprint(ctx context.Context, tx pgx.Tx, collectionID, fp string) (Version, bool, error) {
	list, err := ListVersions(ctx, tx, Filter{Collection: collectionID, Fingerprint: fp})
	if err != nil || len(list) == 0 {
		return Version{}, false, err
	}
	return list[0], true, nil
}

func ensureCollection(ctx context.Context, tx pgx.Tx, in RegisterInput) (string, error) {
	tags := in.Tags
	if tags == nil {
		tags = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO registry_collections (id, kind, name, description, tags, licence, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (name) DO NOTHING`,
		"reg_"+uuid.Must(uuid.NewV7()).String(), in.Kind, in.Name, in.Description, tags, in.Licence, in.Actor); err != nil {
		return "", fmt.Errorf("insert registry collection: %w", err)
	}
	var id, kind string
	if err := tx.QueryRow(ctx, "SELECT id, kind FROM registry_collections WHERE name = $1", in.Name).Scan(&id, &kind); err != nil {
		return "", fmt.Errorf("read registry collection: %w", err)
	}
	if kind != in.Kind {
		return "", fmt.Errorf("register: collection %q holds %s versions, not %s", in.Name, kind, in.Kind)
	}
	return id, nil
}

// VersionEvent is the registry event of a version: topic entity.<kind>.<id>, no projectId.
func VersionEvent(v Version, typ string) events.Draft {
	return events.Draft{
		Topic:   events.EntityTopic(v.Kind, v.ID),
		Type:    typ,
		Entity:  &events.EntityRef{Kind: v.Kind, ID: v.ID, Rev: 1},
		Payload: map[string]any{"version": v.Summary()},
	}
}
