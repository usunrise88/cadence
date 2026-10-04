//go:build integration

package backups

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
)

// The mirror on a mount (backups.mirror_mount, phase 4 · stream I): blobs go to <root>/cas/b3/… of the writable path
// mount and every mirrored blob is a copy on it (blob_copies), which the cache evicts from and materialises back; the
// restore test re-hashes the mirror there. A read-only mount fails the backup instead of falling back.
func TestMirrorOnAMount(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	root := t.TempDir()
	if _, err := f.pool.Exec(ctx, `INSERT INTO mounts (id, name, kind, root, read_only, created_by) VALUES
			('mnt_exports', 'exports', 'local', $1, false, '{"kind":"user","id":"usr_admin"}'),
			('mnt_ro', 'corpora', 'local', $1, true, '{"kind":"user","id":"usr_admin"}')`, root); err != nil {
		t.Fatal(err)
	}
	f.d.Backups.MirrorMount.Value = "exports"
	mr, err := f.svc.MirrorOf(ctx)
	if err != nil || mr.Dir != filepath.Join(root, "cas") || mr.MountID != "mnt_exports" {
		t.Fatalf("mirror %+v, %v", mr, err)
	}
	if _, err := f.svc.Run(ctx, f.queue(TriggerManual, time.Now())); err != nil {
		t.Fatal(err)
	}
	h := cas.Hash([]byte("first blob"))
	hx := strings.TrimPrefix(h, cas.Prefix)
	if _, err := os.Stat(filepath.Join(root, "cas", "b3", hx[:2], hx)); err != nil {
		t.Fatalf("blob not mirrored on the mount: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.svc.Config.Dir, "cas", "b3", hx[:2], hx)); err == nil {
		t.Error("the blob was mirrored beside the sets too")
	}
	var (
		path string
		n    int
	)
	if err := f.pool.QueryRow(ctx, `SELECT path, (SELECT count(*) FROM blob_copies WHERE mount_id = 'mnt_exports')
		FROM blob_copies WHERE hash = $1 AND mount_id = 'mnt_exports'`, h).Scan(&path, &n); err != nil {
		t.Fatal(err)
	}
	if path != "cas/b3/"+hx[:2]+"/"+hx || n != 2 {
		t.Errorf("copy path %q, %d copies", path, n)
	}
	checked, err := f.svc.checkCAS(ctx)
	if err != nil || checked != 2 {
		t.Errorf("restore check of the mirror: %d, %v", checked, err)
	}
	f.d.Backups.MirrorMount.Value = "corpora"
	if _, err := f.svc.Run(ctx, f.queue(TriggerManual, time.Now())); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("a read-only mirror mount: %v", err)
	}
}
