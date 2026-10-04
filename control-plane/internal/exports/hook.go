package exports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The export artifact (type export): a directory artifact whose export.json (Manifest, format cadence.export/1)
// says what an export step wrote and where; with target cas the files themselves are in the artifact under files/.
const (
	ManifestFile   = "export.json"
	ManifestFormat = "cadence.export/1"
	maxManifest    = 64 << 20
	sampleFiles    = 20
)

// Manifest is export.json.
type Manifest struct {
	Format       string `json:"format"`
	ExportFormat string `json:"exportFormat"`
	Target       string `json:"target"`
	Version      string `json:"version,omitempty"`
	Utterances   int    `json:"utterances"`
	Files        []File `json:"files"`
	Hub          *Hub   `json:"hub,omitempty"`
}

// Hooker installs the export output hook.
type Hooker struct {
	CAS *cas.Store
	Now func() time.Time
}

// Register installs the hook for artifact type export.
func (h *Hooker) Register(hooks *steps.Hooks) { hooks.On(ArtifactType, h.Hook) }

// Hook completes the export out belongs to (or records one a project pipeline ran on its own): files, bytes, the Hub
// commit, and — for a target on a mount — every file whose hash is a blob of the exported dataset artifact as a copy
// of that blob on the mount (mounts.RecordCopies), so the cache may evict it.
func (h *Hooker) Hook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	m, err := ReadManifest(h.CAS, out.Artifact.Hash)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	dataset := out.Spec.Inputs["dataset"]
	copies := 0
	if strings.HasPrefix(m.Target, mounts.Scheme) && dataset.Hash != "" {
		if copies, err = recordCopies(ctx, tx, m, dataset.Hash); err != nil {
			return nil, err
		}
	}
	var bytes int64
	for _, f := range m.Files {
		bytes += f.Bytes
	}
	sample := m.Files[:min(len(m.Files), sampleFiles)]
	sb, _ := json.Marshal(sample)
	var hub []byte
	if m.Hub != nil {
		hub, _ = json.Marshal(m.Hub)
	}
	versionID, err := versionOf(ctx, tx, m.Version, dataset.Hash)
	if err != nil {
		return nil, err
	}
	actor := auth.Actor{Kind: auth.KindAutomation, ID: out.PipelineRunID, Name: "pipeline run " + out.PipelineRunID}
	id := "dex_" + uuid.Must(uuid.NewV7()).String()
	err = tx.QueryRow(ctx, `INSERT INTO dataset_exports (id, version_id, project_id, format, target, pipeline_run_id, step_id,
			step_kind, artifact, files, bytes, copies, sample, hub, created_by, finished_at)
		VALUES ($1, NULLIF($2, ''), $3, $4, $5, NULLIF($6, ''), $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (pipeline_run_id, step_id) DO UPDATE SET artifact = excluded.artifact, files = excluded.files,
			bytes = excluded.bytes, copies = excluded.copies, sample = excluded.sample, hub = excluded.hub,
			finished_at = excluded.finished_at, rev = dataset_exports.rev + 1
		RETURNING id`, id, versionID, out.ProjectID, m.ExportFormat, m.Target, out.PipelineRunID, out.StepID,
		out.Spec.KindRef(), out.Artifact.Hash, len(m.Files), bytes, copies, sb, hub, actor, now).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("record the export: %w", err)
	}
	x, err := Get(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return []events.Draft{draft(x, EventDone)}, nil
}

