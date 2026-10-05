package exports

// Project bundles (projects.export; phase 4 tail, ROADMAP "Phase 4 notes"): a whole project as a directory on a
// writable path mount, self-contained so another Cadence instance can make a project from it (projects.new with
// bundle) or adopt its versions (bundles.adopt, internal/bundles):
//
//	bundle.json                       cadence.project-bundle/1 (BundleDoc), written last: its presence marks a whole bundle
//	repository.bundle                 a git bundle, main at the exported commit
//	data.lock                         the repository's data.lock at that commit
//	datasets/<collection>/<version>/  one dataset bundle per dataset version and noise bank (cadence.bundle/1, the layout
//	                                  datasets.export cadence-bundle writes; dataset_import imports one on its own)
//	cas/b3/<ab>/<hex>                 every other blob a version's payload names (checkpoints, cards, template files)
//
// A control-plane job writes it (the repository and the registry live here, not in a worker); the export row keeps
// its own state (migration 0047). The versions are every version the project adopted and, transitively, every version
// an adopted version's payload names; versions published by workers (runtimes, step kinds, model families) travel as
// references only.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/lineage"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Project bundle names.
const (
	FormatProject       = "cadence-project-bundle" // the export format (dataset_exports.format)
	ProjectOperation    = "projects.export"
	JobProject          = "projects.export" // the job kind that writes a bundle
	ProjectBundleFormat = "cadence.project-bundle/1"
	DatasetBundleFormat = "cadence.bundle/1" // a dataset bundle (worker dataset_export / dataset_import)
	BundleFile          = "bundle.json"
	RepositoryFile      = "repository.bundle"
	DataLockFile        = "data.lock"
	DatasetsDir         = "datasets"
	CASDir              = "cas"
)

// Export states a project bundle's row keeps (the other formats read their pipeline run's).
const (
	StateRunning = "running"
	StateDone    = "done"
	StateFailed  = "failed"
)

// ---------------------------------------------------------------- the document

// BundleDoc is a project bundle's bundle.json.
type BundleDoc struct {
	Format     string           `json:"format"`
	CreatedAt  time.Time        `json:"createdAt"`
	Project    BundleProject    `json:"project"`
	Ref        string           `json:"ref"`
	Commit     string           `json:"commit"`
	Repository string           `json:"repository"`
	DataLock   string           `json:"dataLock,omitempty"`
	Versions   []BundleEntry    `json:"versions"`
	Aliases    []BundleAlias    `json:"aliases"`
	Artifacts  []BundleArtifact `json:"artifacts"`
	Blobs      int              `json:"blobs"`
	Bytes      int64            `json:"bytes"`
}

// BundleProject is the exported project's facts.
type BundleProject struct {
	Name        string   `json:"name"`
	Slug        string   `json:"slug"`
	Description string   `json:"description,omitempty"`
	Locales     []string `json:"locales"`
	Domain      string   `json:"domain"`
	BaseModel   string   `json:"baseModel,omitempty"` // the base model's version id in the bundle
}

// BundleEntry is one registry version in a bundle.
type BundleEntry struct {
	Record    Record   `json:"record"`
	Adopted   bool     `json:"adopted"`
	Published bool     `json:"published,omitempty"`
	Dataset   string   `json:"dataset,omitempty"`   // its dataset bundle's directory (dataset versions, noise banks)
	Artifacts []string `json:"artifacts,omitempty"` // hashes of the artifacts its payload names, under cas/ (BundleDoc.Artifacts)
	Blobs     int      `json:"blobs"`
	Bytes     int64    `json:"bytes"`
}

// BundleAlias is one of the exported project's aliases.
type BundleAlias struct {
	Name      string `json:"name"`
	VersionID string `json:"versionId"`
}

// BundleArtifact is an artifact under cas/: its blob (a manifest for a directory) and, for a directory, its files.
type BundleArtifact struct {
	Hash      string          `json:"hash"`
	Type      string          `json:"type"`
	Size      int64           `json:"size"`
	Directory bool            `json:"directory,omitempty"`
	Meta      json.RawMessage `json:"meta,omitempty"`
	Files     []cas.File      `json:"files,omitempty"`
}

