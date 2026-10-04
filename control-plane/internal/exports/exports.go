// Package exports runs dataset exports (docs/spec/03-pipelines-defaults.md "Interoperability";
// docs/review/2026-10-03-phase-4-plan.md "Waves", stream I): datasets.export plans an export of a frozen dataset
// version, starts it as a one-step pipeline run in a project and records it (dataset_exports, migration 0037); the
// `export` output hook (hook.go) completes the record and registers the content-store blobs the export placed on a
// mount unchanged as copies there (mounts.RecordCopy), so the cache may evict the version and datasets.materialize
// can bring it back.
//
// The control plane never interprets a format beyond choosing the step kind that writes it: shar_export@1 (Lhotse
// Shar), hf_push@1 (the Hugging Face Hub, an approval for everyone) and dataset_export@1 (every other format, the
// format passed through as a parameter). A cadence-bundle export also reads the version's registry record, which the
// control plane renders into the content store as a `registry_record` artifact.
package exports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Kind is the entity kind in references and topics (entity.export.{id}).
const Kind = "export"

// Operation is the command that starts an export.
const Operation = "datasets.export"

// Event types on entity.export.{id}.
const (
	EventStarted = "export.started"
	EventDone    = "export.done"
)

// Formats the control plane names (the contract's DatasetExportFormat lists every one; the rest reach
// dataset_export@1 as its format parameter).
const (
	FormatShar   = "lhotse-shar"
	FormatBundle = "cadence-bundle"
	FormatHub    = "hf-hub"
)

// Artifact types an export reads and writes.
const (
	ArtifactType = "export"          // export.json and, with target cas, files/
	RecordType   = "registry_record" // a version's registry record and its sources (cadence-bundle)
	RecordFormat = "cadence.registry-record/1"
)

// The step kinds that write exports (core, runtime-neutral: worker/cadence_worker/steps).
const (
	SharKind    = "shar_export@1"
	HubKind     = "hf_push@1"
	GenericKind = "dataset_export@1"
)

// TargetCAS keeps the export in the content store.
const TargetCAS = "cas"

// StepID is the export step's id in the pipeline datasets.export starts (the row keeps the step's pls_ id, as the
// output hook sees it).
const StepID = "export"

// Topic is an export's topic.
func Topic(id string) string { return events.EntityTopic(Kind, id) }

// Request is datasets.export's body.
type Request struct {
	Version    string
	Format     string
	Project    string // slug
	Target     string
	HubRepo    string
	HubPrivate *bool
}

// Plan is what datasets.export would start (the contract's DatasetExportPlan).
type Plan struct {
	Version    registry.Version
	Format     string
	ProjectID  string
	Project    string
	Target     string
	StepKind   string
	Utterances int
	Hours      float64
	Bytes      int64
	Licence    string
	Sources    []string
	Approval   bool
	Copies     bool
	Dataset    steps.ArtifactRef
	Params     map[string]any
}

// datasetPayload is what an export reads of a dataset version's payload.
type datasetPayload struct {
	Frozen     *bool             `json:"frozen"`
	Utterances int               `json:"utterances"`
	Hours      float64           `json:"hours"`
	Bytes      int64             `json:"bytes"`
	Licence    string            `json:"licence"`
	SourceIDs  []string          `json:"sourceIds"`
	Artifact   steps.ArtifactRef `json:"artifact"`
	Lineage    struct {
		ProjectID string `json:"projectId"`
	} `json:"lineage"`
	Recipe *struct {
		ProjectID string `json:"projectId"`
	} `json:"recipe"`
}

type source struct {
	ID, Name, Licence, Kind, URL string
	Languages                    []string
	TrainingCleared              bool
}

func readSources(ctx context.Context, q storage.Querier, ids []string) ([]source, error) {
	rows, err := q.Query(ctx, `SELECT id, name, licence, kind, url, languages, training_cleared FROM sources
		WHERE id = ANY($1) ORDER BY name`, ids)
	if err != nil {
		return nil, fmt.Errorf("read the version's sources: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (source, error) {
		var s source
		err := row.Scan(&s.ID, &s.Name, &s.Licence, &s.Kind, &s.URL, &s.Languages, &s.TrainingCleared)
		return s, err
	})
}

