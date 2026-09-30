package data

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// MixArtifactType is the artifact type of a rendered mix revision (docs/spec/03 "Artifact types").
const MixArtifactType = "mix"

// maxMixBytes bounds how much of a mix artifact is read to find its dataset versions.
const maxMixBytes = 4 << 20

// TrainableArtifact fails with eval-only-dataset when a step that trains would read ref: a dataset artifact that a
// dataset version registers (payload artifact.hash) which is not Trainable, or a mix artifact that references one.
// A dataset artifact no version registers yet (an import's output before its hook ran elsewhere) passes: nothing
// marks it eval-only. A mix artifact names its dataset versions in meta.datasets or, rendered from a revision, in
// its content's groups[].datasets (ver_ ids); other artifact types pass.
func TrainableArtifact(ctx context.Context, q storage.Querier, store *cas.Store, ref steps.ArtifactRef) error {
	var ids []string
	switch ref.Type {
	case ArtifactType:
		rows, err := q.Query(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
			WHERE c.kind = $1 AND v.payload->'artifact'->>'hash' = $2 ORDER BY v.id`, registry.KindDataset, ref.Hash)
		if err != nil {
			return fmt.Errorf("find the dataset versions of %s: %w", ref.Hash, err)
		}
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return fmt.Errorf("find the dataset versions of %s: %w", ref.Hash, err)
		}
	case MixArtifactType:
		var err error
		if ids, err = mixDatasets(store, ref); err != nil {
			return err
		}
	default:
		return nil
	}
	if len(ids) == 0 {
		return nil
	}
	versions, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindDataset, IDs: ids})
	if err != nil {
		return err
	}
	slices.SortFunc(versions, func(a, b registry.Version) int { return strings.Compare(a.ID, b.ID) })
	for _, v := range versions {
		if err := Trainable(ctx, q, v); err != nil {
			return err
		}
	}
	return nil
}

// mixDatasets reads the dataset version ids a mix artifact references.
func mixDatasets(store *cas.Store, ref steps.ArtifactRef) ([]string, error) {
	var meta struct {
		Datasets []string `json:"datasets"`
	}
	if len(ref.Meta) > 0 && json.Unmarshal(ref.Meta, &meta) == nil && len(meta.Datasets) > 0 {
		return meta.Datasets, nil
	}
	if store == nil {
		return nil, nil
	}
	f, err := store.Open(ref.Hash)
	if err != nil {
		return nil, nil //nolint:nilerr // not in the store: the run's own input check reports it
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxMixBytes))
	if err != nil {
		return nil, fmt.Errorf("read mix artifact %s: %w", ref.Hash, err)
	}
	var doc struct {
		Datasets []string `json:"datasets"`
		Groups   []struct {
			Datasets []string `json:"datasets"`
		} `json:"groups"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil, nil // not a JSON rendering: nothing to resolve
	}
	out := append([]string{}, doc.Datasets...)
	for _, g := range doc.Groups {
		out = append(out, g.Datasets...)
	}
	slices.Sort(out)
	return slices.Compact(out), nil
}
