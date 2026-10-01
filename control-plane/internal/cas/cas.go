// Package cas is the content-addressed blob store under the artifact store (docs/spec/08-resolutions.md R15,
// docs/review/2026-09-30-phase-2-plan.md "Worker protocol"). A blob is addressed by "b3:<64 hex>", the BLAKE3-256 of
// its bytes, and lives at <root>/b3/<first two hex>/<64 hex>. Writers stream into <root>/tmp and rename, so a blob
// either exists whole or not at all; blobs are immutable and a second write of the same content is a no-op.
//
// A directory artifact (a Shar set, a checkpoint directory) is a Manifest stored as a blob whose hash is the
// artifact's hash; each file of the directory is its own blob. The control plane and the v1 worker share the root
// by volume; remote workers upload through workerArtifacts.set.
package cas

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lukechampine.com/blake3"

	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Prefix starts every hash.
const Prefix = "b3:"

// ErrNotFound is returned for a hash the store does not hold.
var ErrNotFound = errors.New("blob not found")

// ErrHashMismatch is returned when written bytes do not hash to the expected hash.
var ErrHashMismatch = errors.New("content does not match its hash")

// Store is a blob store rooted at a directory.
type Store struct{ root string }

// New returns a store at root, creating its directories.
func New(root string) (*Store, error) {
	for _, d := range []string{filepath.Join(root, "b3"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("cas: %w", err)
		}
	}
	return &Store{root: root}, nil
}

// Root is the store's directory.
func (s *Store) Root() string { return s.root }

// Hash returns the hash of b.
func Hash(b []byte) string {
	sum := blake3.Sum256(b)
	return Prefix + hex.EncodeToString(sum[:])
}

// HashReader hashes everything r yields.
func HashReader(r io.Reader) (string, int64, error) {
	h := blake3.New(32, nil)
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return Prefix + hex.EncodeToString(h.Sum(nil)), n, nil
}

// Path is where hash lives (whether or not it exists).
func (s *Store) Path(hash string) (string, error) {
	if !steps.ValidHash(hash) {
		return "", fmt.Errorf("cas: %q is not a b3 hash", hash)
	}
	hx := strings.TrimPrefix(hash, Prefix)
	return filepath.Join(s.root, "b3", hx[:2], hx), nil
}

// Has reports whether the store holds hash and its size.
func (s *Store) Has(hash string) (bool, int64, error) {
	p, err := s.Path(hash)
	if err != nil {
		return false, 0, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, err
	}
	return true, fi.Size(), nil
}

// Open opens the blob hash.
func (s *Store) Open(hash string) (*os.File, error) {
	p, err := s.Path(hash)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p) //nolint:gosec // the path is derived from a validated hash
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
	}
	return f, err
}

// Delete removes the blob hash and reports the bytes it held; a blob already gone is not an error (0 bytes).
// Only the eviction job (artifacts.evict, after a person approved it) deletes blobs: everything else treats the
// store as append-only.
func (s *Store) Delete(hash string) (int64, error) {
	p, err := s.Path(hash)
	if err != nil {
		return 0, err
	}
	fi, err := os.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("cas: %w", err)
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("cas: delete %s: %w", hash, err)
	}
	return fi.Size(), nil
}

// Put streams r into the store and returns its hash and size. When want is not empty the content must hash to it
// (ErrHashMismatch otherwise, and nothing is stored).
func (s *Store) Put(r io.Reader, want string) (string, int64, error) {
	if want != "" && !steps.ValidHash(want) {
		return "", 0, fmt.Errorf("cas: %q is not a b3 hash", want)
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "put-*")
	if err != nil {
		return "", 0, fmt.Errorf("cas: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	h := blake3.New(32, nil)
	n, err := io.Copy(io.MultiWriter(tmp, h), r)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", n, fmt.Errorf("cas: write: %w", err)
	}
	got := Prefix + hex.EncodeToString(h.Sum(nil))
	if want != "" && got != want {
		return got, n, fmt.Errorf("%w: got %s, want %s", ErrHashMismatch, got, want)
	}
	dst, _ := s.Path(got)
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", n, fmt.Errorf("cas: %w", err)
	}
	if _, err := os.Stat(dst); err == nil {
		return got, n, nil
	}
	// Read-only for owner and group: the worker shares the volume under the same group.
	if err := os.Chmod(tmp.Name(), 0o440); err != nil { //nolint:gosec // blobs are immutable and shared with the worker by group
		return "", n, fmt.Errorf("cas: %w", err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return "", n, fmt.Errorf("cas: %w", err)
	}
	return got, n, nil
}

// PutBytes stores b.
func (s *Store) PutBytes(b []byte) (string, error) {
	h, _, err := s.Put(strings.NewReader(string(b)), "")
	return h, err
}

// File is one file of a directory artifact.
type File struct {
	Path string `json:"path"` // slash-separated, relative, no ".." segments
	Hash string `json:"hash"`
	Size int64  `json:"size"`
}

// Manifest describes a directory artifact; stored as a blob, its hash is the artifact's hash.
type Manifest struct {
	Files []File `json:"files"`
}

// Encode returns the canonical bytes of m (files sorted by path), so equal directories hash equally.
func (m Manifest) Encode() ([]byte, error) {
	files := append([]File(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		if err := checkRel(f.Path); err != nil {
			return nil, err
		}
		if !steps.ValidHash(f.Hash) {
			return nil, fmt.Errorf("cas: manifest file %q: %q is not a b3 hash", f.Path, f.Hash)
		}
	}
	return json.Marshal(Manifest{Files: files})
}

// PutManifest stores m and returns the directory artifact's hash; every file must already be in the store.
func (s *Store) PutManifest(m Manifest) (string, error) {
	for _, f := range m.Files {
		ok, _, err := s.Has(f.Hash)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("%w: %s (%s)", ErrNotFound, f.Hash, f.Path)
		}
	}
	b, err := m.Encode()
	if err != nil {
		return "", err
	}
	return s.PutBytes(b)
}

// ReadManifest reads the manifest blob hash.
func (s *Store) ReadManifest(hash string) (Manifest, error) {
	f, err := s.Open(hash)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = f.Close() }()
	var m Manifest
	dec := json.NewDecoder(io.LimitReader(f, 64<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("cas: %s is not a manifest: %w", hash, err)
	}
	for _, fl := range m.Files {
		if err := checkRel(fl.Path); err != nil {
			return Manifest{}, err
		}
	}
	return m, nil
}

func checkRel(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("cas: manifest path %q must be relative and slash-separated", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("cas: manifest path %q has an empty, . or .. segment", p)
		}
	}
	return nil
}