// StepKindOf is the step kind that writes format.
func StepKindOf(format string) string {
	switch format {
	case FormatShar:
		return SharKind
	case FormatHub:
		return HubKind
	}
	return GenericKind
}

// PlanExport checks req and answers what the export would do: the version must be a frozen dataset version with a
// content-store artifact, the project must exist (default: the one the version was ingested or imported in), and the
// target must be the content store or a directory on a writable path mount. A Hub push names its repository and is
// refused for production sources, sources without a usable licence and versions a golden set is built on.
func PlanExport(ctx context.Context, q storage.Querier, d *defaults.Defaults, req Request) (Plan, error) {
	v, err := registry.GetVersion(ctx, q, registry.KindDataset, req.Version)
	if err != nil {
		return Plan{}, err
	}
	var p datasetPayload
	if err := json.Unmarshal(v.Payload, &p); err != nil {
		return Plan{}, fmt.Errorf("decode dataset %s: %w", v.ID, err)
	}
	if v.State == registry.StateDraft || (p.Frozen != nil && !*p.Frozen) {
		return Plan{}, problems.DatasetNotFrozen.New("%s %s is a draft; only frozen versions are exported — freeze it with datasets.freeze first",
			v.Name, v.Version)
	}
	if p.Artifact.Hash == "" {
		return Plan{}, problems.ValidationFailed.New("%s %s has no content-store artifact (a fixture): there is nothing to export", v.Name, v.Version)
	}
	pl := Plan{Version: v, Format: req.Format, StepKind: StepKindOf(req.Format), Utterances: p.Utterances, Hours: p.Hours,
		Bytes: p.Bytes, Licence: p.Licence, Dataset: steps.ArtifactRef{Hash: p.Artifact.Hash, Type: data.ArtifactType, Size: p.Artifact.Size}}
	if pl.Licence == "" {
		pl.Licence = v.Licence
	}
	srcs, err := readSources(ctx, q, p.SourceIDs)
	if err != nil {
		return Plan{}, err
	}
	pl.Sources = []string{}
	for _, s := range srcs {
		pl.Sources = append(pl.Sources, s.Name)
	}
	if err := pl.project(ctx, q, req.Project, p); err != nil {
		return Plan{}, err
	}
	collection := strings.TrimPrefix(v.Name, "dataset/")
	pl.Params = map[string]any{"version": v.ID, "name": collection}
	if req.Format == FormatHub {
		if err := hubAllowed(ctx, q, v, srcs); err != nil {
			return Plan{}, err
		}
		if req.HubRepo == "" {
			return Plan{}, problems.Validation([]problems.FieldError{{Path: "/hubRepo", Message: "hf-hub needs hubRepo (<org>/<name>)"}})
		}
		if req.Target != "" {
			return Plan{}, problems.Validation([]problems.FieldError{{Path: "/target", Message: "hf-hub pushes to hubRepo; it takes no target"}})
		}
		private := d.Storage.ExportHubPrivate.Value
		if req.HubPrivate != nil {
			private = *req.HubPrivate
		}
		pl.Target, pl.Approval = "hf://datasets/"+req.HubRepo, true
		pl.Params["repo"], pl.Params["private"], pl.Params["licence"] = req.HubRepo, private, pl.Licence
		return pl, nil
	}
	if req.HubRepo != "" || req.HubPrivate != nil {
		return Plan{}, problems.Validation([]problems.FieldError{{Path: "/hubRepo", Message: "hubRepo and hubPrivate are for format hf-hub only"}})
	}
	if pl.Target, err = target(ctx, q, d, req.Target, path.Join(collection, v.Version, req.Format)); err != nil {
		return Plan{}, err
	}
	pl.Params["target"] = pl.Target
	if req.Format != FormatShar {
		pl.Params["format"] = req.Format
		pl.Copies = pl.Target != TargetCAS
	}
	return pl, nil
}

// project resolves the project the export runs in.
func (pl *Plan) project(ctx context.Context, q storage.Querier, slug string, p datasetPayload) error {
	var (
		pr  projects.Project
		err error
	)
	switch {
	case slug != "":
		pr, err = projects.Get(ctx, q, slug)
	case p.Recipe != nil && p.Recipe.ProjectID != "":
		pr, err = projects.GetByID(ctx, q, p.Recipe.ProjectID)
	case p.Lineage.ProjectID != "":
		pr, err = projects.GetByID(ctx, q, p.Lineage.ProjectID)
	default:
		return problems.Validation([]problems.FieldError{{Path: "/project",
			Message: "the version was not ingested or imported in a project; name the project the export runs in"}})
	}
	if err != nil {
		return err
	}
	pl.ProjectID, pl.Project = pr.ID, pr.Slug
	return nil
}

