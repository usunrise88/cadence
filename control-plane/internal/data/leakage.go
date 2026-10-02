package data

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Overlap is what one dataset version shares with another: utterances of VersionID that are also in OtherID, either
// the same utterance (content hash) or another utterance with a fingerprint (kind, value) in common.
type Overlap struct {
	VersionID string
	OtherID   string
	// Utterances counts the distinct utterances of VersionID that overlap; ByFingerprint how many of them overlap
	// only through a fingerprint of a different utterance (an acoustic near-duplicate, phase 4).
	Utterances    int
	ByFingerprint int
}

// The overlap query: utterances of the versions in $1, matched to the memberships of the versions the condition
// selects, by identity and by any shared fingerprint. It starts from the $1 side and reaches the other side only
// through index lookups (dataset_utterances by version, then by utterance; utterance_fingerprints by utterance, then
// by kind and value), so its cost follows the size of the $1 side, not the size of the registry.
const overlapSQL = `WITH a AS (SELECT version_id, utterance_id FROM dataset_utterances WHERE version_id = ANY($1)),
	hits AS (
		SELECT a.version_id AS a_id, m.version_id AS b_id, a.utterance_id, false AS by_fp
		FROM a JOIN dataset_utterances m ON m.utterance_id = a.utterance_id
		WHERE m.version_id <> a.version_id AND {other}
		UNION ALL
		SELECT a.version_id, m.version_id, a.utterance_id, true
		FROM a JOIN utterance_fingerprints fa ON fa.utterance_id = a.utterance_id
		JOIN utterance_fingerprints fb ON fb.kind = fa.kind AND fb.value = fa.value AND fb.utterance_id <> fa.utterance_id
		JOIN dataset_utterances m ON m.utterance_id = fb.utterance_id
		WHERE m.version_id <> a.version_id AND {other})
	SELECT a_id, b_id, count(DISTINCT utterance_id)::int,
		(count(DISTINCT utterance_id) - count(DISTINCT utterance_id) FILTER (WHERE NOT by_fp))::int
	FROM hits GROUP BY a_id, b_id ORDER BY a_id, b_id`

// The other sides the overlap query is asked about, as conditions on the membership m (constant SQL, never built
// from input).
const (
	// otherListed: the versions in $2.
	otherListed = `m.version_id = ANY($2)`
	// otherTrainable: every dataset version not registered eval-only (the freeze check). Versions eval-only only
	// because a source is not cleared yet count: clearing the source would make them trainable.
	otherTrainable = `m.version_id IN (SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = 'dataset_version' AND NOT coalesce((v.payload->>'evalOnly')::boolean, false))`
	// otherGolden: the dataset versions behind a golden set (the training exclusion).
	otherGolden = `m.version_id IN (SELECT dataset_version_id FROM golden_sets)`
)

// overlapQueries holds the overlap query for each other side, built once from constants.
var overlapQueries = map[string]string{
	otherListed:    strings.ReplaceAll(overlapSQL, "{other}", otherListed),
	otherTrainable: strings.ReplaceAll(overlapSQL, "{other}", otherTrainable),
	otherGolden:    strings.ReplaceAll(overlapSQL, "{other}", otherGolden),
}

// GoldenOverlapsQuery is the query behind GoldenOverlaps ($1: the dataset version ids), for EXPLAIN in tests.
func GoldenOverlapsQuery() string { return overlapQueries[otherGolden] }

// TrainableOverlapsQuery is the query behind TrainableOverlaps ($1: the dataset version ids), for EXPLAIN in tests.
func TrainableOverlapsQuery() string { return overlapQueries[otherTrainable] }

// OverlapsQuery is the query behind Overlaps ($1: the dataset version ids, $2: the others), for EXPLAIN in tests.
func OverlapsQuery() string { return overlapQueries[otherListed] }

