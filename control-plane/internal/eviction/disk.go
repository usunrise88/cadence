package eviction

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/sys/unix"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
)

// The content store's disk: how full its filesystem is, and a warning when little is left. Training states (7.66 GB
// each for the 0.6B model) fill a disk in a day of fine-tunes, and only a person can approve their eviction, so the
// control plane tells that person in time — once a day while the disk stays low — with what an eviction would free.

// Topic and EventLowSpace are the warning's event (a failure-class notification, internal/notify).
const (
	Topic         = "storage"
	EventLowSpace = "storage.low_space"
)

// Disk is the filesystem holding the content store (the contract's StoreDisk).
type Disk struct {
	TotalBytes      int64   `json:"totalBytes"`
	FreeBytes       int64   `json:"freeBytes"`
	LowFreeFraction float64 `json:"lowFreeFraction"`
}

// Low reports whether less than the warning share is free.
func (d Disk) Low() bool {
	return d.TotalBytes > 0 && float64(d.FreeBytes)/float64(d.TotalBytes) < d.LowFreeFraction
}

// DiskOf reads the filesystem under dir; low is cache.store_low_free.
func DiskOf(dir string, low float64) (Disk, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return Disk{}, fmt.Errorf("statfs %s: %w", dir, err)
	}
	// Block counts and sizes are unsigned; a filesystem past 2^63 bytes does not exist, so the sizes are capped there.
	bsize := uint64(st.Bsize) //nolint:unconvert,gosec // the field type differs between platforms; a block size is positive
	bytes := func(blocks uint64) int64 {
		if bsize != 0 && blocks > math.MaxInt64/bsize {
			return math.MaxInt64
		}
		return int64(blocks * bsize) //nolint:gosec // bounded above
	}
	return Disk{TotalBytes: bytes(st.Blocks), FreeBytes: bytes(st.Bavail), LowFreeFraction: low}, nil
}

// Disk reads the content store's filesystem now.
func (s *Service) Disk(d *defaults.Defaults) (*Disk, error) {
	if s.CAS == nil {
		return nil, nil
	}
	disk, err := DiskOf(s.CAS.Root(), d.Cache.StoreLowFree.Value)
	if err != nil {
		return nil, err
	}
	return &disk, nil
}

// LowSpace is the warning's payload.
type LowSpace struct {
	Disk
	// Evictable is what artifacts.evict with no filter would free now (its dry run).
	EvictableArtifacts int   `json:"evictableArtifacts"`
	EvictableBytes     int64 `json:"evictableBytes"`
	Permanent          bool  `json:"permanent"`
}

// Watcher warns when the content store's disk runs low: a periodic job calls Tick.
type Watcher struct {
	Service  *Service
	Defaults func() *defaults.Defaults
	Now      func() time.Time
	// Every is how often a warning may repeat while the disk stays low (24 h when zero).
	Every time.Duration

	mu     sync.Mutex
	warned time.Time
}

// Tick checks the disk once and emits storage.low_space when it is low and no warning went out within Every.
func (w *Watcher) Tick(ctx context.Context) error {
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	every := w.Every
	if every <= 0 {
		every = 24 * time.Hour
	}
	disk, err := w.Service.Disk(w.Defaults())
	if err != nil || disk == nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !disk.Low() {
		w.warned = time.Time{} // recovered: warn at once next time
		return nil
	}
	if !w.warned.IsZero() && now.Sub(w.warned) < every {
		return nil
	}
	err = pgx.BeginFunc(ctx, w.Service.Pool, func(tx pgx.Tx) error {
		p, err := w.Service.Plan(ctx, tx, Filter{})
		if err != nil {
			return err
		}
		payload := LowSpace{Disk: *disk, EvictableArtifacts: len(p.Artifacts), EvictableBytes: p.BytesFreed, Permanent: p.Permanent}
		return events.Append(ctx, tx, jobs.System, nil, []events.Draft{{Topic: Topic, Type: EventLowSpace, Payload: payload}})
	})
	if err != nil {
		return fmt.Errorf("content store low-space warning: %w", err)
	}
	w.warned = now
	w.Service.Log.WarnContext(ctx, "content store low on space", "free_bytes", disk.FreeBytes, "total_bytes", disk.TotalBytes)
	return nil
}