// ReadManifest reads and checks export.json of an export artifact.
func ReadManifest(store *cas.Store, hash string) (Manifest, error) {
	if store == nil {
		return Manifest{}, errors.New("export artifact: this control plane has no content store (CADENCE_CAS_DIR)")
	}
	man, err := store.ReadManifest(hash)
	if err != nil {
		return Manifest{}, fmt.Errorf("export artifact %s: %w", hash, err)
	}
	i := slices.IndexFunc(man.Files, func(f cas.File) bool { return f.Path == ManifestFile })
	if i < 0 {
		return Manifest{}, fmt.Errorf("export artifact %s: no %s", hash, ManifestFile)
	}
	r, err := store.Open(man.Files[i].Hash)
	if err != nil {
		return Manifest{}, fmt.Errorf("export artifact: %s: %w", ManifestFile, err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(io.LimitReader(r, maxManifest+1))
	if err != nil {
		return Manifest{}, fmt.Errorf("export artifact: read %s: %w", ManifestFile, err)
	}
	if len(b) > maxManifest {
		return Manifest{}, fmt.Errorf("export artifact: %s is larger than %d bytes", ManifestFile, maxManifest)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Manifest{}, fmt.Errorf("export artifact: %s: %w", ManifestFile, err)
	}
	return m, m.check()
}

func (m Manifest) check() error {
	var bad []string
	if m.Format != ManifestFormat {
		bad = append(bad, fmt.Sprintf("format %q is not %s", m.Format, ManifestFormat))
	}
	if !api.DatasetExportFormat(m.ExportFormat).Valid() {
		bad = append(bad, fmt.Sprintf("exportFormat %q is not an export format (DatasetExportFormat)", m.ExportFormat))
	}
	switch {
	case m.Target == TargetCAS || strings.HasPrefix(m.Target, "hf://datasets/"):
	case strings.HasPrefix(m.Target, mounts.Scheme):
		if _, err := mounts.ParseURI(m.Target); err != nil {
			bad = append(bad, err.Error())
		}
	default:
		bad = append(bad, fmt.Sprintf("target %q is not cas, a mount URI or hf://datasets/<repo>", m.Target))
	}
	for i, f := range m.Files {
		if f.Path == "" || strings.HasPrefix(f.Path, "/") || slices.Contains(strings.Split(f.Path, "/"), "..") {
			bad = append(bad, fmt.Sprintf("files[%d]: path %q must be relative to the target", i, f.Path))
		}
		if f.Hash != "" && !steps.ValidHash(f.Hash) {
			bad = append(bad, fmt.Sprintf("files[%d]: hash %q is not a b3 hash", i, f.Hash))
		}
		if f.Bytes < 0 {
			bad = append(bad, fmt.Sprintf("files[%d]: bytes %d", i, f.Bytes))
		}
		if len(bad) > 10 {
			break
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("export artifact: %s: %s", ManifestFile, strings.Join(bad, "; "))
	}
	return nil
}

// recordCopies records the files of m whose hash is a blob of the dataset artifact (its files or its manifest) as
// copies on m's target mount, and answers how many.
func recordCopies(ctx context.Context, tx pgx.Tx, m Manifest, dataset string) (int, error) {
	u, err := mounts.ParseURI(m.Target)
	if err != nil {
		return 0, err
	}
	mt, err := mounts.Get(ctx, tx, u.Mount)
	if err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `SELECT file_hash FROM artifact_files WHERE hash = $1 UNION SELECT $1::text`, dataset)
	if err != nil {
		return 0, fmt.Errorf("read the dataset's blobs: %w", err)
	}
	hashes, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("read the dataset's blobs: %w", err)
	}
	blobs := make(map[string]bool, len(hashes))
	for _, x := range hashes {
		blobs[x] = true
	}
	var list []mounts.Copy
	seen := map[string]bool{}
	for _, f := range m.Files {
		if f.Hash == "" || !blobs[f.Hash] || seen[f.Hash] {
			continue
		}
		seen[f.Hash] = true
		list = append(list, mounts.Copy{Hash: f.Hash, Path: path.Join(u.Path, f.Path), Size: f.Bytes})
	}
	if err := mounts.RecordCopies(ctx, tx, mt.ID, list); err != nil {
		return 0, err
	}
	return len(list), nil
}

// versionOf is the dataset version an export names, else the frozen version whose artifact is the exported one.
func versionOf(ctx context.Context, tx pgx.Tx, named, artifact string) (string, error) {
	if named != "" {
		if _, err := registry.GetVersion(ctx, tx, registry.KindDataset, named); err == nil {
			return named, nil
		}
	}
	if artifact == "" {
		return "", nil
	}
	var id string
	err := tx.QueryRow(ctx, `SELECT v.id FROM registry_versions v JOIN registry_collections c ON c.id = v.collection_id
		WHERE c.kind = $1 AND v.payload->'artifact'->>'hash' = $2 ORDER BY v.created_at LIMIT 1`, registry.KindDataset, artifact).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("find the exported version: %w", err)
	}
	return id, nil
}
