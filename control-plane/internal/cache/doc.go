// Package cache is the local cache tier (docs/review/2026-10-03-phase-4-plan.md decision 2: the cache is the content
// store): it accounts what the store holds, pins the dataset versions queued or running work and promoted models
// need, evicts unpinned dataset shards least-recently-used between storage.cache_high_water_pct and
// storage.cache_low_water_pct, enforces storage.project_quota_gb on what a project freezes, and copies evicted
// shards back from their mount copies (datasets.materialize).
//
// Eviction never deletes a blob that lives on no mount (imported audio, anything without a blob_copies row): a
// dataset version is evictable only when every file blob of its artifact either has a copy on a mount or is listed
// by another live artifact (it stays then). The evicted artifact keeps its row and manifest, marked evicted like
// artifacts.evict marks one, so lineage resolves and datasets.materialize knows what to copy back.
package cache
