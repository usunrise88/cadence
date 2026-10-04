package mounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Job kinds: a scan runs in the control plane; a health check runs on a worker (steps.MountCheckJobKind).
const (
	JobScan        = "mounts.scan"
	JobCheck       = steps.MountCheckJobKind
	periodicChecks = "mounts.health"
)

// Service runs scans and health checks.
type Service struct {
	Pool     *pgxpool.Pool
	Jobs     *jobs.Service
	Leases   steps.Leases
	Secrets  Secrets
	HTTP     *http.Client
	Log      *slog.Logger
	Defaults func() *defaults.Defaults
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

// Register adds the scan and health-check job kinds and the periodic health check; call it before the job runner
// starts.
func (s *Service) Register(js *jobs.Service) {
	s.Jobs = js
	d := s.defaults()
	js.Register(JobScan, s.runScan, jobs.KindOptions{Timeout: 6 * time.Hour})
	// The check waits for a worker on the steps queue; its own deadline (storage.mount_check_timeout_minutes) ends it.
	js.Register(JobCheck, s.runCheck, jobs.KindOptions{Queue: jobs.QueueSteps,
		Timeout: time.Duration(d.Storage.MountCheckTimeoutMinutes.Value+5) * time.Minute})
	js.AddPeriodic(periodicChecks, time.Duration(d.Storage.MountCheckHours.Value)*time.Hour, s.checkAll)
}

// ---------------------------------------------------------------- health checks (on a worker)

type checkParams struct {
	Mount      string `json:"mount"`
	URI        string `json:"uri"`
	SampleMB   int    `json:"sample_mb"`
	ProbeWrite bool   `json:"probe_write"`
}

// EnqueueCheck queues a health check of m (mount_check@1 on a worker), unless one is queued or running already
// (that job is answered then). The job id is recorded in the mount's health.
func (s *Service) EnqueueCheck(ctx context.Context, tx pgx.Tx, m Mount) (jobs.Job, []events.Draft, error) {
	if s.Jobs == nil {
		return jobs.Job{}, nil, errors.New("mounts: the job kinds are not registered")
	}
	if m.Health.JobID != "" {
		if j, err := jobs.Get(ctx, tx, m.Health.JobID); err == nil && !jobs.Terminal(j.State) {
			return j, nil, nil
		}
	}
	params, err := json.Marshal(checkParams{Mount: m.Name, URI: m.URIPrefix(),
		SampleMB: s.defaults().Storage.MountCheckSampleMB.Value, ProbeWrite: !m.ReadOnly})
	if err != nil {
		return jobs.Job{}, nil, err
	}
	timeout := float64(s.defaults().Storage.MountCheckTimeoutMinutes.Value * 60)
	spec := steps.Spec{StepID: "mount-check", PipelineRunID: m.ID, Kind: CheckKind, KindVersion: CheckKindVersion,
		Params: params, Inputs: map[string]steps.ArtifactRef{}, Outputs: map[string]string{},
		Resources: steps.Resources{JobKind: steps.JobData}, Priority: 100, EstimateSeconds: &timeout, Attempt: 1}
	j, drafts, err := s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobCheck, Args: spec})
	if err != nil {
		return jobs.Job{}, nil, err
	}
	h := m.Health
	h.JobID = j.ID
	if _, err := tx.Exec(ctx, "UPDATE mounts SET health = $2 WHERE id = $1", m.ID, h); err != nil {
		return jobs.Job{}, nil, fmt.Errorf("record health check job: %w", err)
	}
	return j, drafts, nil
}

