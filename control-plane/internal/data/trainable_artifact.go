package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// MixArtifactType is the artifact type of a rendered mix revision (docs/spec/03 "Artifact types").
const MixArtifactType = "mix"

// MixFormat is the format tag of a rendered mix artifact (runs.RenderMix); training reads only this format.
const MixFormat = "cadence.mix/1"

// maxMixBytes bounds how much of a mix artifact is read to find its dataset versions.
const maxMixBytes = 4 << 20

// TrainableArtifact refuses an artifact a training step would read unless everything it trains on is a registered,
// trainable dataset version. Nothing the caller says about the artifact is trusted: its type and meta come from the
// artifact index (a type other than the indexed one is refused, validation-failed); only an artifact the index does
// not know yet (a facade's freshly rendered mix) is taken at the type sent.
//
//   - dataset: refused when its meta marks an augmented copy of a golden set (golden-set-leakage), when no dataset
//     version registers it (eval-only-dataset: training reads registered versions only), or when a version that
//     registers it is not Trainable (eval-only, uncleared source, golden-set leakage); and when the cache evicted it
//     (artifact-missing, naming datasets.materialize) — so the queue refuses it, not the lease. A plan
//     (TrainableUnlessEvicted) reports it as needs-materialize instead, so a dry run says what to bring back.
//   - mix: its content must be a cadence.mix/1 rendering (what the worker reads); every dataset version it names
//     and every dataset artifact it points at passes the checks above. The meta's datasets list is ignored.
//
// Other types pass.
func TrainableArtifact(ctx context.Context, q storage.Querier, store *cas.Store, ref steps.ArtifactRef) error {
	return trainableArtifact(ctx, q, store, ref, nil)
}

// Evicted is a dataset version a training input reads whose artifact the cache evicted (its shards are on a mount).
type Evicted struct {
	VersionID string
	Artifact  string
}

