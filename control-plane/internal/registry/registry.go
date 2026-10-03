// Package registry holds the Cadence-wide registry: collections of immutable versions (base models, dataset
// versions, templates), their adoption by projects and the per-project aliases that point at them
// (docs/spec/02-domain-projects-registry.md "Registry", docs/spec/08-resolutions.md R8, R36).
//
// A version's string is YYYY-MM-DD.<sha>: the registration date and the first 12 hex digits of the sha256 of its
// canonical content, assigned here, never by the caller. Frozen versions never change (a database trigger holds
// that); only aliases move. Registry events carry no projectId; adoption and alias events are project work.
package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Registry kinds (the contract's RegistryKind); each is also the EntityKind of its versions.
const (
	KindBaseModel = "base_model"
	KindDataset   = "dataset_version"
	KindTemplate  = "template"
	// Published by workers at start (phase 2, R40, R41, R45): keyed by the runtime's image digest.
	KindRuntime     = "runtime"
	KindModelFamily = "model_family"
	KindStepKind    = "step_kind"
	// Background noise for augmentation (spec 02 entity Noise bank): a dataset_import with purpose noise.
	KindNoiseBank = "noise_bank"
	// Evaluation (phase 3): golden sets frozen from eval-only dataset versions, scoring normalizers (R21) and model
	// versions registered from checkpoints whose gate passed (R22).
	KindGoldenSet  = "golden_set"
	KindNormalizer = "normalizer"
	KindModel      = "model"
	// Auxiliary models (phase 4, R26): LID classifiers, pseudo-label members and aligners, by licence
	// (internal/auxiliary checks the payload and gates adoption).
	KindAuxiliary = "auxiliary"
)

// CollectionKind is the EntityKind of collections.
const CollectionKind = "registry_collection"

// Version states.
const (
	StateDraft      = "draft"
	StateFrozen     = "frozen"
	StateDeprecated = "deprecated"
)

// kinds maps each kind to the prefix of its collection names and the noun used in messages.
var kinds = map[string]struct{ prefix, noun string }{
	KindBaseModel:   {"base-model/", "base model"},
	KindDataset:     {"dataset/", "dataset"},
	KindTemplate:    {"template/", "template"},
	KindRuntime:     {"runtime/", "runtime"},
	KindModelFamily: {"model-family/", "model family"},
	KindStepKind:    {"step-kind/", "step kind"},
	KindNoiseBank:   {"noise-bank/", "noise bank"},
	KindGoldenSet:   {"golden-set/", "golden set"},
	KindNormalizer:  {"normalizer/", "normalizer"},
	KindModel:       {"model/", "model"},
	KindAuxiliary:   {"auxiliary/", "auxiliary model"},
}

// Known reports whether kind is a registry kind.
func Known(kind string) bool { _, ok := kinds[kind]; return ok }

// Noun names a kind in messages ("base model").
func Noun(kind string) string {
	if k, ok := kinds[kind]; ok {
		return k.noun
	}
	return "registry"
}

// Version is one registry version with its collection's name, tags and licence.
type Version struct {
	ID           string
	CollectionID string
	Kind         string
	Name         string
	Version      string
	Fingerprint  string
	State        string
	Tags         []string
	Licence      string
	Payload      json.RawMessage
	Actor        auth.Actor
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Summary is the compact form of a version carried in events and collection listings.
type Summary struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind,omitempty"`
	Name      string    `json:"name,omitempty"`
	Version   string    `json:"version"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"createdAt"`
}

// Summary returns v's compact form.
func (v Version) Summary() Summary {
	return Summary{ID: v.ID, Kind: v.Kind, Name: v.Name, Version: v.Version, State: v.State, CreatedAt: v.CreatedAt}
}

const versionSelect = `SELECT v.id, v.collection_id, c.kind, c.name, v.version, v.fingerprint, v.state, c.tags, c.licence,
	v.payload, v.created_by, v.created_at, coalesce(v.deprecated_at, v.frozen_at, v.created_at)
	FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id`

func scanVersion(row pgx.CollectableRow) (Version, error) {
	var v Version
	err := row.Scan(&v.ID, &v.CollectionID, &v.Kind, &v.Name, &v.Version, &v.Fingerprint, &v.State, &v.Tags, &v.Licence,
		&v.Payload, &v.Actor, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// Filter narrows a version listing; zero fields do not filter.
type Filter struct {
	Kind         string
	Collection   string // id (reg_…) or name
	State        string
	Fingerprint  string
	TemplateKind string
	ProjectID    string // only versions this project adopted
	IDs          []string
	Text         []string // every term must match the collection's name or description
	Tags         []string // every tag must be on the collection
	Locales      []string // a locale:<x> tag must match each (he-IL matches locale:he and the other way round)
	Limit        int
}

func (f Filter) where() (string, []any) {
	var (
		conds []string
		args  []any
	)
	arg := func(v any) string { args = append(args, v); return fmt.Sprintf("$%d", len(args)) }
	if f.Kind != "" {
		conds = append(conds, "c.kind = "+arg(f.Kind))
	}
	if f.Collection != "" {
		a := arg(f.Collection)
		conds = append(conds, "(c.id = "+a+" OR c.name = "+a+")")
	}
	if f.State != "" {
		conds = append(conds, "v.state = "+arg(f.State))
	}
	if f.Fingerprint != "" {
		conds = append(conds, "v.fingerprint = "+arg(f.Fingerprint))
	}
	if f.TemplateKind != "" {
		conds = append(conds, "v.payload->>'templateKind' = "+arg(f.TemplateKind))
	}
	if f.ProjectID != "" {
		conds = append(conds, "EXISTS (SELECT 1 FROM adoptions a WHERE a.version_id = v.id AND a.project_id = "+arg(f.ProjectID)+")")
	}
	if f.IDs != nil {
		conds = append(conds, "v.id = ANY("+arg(f.IDs)+")")
	}
	for _, t := range f.Text {
		a := arg("%" + escapeLike(t) + "%")
		conds = append(conds, "(c.name ILIKE "+a+" OR c.description ILIKE "+a+")")
	}
	for _, t := range f.Tags {
		conds = append(conds, arg(t)+" = ANY(c.tags)")
	}
	for _, l := range f.Locales {
		a := arg("locale:" + l)
		conds = append(conds, "EXISTS (SELECT 1 FROM unnest(c.tags) t WHERE t = "+a+" OR t LIKE "+a+" || '-%' OR "+a+" LIKE t || '-%')")
	}
	if len(conds) == 0 {
		return "true", args
	}
	return strings.Join(conds, " AND "), args
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ListVersions returns versions matching f, newest first.
func ListVersions(ctx context.Context, q storage.Querier, f Filter) ([]Version, error) {
	where, args := f.where()
	sql := versionSelect + " WHERE " + where + " ORDER BY v.created_at DESC, v.id DESC"
	if f.Limit > 0 {
		args = append(args, f.Limit)
		sql += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	rows, err := q.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("list registry versions: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanVersion)
	if err != nil {
		return nil, fmt.Errorf("list registry versions: %w", err)
	}
	return out, nil
}

// KindCount is how many versions of one kind match a search.
type KindCount struct {
	Kind  string
	Count int
}

// CountKinds counts the versions matching f per kind (f.Kind and f.Limit are ignored, so every kind shows).
func CountKinds(ctx context.Context, q storage.Querier, f Filter) ([]KindCount, error) {
	f.Kind, f.Limit = "", 0
	where, args := f.where()
	rows, err := q.Query(ctx, `SELECT c.kind, count(*) FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE `+where+` GROUP BY c.kind ORDER BY c.kind`, args...)
	if err != nil {
		return nil, fmt.Errorf("count registry kinds: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (KindCount, error) {
		var k KindCount
		return k, row.Scan(&k.Kind, &k.Count)
	})
	if err != nil {
		return nil, fmt.Errorf("count registry kinds: %w", err)
	}
	return out, nil
}

// GetVersion returns the version with id; kind, when set, must match (a dataset id asked as a base model is not
// found).
func GetVersion(ctx context.Context, q storage.Querier, kind, id string) (Version, error) {
	f := Filter{Kind: kind, IDs: []string{id}}
	list, err := ListVersions(ctx, q, f)
	if err != nil {
		return Version{}, err
	}
	if len(list) == 0 {
		return Version{}, problems.NotFound.New("no %s version %q in the registry", Noun(kind), id)
	}
	return list[0], nil
}

// Latest returns the newest frozen version of the collection with this name (or id); kind, when set, must match.
func Latest(ctx context.Context, q storage.Querier, kind, collection string) (Version, error) {
	list, err := ListVersions(ctx, q, Filter{Kind: kind, Collection: collection, State: StateFrozen, Limit: 1})
	if err != nil {
		return Version{}, err
	}
	if len(list) == 0 {
		return Version{}, problems.NotFound.New("no frozen %s version in collection %q", Noun(kind), collection)
	}
	return list[0], nil
}

// Search parses q (free text plus kind:, tag:, locale:, state: qualifiers) into a filter over base.
func Search(base Filter, q string) Filter {
	f := base
	for _, tok := range strings.Fields(q) {
		key, val, ok := strings.Cut(tok, ":")
		switch {
		case ok && val != "" && key == "kind":
			f.Kind = val
		case ok && val != "" && key == "tag":
			f.Tags = append(f.Tags, val)
		case ok && val != "" && key == "locale":
			f.Locales = append(f.Locales, val)
		case ok && val != "" && key == "state":
			f.State = val
		default:
			f.Text = append(f.Text, tok)
		}
	}
	return f
}

// UsedBy is a project that adopted a version, with its aliases pointing at the version.
type UsedBy struct {
	ProjectID   string
	ProjectSlug string
	AdoptedAt   time.Time
	Aliases     []string
}

// UsedByOf returns, per version id, the projects that adopted it.
func UsedByOf(ctx context.Context, q storage.Querier, ids []string) (map[string][]UsedBy, error) {
	rows, err := q.Query(ctx, `SELECT a.version_id, p.id, p.slug, a.adopted_at,
			coalesce(array_agg(al.name ORDER BY al.name) FILTER (WHERE al.name IS NOT NULL), '{}')
		FROM adoptions a JOIN projects p ON p.id = a.project_id
		LEFT JOIN aliases al ON al.project_id = a.project_id AND al.version_id = a.version_id
		WHERE a.version_id = ANY($1) GROUP BY a.version_id, p.id, p.slug, a.adopted_at ORDER BY p.slug`, ids)
	if err != nil {
		return nil, fmt.Errorf("query used-by: %w", err)
	}
	out := map[string][]UsedBy{}
	var (
		vid string
		u   UsedBy
	)
	_, err = pgx.ForEachRow(rows, []any{&vid, &u.ProjectID, &u.ProjectSlug, &u.AdoptedAt, &u.Aliases}, func() error {
		out[vid] = append(out[vid], u)
		u = UsedBy{}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read used-by: %w", err)
	}
	return out, nil
}

// Collection is a named series of versions of one kind.
type Collection struct {
	ID           string
	Kind         string
	Name         string
	Description  string
	Tags         []string
	Licence      string
	CreatedAt    time.Time
	VersionCount int
	Latest       *Summary
	Versions     []Summary // GetCollection only
}

// ListCollections returns collections by name, optionally of one kind and carrying one tag.
func ListCollections(ctx context.Context, q storage.Querier, kind, tag string) ([]Collection, error) {
	return collections(ctx, q, "($1 = '' OR c.kind = $1) AND ($2 = '' OR $2 = ANY(c.tags))", kind, tag)
}

// GetCollection returns the collection with this id or name and its versions, newest first.
func GetCollection(ctx context.Context, q storage.Querier, idOrName string) (Collection, error) {
	list, err := collections(ctx, q, "(c.id = $1 OR c.name = $1)", idOrName)
	if err != nil {
		return Collection{}, err
	}
	if len(list) == 0 {
		return Collection{}, problems.NotFound.New("no registry collection %q", idOrName)
	}
	c := list[0]
	versions, err := ListVersions(ctx, q, Filter{Collection: c.ID})
	if err != nil {
		return Collection{}, err
	}
	c.Versions = make([]Summary, 0, len(versions))
	for _, v := range versions {
		s := v.Summary()
		s.Kind, s.Name = "", ""
		c.Versions = append(c.Versions, s)
	}
	return c, nil
}

func collections(ctx context.Context, q storage.Querier, where string, args ...any) ([]Collection, error) {
	rows, err := q.Query(ctx, `SELECT c.id, c.kind, c.name, c.description, c.tags, c.licence, c.created_at,
			(SELECT count(*) FROM registry_versions v WHERE v.collection_id = c.id), l.id, l.version, l.state, l.created_at
		FROM registry_collections c LEFT JOIN LATERAL (SELECT id, version, state, created_at FROM registry_versions v
			WHERE v.collection_id = c.id ORDER BY created_at DESC, id DESC LIMIT 1) l ON true
		WHERE `+where+` ORDER BY c.name`, args...)
	if err != nil {
		return nil, fmt.Errorf("list registry collections: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Collection, error) {
		var (
			c                 Collection
			lid, lver, lstate *string
			lcreated          *time.Time
		)
		if err := row.Scan(&c.ID, &c.Kind, &c.Name, &c.Description, &c.Tags, &c.Licence, &c.CreatedAt, &c.VersionCount,
			&lid, &lver, &lstate, &lcreated); err != nil {
			return c, err
		}
		if lid != nil {
			c.Latest = &Summary{ID: *lid, Version: *lver, State: *lstate, CreatedAt: *lcreated}
		}
		return c, nil
	})
	if err != nil {
		return nil, fmt.Errorf("list registry collections: %w", err)
	}
	return out, nil
}

// Canonical re-serialises a JSON document with sorted keys and no insignificant whitespace, so equal content has
// equal bytes and an equal fingerprint.
func Canonical(doc []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("canonical JSON: trailing data")
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("canonical JSON: %w", err)
	}
	return out, nil
}

// Fingerprint is the sha256 (hex) of a canonical payload.
func Fingerprint(canonical []byte) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

// VersionString names a version registered at t with fingerprint fp: YYYY-MM-DD.<first 12 hex digits>.
func VersionString(t time.Time, fp string) string {
	return t.UTC().Format(time.DateOnly) + "." + fp[:12]
}
