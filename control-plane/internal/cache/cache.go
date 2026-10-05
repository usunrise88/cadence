package cache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Dataset states in the cache.
const (
	StateCached  = "cached"
	StateEvicted = "evicted"
)

// Service accounts, evicts and materialises the cache.
type Service struct {
	Pool     *pgxpool.Pool
	CAS      *cas.Store
	Jobs     *jobs.Service
	Secrets  mounts.Secrets
	HTTP     *http.Client
	Log      *slog.Logger
	Defaults func() *defaults.Defaults
	// Disk reads the store's filesystem (total, free bytes); nil reads it with statfs.
	Disk func(dir string) (total, free int64, err error)
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Dataset is a dataset version with a content-store artifact, as the cache sees it (the contract's StorageDataset).
type Dataset struct {
	VersionID  string     `json:"versionId"`
	Name       string     `json:"name"`
	Version    string     `json:"version"`
	Artifact   string     `json:"artifact"`
	ProjectID  string     `json:"projectId,omitempty"`
	Bytes      int64      `json:"bytes"`
	State      string     `json:"state"`
	Pinned     []string   `json:"pinned"`
	Copies     int        `json:"copies"`
	Shards     int        `json:"shards"`
	Evictable  bool       `json:"evictable"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	directory  bool
	stranded   int // shards on no mount and in no other live artifact
}

// listDatasets reads dataset versions whose payload names a content-store artifact, least recently used first;
// versionID narrows it to one.
func listDatasets(ctx context.Context, q storage.Querier, versionID string) ([]Dataset, error) {
	rows, err := q.Query(ctx, `SELECT v.id, c.name, v.version, a.hash, coalesce(a.project_id, ''), a.size,
			a.evicted_at IS NOT NULL, coalesce(a.last_used_at, a.created_at), a.directory
		FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		JOIN artifacts a ON a.hash = `+datasetHashSQL+`
		WHERE c.kind = $1 AND ($2 = '' OR v.id = $2)
		ORDER BY coalesce(a.last_used_at, a.created_at), v.id`, registry.KindDataset, versionID)
	if err != nil {
		return nil, fmt.Errorf("list cached datasets: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Dataset, error) {
		var (
			d       Dataset
			evicted bool
			used    time.Time
		)
		err := row.Scan(&d.VersionID, &d.Name, &d.Version, &d.Artifact, &d.ProjectID, &d.Bytes, &evicted, &used, &d.directory)
		d.State = StateCached
		if evicted {
			d.State = StateEvicted
		}
		d.LastUsedAt = &used
		d.Pinned = []string{}
		return d, err
	})
}

// fileSQL lists the shards of artifact $1 with whether each has a copy on a mount and whether another live artifact
// lists it (so it stays whatever happens to this one). A file artifact is its own single shard.
const fileSQL = `WITH f AS (
		SELECT path, file_hash, size FROM artifact_files WHERE hash = $1
		UNION ALL SELECT '', hash, size FROM artifacts WHERE hash = $1 AND NOT directory)
	SELECT f.path, f.file_hash, f.size,
		EXISTS (SELECT 1 FROM blob_copies b WHERE b.hash = f.file_hash),
		EXISTS (SELECT 1 FROM artifact_files g JOIN artifacts o ON o.hash = g.hash
			WHERE g.file_hash = f.file_hash AND g.hash <> $1 AND o.evicted_at IS NULL)
	FROM f ORDER BY f.path`

type shard struct {
	path, hash string
	size       int64
	copied     bool // a copy lives on a mount
	shared     bool // another live artifact lists it
}

func shards(ctx context.Context, q storage.Querier, artifact string) ([]shard, error) {
	rows, err := q.Query(ctx, fileSQL, artifact)
	if err != nil {
		return nil, fmt.Errorf("read shards of %s: %w", artifact, err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (shard, error) {
		var s shard
		err := row.Scan(&s.path, &s.hash, &s.size, &s.copied, &s.shared)
		return s, err
	})
	if err != nil {
		return nil, fmt.Errorf("read shards of %s: %w", artifact, err)
	}
	return out, nil
}

// pins says why a dataset version must stay in the cache: a waiting or leased step job or a running pipeline names
// it (its artifact or its version id), a model version that holds an alias (promoted) was trained on it, or a golden
// set is built on it (evals read it).
func pins(ctx context.Context, q storage.Querier, d Dataset) ([]string, error) {
	out := []string{}
	var id string
	err := q.QueryRow(ctx, `SELECT job_id FROM step_jobs WHERE state IN ('waiting', 'leased')
		AND (artifact_hashes_in(spec) @> ARRAY[$1::text] OR strpos(spec::text, $2) > 0) ORDER BY job_id LIMIT 1`,
		d.Artifact, d.VersionID).Scan(&id)
	switch {
	case err == nil:
		out = append(out, "job "+id+" is queued or running on it")
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("pins: step jobs: %w", err)
	}
	err = q.QueryRow(ctx, `SELECT id FROM pipeline_runs WHERE state = 'running'
		AND (artifact_hashes_in(inputs) @> ARRAY[$1::text] OR strpos(inputs::text, $2) > 0) ORDER BY id LIMIT 1`,
		d.Artifact, d.VersionID).Scan(&id)
	switch {
	case err == nil:
		out = append(out, "pipeline run "+id+" is running on it")
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("pins: pipeline runs: %w", err)
	}
	var alias string
	err = q.QueryRow(ctx, `SELECT mv.id, al.name FROM aliases al JOIN registry_versions mv ON mv.id = al.version_id
		WHERE mv.payload->'lineage'->'datasetVersionIds' ? $1 ORDER BY mv.id LIMIT 1`, d.VersionID).Scan(&id, &alias)
	switch {
	case err == nil:
		out = append(out, "model "+id+" (alias "+alias+") was trained on it")
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("pins: models: %w", err)
	}
	err = q.QueryRow(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = $2 AND v.payload->>'datasetVersionId' = $1 ORDER BY v.id LIMIT 1`, d.VersionID, registry.KindGoldenSet).Scan(&id)
	switch {
	case err == nil:
		out = append(out, "golden set "+id+" is built on it")
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, fmt.Errorf("pins: golden sets: %w", err)
	}
	return out, nil
}

// fill completes d with its shards, copies and pins, and decides whether it is evictable.
func fill(ctx context.Context, q storage.Querier, d *Dataset) ([]shard, error) {
	sh, err := shards(ctx, q, d.Artifact)
	if err != nil {
		return nil, err
	}
	d.Shards = len(sh)
	for _, s := range sh {
		if s.copied {
			d.Copies++
		}
		if !s.copied && !s.shared {
			d.stranded++
		}
	}
	if d.Pinned, err = pins(ctx, q, *d); err != nil {
		return nil, err
	}
	d.Evictable = d.State == StateCached && len(d.Pinned) == 0 && d.stranded == 0 && len(sh) > 0
	return sh, nil
}

// Plan is a datasets.materialize or datasets.evict plan (the contract's DatasetCachePlan).
type Plan struct {
	VersionID  string   `json:"versionId"`
	Artifact   string   `json:"artifact"`
	State      string   `json:"state"`
	Shards     int      `json:"shards"`
	Bytes      int64    `json:"bytes"`
	CopyShards int      `json:"copyShards"`
	CopyBytes  int64    `json:"copyBytes"`
	From       []Source `json:"from"`
	Missing    int      `json:"missing"`
	FreeShards int      `json:"freeShards"`
	FreeBytes  int64    `json:"freeBytes"`
	Pinned     []string `json:"pinned"`
	Blocked    []string `json:"blocked"`
	// copies are the shards to copy back, each with the mounts that hold it (materialize).
	copies []copyJob
	// free are the blobs an eviction deletes.
	free []string
}

// Source is a mount a materialisation copies from (the contract's DatasetCacheSource).
type Source struct {
	Mount  string `json:"mount"`
	Shards int    `json:"shards"`
	Bytes  int64  `json:"bytes"`
}

type copyJob struct {
	hash string
	size int64
}

// one reads a dataset version with a content-store artifact.
func one(ctx context.Context, q storage.Querier, versionID string) (Dataset, error) {
	list, err := listDatasets(ctx, q, versionID)
	if err != nil {
		return Dataset{}, err
	}
	if len(list) == 0 {
		if _, err := registry.GetVersion(ctx, q, registry.KindDataset, versionID); err != nil {
			return Dataset{}, err
		}
		return Dataset{}, problems.ValidationFailed.New("dataset version %s has no content-store artifact (a fixture): nothing is cached", versionID)
	}
	return list[0], nil
}

// PlanEvict plans datasets.evict of one version: the blobs that go (shards with a copy on a mount that no other
// live artifact lists) and what blocks it.
func (s *Service) PlanEvict(ctx context.Context, q storage.Querier, versionID string) (Plan, error) {
	d, err := one(ctx, q, versionID)
	if err != nil {
		return Plan{}, err
	}
	sh, err := fill(ctx, q, &d)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{VersionID: d.VersionID, Artifact: d.Artifact, State: d.State, Shards: len(sh), Bytes: d.Bytes,
		From: []Source{}, Pinned: d.Pinned, Blocked: []string{}}
	switch {
	case d.State == StateEvicted:
		p.Blocked = append(p.Blocked, "already evicted (datasets.materialize brings it back)")
	case len(sh) == 0:
		p.Blocked = append(p.Blocked, "its artifact lists no shards")
	}
	if len(d.Pinned) > 0 {
		p.Blocked = append(p.Blocked, "pinned: "+strings.Join(d.Pinned, "; "))
	}
	if d.stranded > 0 {
		p.Blocked = append(p.Blocked, fmt.Sprintf("%d of %d shards exist on no mount (only the cache holds them; scan a mount that has copies, or export them first)", d.stranded, len(sh)))
	}
	seen := map[string]bool{}
	for _, x := range sh {
		if x.copied && !x.shared && !seen[x.hash] {
			seen[x.hash] = true
			p.FreeShards++
			p.FreeBytes += x.size
			p.free = append(p.free, x.hash)
		}
	}
	return p, nil
}

// PlanMaterialize plans datasets.materialize of one version: the shards missing from the cache and the mounts that
// hold copies of them.
func (s *Service) PlanMaterialize(ctx context.Context, q storage.Querier, versionID string) (Plan, error) {
	return PlanMaterialize(ctx, q, s.CAS, versionID)
}

// PlanMaterialize is Service.PlanMaterialize against store: a pipeline's dry run tells what a training step's evicted
// dataset version would copy back (needs-materialize) with the plan datasets.materialize would answer.
func PlanMaterialize(ctx context.Context, q storage.Querier, store *cas.Store, versionID string) (Plan, error) {
	s := &Service{CAS: store}
	if s.CAS == nil {
		return Plan{}, errors.New("cache: no content store")
	}
	d, err := one(ctx, q, versionID)
	if err != nil {
		return Plan{}, err
	}
	sh, err := fill(ctx, q, &d)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{VersionID: d.VersionID, Artifact: d.Artifact, State: d.State, Shards: len(sh), Bytes: d.Bytes,
		From: []Source{}, Pinned: d.Pinned, Blocked: []string{}}
	need := []shard{}
	if d.directory {
		if ok, _, err := s.CAS.Has(d.Artifact); err != nil {
			return Plan{}, err
		} else if !ok {
			need = append(need, shard{hash: d.Artifact}) // the manifest itself
		}
	}
	seen := map[string]bool{}
	for _, x := range sh {
		if seen[x.hash] {
			continue
		}
		seen[x.hash] = true
		ok, _, err := s.CAS.Has(x.hash)
		if err != nil {
			return Plan{}, err
		}
		if !ok {
			need = append(need, x)
		}
	}
	from := map[string]*Source{}
	for _, x := range need {
		var mount string
		var size int64
		err := q.QueryRow(ctx, `SELECT m.name, b.size FROM blob_copies b JOIN mounts m ON m.id = b.mount_id
			WHERE b.hash = $1 ORDER BY (m.health->>'state' = 'unhealthy'), m.name LIMIT 1`, x.hash).Scan(&mount, &size)
		if errors.Is(err, pgx.ErrNoRows) {
			p.Missing++
			continue
		}
		if err != nil {
			return Plan{}, fmt.Errorf("find copies: %w", err)
		}
		src := from[mount]
		if src == nil {
			src = &Source{Mount: mount}
			from[mount] = src
		}
		src.Shards++
		src.Bytes += size
		p.CopyShards++
		p.CopyBytes += size
		p.copies = append(p.copies, copyJob{hash: x.hash, size: size})
	}
	for _, src := range from {
		p.From = append(p.From, *src)
	}
	sort.Slice(p.From, func(i, j int) bool { return p.From[i].Mount < p.From[j].Mount })
	if p.Missing > 0 {
		p.Blocked = append(p.Blocked, fmt.Sprintf("%d shards are in no cache and on no mount; freeze or import the data again", p.Missing))
	}
	return p, nil
}

// CheckQuota refuses (storage-quota-exceeded) a freeze that would put project projectID's cached dataset bytes past
// storage.project_quota_gb; adding is the size of the shards the freeze writes. Stream D's datasets.freeze calls it.
func CheckQuota(ctx context.Context, q storage.Querier, d *defaults.Defaults, projectID string, adding int64) error {
	if projectID == "" {
		return nil
	}
	used, err := projectBytes(ctx, q, projectID)
	if err != nil {
		return err
	}
	quota := quotaBytes(d)
	if used+adding > quota {
		return problems.StorageQuotaExceeded.New("the project's cached datasets hold %s of its %s quota (storage.project_quota_gb); this freeze adds %s. Evict datasets it no longer trains on (datasets.evict) or ask the admin to raise the quota",
			gb(used), gb(quota), gb(adding))
	}
	return nil
}

func quotaBytes(d *defaults.Defaults) int64 {
	return int64(d.Storage.ProjectQuotaGB.Value * 1e9)
}

func gb(n int64) string { return fmt.Sprintf("%.1f GB", float64(n)/1e9) }

// projectBytes is the size of the cached dataset artifacts project projectID produced (froze or imported).
func projectBytes(ctx context.Context, q storage.Querier, projectID string) (int64, error) {
	var n int64
	err := q.QueryRow(ctx, `SELECT coalesce(sum(a.size), 0)::bigint FROM artifacts a
		WHERE a.type = 'dataset' AND a.evicted_at IS NULL AND a.project_id = $1
		AND EXISTS (SELECT 1 FROM registry_versions v WHERE `+datasetHashSQL+` = a.hash)`, projectID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("project cache bytes: %w", err)
	}
	return n, nil
}
