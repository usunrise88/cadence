package backups

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
)

// Mirror is where the content-store mirror lives: blobs at <Dir>/b3/<2 hex>/<64 hex>. On a mount (phase 4 · stream I:
// backups.mirror_mount) MountID names it and every mirrored blob is recorded as a copy at cas/b3/… on that mount, so
// the cache may evict it and datasets.materialize copies it back.
type Mirror struct {
	Dir       string
	MountID   string
	MountName string
}

// mirrorRel is the mirror's directory under a mount's root (the layout a mount scan recognises as blob copies).
const mirrorRel = "cas"

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

// MirrorOf resolves the content-store mirror: <root>/cas of the writable path mount backups.mirror_mount names, else
// CADENCE_BACKUP_DIR/cas. A mount that is missing, read-only or not a path mount is an error (the backup fails and
// says so) rather than a silent fallback.
func (s *Service) MirrorOf(ctx context.Context) (Mirror, error) {
	name := s.defaults().Backups.MirrorMount.Value
	if name == "" || s.Pool == nil {
		return Mirror{Dir: filepath.Join(s.Config.Dir, mirrorRel)}, nil
	}
	m, err := mounts.Get(ctx, s.Pool, name)
	if err != nil {
		return Mirror{}, fmt.Errorf("backups.mirror_mount: %w", err)
	}
	switch {
	case m.ReadOnly:
		return Mirror{}, fmt.Errorf("backups.mirror_mount names mount %s, which is read-only", m.Name)
	case !mounts.PathKind(m.Kind):
		return Mirror{}, fmt.Errorf("backups.mirror_mount names mount %s of kind %s; the mirror is written to a path mount (local, nfs, smb)", m.Name, m.Kind)
	}
	return Mirror{Dir: filepath.Join(m.Root, mirrorRel), MountID: m.ID, MountName: m.Name}, nil
}

// recordMirrored records the blobs a mirror on a mount holds as copies there.
func (s *Service) recordMirrored(ctx context.Context, mr Mirror, blobs []mounts.Copy) error {
	if mr.MountID == "" || len(blobs) == 0 || s.Pool == nil {
		return nil
	}
	return mounts.RecordCopies(ctx, s.Pool, mr.MountID, blobs)
}