// hubAllowed is the licence check before a Hub push (spec 03 "Interoperability": "after a licence check"): nothing
// from a production source (customers' audio) or a source without a usable licence, and never a golden set's data
// (held-out test audio made public is no longer held out).
func hubAllowed(ctx context.Context, q storage.Querier, v registry.Version, srcs []source) error {
	var bad []string
	for _, s := range srcs {
		switch {
		case s.Kind == "production":
			bad = append(bad, fmt.Sprintf("source %s is production audio", s.Name))
		case data.Unlicensed(s.Licence):
			bad = append(bad, fmt.Sprintf("source %s has no usable licence (%q)", s.Name, s.Licence))
		}
	}
	if len(srcs) == 0 {
		bad = append(bad, "the version names no source, so its licence is unknown")
	}
	var golden string
	err := q.QueryRow(ctx, "SELECT version_id FROM golden_sets WHERE dataset_version_id = $1 LIMIT 1", v.ID).Scan(&golden)
	switch {
	case err == nil:
		bad = append(bad, "golden set "+golden+" is built on it (held-out test audio)")
	case !errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("check golden sets: %w", err)
	}
	if len(bad) > 0 {
		return problems.ExportNotAllowed.New("%s %s is not pushed to the Hugging Face Hub: %s", v.Name, v.Version, strings.Join(bad, "; "))
	}
	return nil
}

// target resolves the export's target: cas, a directory on a writable path mount, or (empty) the default — the
// storage.export_mount mount under rel when it is registered, writable and a path mount, else cas.
func target(ctx context.Context, q storage.Querier, d *defaults.Defaults, want, rel string) (string, error) {
	if want == TargetCAS {
		return TargetCAS, nil
	}
	if want == "" {
		m, err := mounts.Get(ctx, q, d.Storage.ExportMount.Value)
		if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
			return TargetCAS, nil
		}
		if err != nil {
			return "", err
		}
		if m.ReadOnly || !mounts.PathKind(m.Kind) {
			return TargetCAS, nil
		}
		return mounts.Scheme + m.Name + "/" + rel, nil
	}
	bad := func(format string, args ...any) error {
		return problems.Validation([]problems.FieldError{{Path: "/target", Message: fmt.Sprintf(format, args...)}})
	}
	u, err := mounts.ParseURI(want)
	if err != nil {
		return "", bad("%v (cas or mount://<mount>/<directory>)", err)
	}
	if u.Start != nil || u.Channel != nil {
		return "", bad("a target is a directory; it takes no #t or ch fragment")
	}
	m, err := mounts.Get(ctx, q, u.Mount)
	if err != nil {
		return "", err
	}
	switch {
	case m.ReadOnly:
		return "", bad("mount %s is read-only; exports go to a writable mount (mounts.new with readOnly false)", m.Name)
	case !mounts.PathKind(m.Kind):
		return "", bad("mount %s is %s; exports are written to a path mount (local, nfs, smb)", m.Name, m.Kind)
	case m.Health.State == mounts.HealthUnhealthy:
		return "", problems.MountUnhealthy.New("mount %s failed its last health check (%s); fix it and run mounts.verify", m.Name, m.Health.Detail)
	}
	return u.String(), nil
}

// Export is one dataset export (the contract's DatasetExport).
type Export struct {
	ID            string
	VersionID     string
	Collection    string
	Version       string
	ProjectID     string
	Format        string
	Target        string
	State         string
	PipelineRunID string
	StepKind      string
	Artifact      string
	Files         int
	Bytes         int64
	Copies        int
	Sample        []File
	Hub           *Hub
	Error         string
	CreatedBy     auth.Actor
	ApprovalID    string
	CreatedAt     time.Time
	FinishedAt    *time.Time
	Rev           int
}

// File is a file an export wrote.
type File struct {
	Path  string `json:"path"`
	Hash  string `json:"hash,omitempty"`
	Bytes int64  `json:"bytes"`
}

