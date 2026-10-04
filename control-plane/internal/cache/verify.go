package cache

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
)

// An eviction deletes the cache's copy of a blob only once a copy on a mount has been read back and hashes to the
// blob's hash (audit 2026-10-04 M1): blob_copies rows come from scans that match file names, from export manifests
// a worker reports and from the backup mirror, and none of them proves the bytes are still there. Reading the copy
// costs what a materialisation of it would; the eviction job's timeout allows for it.

// verifier checks blob copies on mounts, opening each mount once.
type verifier struct {
	s       *Service
	readers map[string]mounts.Reader
	done    map[string]bool // blob → a copy verified (true) or none could be (false)
	dropped int             // copies that read but held other bytes, removed from blob_copies
}

func (s *Service) newVerifier() *verifier {
	return &verifier{s: s, readers: map[string]mounts.Reader{}, done: map[string]bool{}}
}

// reader opens mount id once per verifier (or per materialisation).
func (s *Service) reader(ctx context.Context, id string, readers map[string]mounts.Reader) (mounts.Reader, error) {
	if rd := readers[id]; rd != nil {
		return rd, nil
	}
	m, err := mounts.Get(ctx, s.Pool, id)
	if err != nil {
		return nil, err
	}
	rd, err := mounts.Open(ctx, m, s.Secrets, s.HTTP)
	if err != nil {
		return nil, err
	}
	readers[id] = rd
	return rd, nil
}

// verify reports whether blob h has a copy on a mount that hashes to h. A copy that reads but hashes otherwise (or
// is missing) is removed from blob_copies, so the version is no longer evictable on its strength; a copy that cannot
// be read now (an unreachable mount) stays recorded and does not count. A blob no longer in the cache (a retried
// eviction deleted it already) needs no copy checked.
func (v *verifier) verify(ctx context.Context, h string) (bool, error) {
	if ok, seen := v.done[h]; seen {
		return ok, nil
	}
	if has, _, err := v.s.CAS.Has(h); err != nil {
		return false, err
	} else if !has {
		v.done[h] = true
		return true, nil
	}
	rows, err := v.s.Pool.Query(ctx, `SELECT b.mount_id, b.path FROM blob_copies b JOIN mounts m ON m.id = b.mount_id
		WHERE b.hash = $1 ORDER BY (m.health->>'state' = 'unhealthy'), m.name`, h)
	if err != nil {
		return false, fmt.Errorf("find copies of %s: %w", h, err)
	}
	type loc struct{ mount, path string }
	locs, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (loc, error) {
		var l loc
		return l, row.Scan(&l.mount, &l.path)
	})
	if err != nil {
		return false, fmt.Errorf("find copies of %s: %w", h, err)
	}
	for _, l := range locs {
		rd, err := v.s.reader(ctx, l.mount, v.readers)
		if err != nil {
			v.s.log().WarnContext(ctx, "eviction: cannot open the mount of a copy", "blob", h, "mount", l.mount, "err", err)
			continue
		}
		got, ok, err := hashCopy(ctx, rd, l.path)
		if err != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			v.s.log().WarnContext(ctx, "eviction: cannot read a copy", "blob", h, "mount", l.mount, "path", l.path, "err", err)
			continue
		}
		if ok && got == h {
			v.done[h] = true
			return true, nil
		}
		// The copy is gone or holds other bytes: it brings nothing back.
		if _, err := v.s.Pool.Exec(ctx, "DELETE FROM blob_copies WHERE hash = $1 AND mount_id = $2 AND path = $3", h, l.mount, l.path); err != nil {
			return false, fmt.Errorf("drop the bad copy of %s: %w", h, err)
		}
		v.dropped++
		v.s.log().WarnContext(ctx, "eviction: a recorded copy does not hold the blob; dropped it", "blob", h, "mount", l.mount,
			"path", l.path, "got", got)
	}
	v.done[h] = false
	return false, nil
}

// hashCopy hashes the file at rel on a mount; ok is false when a path mount says the file does not exist.
func hashCopy(ctx context.Context, rd mounts.Reader, rel string) (string, bool, error) {
	f, err := rd.Open(ctx, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) { // a path mount; elsewhere an unreadable copy is kept, never counted
			return "", false, nil
		}
		return "", false, err
	}
	defer func() { _ = f.Close() }()
	h, _, err := cas.HashReader(f)
	if err != nil {
		return "", false, err
	}
	return h, true, nil
}
