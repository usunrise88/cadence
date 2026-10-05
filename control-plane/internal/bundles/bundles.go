// Package bundles reads Cadence project bundles (projects.export, internal/exports/project.go) into this instance:
// bundles.adopt adopts a bundle's versions into an existing project, and projects.new with bundle makes a new project
// from one (the bootstrap job calls Import after it made the repository from repository.bundle). ROADMAP "Phase 4
// notes"; docs/spec/03-pipelines-defaults.md "Interoperability".
//
// An import registers the versions this instance lacks — by collection and fingerprint, so the same content is never
// registered twice — under the version strings the bundle's instance gave them, then adopts every version the
// bundle's project adopted:
//
//   - blobs are copied from the bundle into the content store and checked against their hashes (bundle-invalid);
//   - dataset versions and noise banks are re-imported from their dataset bundles through the dataset importer, as
//     dataset_import format cadence-bundle would (a freeze's cut becomes an import), after their sources are made
//     (a source cleared for training there is cleared here: the import is the admin's approval);
//   - golden sets are re-frozen through this instance's checks (goldensets.Prepare: eval-only data, leakage);
//   - every other kind is registered from its record, its payload's version ids mapped to the versions here;
//   - versions published by workers (runtimes, step kinds, model families) are never registered: a payload naming
//     one points at the version here with the same content when there is one.
//
// Registering versions is a registry change for every project, so both commands are approvals the admin decides,
// for people too (preset rule bundle-import).
package bundles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/auxiliary"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/exports"
	"github.com/usunrise88/cadence/control-plane/internal/goldensets"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/lineage"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Operation is the command that adopts a bundle into a project; JobAdopt its job kind.
const (
	Operation = "bundles.adopt"
	JobAdopt  = "bundles.adopt"
	// GateParam names a bundle import to the policy engine (preset rule bundle-import: from=bundle), on bundles.adopt
	// and on projects.new with bundle.
	GateParam = "from"
	GateValue = "bundle"
)

// Plan actions per version.
const (
	ActionRegister  = "register"
	ActionReuse     = "reuse"
	ActionReference = "reference"
	ActionMissing   = "missing"
)

// Alias actions.
const (
	AliasSet  = "set"
	AliasKeep = "keep"
	AliasSkip = "skip"
)

const maxDoc = 64 << 20

var versionRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\.[0-9a-f]{12}$`)

// Facts commits the files a project's facts appear in (project.yaml, AGENTS.md, data.lock): the bootstrap service.
type Facts interface {
	CommitFacts(ctx context.Context, tx pgx.Tx, p projects.Project, actor auth.Actor, message string, dryRun bool) ([]events.Draft, error)
}

// Service reads bundles and imports them.
type Service struct {
	Pool     *pgxpool.Pool
	CAS      *cas.Store
	Jobs     *jobs.Service
	Secrets  mounts.Secrets
	HTTP     *http.Client
	Facts    Facts
	Defaults func() *defaults.Defaults
	Log      *slog.Logger
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) defaults() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

func invalid(format string, args ...any) error { return problems.BundleInvalid.New(format, args...) }

// ---------------------------------------------------------------- reading

// Bundle is an opened project bundle.
type Bundle struct {
	URI   string
	Mount mounts.Mount
	Dir   string // relative to the mount's root
	Doc   exports.BundleDoc
	rd    mounts.Reader
}

// Open reads the bundle.json of the bundle at uri (mount://<mount>/<dir>) and checks it.
func (s *Service) Open(ctx context.Context, q storage.Querier, uri string) (*Bundle, error) {
	u, err := mounts.ParseURI(uri)
	if err != nil {
		return nil, problems.Validation([]problems.FieldError{{Path: "/bundle", Message: err.Error()}})
	}
	if u.Start != nil || u.Channel != nil {
		return nil, problems.Validation([]problems.FieldError{{Path: "/bundle", Message: "a bundle is a directory; it takes no #t or ch fragment"}})
	}
	m, err := mounts.Get(ctx, q, u.Mount)
	if err != nil {
		return nil, err
	}
	rd, err := mounts.Open(ctx, m, s.Secrets, s.HTTP)
	if err != nil {
		return nil, err
	}
	b := &Bundle{URI: u.String(), Mount: m, Dir: strings.Trim(u.Path, "/"), rd: rd}
	raw, err := b.read(ctx, exports.BundleFile)
	if err != nil {
		return nil, invalid("%s: no readable %s (a project bundle from projects.export): %v", b.URI, exports.BundleFile, err)
	}
	if err := json.Unmarshal(raw, &b.Doc); err != nil {
		return nil, invalid("%s/%s is not JSON: %v", b.URI, exports.BundleFile, err)
	}
	if err := b.check(); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Bundle) read(ctx context.Context, rel string) ([]byte, error) {
	f, err := b.rd.Open(ctx, path.Join(b.Dir, rel))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxDoc+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDoc {
		return nil, fmt.Errorf("%s is larger than %d MB", rel, maxDoc>>20)
	}
	return data, nil
}

func (b *Bundle) open(ctx context.Context, rel string) (io.ReadCloser, error) {
	return b.rd.Open(ctx, path.Join(b.Dir, rel))
}

// SaveRepository copies the bundle's git bundle (repository.bundle) to dst, a local file (git reads it from there,
// whatever kind of mount the bundle is on).
func (b *Bundle) SaveRepository(ctx context.Context, dst string) error {
	name := b.Doc.Repository
	if name == "" {
		name = exports.RepositoryFile
	}
	if strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return invalid("%s: repository %q leaves the bundle", b.URI, name)
	}
	src, err := b.open(ctx, name)
	if err != nil {
		return invalid("%s: no %s: %v", b.URI, name, err)
	}
	defer func() { _ = src.Close() }()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600) //nolint:gosec // a scratch file the caller names
	if err != nil {
		return err
	}
	_, err = io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (b *Bundle) check() error {
	d := b.Doc
	if d.Format != exports.ProjectBundleFormat {
		return invalid("%s/%s is %q, not a project bundle (%s); a dataset bundle is imported with dataset_import format cadence-bundle",
			b.URI, exports.BundleFile, d.Format, exports.ProjectBundleFormat)
	}
	ids := map[string]bool{}
	for _, e := range d.Versions {
		r := e.Record
		switch {
		case !registry.Known(r.Kind):
			return invalid("version %s: unknown registry kind %q", r.VersionID, r.Kind)
		case !registry.ValidName(r.Kind, r.Collection):
			return invalid("version %s: %q is not a %s collection name", r.VersionID, r.Collection, registry.Noun(r.Kind))
		case !versionRe.MatchString(r.Version):
			return invalid("%s: %q is not a version string (YYYY-MM-DD.<12 hex>)", r.Collection, r.Version)
		case len(r.Fingerprint) != 64:
			return invalid("%s %s: the record has no fingerprint", r.Collection, r.Version)
		case r.VersionID == "" || ids[r.VersionID]:
			return invalid("%s %s: missing or repeated version id %q", r.Collection, r.Version, r.VersionID)
		case e.Dataset != "" && (strings.Contains(e.Dataset, "..") || strings.HasPrefix(e.Dataset, "/")):
			return invalid("%s %s: dataset bundle path %q leaves the bundle", r.Collection, r.Version, e.Dataset)
		}
		ids[r.VersionID] = true
	}
	for _, a := range d.Artifacts {
		if !steps.ValidHash(a.Hash) || !artifacts.ValidType(a.Type) && a.Type != "blob" {
			return invalid("artifact %q (%s) is not a content-store artifact", a.Hash, a.Type)
		}
	}
	return nil
}

// ---------------------------------------------------------------- the plan

// PlanVersion is what an import does with one version.
type PlanVersion struct {
	Entry  exports.BundleEntry
	Action string
	Local  *registry.Version // reuse, reference
	Blobs  int               // blobs this store lacks
	Bytes  int64
}

// AliasPlan is what an import does with one alias.
type AliasPlan struct {
	Name, VersionID, LocalVersionID, Action string
}

// Plan is what an import of a bundle does (the contract's BundlePlan).
type Plan struct {
	Bundle   *Bundle
	Versions []PlanVersion
	Aliases  []AliasPlan
	Register int
	Reuse    int
	Blobs    int
	Bytes    int64
	Sources  []string
	blobs    []blobCopy // what Import copies
}

type blobCopy struct {
	hash string
	rel  string // relative to the bundle's directory
	size int64  // as listed; 0 for a manifest
}

// PlanImport checks bundle b against this instance: which versions it holds already, which it would register, the
// blobs it lacks, and (for a project, "" for a new one) which aliases would be set.
func (s *Service) PlanImport(ctx context.Context, q storage.Querier, b *Bundle, projectID string, aliases bool) (Plan, error) {
	pl := Plan{Bundle: b, Sources: []string{}, Aliases: []AliasPlan{}}
	srcs := map[string]bool{}
	seenBlob := map[string]bool{}
	for _, e := range b.Doc.Versions {
		r := e.Record
		pv := PlanVersion{Entry: e}
		local, found, err := Local(ctx, q, r)
		if err != nil {
			return Plan{}, err
		}
		switch {
		case found && e.Published:
			pv.Action, pv.Local = ActionReference, &local
		case found:
			pv.Action, pv.Local = ActionReuse, &local
			pl.Reuse++
		case e.Published:
			pv.Action = ActionMissing
		default:
			pv.Action = ActionRegister
			pl.Register++
			blobs, err := s.blobsOf(ctx, b, e)
			if err != nil {
				return Plan{}, err
			}
			for _, c := range blobs {
				if seenBlob[c.hash] {
					continue
				}
				seenBlob[c.hash] = true
				if s.CAS != nil {
					if ok, _, err := s.CAS.Has(c.hash); err == nil && ok {
						continue
					}
				}
				pl.blobs = append(pl.blobs, c)
				pv.Blobs++
				pv.Bytes += c.size
			}
		}
		for _, src := range r.Sources {
			srcs[src.Name] = true
		}
		pl.Blobs += pv.Blobs
		pl.Bytes += pv.Bytes
		pl.Versions = append(pl.Versions, pv)
	}
	for n := range srcs {
		pl.Sources = append(pl.Sources, n)
	}
	sort.Strings(pl.Sources)
	if !aliases {
		return pl, nil
	}
	have := map[string]string{}
	if projectID != "" {
		list, err := registry.ListAliases(ctx, q, projectID)
		if err != nil {
			return Plan{}, err
		}
		for _, a := range list {
			have[a.Name] = a.Version.ID
		}
	}
	for _, a := range b.Doc.Aliases {
		ap := AliasPlan{Name: a.Name, VersionID: a.VersionID, Action: AliasSet}
		for _, pv := range pl.Versions {
			if pv.Entry.Record.VersionID == a.VersionID && pv.Local != nil {
				ap.LocalVersionID = pv.Local.ID
			}
		}
		cur, exists := have[a.Name]
		switch {
		case registry.Reservation(a.Name) == registry.AliasPromotion:
			ap.Action = AliasSkip
		case exists && ap.LocalVersionID != "" && cur == ap.LocalVersionID:
			ap.Action = AliasKeep
		case exists:
			ap.Action = AliasSkip
		}
		pl.Aliases = append(pl.Aliases, ap)
	}
	return pl, nil
}

// Local finds the version here with the record's collection and fingerprint.
func Local(ctx context.Context, q storage.Querier, r exports.Record) (registry.Version, bool, error) {
	list, err := registry.ListVersions(ctx, q, registry.Filter{Collection: r.Collection, Fingerprint: r.Fingerprint, Limit: 1})
	if err != nil || len(list) == 0 {
		return registry.Version{}, false, err
	}
	if list[0].Kind != r.Kind {
		return registry.Version{}, false, invalid("collection %s holds %s versions here, the bundle's %s is a %s",
			r.Collection, registry.Noun(list[0].Kind), r.Version, registry.Noun(r.Kind))
	}
	return list[0], true, nil
}

// blobsOf lists the blobs a version brings: its dataset bundle's manifest and files, and its artifacts under cas/.
func (s *Service) blobsOf(ctx context.Context, b *Bundle, e exports.BundleEntry) ([]blobCopy, error) {
	var out []blobCopy
	if e.Dataset != "" {
		db, err := s.datasetDoc(ctx, b, e)
		if err != nil {
			return nil, err
		}
		out = append(out, blobCopy{hash: db.Artifact, rel: path.Join(e.Dataset, exports.BlobPath(db.Artifact))})
		for _, f := range db.Files {
			out = append(out, blobCopy{hash: f.Hash, rel: path.Join(e.Dataset, exports.BlobPath(f.Hash)), size: f.Bytes})
		}
	}
	for _, h := range e.Artifacts {
		a, ok := b.artifact(h)
		if !ok {
			return nil, invalid("%s %s names artifact %s, which bundle.json does not list", e.Record.Collection, e.Record.Version, h)
		}
		size := a.Size
		if a.Directory {
			size = 0
		}
		out = append(out, blobCopy{hash: a.Hash, rel: exports.BlobPath(a.Hash), size: size})
		for _, f := range a.Files {
			out = append(out, blobCopy{hash: f.Hash, rel: exports.BlobPath(f.Hash), size: f.Size})
		}
	}
	for _, c := range out { // a hash names the file it is read from: nothing but b3 hashes
		if !steps.ValidHash(c.hash) {
			return nil, invalid("%s %s lists %q, which is not a content-store hash", e.Record.Collection, e.Record.Version, c.hash)
		}
	}
	return out, nil
}

func (b *Bundle) artifact(h string) (exports.BundleArtifact, bool) {
	for _, a := range b.Doc.Artifacts {
		if a.Hash == h {
			return a, true
		}
	}
	return exports.BundleArtifact{}, false
}

func (s *Service) datasetDoc(ctx context.Context, b *Bundle, e exports.BundleEntry) (exports.DatasetBundleDoc, error) {
	var db exports.DatasetBundleDoc
	raw, err := b.read(ctx, path.Join(e.Dataset, exports.BundleFile))
	if err != nil {
		return db, invalid("%s %s: its dataset bundle %s is unreadable: %v", e.Record.Collection, e.Record.Version, e.Dataset, err)
	}
	if err := json.Unmarshal(raw, &db); err != nil || db.Format != exports.DatasetBundleFormat || !steps.ValidHash(db.Artifact) {
		return db, invalid("%s/%s is not a dataset bundle (%s)", e.Dataset, exports.BundleFile, exports.DatasetBundleFormat)
	}
	return db, nil
}

// ---------------------------------------------------------------- the import

// Options steer an import.
type Options struct {
	Aliases    bool   // set the bundle's aliases the project lacks
	BaseModel  bool   // make the bundle's base model the project's (a new project from the bundle)
	ApprovalID string // the approval the import runs under
	Message    string // the data.lock commit's message
}

// Result is what an import did (the job's result).
type Result struct {
	Bundle     string          `json:"bundle"`
	Commit     string          `json:"commit"`
	Registered []ResultVersion `json:"registered"`
	Reused     []ResultVersion `json:"reused"`
	Adopted    int             `json:"adopted"`
	Aliases    []string        `json:"aliases"`
	Missing    []ResultVersion `json:"missing"`
	Blobs      int             `json:"blobs"`
	Bytes      int64           `json:"bytes"`
	Sources    []string        `json:"sources"`
	Changed    []ResultVersion `json:"changed,omitempty"` // registered under another version string than the bundle's
	BaseModel  *ResultVersion  `json:"baseModel,omitempty"`
	Notes      []string        `json:"notes,omitempty"`
	ids        map[string]string
}

// ResultVersion is one version an import touched.
type ResultVersion struct {
	BundleID   string `json:"bundleId"`
	ID         string `json:"id,omitempty"`
	Kind       string `json:"kind"`
	Collection string `json:"collection"`
	Version    string `json:"version"`
	Local      string `json:"localVersion,omitempty"`
}

// Import copies the bundle's blobs into the content store, then in one transaction registers what this instance
// lacks, adopts what the bundle's project adopted into p, sets aliases, and commits data.lock. progress may be nil.
func (s *Service) Import(ctx context.Context, b *Bundle, p projects.Project, actor auth.Actor, opt Options,
	progress func(fraction float64, msg string)) (Result, error) {
	if progress == nil {
		progress = func(float64, string) {}
	}
	if s.CAS == nil {
		return Result{}, errors.New("bundles: no content store")
	}
	pl, err := s.PlanImport(ctx, s.Pool, b, p.ID, opt.Aliases)
	if err != nil {
		return Result{}, err
	}
	res := Result{Bundle: b.URI, Commit: b.Doc.Commit, Registered: []ResultVersion{}, Reused: []ResultVersion{}, Aliases: []string{},
		Missing: []ResultVersion{}, Sources: pl.Sources, ids: map[string]string{}}
	for i, c := range pl.blobs {
		if err := s.copyBlob(ctx, b, c); err != nil {
			return Result{}, err
		}
		res.Blobs++
		res.Bytes += c.size
		if i%200 == 0 {
			progress(0.8*float64(i+1)/float64(len(pl.blobs)), fmt.Sprintf("%d of %d blobs copied", i+1, len(pl.blobs)))
		}
	}
	progress(0.85, "registering versions")
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		drafts, err := s.importTx(ctx, tx, pl, p, actor, opt, &res)
		if err != nil {
			return err
		}
		return events.Append(ctx, tx, actor, nil, drafts)
	})
	if err != nil {
		return Result{}, err
	}
	progress(1, fmt.Sprintf("%d registered, %d reused, %d adopted", len(res.Registered), len(res.Reused), res.Adopted))
	return res, nil
}

// copyBlob copies one blob from the bundle into the content store, checking its hash.
func (s *Service) copyBlob(ctx context.Context, b *Bundle, c blobCopy) error {
	f, err := b.open(ctx, c.rel)
	if err != nil {
		return invalid("blob %s is missing from the bundle (%s): %v", c.hash, c.rel, err)
	}
	defer func() { _ = f.Close() }()
	if _, _, err := s.CAS.Put(f, c.hash); err != nil {
		if errors.Is(err, cas.ErrHashMismatch) {
			return invalid("%s does not hash to %s: the bundle is damaged", c.rel, c.hash)
		}
		return err
	}
	return nil
}

func (s *Service) importTx(ctx context.Context, tx pgx.Tx, pl Plan, p projects.Project, actor auth.Actor, opt Options,
	res *Result) ([]events.Draft, error) {
	if err := artifacts.LockShared(ctx, tx); err != nil {
		return nil, err
	}
	var drafts []events.Draft
	for _, pv := range pl.Versions {
		if pv.Local != nil {
			res.ids[pv.Entry.Record.VersionID] = pv.Local.ID
		}
	}
	for _, pv := range order(pl.Versions) {
		r := pv.Entry.Record
		rv := ResultVersion{BundleID: r.VersionID, Kind: r.Kind, Collection: r.Collection, Version: r.Version}
		switch pv.Action {
		case ActionReuse:
			rv.ID, rv.Local = pv.Local.ID, pv.Local.Version
			res.Reused = append(res.Reused, rv)
			continue
		case ActionReference:
			continue
		case ActionMissing:
			res.Missing = append(res.Missing, rv)
			continue
		}
		v, more, err := s.register(ctx, tx, pl.Bundle, pv.Entry, p, actor, opt, res)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", r.Collection, r.Version, err)
		}
		drafts = append(drafts, more...)
		res.ids[r.VersionID] = v.ID
		rv.ID, rv.Local = v.ID, v.Version
		res.Registered = append(res.Registered, rv)
		if v.Version != r.Version {
			res.Changed = append(res.Changed, rv)
		}
	}
	// Adopt what the bundle's project adopted, with the adoption checks (the locale check is the bundle's project's:
	// its adoptions were checked there, replay data included).
	var adopt []string
	for _, pv := range pl.Versions {
		if !pv.Entry.Adopted || pv.Entry.Published {
			continue
		}
		id := res.ids[pv.Entry.Record.VersionID]
		v, err := registry.GetVersion(ctx, tx, "", id)
		if err != nil {
			return nil, err
		}
		for _, check := range []error{registry.CheckLicence(v), auxiliary.CheckAdoption(v), goldensets.CheckAdoption(ctx, tx, p.ID, v)} {
			if check != nil {
				return nil, check
			}
		}
		adopt = append(adopt, id)
	}
	n, err := registry.AdoptQuietly(ctx, tx, p.ID, adopt, actor)
	if err != nil {
		return nil, err
	}
	res.Adopted = n
	for _, a := range pl.Aliases {
		if a.Action != AliasSet {
			continue
		}
		id := res.ids[a.VersionID]
		if id == "" {
			res.Notes = append(res.Notes, fmt.Sprintf("alias @%s: its version is not here", a.Name))
			continue
		}
		if _, more, err := registry.SetAlias(ctx, tx, p.ID, a.Name, nil, id, actor); err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("alias @%s not set: %v", a.Name, err))
		} else {
			res.Aliases = append(res.Aliases, a.Name)
			drafts = append(drafts, more...)
		}
	}
	cur, err := projects.GetByID(ctx, tx, p.ID)
	if err != nil {
		return nil, err
	}
	if opt.BaseModel && pl.Bundle.Doc.Project.BaseModel != "" {
		if id := res.ids[pl.Bundle.Doc.Project.BaseModel]; id != "" && (cur.BaseModel == nil || cur.BaseModel.VersionID != id) {
			if _, err := registry.AdoptQuietly(ctx, tx, p.ID, []string{id}, actor); err != nil {
				return nil, err
			}
			edited, more, err := projects.Edit(ctx, tx, cur.Slug, cur.Rev, projects.EditInput{BaseModelVersionID: &id})
			if err != nil {
				return nil, err
			}
			cur, drafts = edited, append(drafts, more...)
			if cur.BaseModel != nil {
				res.BaseModel = &ResultVersion{ID: id, Collection: cur.BaseModel.Name, Version: cur.BaseModel.Version, Kind: registry.KindBaseModel}
			}
		}
	}
	if cur, err = projects.Touch(ctx, tx, cur.Slug, cur.Rev); err != nil {
		return nil, err
	}
	drafts = append(drafts, projects.Event(cur, "project.adopted", map[string]any{
		"bundle": map[string]any{"uri": pl.Bundle.URI, "commit": pl.Bundle.Doc.Commit, "registered": len(res.Registered),
			"reused": len(res.Reused), "adopted": res.Adopted},
	})...)
	if s.Facts != nil {
		msg := opt.Message
		if msg == "" {
			msg = "adopt bundle " + short(pl.Bundle.Doc.Commit) + " (" + pl.Bundle.URI + ")"
		}
		more, err := s.Facts.CommitFacts(ctx, tx, cur, actor, msg, false)
		if err != nil {
			return nil, err
		}
		drafts = append(drafts, more...)
	}
	return drafts, nil
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// order puts every version after the bundle's versions its payload names (the exporter writes them so already;
// a hand-made bundle may not).
func order(list []PlanVersion) []PlanVersion {
	byID := map[string]int{}
	for i, pv := range list {
		byID[pv.Entry.Record.VersionID] = i
	}
	state := make([]int, len(list)) // 0 new, 1 on the stack, 2 done
	out := make([]PlanVersion, 0, len(list))
	var visit func(i int)
	visit = func(i int) {
		if state[i] != 0 {
			return
		}
		state[i] = 1
		refs, _ := lineage.RefsIn(list[i].Entry.Record.Payload)
		for _, r := range refs {
			if j, ok := byID[r.ID]; ok && j != i {
				visit(j)
			}
		}
		state[i] = 2
		out = append(out, list[i])
	}
	for i := range list {
		visit(i)
	}
	return out
}

// versionDate is the date a version string carries.
func versionDate(v string) time.Time {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.DateOnly, m[1])
	if err != nil {
		return time.Time{}
	}
	return t
}

// remap replaces every string of doc equal to a bundle version id with the id of the version here.
func remap(doc json.RawMessage, ids map[string]string) (json.RawMessage, error) {
	if len(doc) == 0 {
		return doc, nil
	}
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	var walk func(any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			for k, e := range t {
				t[k] = walk(e)
			}
		case []any:
			for i, e := range t {
				t[i] = walk(e)
			}
		case string:
			if id, ok := ids[t]; ok {
				return id
			}
		}
		return v
	}
	return json.Marshal(walk(v))
}

// register registers one version from the bundle.
func (s *Service) register(ctx context.Context, tx pgx.Tx, b *Bundle, e exports.BundleEntry, p projects.Project, actor auth.Actor,
	opt Options, res *Result) (registry.Version, []events.Draft, error) {
	r := e.Record
	on := versionDate(r.Version)
	for _, h := range e.Artifacts { // what the payload names is in the store; index it before anything reads it
		a, _ := b.artifact(h)
		if a.Type == "blob" {
			continue
		}
		if _, err := artifacts.Record(ctx, tx, s.CAS, steps.ArtifactRef{Hash: a.Hash, Type: a.Type, Size: dirSize(a), Meta: a.Meta}, "", nil); err != nil {
			return registry.Version{}, nil, invalid("artifact %s (%s): %v", a.Hash, a.Type, err)
		}
	}
	switch {
	case e.Dataset != "":
		return s.registerDataset(ctx, tx, b, e, p, actor, on)
	case r.Kind == registry.KindGoldenSet:
		return s.registerGoldenSet(ctx, tx, e, actor, opt, on, res)
	}
	payload, err := remap(r.Payload, res.ids)
	if err != nil {
		return registry.Version{}, nil, err
	}
	v, _, drafts, err := registry.Register(ctx, tx, registry.RegisterInput{Kind: r.Kind, Name: r.Collection, Description: r.Description,
		Tags: r.Tags, Licence: r.Licence, Payload: payload, Actor: actor, Freeze: true, Fingerprint: r.Fingerprint, On: on}, s.now())
	return v, drafts, err
}

func dirSize(a exports.BundleArtifact) int64 {
	if !a.Directory {
		return a.Size
	}
	var n int64
	for _, f := range a.Files {
		n += f.Size
	}
	return n
}

// registerDataset re-imports a dataset version or noise bank from its dataset bundle: the sources first (cleared for
// training when the record says so), then the artifact with its header rewritten as dataset_import's cadence-bundle
// reader does (a freeze's cut becomes an import), through the dataset importer.
func (s *Service) registerDataset(ctx context.Context, tx pgx.Tx, b *Bundle, e exports.BundleEntry, p projects.Project,
	actor auth.Actor, on time.Time) (registry.Version, []events.Draft, error) {
	r := e.Record
	db, err := s.datasetDoc(ctx, b, e)
	if err != nil {
		return registry.Version{}, nil, err
	}
	var drafts []events.Draft
	for _, src := range r.Sources {
		got, created, more, err := data.Ensure(ctx, tx, data.SourceInput{Name: src.Name, Licence: src.Licence, Kind: src.Kind,
			Languages: src.Languages, URL: src.URL}, actor, s.now())
		if err != nil {
			return registry.Version{}, nil, invalid("source %s: %v", src.Name, err)
		}
		drafts = append(drafts, more...)
		if src.TrainingCleared && !got.TrainingCleared && created {
			yes := true
			_, more, err := data.Edit(ctx, tx, got.ID, got.Rev, data.EditInput{TrainingCleared: &yes}, actor, s.now())
			if err != nil {
				return registry.Version{}, nil, err
			}
			drafts = append(drafts, more...)
		}
	}
	m, err := s.CAS.ReadManifest(db.Artifact)
	if err != nil {
		return registry.Version{}, nil, invalid("dataset bundle %s: its manifest %s: %v", e.Dataset, db.Artifact, err)
	}
	ref, err := s.rewriteHeader(m, r)
	if err != nil {
		return registry.Version{}, nil, err
	}
	if _, err := artifacts.Record(ctx, tx, s.CAS, ref, p.ID, nil); err != nil {
		return registry.Version{}, nil, invalid("dataset bundle %s: %v", e.Dataset, err)
	}
	im := &data.Importer{CAS: s.CAS, Now: s.Now, VersionOn: on, Actor: &actor}
	v, more, err := im.Import(ctx, tx, steps.Output{ProjectID: p.ID, Artifact: ref})
	if err != nil {
		return registry.Version{}, nil, err
	}
	return v, append(drafts, more...), nil
}

// rewriteHeader stores the dataset artifact with dataset.json naming the record's source and collection and none of
// a freeze's draft, ingest or mining links.
func (s *Service) rewriteHeader(m cas.Manifest, r exports.Record) (steps.ArtifactRef, error) {
	i := slices.IndexFunc(m.Files, func(f cas.File) bool { return f.Path == "dataset.json" })
	if i < 0 {
		return steps.ArtifactRef{}, invalid("%s %s: the dataset artifact has no dataset.json", r.Collection, r.Version)
	}
	f, err := s.CAS.Open(m.Files[i].Hash)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxDoc))
	_ = f.Close()
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	var h map[string]any
	if err := json.Unmarshal(raw, &h); err != nil {
		return steps.ArtifactRef{}, invalid("%s %s: dataset.json: %v", r.Collection, r.Version, err)
	}
	for _, k := range []string{"draftVersionId", "sourceInfo", "steps", "mined"} {
		delete(h, k)
	}
	srcName := ""
	if hs, ok := h["source"].(map[string]any); ok {
		srcName, _ = hs["name"].(string)
	}
	var src *exports.RecordSource
	for i := range r.Sources {
		if r.Sources[i].Name == srcName || len(r.Sources) == 1 {
			src = &r.Sources[i]
		}
	}
	if src == nil {
		return steps.ArtifactRef{}, invalid("%s %s: the record names no source %q", r.Collection, r.Version, srcName)
	}
	hs := map[string]any{"name": src.Name, "licence": src.Licence, "kind": src.Kind, "languages": src.Languages}
	if src.URL != "" {
		hs["url"] = src.URL
	}
	h["source"] = hs
	_, name, _ := strings.Cut(r.Collection, "/")
	h["name"] = name
	if slices.Contains(r.Tags, data.TagEvalOnly) {
		h["evalOnly"] = true
	}
	body, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	hh, err := s.CAS.PutBytes(append(body, '\n'))
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	files := slices.Clone(m.Files)
	files[i] = cas.File{Path: "dataset.json", Hash: hh, Size: int64(len(body) + 1)}
	ah, err := s.CAS.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	var size int64
	for _, f := range files {
		size += f.Size
	}
	return steps.ArtifactRef{Hash: ah, Type: data.ArtifactType, Size: size}, nil
}

// registerGoldenSet re-freezes a golden set here: its dataset version and normalizer mapped to the versions here,
// through goldensets.Prepare (eval-only data, leakage against this instance's training data), under the bundle's
// fingerprint and version date.
func (s *Service) registerGoldenSet(ctx context.Context, tx pgx.Tx, e exports.BundleEntry, actor auth.Actor, opt Options,
	on time.Time, res *Result) (registry.Version, []events.Draft, error) {
	r := e.Record
	var gp goldensets.Payload
	if err := json.Unmarshal(r.Payload, &gp); err != nil {
		return registry.Version{}, nil, invalid("golden set %s: payload: %v", r.Collection, err)
	}
	ds, nl := res.ids[gp.DatasetVersionID], res.ids[gp.NormalizerVersionID]
	if ds == "" || nl == "" {
		return registry.Version{}, nil, invalid("golden set %s %s: its dataset version or normalizer is not in the bundle", r.Collection, r.Version)
	}
	plan, err := goldensets.Prepare(ctx, tx, goldensets.FreezeInput{Dataset: ds, Normalizer: nl, Name: r.Collection, Domain: gp.Domain,
		Groups: gp.Groups, Actor: actor, ApprovalID: opt.ApprovalID, Annotation: gp.Annotation}, s.defaults())
	if err != nil {
		return registry.Version{}, nil, err
	}
	plan.Register.Fingerprint, plan.Register.On = r.Fingerprint, on
	if r.Description != "" {
		plan.Register.Description = r.Description
	}
	v, created, drafts, err := registry.Register(ctx, tx, plan.Register, s.now())
	if err != nil {
		return registry.Version{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golden_sets (version_id, dataset_version_id, normalizer_version_id, created_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT (version_id) DO NOTHING`, v.ID, plan.Dataset.ID, plan.Normalizer.ID, s.now()); err != nil {
		return registry.Version{}, nil, fmt.Errorf("record golden set: %w", err)
	}
	if created {
		drafts = append(drafts, registry.VersionEvent(v, "golden_set.frozen"))
	}
	return v, drafts, nil
}