// Record is a version's registry record: the fields RenderRecord writes for a dataset bundle, plus the collection's
// description.
type Record struct {
	Format      string          `json:"format"`
	Kind        string          `json:"kind"`
	Collection  string          `json:"collection"`
	Version     string          `json:"version"`
	VersionID   string          `json:"versionId"`
	Licence     string          `json:"licence"`
	Tags        []string        `json:"tags"`
	Fingerprint string          `json:"fingerprint"`
	Payload     json.RawMessage `json:"payload"`
	Sources     []RecordSource  `json:"sources"`
	Description string          `json:"description,omitempty"`
}

// RecordSource is a source in a record.
type RecordSource struct {
	Name            string   `json:"name"`
	Licence         string   `json:"licence"`
	Kind            string   `json:"kind"`
	URL             string   `json:"url,omitempty"`
	Languages       []string `json:"languages"`
	TrainingCleared bool     `json:"trainingCleared"`
}

// DatasetBundleDoc is a dataset bundle's bundle.json (worker cadence_worker/steps/dataset_export.py bundle()).
type DatasetBundleDoc struct {
	Format   string `json:"format"`
	Record   Record `json:"record"`
	Artifact string `json:"artifact"`
	Files    []File `json:"files"`
}

// BlobPath is where a blob lives in a content-store layout: cas/b3/<ab>/<hex>.
func BlobPath(h string) string {
	hex := strings.TrimPrefix(h, cas.Prefix)
	if len(hex) < 2 {
		return path.Join(CASDir, "b3", hex)
	}
	return path.Join(CASDir, "b3", hex[:2], hex)
}

// DatasetDir is a dataset bundle's directory in a project bundle.
func DatasetDir(collection, version string) string {
	return path.Join(DatasetsDir, collection, version)
}

// ---------------------------------------------------------------- the plan

// ProjectRequest is projects.export's body.
type ProjectRequest struct {
	Ref    string
	Target string
}

// ProjectPlan is what projects.export writes (the contract's ProjectExportPlan).
type ProjectPlan struct {
	Project  projects.Project
	Ref      string
	Commit   string
	Target   string
	Mount    mounts.Mount
	Dir      string // the target directory relative to the mount's root
	Versions []PlanVersion
	Aliases  []BundleAlias
	Blobs    int
	Bytes    int64
	Sources  []string
}

// PlanVersion is one version a bundle carries, with the blobs that go with it.
type PlanVersion struct {
	Version     registry.Version
	Description string
	Adopted     bool
	Published   bool
	Path        string // dataset bundle directory
	Dataset     *artifacts.Artifact
	DatasetFile []cas.File
	Artifacts   []artifacts.Artifact // other artifacts its payload names (directories with their files)
	Files       map[string][]cas.File
	Sources     []source
	Blobs       int
	Bytes       int64
}

// published reports whether a kind is published by workers (never adopted, never registered from a bundle).
func published(kind string) bool { return registry.Published(kind) }

// datasetLike reports whether a version is carried as a dataset bundle: a dataset version or a noise bank whose
// payload names a dataset artifact.
func datasetLike(kind string) bool {
	return kind == registry.KindDataset || kind == registry.KindNoiseBank
}