// Hub is where a Hub push went.
type Hub struct {
	Repo    string `json:"repo"`
	Commit  string `json:"commit,omitempty"`
	URL     string `json:"url,omitempty"`
	Private *bool  `json:"private,omitempty"`
}

// Start renders the bundle's registry record when the format needs it, starts the export's pipeline run in the
// plan's project and records the export. An identical earlier export is reused by its input hash (the hook then ran
// inside Start and the export comes back done).
func Start(ctx context.Context, tx pgx.Tx, eng *pipelines.Engine, store *cas.Store, pl Plan, actor auth.Actor, approvalID string) (Export, []events.Draft, error) {
	inputs := map[string]steps.ArtifactRef{"dataset": pl.Dataset}
	pipe := &pipelines.Pipeline{Name: "dataset-export", Description: "Export a frozen dataset version (" + pl.Format + ")",
		Inputs: map[string]string{"dataset": data.ArtifactType},
		Steps: []pipelines.Step{{ID: StepID, Kind: pl.StepKind, In: map[string]string{"dataset": pipelines.InputsRef + "dataset"},
			Params: pl.Params}}}
	if pl.Format == FormatBundle {
		rec, err := RenderRecord(ctx, tx, store, pl.Version)
		if err != nil {
			return Export{}, nil, err
		}
		inputs["record"] = rec
		pipe.Inputs["record"] = RecordType
		pipe.Steps[0].In["record"] = pipelines.InputsRef + "record"
	}
	r, drafts, err := eng.Start(ctx, tx, pipelines.StartInput{ProjectID: pl.ProjectID, Pipeline: pipe, Inputs: inputs, Actor: actor})
	if err != nil {
		return Export{}, nil, err
	}
	stepID := ""
	for _, st := range r.Steps {
		if st.Step == StepID {
			stepID = st.ID
		}
	}
	id := "dex_" + uuid.Must(uuid.NewV7()).String()
	if err := tx.QueryRow(ctx, `INSERT INTO dataset_exports (id, version_id, project_id, format, target, pipeline_run_id, step_id,
			step_kind, created_by, approval_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''))
		ON CONFLICT (pipeline_run_id, step_id) DO UPDATE SET version_id = excluded.version_id, format = excluded.format,
			target = excluded.target, created_by = excluded.created_by, approval_id = excluded.approval_id
		RETURNING id`, id, pl.Version.ID, pl.ProjectID, pl.Format, pl.Target, r.ID, stepID, pl.StepKind, actor, approvalID).Scan(&id); err != nil {
		return Export{}, nil, fmt.Errorf("record the export: %w", err)
	}
	x, err := Get(ctx, tx, id)
	if err != nil {
		return Export{}, nil, err
	}
	drafts = append(drafts, draft(x, EventStarted))
	return x, drafts, nil
}

func draft(x Export, typ string) events.Draft {
	return events.Draft{Topic: Topic(x.ID), Type: typ, ProjectID: x.ProjectID, Entity: &events.EntityRef{Kind: Kind, ID: x.ID, Rev: x.Rev},
		Payload: map[string]any{"id": x.ID, "versionId": x.VersionID, "format": x.Format, "target": x.Target, "state": x.State,
			"pipelineRunId": x.PipelineRunID}}
}