// runCheck waits for the worker's mount_check@1 and records the mount's health from its outcome: done with its
// metrics is healthy; a failure (unreachable, not a directory, a write probe refused on a writable mount) is
// unhealthy with the step's message. A check no worker picked up in time leaves the state and says so.
func (s *Service) runCheck(ctx context.Context, r *jobs.Run) (any, error) {
	var spec steps.Spec
	if err := json.Unmarshal(r.Args, &spec); err != nil {
		return nil, fmt.Errorf("read health check args: %w", err)
	}
	if s.Leases == nil {
		return nil, steps.ErrNoWorkerProtocol
	}
	limit := time.Duration(s.defaults().Storage.MountCheckTimeoutMinutes.Value) * time.Minute
	wctx, cancel := context.WithTimeout(ctx, limit)
	out, err := s.Leases.Await(wctx, r.Job.ID)
	cancel()
	now := time.Now().UTC()
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err() // cancelled or stopping: the job's own end says so
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		// Nobody leased it in time: take it out of the queue (a running check still reports when it ends).
		if _, err := s.Pool.Exec(context.WithoutCancel(ctx), `UPDATE step_jobs SET state = 'ended', updated_at = now(),
			outcome = '{"state": "cancelled", "error": {"type": "cancelled", "message": "no worker ran the check in time"}}'
			WHERE job_id = $1 AND state = 'waiting'`, r.Job.ID); err != nil {
			return nil, fmt.Errorf("end the waiting health check: %w", err)
		}
		out = steps.Outcome{State: steps.StateCancelled}
	}
	h, err := s.healthFrom(context.WithoutCancel(ctx), r.Job.ID, out, now, limit)
	if err != nil {
		return nil, err
	}
	bg := context.WithoutCancel(ctx)
	err = pgx.BeginFunc(bg, s.Pool, func(tx pgx.Tx) error {
		m, err := Get(bg, tx, spec.PipelineRunID)
		if err != nil {
			return err
		}
		if out.State == steps.StateCancelled {
			h.State = m.Health.State // nothing was learnt
			h.Reachable, h.FreeBytes, h.TotalBytes, h.ThroughputMBps = m.Health.Reachable, m.Health.FreeBytes, m.Health.TotalBytes, m.Health.ThroughputMBps
			if h.CheckedAt = m.Health.CheckedAt; h.Host == "" {
				h.Host = m.Health.Host
			}
		}
		drafts, err := SetHealth(bg, tx, m.ID, h)
		if err != nil {
			return err
		}
		return events.Append(bg, tx, jobs.System, nil, drafts)
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

func (s *Service) healthFrom(ctx context.Context, jobID string, out steps.Outcome, now time.Time, limit time.Duration) (Health, error) {
	h := Health{JobID: jobID}
	var host string
	err := s.Pool.QueryRow(ctx, `SELECT h.name FROM leases l JOIN compute_hosts h ON h.id = l.host_id
		WHERE l.job_id = $1 ORDER BY l.created_at DESC LIMIT 1`, jobID).Scan(&host)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return h, fmt.Errorf("read the check's host: %w", err)
	}
	h.Host = host
	switch out.State {
	case steps.StateDone:
		h.State, h.CheckedAt = HealthHealthy, &now
		met := out.Metrics
		yes := true
		h.Reachable = &yes
		if v, ok := met["free_bytes"]; ok {
			n := int64(v)
			h.FreeBytes = &n
		}
		if v, ok := met["total_bytes"]; ok {
			n := int64(v)
			h.TotalBytes = &n
		}
		if v, ok := met["throughput_mbps"]; ok {
			h.ThroughputMBps = &v
		}
		if v, ok := met["sampled_bytes"]; ok {
			n := int64(v)
			h.SampledBytes = &n
		}
		if v, ok := met["writable"]; ok {
			w := v > 0
			h.Writable = &w
		}
		h.Detail = "reachable from " + host
	case steps.StateFailed:
		no := false
		h.State, h.CheckedAt, h.Reachable = HealthUnhealthy, &now, &no
		if out.Error != nil {
			h.Detail = out.Error.Message
		}
	default:
		h.Detail = fmt.Sprintf("no worker ran the health check within %s; is a worker with mount_check@1 up?", limit)
	}
	return h, nil
}

// checkAll queues a health check of every mount (the periodic chore, and once at start).
func (s *Service) checkAll(ctx context.Context) error {
	if s.Jobs == nil {
		return nil
	}
	list, err := List(ctx, s.Pool)
	if err != nil {
		return err
	}
	for _, m := range list {
		err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			_, drafts, err := s.EnqueueCheck(ctx, tx, m)
			if err != nil {
				return err
			}
			return events.Append(ctx, tx, jobs.System, nil, drafts)
		})
		if err != nil {
			s.log().WarnContext(ctx, "queue mount health check", "mount", m.Name, "err", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- scans (in the control plane)

type scanArgs struct {
	MountID string `json:"mountId"`
	Path    string `json:"path,omitempty"`
}

// EnqueueScan queues a scan of m (under path, "" for all of it).
func (s *Service) EnqueueScan(ctx context.Context, tx pgx.Tx, m Mount, path string) (jobs.Job, []events.Draft, error) {
	if s.Jobs == nil {
		return jobs.Job{}, nil, errors.New("mounts: the job kinds are not registered")
	}
	if path != "" {
		if err := checkPath(path); err != nil {
			return jobs.Job{}, nil, fmt.Errorf("scan path: %w", err)
		}
	}
	return s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobScan, Args: scanArgs{MountID: m.ID, Path: path}})
}

// blobRe finds content-store blobs laid out as a store or its backup mirror: …/b3/<ab>/<64 hex>.
var blobRe = regexp.MustCompile(`(?:^|/)b3/([0-9a-f]{2})/([0-9a-f]{64})$`)

// entryOf is the inventory entry a file counts under: its first two path segments (<source>/<revision>), or its
// directory for a file one level deep.
func entryOf(rel string) string {
	segs := strings.SplitN(rel, "/", 3)
	switch len(segs) {
	case 1:
		return "."
	case 2:
		return segs[0]
	}
	return segs[0] + "/" + segs[1]
}

// maxEntries bounds the entries an inventory keeps (the largest).
const maxEntries = 500

// Scan walks r under path and returns the inventory and the blob copies it found (hash → path, size).
func Scan(ctx context.Context, r Reader, path string, maxFiles int, progress func(files int64)) (Inventory, map[string]BlobCopy, error) {
	inv := Inventory{Path: path, Entries: []Entry{}}
	entries := map[string]*Entry{}
	blobs := map[string]BlobCopy{}
	err := r.Walk(ctx, path, func(rel string, size int64) error {
		if maxFiles > 0 && inv.Files >= int64(maxFiles) {
			inv.Truncated = true
			return ErrStop
		}
		inv.Files++
		inv.Bytes += size
		k := entryOf(rel)
		e := entries[k]
		if e == nil {
			e = &Entry{Path: k}
			entries[k] = e
		}
		e.Files++
		e.Bytes += size
		if m := blobRe.FindStringSubmatch(rel); m != nil && strings.HasPrefix(m[2], m[1]) {
			blobs["b3:"+m[2]] = BlobCopy{Path: rel, Size: size}
			inv.Blobs++
			inv.BlobBytes += size
		}
		if progress != nil && inv.Files%10000 == 0 {
			progress(inv.Files)
		}
		return nil
	})
	if err != nil {
		return inv, nil, err
	}
	for _, e := range entries {
		inv.Entries = append(inv.Entries, *e)
	}
	sort.Slice(inv.Entries, func(i, j int) bool {
		if inv.Entries[i].Bytes != inv.Entries[j].Bytes {
			return inv.Entries[i].Bytes > inv.Entries[j].Bytes
		}
		return inv.Entries[i].Path < inv.Entries[j].Path
	})
	if len(inv.Entries) > maxEntries {
		inv.Entries = inv.Entries[:maxEntries]
	}
	return inv, blobs, nil
}

// knownSizes reads the size the artifact index records for each blob a scan found (a file of a directory artifact,
// or a file artifact); blobs the index does not know are absent.
func knownSizes(ctx context.Context, tx pgx.Tx, blobs map[string]BlobCopy) (map[string]int64, error) {
	hashes := make([]string, 0, len(blobs))
	for h := range blobs {
		hashes = append(hashes, h)
	}
	out := make(map[string]int64, len(hashes))
	const per = 5000
	for start := 0; start < len(hashes); start += per {
		part := hashes[start:min(start+per, len(hashes))]
		rows, err := tx.Query(ctx, `SELECT DISTINCT ON (h) h, size FROM (
				SELECT file_hash AS h, size FROM artifact_files WHERE file_hash = ANY($1)
				UNION ALL SELECT hash, size FROM artifacts WHERE hash = ANY($1) AND NOT directory) k ORDER BY h`, part)
		if err != nil {
			return nil, fmt.Errorf("read blob sizes: %w", err)
		}
		var (
			h    string
			size int64
		)
		if _, err := pgx.ForEachRow(rows, []any{&h, &size}, func() error {
			out[h] = size
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read blob sizes: %w", err)
		}
	}
	return out, nil
}

// BlobCopy is a content-store blob a scan found on a mount: its path under the root and its size.
type BlobCopy struct {
	Path string
	Size int64
}

// runScan walks the mount from the control plane, records its inventory and the blob copies it found (dropping
// copies under the scanned path it no longer finds), emits mount.scanned and queues a health check.
func (s *Service) runScan(ctx context.Context, r *jobs.Run) (any, error) {
	var args scanArgs
	if err := json.Unmarshal(r.Args, &args); err != nil {
		return nil, fmt.Errorf("read scan args: %w", err)
	}
	m, err := Get(ctx, s.Pool, args.MountID)
	if err != nil {
		return nil, err
	}
	reader, err := Open(ctx, m, s.Secrets, s.HTTP)
	if err != nil {
		return nil, err
	}
	started := time.Now().UTC()
	inv, blobs, err := Scan(ctx, reader, args.Path, s.defaults().Storage.MountScanMaxFiles.Value, func(n int64) {
		_ = r.Progress(ctx, 0, fmt.Sprintf("%d files so far", n))
	})
	if err != nil {
		return nil, fmt.Errorf("scan mount %s: %w", m.Name, err)
	}
	inv.ScannedAt, inv.JobID = time.Now().UTC(), r.Job.ID
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		sizes, err := knownSizes(ctx, tx, blobs)
		if err != nil {
			return err
		}
		for h, c := range blobs {
			if want, known := sizes[h]; known && want != c.Size {
				// A file named like the blob that is not it (truncated, rewritten): no copy, and any earlier record of
				// one at this mount goes. The eviction reads a copy back before it relies on it; this keeps the
				// accounts honest before that.
				inv.BlobsMismatched++
				if _, err := tx.Exec(ctx, "DELETE FROM blob_copies WHERE hash = $1 AND mount_id = $2", h, m.ID); err != nil {
					return fmt.Errorf("drop a mismatched copy: %w", err)
				}
				s.log().WarnContext(ctx, "scan: a file named like a blob has another size", "mount", m.Name, "path", c.Path,
					"size", c.Size, "want", want)
				continue
			}
			if err := RecordCopy(ctx, tx, m.ID, h, c.Path, c.Size); err != nil {
				return err
			}
		}
		if !inv.Truncated {
			like := ""
			if args.Path != "" {
				like = args.Path + "/"
			}
			if _, err := tx.Exec(ctx, `DELETE FROM blob_copies WHERE mount_id = $1 AND seen_at < $2 AND starts_with(path, $3)`,
				m.ID, started, like); err != nil {
				return fmt.Errorf("drop vanished copies: %w", err)
			}
		}
		if _, err := tx.Exec(ctx, "UPDATE mounts SET inventory = $2, updated_at = now() WHERE id = $1", m.ID, inv); err != nil {
			return fmt.Errorf("record inventory: %w", err)
		}
		drafts := []events.Draft{{Topic: Topic(m.ID), Type: EventScanned, Entity: &events.EntityRef{Kind: Kind, ID: m.ID},
			Payload: map[string]any{"id": m.ID, "files": inv.Files, "bytes": inv.Bytes, "blobs": inv.Blobs, "truncated": inv.Truncated}}}
		if _, more, err := s.EnqueueCheck(ctx, tx, m); err == nil {
			drafts = append(drafts, more...)
		} else {
			s.log().WarnContext(ctx, "queue mount health check after a scan", "mount", m.Name, "err", err)
		}
		return events.Append(ctx, tx, r.Job.Actor, nil, drafts)
	})
	if err != nil {
		return nil, err
	}
	return inv, nil
}