func overlaps(ctx context.Context, q storage.Querier, ids []string, other string, args ...any) ([]Overlap, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.Query(ctx, overlapQueries[other], append([]any{ids}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("query dataset overlaps: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Overlap, error) {
		var o Overlap
		return o, row.Scan(&o.VersionID, &o.OtherID, &o.Utterances, &o.ByFingerprint)
	})
	if err != nil {
		return nil, fmt.Errorf("read dataset overlaps: %w", err)
	}
	return out, nil
}

// Overlaps returns what each dataset version of ids shares with each version of others (pairs that share nothing
// are left out; a version never overlaps itself).
func Overlaps(ctx context.Context, q storage.Querier, ids, others []string) ([]Overlap, error) {
	if len(others) == 0 {
		return nil, nil
	}
	return overlaps(ctx, q, ids, otherListed, others)
}

// TrainableOverlaps returns what the dataset version id shares with every dataset version not registered eval-only:
// a golden set frozen from id must share nothing with them.
func TrainableOverlaps(ctx context.Context, q storage.Querier, id string) ([]Overlap, error) {
	return overlaps(ctx, q, []string{id}, otherTrainable)
}

// GoldenOverlap is an overlap of a dataset version with the dataset version behind a golden set.
type GoldenOverlap struct {
	Overlap
	GoldenSetID string
}

// GoldenOverlaps returns, for the dataset versions ids, every golden set they share an utterance with (by identity
// or fingerprint). A dataset version two golden sets are frozen from yields a row for each.
func GoldenOverlaps(ctx context.Context, q storage.Querier, ids []string) ([]GoldenOverlap, error) {
	list, err := overlaps(ctx, q, ids, otherGolden)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	backing := make([]string, 0, len(list))
	for _, o := range list {
		backing = append(backing, o.OtherID)
	}
	rows, err := q.Query(ctx, `SELECT dataset_version_id, version_id FROM golden_sets WHERE dataset_version_id = ANY($1)
		ORDER BY version_id`, backing)
	if err != nil {
		return nil, fmt.Errorf("query golden sets: %w", err)
	}
	sets := map[string][]string{}
	var ds, gs string
	if _, err := pgx.ForEachRow(rows, []any{&ds, &gs}, func() error {
		sets[ds] = append(sets[ds], gs)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("read golden sets: %w", err)
	}
	var out []GoldenOverlap
	for _, o := range list {
		for _, g := range sets[o.OtherID] {
			out = append(out, GoldenOverlap{Overlap: o, GoldenSetID: g})
		}
	}
	return out, nil
}

// Leak is one line of a golden-set-leakage problem: a dataset version, the version it overlaps and how much.
type Leak struct {
	DatasetID string // the trainable side
	OtherID   string // the golden side: a golden set version, or the dataset version a freeze would make one
	Overlap
}

// LeakageError renders leaks as golden-set-leakage: detail starts with lead and names the first leak; errors lists
// every one as "<dataset> shares N utterances with <other>" with both versions' names and ids, so a person can
// re-freeze the dataset without them.
func LeakageError(ctx context.Context, q storage.Querier, lead string, leaks []Leak) error {
	ids := make([]string, 0, 2*len(leaks))
	for _, l := range leaks {
		ids = append(ids, l.DatasetID, l.OtherID)
	}
	slices.Sort(ids)
	versions, err := registry.ListVersions(ctx, q, registry.Filter{IDs: slices.Compact(ids)})
	if err != nil {
		return err
	}
	names := make(map[string]string, len(versions))
	for _, v := range versions {
		names[v.ID] = fmt.Sprintf("%s %s (%s)", v.Name, v.Version, v.ID)
	}
	name := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		return id
	}
	pe := problems.GoldenSetLeakage.New("%s", lead)
	for i, l := range leaks {
		msg := fmt.Sprintf("%s shares %d utterance%s with %s", name(l.DatasetID), l.Utterances, plural(l.Utterances), name(l.OtherID))
		if l.ByFingerprint > 0 {
			msg += fmt.Sprintf(" (%d only through a shared fingerprint)", l.ByFingerprint)
		}
		if i == 0 {
			pe.Detail = lead + ": " + msg
			if len(leaks) > 1 {
				pe.Detail += fmt.Sprintf(", and %d more overlap%s (errors)", len(leaks)-1, plural(len(leaks)-1))
			}
		}
		pe.Errors = append(pe.Errors, problems.FieldError{Path: fmt.Sprintf("/overlaps/%d", i), Message: msg})
	}
	return pe
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// NotGolden fails with golden-set-leakage when the dataset version v shares an utterance (by identity or by
// fingerprint) with any golden set: golden-set audio never reaches training (docs/spec/04-blocks.md Block 3).
func NotGolden(ctx context.Context, q storage.Querier, v registry.Version) error {
	list, err := GoldenOverlaps(ctx, q, []string{v.ID})
	if err != nil || len(list) == 0 {
		return err
	}
	leaks := make([]Leak, 0, len(list))
	for _, o := range list {
		leaks = append(leaks, Leak{DatasetID: o.VersionID, OtherID: o.GoldenSetID, Overlap: o.Overlap})
	}
	return LeakageError(ctx, q, fmt.Sprintf("%s %s holds golden-set audio and can never be trained on; re-freeze it without those utterances",
		v.Name, strings.TrimSpace(v.Version)), leaks)
}
