package cache

import (
	"context"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Shard locations datasets.get reports (the contract's DatasetShard.location).
const (
	LocationCAS     = "cas"     // in the content store
	LocationMount   = "mount"   // evicted; a copy on a mount brings it back (datasets.materialize)
	LocationMissing = "missing" // evicted and on no mount: freeze or import the data again
)

// Live is a frozen dataset version's cache state now: the shard state its payload recorded at freeze is history,
// this is what datasets.get overlays on it.
type Live struct {
	// Evicted: the version's artifact is evicted.
	Evicted bool
	// Pinned lists why the cache keeps the version (a queued job, a promoted model, a golden set); empty when it may
	// be evicted.
	Pinned []string
	files  map[string]shard
}

// Location is where the file with hash h (a shard's cuts manifest) lives now.
func (l Live) Location(h string) string {
	if !l.Evicted {
		return LocationCAS
	}
	f, ok := l.files[h]
	switch {
	case ok && f.shared:
		return LocationCAS // another live artifact keeps it
	case ok && f.copied:
		return LocationMount
	}
	return LocationMissing
}

// LiveState reads the cache state of dataset version versionID; false when the version has no content-store
// artifact (a draft or a phase-1 fixture: nothing is cached).
func LiveState(ctx context.Context, q storage.Querier, versionID string) (Live, bool, error) {
	list, err := listDatasets(ctx, q, versionID)
	if err != nil || len(list) == 0 {
		return Live{}, false, err
	}
	d := list[0]
	sh, err := fill(ctx, q, &d)
	if err != nil {
		return Live{}, false, err
	}
	l := Live{Evicted: d.State == StateEvicted, Pinned: d.Pinned, files: make(map[string]shard, len(sh))}
	for _, s := range sh {
		l.files[s.hash] = s
	}
	return l, true, nil
}