// PlanProject checks a projects.export request and lists what the bundle carries. The project must have a
// repository with ref (default main) resolving to a commit; every dataset artifact must be in the cache.
func PlanProject(ctx context.Context, q storage.Querier, store *cas.Store, rp *repos.Store, d *defaults.Defaults,
	p projects.Project, req ProjectRequest) (ProjectPlan, error) {
	if rp == nil || !rp.Exists(p.Slug) {
		return ProjectPlan{}, problems.Conflict.New("project %s has no repository on this server to bundle", p.Slug)
	}
	ref := req.Ref
	if ref == "" {
		ref = repos.Main
	}
	commit, err := rp.Resolve(ctx, p.Slug, ref)
	switch {
	case errors.Is(err, repos.ErrNotFound):
		return ProjectPlan{}, problems.Validation([]problems.FieldError{{Path: "/ref", Message: fmt.Sprintf("no branch or commit %q in the repository", ref)}})
	case err != nil:
		return ProjectPlan{}, err
	case commit == "":
		return ProjectPlan{}, problems.Conflict.New("project %s has no commit on %s yet", p.Slug, ref)
	}
	pl := ProjectPlan{Project: p, Ref: ref, Commit: commit, Sources: []string{}, Aliases: []BundleAlias{}}
	want := req.Target
	if want == TargetCAS {
		return ProjectPlan{}, problems.Validation([]problems.FieldError{{Path: "/target", Message: "a project bundle is written to a writable path mount, not the content store"}})
	}
	t, err := target(ctx, q, d, want, path.Join("projects", p.Slug, commit[:12]))
	if err != nil {
		return ProjectPlan{}, err
	}
	if t == TargetCAS {
		return ProjectPlan{}, problems.Validation([]problems.FieldError{{Path: "/target",
			Message: fmt.Sprintf("no writable path mount %q (storage.export_mount) to write the bundle to; name target mount://<mount>/<dir>", d.Storage.ExportMount.Value)}})
	}
	u, err := mounts.ParseURI(t)
	if err != nil {
		return ProjectPlan{}, err
	}
	if pl.Mount, err = mounts.Get(ctx, q, u.Mount); err != nil {
		return ProjectPlan{}, err
	}
	pl.Target, pl.Dir = t, strings.Trim(u.Path, "/")
	if dir, err := mounts.InRoot(pl.Mount.Root, pl.Dir); err == nil {
		if _, err := os.Stat(filepath.Join(dir, BundleFile)); err == nil {
			return ProjectPlan{}, problems.Conflict.New("%s already holds a bundle (%s); export to another directory", t, BundleFile)
		}
	}
	if err := pl.versions(ctx, q, store); err != nil {
		return ProjectPlan{}, err
	}
	als, err := registry.ListAliases(ctx, q, p.ID)
	if err != nil {
		return ProjectPlan{}, err
	}
	for _, a := range als {
		pl.Aliases = append(pl.Aliases, BundleAlias{Name: a.Name, VersionID: a.Version.ID})
	}
	return pl, nil
}

// versions collects the adopted versions and, transitively, the versions their payloads name.
func (pl *ProjectPlan) versions(ctx context.Context, q storage.Querier, store *cas.Store) error {
	adopted, err := registry.ListAdoptions(ctx, q, pl.Project.ID, "")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var queue []PlanVersion
	for _, a := range adopted {
		seen[a.Version.ID] = true
		queue = append(queue, PlanVersion{Version: a.Version, Adopted: true})
	}
	if b := pl.Project.BaseModel; b != nil && b.VersionID != "" && !seen[b.VersionID] {
		id := b.VersionID
		v, err := registry.GetVersion(ctx, q, "", id)
		if err != nil {
			return err
		}
		seen[id] = true
		queue = append(queue, PlanVersion{Version: v, Adopted: true})
	}
	srcNames := map[string]bool{}
	for i := 0; i < len(queue); i++ {
		pv := &queue[i]
		pv.Published = published(pv.Version.Kind)
		if pv.Published {
			continue
		}
		refs, err := lineage.RefsIn(pv.Version.Payload)
		if err != nil {
			return err
		}
		for _, r := range refs {
			if !strings.HasPrefix(r.ID, "ver_") || seen[r.ID] {
				continue
			}
			v, err := registry.GetVersion(ctx, q, "", r.ID)
			var pe *problems.Error
			if errors.As(err, &pe) && pe.Type == problems.NotFound {
				continue // a payload naming a version this instance never held (an import's)
			}
			if err != nil {
				return err
			}
			seen[r.ID] = true
			queue = append(queue, PlanVersion{Version: v})
		}
		if err := pv.content(ctx, q, store); err != nil {
			return err
		}
		for _, s := range pv.Sources {
			srcNames[s.Name] = true
		}
		if err := q.QueryRow(ctx, "SELECT description FROM registry_collections WHERE id = $1", pv.Version.CollectionID).
			Scan(&pv.Description); err != nil {
			return fmt.Errorf("read collection %s: %w", pv.Version.Name, err)
		}
	}
	// Dependencies first: an import registers a version only after the versions its payload names.
	sort.SliceStable(queue, func(i, j int) bool { return kindRank(queue[i].Version.Kind) < kindRank(queue[j].Version.Kind) })
	pl.Versions = queue
	blobs := map[string]int64{}
	for _, pv := range pl.Versions {
		for h, n := range pv.blobSet() {
			blobs[h] = n
		}
	}
	pl.Blobs = len(blobs)
	for _, n := range blobs {
		pl.Bytes += n
	}
	for n := range srcNames {
		pl.Sources = append(pl.Sources, n)
	}
	sort.Strings(pl.Sources)
	return nil
}