// ---------------------------------------------------------------- the bundles.adopt job

type adoptArgs struct {
	ProjectID  string `json:"projectId"`
	Bundle     string `json:"bundle"`
	Aliases    bool   `json:"aliases"`
	ApprovalID string `json:"approvalId,omitempty"`
}

// Register adds the bundles.adopt job kind; call it before the job service starts.
func (s *Service) Register(js *jobs.Service) {
	s.Jobs = js
	js.Register(JobAdopt, s.runAdopt, jobs.KindOptions{MaxAttempts: 2, Timeout: 24 * time.Hour})
}

// EnqueueAdopt queues the import of bundle into project projectID (in the command's transaction).
func (s *Service) EnqueueAdopt(ctx context.Context, tx pgx.Tx, projectID, bundle string, aliases bool, approvalID string) (jobs.Job, []events.Draft, error) {
	if s.Jobs == nil {
		return jobs.Job{}, nil, errors.New("bundles: the job kind is not registered")
	}
	return s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobAdopt, ProjectID: projectID,
		Args: adoptArgs{ProjectID: projectID, Bundle: bundle, Aliases: aliases, ApprovalID: approvalID}})
}

func (s *Service) runAdopt(ctx context.Context, r *jobs.Run) (any, error) {
	var args adoptArgs
	if err := json.Unmarshal(r.Args, &args); err != nil {
		return nil, fmt.Errorf("read the import's args: %w", err)
	}
	p, err := projects.GetByID(ctx, s.Pool, args.ProjectID)
	if err != nil {
		return nil, err
	}
	b, err := s.Open(ctx, s.Pool, args.Bundle)
	if err != nil {
		return nil, err
	}
	return s.Import(ctx, b, p, r.Job.Actor, Options{Aliases: args.Aliases, ApprovalID: args.ApprovalID}, func(f float64, msg string) {
		_ = r.Progress(ctx, f, msg)
	})
}
