package mounts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Reader reads a mount from the control plane: a scan walks it, a materialisation copies blobs back from it. Paths
// are relative to the mount's root.
type Reader interface {
	// Walk calls fn for every file under prefix ("" for the whole mount) with its path and size, in no fixed order;
	// an error from fn stops the walk and is returned.
	Walk(ctx context.Context, prefix string, fn func(rel string, size int64) error) error
	// Open opens one file.
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
}

// StampedReader is a Reader whose listing also answers a stamp per file that changes when the file does without
// reading it: a path mount's modification time, an S3 object's ETag, a Hub file's blob id. Every reader Open returns
// implements it; a step's mount fingerprint is built from it (Fingerprinter).
type StampedReader interface {
	Reader
	WalkStamped(ctx context.Context, prefix string, fn func(rel string, size int64, stamp string) error) error
}

// ErrStop ends a walk early without an error (Walk returns nil).
var ErrStop = errors.New("stop walking")

// Secrets reads a mount's credentials (secrets.Store).
type Secrets interface {
	Read(ctx context.Context, name string) ([]byte, error)
}

// Open returns the reader of m: a local, NFS or SMB mount reads the path the OS mounted on the control plane (compose
// binds the same directory into the control plane and the workers); s3 and hf mounts read over HTTP with the
// credentials of their secret.
func Open(ctx context.Context, m Mount, sec Secrets, client *http.Client) (Reader, error) {
	if client == nil {
		client = http.DefaultClient
	}
	cred := ""
	if m.Credentials != "" {
		if sec == nil {
			return nil, fmt.Errorf("mount %s needs secret %q and this control plane has no secret store", m.Name, m.Credentials)
		}
		v, err := sec.Read(ctx, m.Credentials)
		if err != nil {
			return nil, fmt.Errorf("mount %s: read secret %q: %w", m.Name, m.Credentials, err)
		}
		cred = strings.TrimSpace(string(v))
	}
	switch {
	case PathKind(m.Kind):
		st, err := os.Stat(m.Root)
		if err != nil {
			return nil, fmt.Errorf("mount %s: the control plane cannot see %s (bind it into the control plane as into the workers): %w", m.Name, m.Root, err)
		}
		if !st.IsDir() {
			return nil, fmt.Errorf("mount %s: %s is not a directory", m.Name, m.Root)
		}
		return pathReader{root: m.Root}, nil
	case m.Kind == KindS3:
		ak, sk, ok := strings.Cut(cred, ":")
		if !ok || ak == "" || sk == "" {
			return nil, fmt.Errorf("mount %s: secret %q must hold <accessKeyId>:<secretAccessKey>", m.Name, m.Credentials)
		}
		bucket, prefix, _ := strings.Cut(m.Root, "/")
		return &s3Reader{endpoint: strings.TrimRight(m.Endpoint, "/"), region: m.Region, bucket: bucket, prefix: prefix,
			key: ak, secret: sk, client: client}, nil
	case m.Kind == KindHF:
		return &hfReader{base: HubEndpoint(), repo: m.Root, revision: m.Revision, token: cred, client: client}, nil
	}
	return nil, fmt.Errorf("mount %s: unknown kind %q", m.Name, m.Kind)
}

// HubEndpoint is the Hugging Face Hub's base URL (HF_ENDPOINT, as huggingface_hub reads it).
func HubEndpoint() string {
	if v := os.Getenv("HF_ENDPOINT"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://huggingface.co"
}

type pathReader struct{ root string }

func (p pathReader) Walk(ctx context.Context, prefix string, fn func(string, int64) error) error {
	return p.WalkStamped(ctx, prefix, func(rel string, size int64, _ string) error { return fn(rel, size) })
}

// WalkStamped implements StampedReader: a file's stamp is its modification time in nanoseconds.
func (p pathReader) WalkStamped(ctx context.Context, prefix string, fn func(string, int64, string) error) error {
	start := p.root
	if prefix != "" {
		if err := checkPath(prefix); err != nil {
			return err
		}
		var err error
		if start, err = InRoot(p.root, prefix); err != nil {
			return err
		}
	}
	err := filepath.WalkDir(start, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if !d.Type().IsRegular() {
			return nil // directories, symlinks, sockets: a scan counts regular files only
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(p.root, full)
		if err != nil {
			return err
		}
		return fn(filepath.ToSlash(rel), info.Size(), strconv.FormatInt(info.ModTime().UnixNano(), 10))
	})
	if errors.Is(err, ErrStop) {
		return nil
	}
	return err
}

func (p pathReader) Open(_ context.Context, rel string) (io.ReadCloser, error) {
	if err := checkPath(rel); err != nil {
		return nil, err
	}
	full, err := InRoot(p.root, rel)
	if err != nil {
		return nil, err
	}
	return os.Open(full) //nolint:gosec // InRoot: a checked relative path that resolves under the root
}

// InRoot joins rel (a clean relative path) to the root of a path mount and checks that the file it resolves to, links
// followed, is still under the root: a symbolic link on the share never leads the control plane to its own data,
// its secrets or the host's files. A path that does not exist (yet) is checked as far as it exists.
func InRoot(root, rel string) (string, error) {
	full := filepath.Join(root, filepath.FromSlash(path.Clean("/"+rel)))
	realRoot, err := filepath.EvalSymlinks(root)
	if err == nil {
		var resolved string
		if resolved, err = filepath.EvalSymlinks(full); err == nil &&
			resolved != realRoot && !strings.HasPrefix(resolved, realRoot+string(filepath.Separator)) {
			return "", problems.Forbidden.New("%s leaves its mount (a link out of %s)", rel, root)
		}
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("resolve %s on its mount: %w", rel, err)
	}
	return full, nil // a path that does not exist has nothing to follow: the open answers not found
}