// kindRank orders kinds so a version comes after the kinds its payload may name.
func kindRank(kind string) int {
	switch kind {
	case registry.KindRuntime, registry.KindModelFamily, registry.KindStepKind:
		return 0
	case registry.KindTemplate, registry.KindNormalizer, registry.KindAuxiliary, registry.KindBaseModel:
		return 1
	case registry.KindDataset, registry.KindNoiseBank:
		return 2
	case registry.KindGoldenSet:
		return 3
	}
	return 4 // models name base models, datasets and golden sets
}

// content finds the blobs a version carries: a dataset-like version's dataset artifact (which must be in the cache),
// and every other content-store hash its payload names that this instance holds.
func (pv *PlanVersion) content(ctx context.Context, q storage.Querier, store *cas.Store) error {
	v := pv.Version
	var p datasetPayload
	_ = json.Unmarshal(v.Payload, &p)
	if datasetLike(v.Kind) && p.Artifact.Hash != "" {
		a, err := artifacts.Get(ctx, q, p.Artifact.Hash)
		var pe *problems.Error
		if errors.As(err, &pe) && pe.Type == problems.NotFound && store != nil {
			// Not indexed (registered by a hook outside a pipeline run): the store's manifest says what it holds.
			if ok, _, _ := store.Has(p.Artifact.Hash); !ok {
				return problems.Conflict.New("%s %s: its dataset artifact %s is not in the content store", v.Name, v.Version, p.Artifact.Hash)
			}
			a, err = artifacts.Artifact{Hash: p.Artifact.Hash, Type: or(p.Artifact.Type, "dataset"), Directory: true}, nil
		}
		if err != nil {
			return fmt.Errorf("%s %s: its dataset artifact %s: %w", v.Name, v.Version, p.Artifact.Hash, err)
		}
		if a.Evicted != nil {
			return problems.Conflict.New("%s %s was evicted from the cache; bring it back with datasets.materialize, then export", v.Name, v.Version)
		}
		files, err := artifacts.Files(ctx, q, store, a)
		if err != nil {
			return err
		}
		pv.Dataset, pv.DatasetFile = &a, files
		pv.Path = DatasetDir(v.Name, v.Version)
		srcs, err := readSources(ctx, q, p.SourceIDs)
		if err != nil {
			return err
		}
		pv.Sources = srcs
	}
	pv.Files = map[string][]cas.File{}
	for _, h := range hashesIn(v.Payload) {
		if pv.Dataset != nil && h == pv.Dataset.Hash {
			continue
		}
		a, err := artifacts.Get(ctx, q, h)
		var pe *problems.Error
		switch {
		case errors.As(err, &pe) && pe.Type == problems.NotFound:
			if store == nil {
				continue
			}
			ok, n, err := store.Has(h)
			if err != nil || !ok {
				continue // a hash the payload records but this store never held (a fingerprint, another instance's)
			}
			pv.Artifacts = append(pv.Artifacts, artifacts.Artifact{Hash: h, Type: "blob", Size: n})
			continue
		case err != nil:
			return err
		case a.Evicted != nil:
			return problems.Conflict.New("%s %s names artifact %s (%s), evicted from the content store; restore it first", v.Name, v.Version, h, a.Type)
		}
		files, err := artifacts.Files(ctx, q, store, a)
		if err != nil {
			return err
		}
		pv.Artifacts = append(pv.Artifacts, a)
		pv.Files[h] = files
	}
	set := pv.blobSet()
	pv.Blobs = len(set)
	for _, n := range set {
		pv.Bytes += n
	}
	return nil
}