// TrainableUnlessEvicted is TrainableArtifact for a plan: every check but the cache's. The dataset versions the input
// reads whose artifacts the cache evicted come back instead of the artifact-missing refusal, so a dry run can say what
// to materialize first (needs-materialize); EvictedRefusal is what a real call answers for them.
func TrainableUnlessEvicted(ctx context.Context, q storage.Querier, store *cas.Store, ref steps.ArtifactRef) ([]Evicted, error) {
	ev := []Evicted{}
	if err := trainableArtifact(ctx, q, store, ref, &ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// EvictedRefusal is the artifact-missing refusal for an evicted dataset version: training reads only what the cache
// holds, and the answer names datasets.materialize.
func EvictedRefusal(ctx context.Context, q storage.Querier, e Evicted) error {
	name := e.VersionID
	var collection, version string
	if q.QueryRow(ctx, `SELECT c.name, v.version FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE v.id = $1`, e.VersionID).Scan(&collection, &version) == nil {
		name = collection + " " + version + " (" + e.VersionID + ")"
	}
	return problems.ArtifactMissing.New("dataset %s was evicted from the cache (artifact %s): its shards are on a mount, and training reads only what the cache holds. Bring it back with datasets.materialize (versionId %s), wait for the job, and run again",
		name, e.Artifact, e.VersionID)
}

// trainableArtifact is TrainableArtifact; with ev it collects evicted datasets instead of refusing them.
func trainableArtifact(ctx context.Context, q storage.Querier, store *cas.Store, ref steps.ArtifactRef, ev *[]Evicted) error {
	typ, meta := ref.Type, json.RawMessage(nil)
	a, err := artifacts.Get(ctx, q, ref.Hash)
	switch pe, ok := problems.As(err); {
	case err == nil:
		if a.Type != ref.Type {
			return problems.ValidationFailed.New("artifact %s is a %s in the artifact index, not a %s; send the type the index records (artifacts.get)",
				ref.Hash, a.Type, ref.Type)
		}
		typ, meta = a.Type, a.Meta
	case ok && pe.Type == problems.NotFound:
	default:
		return err
	}
	switch typ {
	case ArtifactType:
		return trainableDataset(ctx, q, ref.Hash, meta, ev)
	case MixArtifactType:
		return trainableMix(ctx, q, store, ref.Hash, ev)
	}
	return nil
}

// trainableDataset checks a dataset artifact (hash, with the meta the index holds) a training step would read.
func trainableDataset(ctx context.Context, q storage.Querier, hash string, meta json.RawMessage, ev *[]Evicted) error {
	if Derived(meta) {
		return problems.GoldenSetLeakage.New("dataset artifact %s is derived (an augmented copy of a golden set made for an eval, purpose %s, or untranscribed segments cut for the pseudo-label members, purpose %s); it can never be trained on",
			hash, PurposeAugmented, PurposePseudoLabel)
	}
	rows, err := q.Query(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = $1 AND (v.payload->'artifact'->>'hash' = $2 OR v.payload->>'artifact' = $2) ORDER BY v.id`, registry.KindDataset, hash)
	if err != nil {
		return fmt.Errorf("find the dataset versions of %s: %w", hash, err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("find the dataset versions of %s: %w", hash, err)
	}
	if len(ids) == 0 {
		return problems.EvalOnlyDataset.New("dataset artifact %s is not a registered dataset version; training reads registered, trainable versions only (import it with pipelines/import, then mix the version)",
			hash)
	}
	if err := trainableVersions(ctx, q, ids); err != nil {
		return err
	}
	evicted, err := isEvicted(ctx, q, hash)
	if err != nil || !evicted {
		return err
	}
	e := Evicted{VersionID: ids[0], Artifact: hash}
	if ev != nil {
		if !slices.Contains(*ev, e) {
			*ev = append(*ev, e)
		}
		return nil
	}
	return EvictedRefusal(ctx, q, e)
}

// isEvicted reports whether the cache evicted dataset artifact hash (datasets.evict, the high-water sweep): its
// shards are on a mount, not in the content store, and a training step would fail mid-lease reading them. A real
// call refuses it naming datasets.materialize; the run is not materialised for the caller (a copy back can take hours
// and counts against the project's quota, so it is a decision of its own).
func isEvicted(ctx context.Context, q storage.Querier, hash string) (bool, error) {
	var evicted bool
	err := q.QueryRow(ctx, "SELECT evicted_at IS NOT NULL FROM artifacts WHERE hash = $1", hash).Scan(&evicted)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read the cache state of %s: %w", hash, err)
	}
	return evicted, nil
}

// trainableVersions checks that every dataset version id exists and is Trainable.
func trainableVersions(ctx context.Context, q storage.Querier, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	versions, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindDataset, IDs: ids})
	if err != nil {
		return err
	}
	slices.SortFunc(versions, func(a, b registry.Version) int { return strings.Compare(a.ID, b.ID) })
	for _, id := range ids {
		if !slices.ContainsFunc(versions, func(v registry.Version) bool { return v.ID == id }) {
			return problems.EvalOnlyDataset.New("the mix names %s, which is not a registered dataset version; training reads registered, trainable versions only", id)
		}
	}
	for _, v := range versions {
		if err := Trainable(ctx, q, v); err != nil {
			return err
		}
	}
	return nil
}

// mixEntries is what a rendered mix artifact says training reads: the dataset version ids and dataset artifacts of
// its input_cfg groups (runs.mixDoc).
type mixEntries struct {
	Format   string `json:"format"`
	InputCfg []struct {
		InputCfg []struct {
			Dataset  string `json:"dataset"`
			Artifact string `json:"artifact"`
		} `json:"input_cfg"`
	} `json:"input_cfg"`
}

// trainableMix reads the mix artifact hash from the store and checks every dataset it names or points at.
func trainableMix(ctx context.Context, q storage.Querier, store *cas.Store, hash string, ev *[]Evicted) error {
	if store == nil {
		return errors.New("data: no content store to read mix artifacts from")
	}
	f, err := store.Open(hash)
	if err != nil {
		return problems.ArtifactMissing.New("mix artifact %s is not in the content store: %v", hash, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxMixBytes))
	if err != nil {
		return fmt.Errorf("read mix artifact %s: %w", hash, err)
	}
	var doc mixEntries
	if json.Unmarshal(b, &doc) != nil || doc.Format != MixFormat {
		return problems.ValidationFailed.New("artifact %s is not a %s mix rendering; training reads a mix that runs.new renders from a mix revision",
			hash, MixFormat)
	}
	var ids, hashes []string
	for _, g := range doc.InputCfg {
		for _, d := range g.InputCfg {
			if d.Dataset != "" {
				ids = append(ids, d.Dataset)
			}
			if d.Artifact != "" {
				hashes = append(hashes, d.Artifact)
			}
		}
	}
	slices.Sort(ids)
	if err := trainableVersions(ctx, q, slices.Compact(ids)); err != nil {
		return err
	}
	slices.Sort(hashes)
	for _, h := range slices.Compact(hashes) {
		var meta json.RawMessage
		if a, err := artifacts.Get(ctx, q, h); err == nil {
			if a.Type != ArtifactType {
				return problems.ValidationFailed.New("the mix points at artifact %s, a %s, not a dataset", h, a.Type)
			}
			meta = a.Meta
		}
		if err := trainableDataset(ctx, q, h, meta, ev); err != nil {
			return err
		}
	}
	return nil
}