// RenderRecord puts the registry record of dataset version v into the content store: the version (collection,
// version, licence, tags, fingerprint, payload) and its sources with their licences — what another instance needs to
// register the same version from a bundle.
func RenderRecord(ctx context.Context, q storage.Querier, store *cas.Store, v registry.Version) (steps.ArtifactRef, error) {
	if store == nil {
		return steps.ArtifactRef{}, errors.New("exports: no content store")
	}
	var p datasetPayload
	if err := json.Unmarshal(v.Payload, &p); err != nil {
		return steps.ArtifactRef{}, fmt.Errorf("decode dataset %s: %w", v.ID, err)
	}
	srcs, err := readSources(ctx, q, p.SourceIDs)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	type src struct {
		Name            string   `json:"name"`
		Licence         string   `json:"licence"`
		Kind            string   `json:"kind"`
		URL             string   `json:"url,omitempty"`
		Languages       []string `json:"languages"`
		TrainingCleared bool     `json:"trainingCleared"`
	}
	list := make([]src, 0, len(srcs))
	for _, s := range srcs {
		langs := append([]string{}, s.Languages...)
		sort.Strings(langs)
		list = append(list, src{Name: s.Name, Licence: s.Licence, Kind: s.Kind, URL: s.URL, Languages: langs, TrainingCleared: s.TrainingCleared})
	}
	tags := slices.Clone(v.Tags)
	sort.Strings(tags)
	doc := map[string]any{"format": RecordFormat, "kind": v.Kind, "collection": v.Name, "version": v.Version, "versionId": v.ID,
		"licence": v.Licence, "tags": tags, "fingerprint": v.Fingerprint, "payload": v.Payload, "sources": list}
	b, err := json.Marshal(doc) // map keys marshal sorted: one version, one artifact
	if err != nil {
		return steps.ArtifactRef{}, fmt.Errorf("encode the registry record: %w", err)
	}
	h, err := store.PutBytes(b)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	meta, _ := json.Marshal(map[string]string{"versionId": v.ID})
	return steps.ArtifactRef{Hash: h, Type: RecordType, Size: int64(len(b)), Meta: meta}, nil
}

const exportSelect = `SELECT x.id, coalesce(x.version_id, ''), coalesce(c.name, ''), coalesce(v.version, ''), x.project_id,
	x.format, x.target, coalesce(r.state, 'running'), coalesce(x.pipeline_run_id, ''), x.step_kind, coalesce(x.artifact, ''),
	x.files, x.bytes, x.copies, x.sample, x.hub, coalesce(r.error, ''), x.created_by, coalesce(x.approval_id, ''),
	x.created_at, coalesce(x.finished_at, r.finished_at), x.rev
	FROM dataset_exports x
	LEFT JOIN registry_versions v ON v.id = x.version_id
	LEFT JOIN registry_collections c ON c.id = v.collection_id
	LEFT JOIN pipeline_runs r ON r.id = x.pipeline_run_id`

func scanExport(row pgx.CollectableRow) (Export, error) {
	var (
		x      Export
		sample []byte
		hub    []byte
	)
	err := row.Scan(&x.ID, &x.VersionID, &x.Collection, &x.Version, &x.ProjectID, &x.Format, &x.Target, &x.State,
		&x.PipelineRunID, &x.StepKind, &x.Artifact, &x.Files, &x.Bytes, &x.Copies, &sample, &hub, &x.Error, &x.CreatedBy,
		&x.ApprovalID, &x.CreatedAt, &x.FinishedAt, &x.Rev)
	if err != nil {
		return x, err
	}
	if x.Artifact != "" {
		x.State, x.Error = "done", ""
	} else if x.State == "done" {
		x.State = "running" // the run is done and the hook has not recorded the export yet: never seen in one transaction
	}
	if err := json.Unmarshal(sample, &x.Sample); err != nil {
		return x, fmt.Errorf("decode export sample: %w", err)
	}
	if len(hub) > 0 {
		x.Hub = &Hub{}
		if err := json.Unmarshal(hub, x.Hub); err != nil {
			return x, fmt.Errorf("decode export hub: %w", err)
		}
	}
	return x, nil
}

// Get reads export id.
func Get(ctx context.Context, q storage.Querier, id string) (Export, error) {
	rows, err := q.Query(ctx, exportSelect+" WHERE x.id = $1", id)
	if err != nil {
		return Export{}, fmt.Errorf("read export: %w", err)
	}
	x, err := pgx.CollectOneRow(rows, scanExport)
	if errors.Is(err, pgx.ErrNoRows) {
		return Export{}, problems.NotFound.New("no export %q", id)
	}
	if err != nil {
		return Export{}, fmt.Errorf("read export: %w", err)
	}
	return x, nil
}

// List reads a project's exports, newest first; versionID narrows them to one dataset version.
func List(ctx context.Context, q storage.Querier, projectID, versionID string, limit int) ([]Export, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := q.Query(ctx, exportSelect+` WHERE x.project_id = $1 AND ($2 = '' OR x.version_id = $2)
		ORDER BY x.created_at DESC, x.id DESC LIMIT $3`, projectID, versionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list exports: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanExport)
	if err != nil {
		return nil, fmt.Errorf("list exports: %w", err)
	}
	return out, nil
}