// blobSet is every blob the version carries, with its size.
func (pv PlanVersion) blobSet() map[string]int64 {
	out := map[string]int64{}
	if pv.Dataset != nil {
		out[pv.Dataset.Hash] = 0 // the manifest; its size is read when copied
		for _, f := range pv.DatasetFile {
			out[f.Hash] = f.Size
		}
	}
	for _, a := range pv.Artifacts {
		if a.Directory {
			out[a.Hash] = 0
			for _, f := range pv.Files[a.Hash] {
				out[f.Hash] = f.Size
			}
			continue
		}
		out[a.Hash] = a.Size
	}
	return out
}

// hashesIn lists every content-store hash a JSON document names as a string, sorted, without repeats.
func hashesIn(doc []byte) []string {
	var v any
	if json.Unmarshal(doc, &v) != nil {
		return nil
	}
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			for _, e := range t {
				walk(e)
			}
		case []any:
			for _, e := range t {
				walk(e)
			}
		case string:
			if steps.ValidHash(t) {
				seen[t] = true
			}
		}
	}
	walk(v)
	out := make([]string, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------- start and the job

type projectArgs struct {
	ExportID string `json:"exportId"`
	Ref      string `json:"ref"`
}

// StartProject records a project export (state running) and queues the job that writes it.
func StartProject(ctx context.Context, tx pgx.Tx, js *jobs.Service, pl ProjectPlan, actor auth.Actor) (Export, []events.Draft, error) {
	if js == nil {
		return Export{}, nil, errors.New("exports: the job service is not configured")
	}
	id := "dex_" + uuid.Must(uuid.NewV7()).String()
	job, drafts, err := js.Enqueue(ctx, tx, jobs.Spec{Kind: JobProject, ProjectID: pl.Project.ID, Args: projectArgs{ExportID: id, Ref: pl.Ref}})
	if err != nil {
		return Export{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO dataset_exports (id, project_id, format, target, step_kind, created_by, job_id, state,
			commit_sha, versions) VALUES ($1, $2, $3, $4, '', $5, $6, $7, $8, $9)`,
		id, pl.Project.ID, FormatProject, pl.Target, actor, job.ID, StateRunning, pl.Commit, len(pl.Versions)); err != nil {
		return Export{}, nil, fmt.Errorf("record the export: %w", err)
	}
	x, err := Get(ctx, tx, id)
	if err != nil {
		return Export{}, nil, err
	}
	return x, append(drafts, draft(x, EventStarted)), nil
}

// ProjectWriter writes project bundles (the projects.export job).
type ProjectWriter struct {
	Pool     *pgxpool.Pool
	CAS      *cas.Store
	Repos    *repos.Store
	Defaults func() *defaults.Defaults
	Now      func() time.Time
}

// Register adds the job kind; call it before the job service starts.
func (w *ProjectWriter) Register(js *jobs.Service) {
	js.Register(JobProject, w.run, jobs.KindOptions{Timeout: 24 * time.Hour})
}

func (w *ProjectWriter) now() time.Time {
	if w.Now != nil {
		return w.Now().UTC()
	}
	return time.Now().UTC()
}

func (w *ProjectWriter) run(ctx context.Context, r *jobs.Run) (any, error) {
	var args projectArgs
	if err := json.Unmarshal(r.Args, &args); err != nil {
		return nil, fmt.Errorf("read the export's args: %w", err)
	}
	res, err := w.write(ctx, r, args.ExportID, args.Ref)
	if err != nil {
		fctx := context.WithoutCancel(ctx)
		if ferr := pgx.BeginFunc(fctx, w.Pool, func(tx pgx.Tx) error {
			return w.finish(fctx, tx, args.ExportID, r.Job.Actor, func(x *Export) { x.State, x.Error = StateFailed, err.Error() })
		}); ferr != nil {
			return nil, errors.Join(err, ferr)
		}
		return nil, err
	}
	return res, nil
}

// ProjectResult is what a finished projects.export job reports.
type ProjectResult struct {
	ExportID string `json:"exportId"`
	Target   string `json:"target"`
	Commit   string `json:"commit"`
	Versions int    `json:"versions"`
	Files    int    `json:"files"`
	Bytes    int64  `json:"bytes"`
	Copies   int    `json:"copies"`
}

// bundleWriter writes files under a bundle's directory and keeps count.
type bundleWriter struct {
	root, dir string // the mount's root; the bundle directory relative to it
	files     []File
	bytes     int64
	copies    []mounts.Copy
	have      map[string]bool // paths written and blobs recorded (keys never collide: paths are not b3 hashes)
}

func (b *bundleWriter) abs(rel string) (string, error) {
	return mounts.InRoot(b.root, path.Join(b.dir, rel))
}

func (b *bundleWriter) writeBytes(rel string, data []byte) error {
	p, err := b.abs(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, data, 0o640); err != nil { //nolint:gosec // bundles are read by other instances' services
		return fmt.Errorf("write %s: %w", rel, err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("write %s: %w", rel, err)
	}
	b.files = append(b.files, File{Path: rel, Bytes: int64(len(data))})
	b.bytes += int64(len(data))
	return nil
}

// copyBlob writes blob h from the store to rel (once per path), and records it as a copy on the mount.
func (b *bundleWriter) copyBlob(store *cas.Store, h, rel string) error {
	if b.have[rel] {
		return nil
	}
	p, err := b.abs(rel)
	if err != nil {
		return err
	}
	src, err := store.Open(h)
	if err != nil {
		return fmt.Errorf("blob %s: %w", h, err)
	}
	defer func() { _ = src.Close() }()
	st, err := src.Stat()
	if err != nil {
		return fmt.Errorf("blob %s: %w", h, err)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() != st.Size() {
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		tmp := p + ".part"
		f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640) //nolint:gosec // a path inside the mount (InRoot)
		if err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		_, err = io.Copy(f, src)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmp, p)
		}
		if err != nil {
			_ = os.Remove(tmp)
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	b.have[rel] = true
	b.files = append(b.files, File{Path: rel, Hash: h, Bytes: st.Size()})
	b.bytes += st.Size()
	if !b.have[h] { // one copy per blob on the mount (blob_copies keys on the hash)
		b.have[h] = true
		b.copies = append(b.copies, mounts.Copy{Hash: h, Path: path.Join(b.dir, rel), Size: st.Size()})
	}
	return nil
}

func (w *ProjectWriter) write(ctx context.Context, r *jobs.Run, exportID, ref string) (ProjectResult, error) {
	x, err := Get(ctx, w.Pool, exportID)
	if err != nil {
		return ProjectResult{}, err
	}
	p, err := projects.GetByID(ctx, w.Pool, x.ProjectID)
	if err != nil {
		return ProjectResult{}, err
	}
	d := defaults.Get()
	if w.Defaults != nil {
		d = w.Defaults()
	}
	pl, err := PlanProject(ctx, w.Pool, w.CAS, w.Repos, d, p, ProjectRequest{Ref: x.Commit, Target: x.Target})
	if err != nil {
		return ProjectResult{}, err
	}
	if pl.Mount.ReadOnly || !mounts.PathKind(pl.Mount.Kind) {
		return ProjectResult{}, fmt.Errorf("mount %s is not a writable path mount", pl.Mount.Name)
	}
	bw := &bundleWriter{root: pl.Mount.Root, dir: pl.Dir, have: map[string]bool{}}
	dir, err := bw.abs("")
	if err != nil {
		return ProjectResult{}, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return ProjectResult{}, fmt.Errorf("create %s: %w", pl.Target, err)
	}
	_ = r.Progress(ctx, 0.02, "bundling the repository")
	repoFile := filepath.Join(dir, RepositoryFile)
	if err := w.Repos.Bundle(ctx, p.Slug, pl.Commit, repoFile); err != nil {
		return ProjectResult{}, fmt.Errorf("bundle the repository: %w", err)
	}
	if fi, err := os.Stat(repoFile); err == nil {
		bw.files = append(bw.files, File{Path: RepositoryFile, Bytes: fi.Size()})
		bw.bytes += fi.Size()
	}
	base := ""
	if p.BaseModel != nil {
		base = p.BaseModel.VersionID
	}
	doc := BundleDoc{Format: ProjectBundleFormat, CreatedAt: w.now(), Ref: or(ref, pl.Commit), Commit: pl.Commit, Repository: RepositoryFile,
		Project: BundleProject{Name: p.Name, Slug: p.Slug, Description: p.Description, Locales: p.Locales, Domain: p.Domain,
			BaseModel: base},
		Aliases: pl.Aliases, Versions: []BundleEntry{}, Artifacts: []BundleArtifact{}}
	if lock, _, err := w.Repos.ReadFile(ctx, p.Slug, pl.Commit, DataLockFile); err == nil {
		if err := bw.writeBytes(DataLockFile, lock); err != nil {
			return ProjectResult{}, err
		}
		doc.DataLock = DataLockFile
	}
	seenArtifacts := map[string]bool{}
	for i, pv := range pl.Versions {
		_ = r.Progress(ctx, 0.05+0.9*float64(i)/float64(len(pl.Versions)), fmt.Sprintf("%s %s", pv.Version.Name, pv.Version.Version))
		rec := recordOf(pv)
		e := BundleEntry{Record: rec, Adopted: pv.Adopted, Published: pv.Published, Blobs: pv.Blobs, Bytes: pv.Bytes}
		if pv.Dataset != nil {
			if err := w.writeDataset(bw, pv, rec); err != nil {
				return ProjectResult{}, err
			}
			e.Dataset = pv.Path
		}
		for _, a := range pv.Artifacts {
			e.Artifacts = append(e.Artifacts, a.Hash)
			if seenArtifacts[a.Hash] {
				continue
			}
			seenArtifacts[a.Hash] = true
			ba := BundleArtifact{Hash: a.Hash, Type: a.Type, Size: a.Size, Directory: a.Directory, Meta: a.Meta, Files: pv.Files[a.Hash]}
			if err := bw.copyBlob(w.CAS, a.Hash, BlobPath(a.Hash)); err != nil {
				return ProjectResult{}, err
			}
			for _, f := range ba.Files {
				if err := bw.copyBlob(w.CAS, f.Hash, BlobPath(f.Hash)); err != nil {
					return ProjectResult{}, err
				}
			}
			doc.Artifacts = append(doc.Artifacts, ba)
		}
		doc.Versions = append(doc.Versions, e)
	}
	doc.Blobs, doc.Bytes = len(bw.copies), 0
	for _, c := range bw.copies {
		doc.Bytes += c.Size
	}
	body, err := json.MarshalIndent(doc, "", " ")
	if err != nil {
		return ProjectResult{}, fmt.Errorf("encode %s: %w", BundleFile, err)
	}
	if err := bw.writeBytes(BundleFile, append(body, '\n')); err != nil {
		return ProjectResult{}, err
	}
	res := ProjectResult{ExportID: exportID, Target: pl.Target, Commit: pl.Commit, Versions: len(pl.Versions), Files: len(bw.files),
		Bytes: bw.bytes, Copies: len(bw.copies)}
	err = pgx.BeginFunc(ctx, w.Pool, func(tx pgx.Tx) error {
		if err := mounts.RecordCopies(ctx, tx, pl.Mount.ID, bw.copies); err != nil {
			return err
		}
		return w.finish(ctx, tx, exportID, r.Job.Actor, func(x *Export) {
			x.State, x.Files, x.Bytes, x.Copies, x.Sample = StateDone, len(bw.files), bw.bytes, len(bw.copies),
				bw.files[:min(len(bw.files), sampleFiles)]
		})
	})
	_ = r.Progress(ctx, 1, fmt.Sprintf("%d versions, %d files", len(pl.Versions), len(bw.files)))
	return res, err
}

// writeDataset writes a dataset-like version as a dataset bundle: its artifact's blobs under its own cas/ and the
// bundle.json dataset_import reads.
func (w *ProjectWriter) writeDataset(bw *bundleWriter, pv PlanVersion, rec Record) error {
	if err := bw.copyBlob(w.CAS, pv.Dataset.Hash, path.Join(pv.Path, BlobPath(pv.Dataset.Hash))); err != nil {
		return err
	}
	files := make([]File, 0, len(pv.DatasetFile))
	for _, f := range pv.DatasetFile {
		if err := bw.copyBlob(w.CAS, f.Hash, path.Join(pv.Path, BlobPath(f.Hash))); err != nil {
			return err
		}
		files = append(files, File{Path: f.Path, Hash: f.Hash, Bytes: f.Size})
	}
	b, err := json.MarshalIndent(DatasetBundleDoc{Format: DatasetBundleFormat, Record: rec, Artifact: pv.Dataset.Hash, Files: files}, "", " ")
	if err != nil {
		return fmt.Errorf("encode the dataset bundle of %s: %w", pv.Version.Name, err)
	}
	return bw.writeBytes(path.Join(pv.Path, BundleFile), append(b, '\n'))
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func recordOf(pv PlanVersion) Record {
	v := pv.Version
	tags := slices.Clone(v.Tags)
	if tags == nil {
		tags = []string{}
	}
	sort.Strings(tags)
	srcs := make([]RecordSource, 0, len(pv.Sources))
	for _, s := range pv.Sources {
		langs := append([]string{}, s.Languages...)
		sort.Strings(langs)
		srcs = append(srcs, RecordSource{Name: s.Name, Licence: s.Licence, Kind: s.Kind, URL: s.URL, Languages: langs, TrainingCleared: s.TrainingCleared})
	}
	return Record{Format: RecordFormat, Kind: v.Kind, Collection: v.Name, Version: v.Version, VersionID: v.ID, Licence: v.Licence,
		Tags: tags, Fingerprint: v.Fingerprint, Payload: v.Payload, Sources: srcs, Description: pv.Description}
}

// finish updates a project export's row and emits export.done.
func (w *ProjectWriter) finish(ctx context.Context, tx pgx.Tx, id string, actor auth.Actor, set func(*Export)) error {
	x, err := Get(ctx, tx, id)
	if err != nil {
		return err
	}
	set(&x)
	sample, _ := json.Marshal(x.Sample)
	if _, err := tx.Exec(ctx, `UPDATE dataset_exports SET state = $2, error = NULLIF($3, ''), files = $4, bytes = $5, copies = $6,
			sample = $7, finished_at = $8, rev = rev + 1 WHERE id = $1`,
		id, x.State, x.Error, x.Files, x.Bytes, x.Copies, sample, w.now()); err != nil {
		return fmt.Errorf("record the export: %w", err)
	}
	if x, err = Get(ctx, tx, id); err != nil {
		return err
	}
	return events.Append(ctx, tx, actor, nil, []events.Draft{draft(x, EventDone)})
}
