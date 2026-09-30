// Package artifacts indexes the content store (internal/cas): one row per artifact with its type, size, neutral
// metadata and the pipeline step that produced it (docs/spec/08-resolutions.md R15, R42). The bytes stay in the
// store, addressed by hash; this package never writes blobs. Record verifies that a blob exists with the size its
// producer declared before the artifact becomes visible, so a step can never hand on an artifact the store lacks.
//
// A directory artifact (a Shar set, a checkpoint directory) is a cas.Manifest stored as a blob: its hash is the
// manifest's, and its size is the sum of its files' sizes (each file is its own blob and must exist).
package artifacts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind of an artifact in references.
const Kind = "artifact"

// ContentLimit caps the content Content returns inline.
const ContentLimit = 1 << 20

// manifestLimit caps the blob size Record tries to read as a directory manifest.
const manifestLimit = 64 << 20

var typeRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)

// ValidType reports whether t is an artifact type name.
func ValidType(t string) bool { return typeRe.MatchString(t) }

// Producer is the pipeline step whose output recorded an artifact.
type Producer struct {
	PipelineRunID string `json:"pipelineRunId"`
	StepID        string `json:"stepId"`
	Step          string `json:"step"`
	Output        string `json:"output"`
}

// Artifact is one row of the index.
type Artifact struct {
	Hash      string          `json:"hash"`
	Type      string          `json:"type"`
	Size      int64           `json:"size"`
	Directory bool            `json:"directory"`
	Meta      json.RawMessage `json:"meta"`
	ProjectID string          `json:"projectId,omitempty"`
	Producer  *Producer       `json:"producer,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// Ref returns the artifact as a step input.
func (a Artifact) Ref() steps.ArtifactRef {
	return steps.ArtifactRef{Hash: a.Hash, Type: a.Type, Size: a.Size, Meta: a.Meta}
}

// ErrMissing wraps every "the store does not hold what was declared" failure of Record.
var ErrMissing = errors.New("artifact not in the content store")

// Verify checks ref against the store: the hash is well formed, the blob exists, and either its size equals the
// declared size (a file) or it is a manifest whose files all exist and add up to the declared size (a directory).
// It reports whether the artifact is a directory.
func Verify(store *cas.Store, ref steps.ArtifactRef) (bool, error) {
	if !steps.ValidHash(ref.Hash) {
		return false, fmt.Errorf("%q is not an artifact hash (b3:<64 hex>)", ref.Hash)
	}
	if !ValidType(ref.Type) {
		return false, fmt.Errorf("%q is not an artifact type", ref.Type)
	}
	if ref.Size < 0 {
		return false, fmt.Errorf("artifact %s: negative size", ref.Hash)
	}
	if store == nil {
		return false, errors.New("no content store is configured")
	}
	ok, size, err := store.Has(ref.Hash)
	if err != nil {
		return false, fmt.Errorf("look up %s: %w", ref.Hash, err)
	}
	if !ok {
		return false, fmt.Errorf("%w: %s (%s)", ErrMissing, ref.Hash, ref.Type)
	}
	m, isDir := manifestOf(store, ref.Hash, size)
	if isDir {
		var total int64
		for _, f := range m.Files {
			has, fsize, err := store.Has(f.Hash)
			if err != nil {
				return false, fmt.Errorf("look up %s: %w", f.Hash, err)
			}
			if !has {
				return false, fmt.Errorf("%w: file %s (%s) of directory %s", ErrMissing, f.Path, f.Hash, ref.Hash)
			}
			if fsize != f.Size {
				return false, fmt.Errorf("%w: file %s of directory %s has %d bytes, its manifest says %d", ErrMissing, f.Path, ref.Hash, fsize, f.Size)
			}
			total += f.Size
		}
		if ref.Size == total || ref.Size == size {
			return true, nil
		}
		return false, fmt.Errorf("%w: directory %s holds %d bytes, declared %d", ErrMissing, ref.Hash, total, ref.Size)
	}
	if size != ref.Size {
		return false, fmt.Errorf("%w: %s has %d bytes, declared %d", ErrMissing, ref.Hash, size, ref.Size)
	}
	return false, nil
}

// manifestOf reads the blob as a directory manifest when it plausibly is one.
func manifestOf(store *cas.Store, hash string, size int64) (cas.Manifest, bool) {
	if size < 2 || size > manifestLimit {
		return cas.Manifest{}, false
	}
	f, err := store.Open(hash)
	if err != nil {
		return cas.Manifest{}, false
	}
	first := make([]byte, 1)
	_, err = f.Read(first)
	_ = f.Close()
	if err != nil || first[0] != '{' {
		return cas.Manifest{}, false
	}
	m, err := store.ReadManifest(hash)
	if err != nil || len(m.Files) == 0 {
		return cas.Manifest{}, false
	}
	return m, true
}

// Record verifies ref against the store and indexes it for projectID ("" for a registry artifact), with the step
// that produced it (nil for an input a facade put into the store). An artifact already indexed keeps its first
// row; projectID is linked to it either way. The size stored is the directory's total for a directory artifact.
func Record(ctx context.Context, tx pgx.Tx, store *cas.Store, ref steps.ArtifactRef, projectID string, producer *Producer) (Artifact, error) {
	dir, err := Verify(store, ref)
	if err != nil {
		return Artifact{}, err
	}
	size := ref.Size
	if dir {
		m, _ := store.ReadManifest(ref.Hash)
		size = 0
		for _, f := range m.Files {
			size += f.Size
		}
	}
	meta := ref.Meta
	if len(bytes.TrimSpace(meta)) == 0 || string(meta) == "null" {
		meta = json.RawMessage(`{}`)
	}
	var prun, pstepID, pstep, pout *string
	if producer != nil {
		prun, pstepID, pstep, pout = &producer.PipelineRunID, &producer.StepID, &producer.Step, &producer.Output
	}
	if _, err := tx.Exec(ctx, `INSERT INTO artifacts (hash, type, size, directory, meta, project_id, pipeline_run_id, step_id, step, output)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10) ON CONFLICT (hash) DO NOTHING`,
		ref.Hash, ref.Type, size, dir, meta, projectID, prun, pstepID, pstep, pout); err != nil {
		return Artifact{}, fmt.Errorf("record artifact %s: %w", ref.Hash, err)
	}
	if projectID != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO artifact_projects (hash, project_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			ref.Hash, projectID); err != nil {
			return Artifact{}, fmt.Errorf("link artifact %s: %w", ref.Hash, err)
		}
	}
	a, err := Get(ctx, tx, ref.Hash)
	if err != nil {
		return Artifact{}, err
	}
	if a.Type != ref.Type {
		return Artifact{}, fmt.Errorf("artifact %s is recorded as %s, not %s", ref.Hash, a.Type, ref.Type)
	}
	return a, nil
}

const cols = `hash, type, size, directory, meta, coalesce(project_id, ''), pipeline_run_id, step_id, step, output, created_at`

func scan(row pgx.CollectableRow) (Artifact, error) {
	var (
		a                        Artifact
		prun, pstepID, pstep, po *string
	)
	err := row.Scan(&a.Hash, &a.Type, &a.Size, &a.Directory, &a.Meta, &a.ProjectID, &prun, &pstepID, &pstep, &po, &a.CreatedAt)
	if err == nil && prun != nil {
		a.Producer = &Producer{PipelineRunID: *prun, StepID: deref(pstepID), Step: deref(pstep), Output: deref(po)}
	}
	return a, err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Get returns the artifact with hash, or not-found.
func Get(ctx context.Context, q storage.Querier, hash string) (Artifact, error) {
	rows, err := q.Query(ctx, "SELECT "+cols+" FROM artifacts WHERE hash = $1", hash)
	if err != nil {
		return Artifact{}, fmt.Errorf("query artifact: %w", err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Artifact{}, problems.NotFound.New("no artifact %s", hash)
	}
	if err != nil {
		return Artifact{}, fmt.Errorf("read artifact %s: %w", hash, err)
	}
	return a, nil
}

// Projects returns the projects linked to an artifact.
func Projects(ctx context.Context, q storage.Querier, hash string) ([]string, error) {
	rows, err := q.Query(ctx, "SELECT project_id FROM artifact_projects WHERE hash = $1 ORDER BY project_id", hash)
	if err != nil {
		return nil, fmt.Errorf("query artifact projects: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read artifact projects: %w", err)
	}
	return out, nil
}

// Content is an artifact's content as the API serves it inline.
type Content struct {
	Encoding string // utf8 | base64; empty when omitted
	Content  string
	Omitted  string // why the content is not included
}

// ReadContent returns a file artifact's content, or one file of a directory artifact (path), when it is at most
// ContentLimit bytes; otherwise Omitted says why.
func ReadContent(store *cas.Store, a Artifact, path string) (Content, error) {
	if store == nil {
		return Content{Omitted: "no content store is configured"}, nil
	}
	hash, size := a.Hash, a.Size
	if a.Directory {
		if path == "" {
			return Content{Omitted: "a directory artifact: pass path to read one of its files"}, nil
		}
		m, err := store.ReadManifest(a.Hash)
		if err != nil {
			return Content{}, fmt.Errorf("read manifest %s: %w", a.Hash, err)
		}
		found := false
		for _, f := range m.Files {
			if f.Path == path {
				hash, size, found = f.Hash, f.Size, true
				break
			}
		}
		if !found {
			return Content{}, problems.NotFound.New("directory artifact %s has no file %q", a.Hash, path)
		}
	} else if path != "" {
		return Content{}, problems.BadRequest.New("path applies to directory artifacts only; %s is a file", a.Hash)
	}
	if size > ContentLimit {
		return Content{Omitted: fmt.Sprintf("%d bytes is more than the %d a response carries inline", size, ContentLimit)}, nil
	}
	f, err := store.Open(hash)
	if errors.Is(err, cas.ErrNotFound) {
		return Content{Omitted: "the blob is no longer in the content store"}, nil
	}
	if err != nil {
		return Content{}, fmt.Errorf("open %s: %w", hash, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, ContentLimit+1))
	if err != nil {
		return Content{}, fmt.Errorf("read %s: %w", hash, err)
	}
	if utf8.Valid(b) {
		return Content{Encoding: "utf8", Content: string(b)}, nil
	}
	return Content{Encoding: "base64", Content: base64.StdEncoding.EncodeToString(b)}, nil
}

// Files returns a directory artifact's files (nil for a file artifact).
func Files(store *cas.Store, a Artifact) ([]cas.File, error) {
	if !a.Directory || store == nil {
		return nil, nil
	}
	m, err := store.ReadManifest(a.Hash)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", a.Hash, err)
	}
	return m.Files, nil
}
