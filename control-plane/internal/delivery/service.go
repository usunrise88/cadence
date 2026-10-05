package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/promotions"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

// JobKind is the control-plane job that writes a bundle.
const JobKind = "delivery.build"

// Bundle states (promotion_deliveries.state).
const (
	StateBuilding = "building"
	StateReady    = "ready"
	StateFailed   = "failed"
)

// EventDelivery is emitted on the target's topic when a bundle is ready or failed.
const EventDelivery = "promotion.delivery"

// Sources supply what a bundle carries beyond the record.
type Sources interface {
	// ModelFiles lists the model directory of a deployable artifact (cadence.deployable/1), paths relative to it.
	ModelFiles(ctx context.Context, deployableHash string) ([]ModelFile, error)
	// Smoke is the record's smoke set: up to MaxSmoke utterances of its export's parity sample with the text the
	// staging server wrote, and the family's smoke client.
	Smoke(ctx context.Context, rec promotions.Record) (Smoke, error)
	// Decoding is the record's decoding configuration (decoding.boostLists) as files.
	Decoding(ctx context.Context, rec promotions.Record) ([]DecodingFile, error)
}

// Service queues and runs bundle builds.
type Service struct {
	Pool      *pgxpool.Pool
	CAS       *cas.Store
	Jobs      *jobs.Service
	Templates fs.FS
	Sources   Sources
	Defaults  func() *defaults.Defaults
	Log       *slog.Logger
}

type buildArgs struct {
	RecordID string `json:"recordId"`
}

// Register registers the build job; call it before the job service starts.
func (s *Service) Register(js *jobs.Service) {
	js.Register(JobKind, s.run, jobs.KindOptions{MaxAttempts: 2, Timeout: time.Hour})
}

// Build queues the bundle of a promotion or rollback record in the caller's transaction (stream D4 calls it right
// after promotions.Append; the plan's delivery.Build). The record's delivery is `building` until the job ends.
func (s *Service) Build(ctx context.Context, tx pgx.Tx, recordID string) (jobs.Job, []events.Draft, error) {
	if s.Jobs == nil {
		return jobs.Job{}, nil, problems.Internal.New("no job service: cannot build delivery bundles")
	}
	var kind string
	if err := tx.QueryRow(ctx, "SELECT kind FROM promotion_records WHERE id = $1", recordID).Scan(&kind); errors.Is(err, pgx.ErrNoRows) {
		return jobs.Job{}, nil, problems.NotFound.New("no promotion record %q", recordID)
	} else if err != nil {
		return jobs.Job{}, nil, fmt.Errorf("read promotion record: %w", err)
	}
	if !promotions.Pends(kind) {
		return jobs.Job{}, nil, problems.ValidationFailed.New("a %s record has no delivery bundle", kind)
	}
	j, drafts, err := s.Jobs.Enqueue(ctx, tx, jobs.Spec{Kind: JobKind, Args: buildArgs{RecordID: recordID}})
	if err != nil {
		return jobs.Job{}, nil, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO promotion_deliveries (record_id, state, job_id) VALUES ($1, 'building', $2)
		ON CONFLICT (record_id) DO UPDATE SET state = 'building', job_id = $2, error = NULL, updated_at = now()`, recordID, j.ID); err != nil {
		return jobs.Job{}, nil, fmt.Errorf("record the delivery build: %w", err)
	}
	return j, drafts, nil
}

// Info is a record's delivery as promotions.get shows it.
type Info struct {
	State         string `json:"state"`
	JobID         string `json:"jobId,omitempty"`
	ArtifactHash  string `json:"artifactHash,omitempty"`
	Error         string `json:"error,omitempty"`
	Script        string `json:"script,omitempty"`
	SmokeTotal    *int   `json:"-"`
	SmokeRequired *int   `json:"-"`
}

// Get reads a record's delivery (nil when none was queued).
func Get(ctx context.Context, q storage.Querier, recordID string) (*Info, error) {
	var in Info
	err := q.QueryRow(ctx, `SELECT state, coalesce(job_id, ''), coalesce(artifact_hash, ''), coalesce(error, ''),
			coalesce(script, ''), smoke_total, smoke_required FROM promotion_deliveries WHERE record_id = $1`, recordID).
		Scan(&in.State, &in.JobID, &in.ArtifactHash, &in.Error, &in.Script, &in.SmokeTotal, &in.SmokeRequired)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read delivery of %s: %w", recordID, err)
	}
	return &in, nil
}

func (s *Service) run(ctx context.Context, run *jobs.Run) (any, error) {
	var args buildArgs
	if err := json.Unmarshal(run.Args, &args); err != nil {
		return nil, fmt.Errorf("delivery.build args: %w", err)
	}
	out, err := s.build(ctx, args.RecordID)
	if err != nil {
		if _, uerr := s.Pool.Exec(context.WithoutCancel(ctx), `UPDATE promotion_deliveries SET state = 'failed', error = $2,
			updated_at = now() WHERE record_id = $1`, args.RecordID, err.Error()); uerr != nil && s.Log != nil {
			s.Log.ErrorContext(ctx, "record a failed delivery build", "record", args.RecordID, "err", uerr)
		}
		return nil, err
	}
	return out, nil
}

// Built is the job's result.
type Built struct {
	RecordID      string `json:"recordId"`
	ArtifactHash  string `json:"artifactHash"`
	Files         int    `json:"files"`
	ShipsModel    bool   `json:"shipsModel"`
	SmokeTotal    int    `json:"smokeTotal"`
	SmokeRequired int    `json:"smokeRequired"`
}

// Assemble gathers a record's bundle: the rendered script and every file. It reads the model, smoke and decoding
// sources and checks the model files against the record's deployable before anything is stored.
func (s *Service) Assemble(ctx context.Context, q storage.Querier, recordID string) ([]File, Values, error) {
	rec, chain, err := promotions.Get(ctx, q, recordID)
	if err != nil {
		return nil, Values{}, err
	}
	key, err := promotions.KeyByID(ctx, q, rec.KeyID)
	if err != nil {
		return nil, Values{}, err
	}
	return s.assemble(ctx, rec, chain, key)
}

func (s *Service) assemble(ctx context.Context, rec promotions.Record, chain promotions.Chain, key promotions.Key) ([]File, Values, error) {
	if !promotions.Pends(rec.Kind) {
		return nil, Values{}, fmt.Errorf("a %s record has no delivery bundle", rec.Kind)
	}
	if !rec.Verified {
		return nil, Values{}, fmt.Errorf("record %s does not verify (%s): no bundle is built for it", rec.ID, strings.Join(rec.Problems, "; "))
	}
	t := chain.Target
	body := rec.Body
	dep, _ := body["deployable"].(map[string]any)
	strOf := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	v := Values{
		RecordID: rec.ID, RecordHash: rec.Hash, Kind: rec.Kind, KeyID: rec.KeyID, Target: t.Name, Slot: rec.Slot,
		Stage: strOf(body, "stage"), ModelName: strOf(dep, "modelName"), ManifestSHA256: strOf(dep, "manifestSha256"),
		RepositoryPath: t.RepositoryPath, ServerKind: t.Server.Kind, TrafficShare: "1",
	}
	if v.Stage == "" {
		v.Stage = "production"
	}
	if share, ok := body["trafficShare"].(float64); ok {
		v.TrafficShare = promotions.FormatES6(share)
	}
	// A promotion ships its model directory unless the slot already runs the same deployable (a config-only
	// promotion: a changed boost list); a rollback restores a version that stayed installed.
	v.ShipModel = rec.Kind == promotions.KindPromotion && !installed(chain, rec, strOf(dep, "hash"))
	files := []File{
		{Path: "record.json", Data: rec.Canonical},
		{Path: "record.sig", Data: []byte(rec.Signature + "\n")},
		{Path: "instance.pub", Data: []byte(promotions.PublicPEM(key.Public))},
	}
	if v.ShipModel {
		mf, err := s.Sources.ModelFiles(ctx, strOf(dep, "hash"))
		if err != nil {
			return nil, Values{}, fmt.Errorf("model files of %s: %w", strOf(dep, "hash"), err)
		}
		entries, err := checkModel(mf, dep, v.ManifestSHA256)
		if err != nil {
			return nil, Values{}, err
		}
		for _, f := range mf {
			files = append(files, File{Path: "models/" + v.ModelName + "/" + f.Path, Hash: f.Hash})
		}
		files = append(files, File{Path: "models/" + v.ModelName + ".sha256", Data: []byte(ManifestText(entries))})
		smoke, err := s.Sources.Smoke(ctx, rec)
		if err != nil {
			return nil, Values{}, fmt.Errorf("smoke set: %w", err)
		}
		sf, err := smokeFiles(smoke)
		if err != nil {
			return nil, Values{}, err
		}
		files = append(files, sf...)
		share := 0.995
		if s.Defaults != nil {
			share = s.Defaults().Deploy.ParityMinIdenticalShare.Value
		}
		v.SmokeTotal = len(smoke.Items)
		v.SmokeRequired = promotions.SmokeRequired(v.SmokeTotal, share)
	}
	dec, err := s.Sources.Decoding(ctx, rec)
	if err != nil {
		return nil, Values{}, fmt.Errorf("decoding configuration: %w", err)
	}
	for _, d := range dec {
		files = append(files, File{Path: "decoding/" + d.File, Hash: d.Hash})
	}
	v.Decoding = dec
	script, err := Render(s.Templates, v)
	if err != nil {
		return nil, Values{}, err
	}
	files = append(files, File{Path: "deliver.sh", Data: []byte(script)})
	return files, v, nil
}

// installed reports whether an earlier confirmed promotion of the record's slot shipped the same deployable.
func installed(c promotions.Chain, rec promotions.Record, deployable string) bool {
	if deployable == "" {
		return false
	}
	for _, r := range c.Records {
		if r.Seq >= rec.Seq {
			break
		}
		if r.Kind == promotions.KindPromotion && r.Slot == rec.Slot && r.State == promotions.StateConfirmed {
			if d, _ := r.Body["deployable"].(map[string]any); d != nil && d["hash"] == deployable {
				return true
			}
		}
	}
	return false
}

// checkModel checks the model directory against the record: every path safe, the same files with the same SHA-256
// as the record's deployable.files, and the manifest hashing to manifestSha256.
func checkModel(mf []ModelFile, dep map[string]any, manifest string) ([]ManifestEntry, error) {
	if len(mf) == 0 {
		return nil, errors.New("the deployable has no model files")
	}
	want := map[string]string{}
	if list, ok := dep["files"].([]any); ok {
		for _, it := range list {
			if m, ok := it.(map[string]any); ok {
				p, _ := m["path"].(string)
				h, _ := m["sha256"].(string)
				want[p] = h
			}
		}
	}
	entries := make([]ManifestEntry, 0, len(mf))
	for _, f := range mf {
		if !SafePath(f.Path) {
			return nil, fmt.Errorf("model file %q: the delivery script handles letters, digits, . _ - in path segments only", f.Path)
		}
		if h, ok := want[f.Path]; !ok || h != f.SHA256 {
			return nil, fmt.Errorf("model file %s (sha256 %s) is not the record's (%q)", f.Path, f.SHA256, h)
		}
		delete(want, f.Path)
		entries = append(entries, ManifestEntry{Path: f.Path, SHA256: f.SHA256})
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("%d file(s) the record names are missing from the deployable", len(want))
	}
	if got := ManifestSHA256(entries); got != manifest {
		return nil, fmt.Errorf("the model directory's manifest hashes to %s, the record's to %s", got, manifest)
	}
	return entries, nil
}

func smokeFiles(sm Smoke) ([]File, error) {
	if len(sm.Items) == 0 {
		return nil, errors.New("the smoke set is empty: a promotion that ships a model is smoke-checked on the production host")
	}
	if len(sm.Items) > MaxSmoke {
		return nil, fmt.Errorf("the smoke set has %d utterances; a bundle carries at most %d", len(sm.Items), MaxSmoke)
	}
	if len(sm.Client) == 0 {
		return nil, errors.New("the smoke set has no client (the family's smoke transcriber)")
	}
	var (
		files []File
		tsv   strings.Builder
		seen  = map[string]bool{}
	)
	for _, it := range sm.Items {
		if !pathSegRe.MatchString(it.Name) || it.Name == "manifest.tsv" || it.Name == "transcribe" || seen[it.Name] {
			return nil, fmt.Errorf("smoke file name %q: a unique name of letters, digits, . _ - (not manifest.tsv or transcribe)", it.Name)
		}
		seen[it.Name] = true
		if strings.ContainsAny(it.Text, "\t\n\r") {
			return nil, fmt.Errorf("smoke text of %s holds a tab or line break", it.Name)
		}
		files = append(files, File{Path: "smoke/" + it.Name, Hash: it.Hash})
		tsv.WriteString(it.Name + "\t" + it.Text + "\n")
	}
	return append(files, File{Path: "smoke/manifest.tsv", Data: []byte(tsv.String())},
		File{Path: "smoke/transcribe", Data: sm.Client}), nil
}

func (s *Service) build(ctx context.Context, recordID string) (Built, error) {
	files, v, err := s.Assemble(ctx, s.Pool, recordID)
	if err != nil {
		return Built{}, err
	}
	hash, size, err := Put(s.CAS, files)
	if err != nil {
		return Built{}, err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return Built{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var projectID, targetID string
	if err := tx.QueryRow(ctx, "SELECT coalesce(project_id, ''), target_id FROM promotion_records WHERE id = $1", recordID).
		Scan(&projectID, &targetID); err != nil {
		return Built{}, fmt.Errorf("read promotion record: %w", err)
	}
	meta, _ := json.Marshal(map[string]any{"format": Format, "recordId": recordID, "recordHash": v.RecordHash,
		"modelName": v.ModelName, "shipsModel": v.ShipModel, "smoke": v.SmokeTotal})
	if _, err := artifacts.Record(ctx, tx, s.CAS, steps.ArtifactRef{Hash: hash, Type: ArtifactType, Size: size, Meta: meta}, projectID, nil); err != nil {
		return Built{}, fmt.Errorf("record the delivery artifact: %w", err)
	}
	script := ""
	for _, f := range files {
		if f.Path == "deliver.sh" {
			script = string(f.Data)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE promotion_deliveries SET state = 'ready', artifact_hash = $2, script = $3, smoke_total = $4,
		smoke_required = $5, error = NULL, updated_at = now() WHERE record_id = $1`,
		recordID, hash, script, v.SmokeTotal, v.SmokeRequired); err != nil {
		return Built{}, fmt.Errorf("record the delivery bundle: %w", err)
	}
	if err := events.Append(ctx, tx, jobs.System, nil, []events.Draft{{Topic: targets.Topic(targetID), Type: EventDelivery,
		ProjectID: projectID, Payload: map[string]any{"id": recordID, "state": StateReady, "artifactHash": hash}}}); err != nil {
		return Built{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Built{}, fmt.Errorf("commit the delivery bundle: %w", err)
	}
	return Built{RecordID: recordID, ArtifactHash: hash, Files: len(files), ShipsModel: v.ShipModel,
		SmokeTotal: v.SmokeTotal, SmokeRequired: v.SmokeRequired}, nil
}

// StoreSources reads bundle inputs from the content store. The model directory comes from the deployable artifact
// (cadence.deployable/1: deployable.json names serving.modelDir). The smoke set and decoding files come from
// streams that have not landed yet (TODO markers below).
type StoreSources struct {
	CAS *cas.Store
}

// deployableJSON is the part of deployable.json the bundle reads (03 "Artifacts").
type deployableJSON struct {
	Serving struct {
		ModelDir string `json:"modelDir"`
	} `json:"serving"`
}

// ModelFiles implements Sources: the files under serving.modelDir of the deployable, with their SHA-256 computed
// from the stored bytes.
func (s StoreSources) ModelFiles(_ context.Context, deployableHash string) ([]ModelFile, error) {
	m, err := s.CAS.ReadManifest(deployableHash)
	if err != nil {
		return nil, fmt.Errorf("the deployable is not a directory in the content store: %w", err)
	}
	var meta deployableJSON
	for _, f := range m.Files {
		if f.Path != "deployable.json" {
			continue
		}
		r, err := s.CAS.Open(f.Hash)
		if err != nil {
			return nil, err
		}
		err = json.NewDecoder(io.LimitReader(r, 16<<20)).Decode(&meta)
		_ = r.Close()
		if err != nil {
			return nil, fmt.Errorf("deployable.json: %w", err)
		}
	}
	dir := strings.Trim(meta.Serving.ModelDir, "/")
	if dir == "" {
		return nil, errors.New("deployable.json names no serving.modelDir")
	}
	var out []ModelFile
	for _, f := range m.Files {
		rel, ok := strings.CutPrefix(f.Path, dir+"/")
		if !ok {
			continue
		}
		sum, err := s.sha256(f.Hash)
		if err != nil {
			return nil, err
		}
		out = append(out, ModelFile{Path: rel, Hash: f.Hash, SHA256: sum, Size: f.Size})
	}
	return out, nil
}

func (s StoreSources) sha256(hash string) (string, error) {
	r, err := s.CAS.Open(hash)
	if err != nil {
		return "", err
	}
	defer func() { _ = r.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Smoke implements Sources without a source of smoke sets: a bundle that ships a model fails here. The server reads
// the smoke set of the record's export instead (internal/modelexports Service.Smoke: ≤ 20 utterances of the
// export's parity sample with what the staging server wrote for them, and the family's smoke client from the
// deployable), through SmokeFrom.
func (s StoreSources) Smoke(_ context.Context, rec promotions.Record) (Smoke, error) {
	return Smoke{}, fmt.Errorf("record %s: no source of smoke sets (model exports): %w", rec.ID, errNoSource)
}

// SmokeSource answers the smoke set of a model version's deployable (internal/modelexports).
type SmokeSource func(ctx context.Context, versionID, deployableHash string) (Smoke, error)

// WithSmoke is Sources with the smoke set read by smoke for the record's model version and deployable.
type WithSmoke struct {
	Sources
	From SmokeSource
}

// Smoke implements Sources.
func (w WithSmoke) Smoke(ctx context.Context, rec promotions.Record) (Smoke, error) {
	dep, _ := rec.Body["deployable"].(map[string]any)
	model, _ := rec.Body["model"].(map[string]any)
	hash, _ := dep["hash"].(string)
	versionID, _ := model["versionId"].(string)
	if hash == "" {
		return Smoke{}, fmt.Errorf("record %s names no deployable hash", rec.ID)
	}
	return w.From(ctx, versionID, hash)
}

// Decoding implements Sources. TODO(B, D4): static boost lists ship as decoding configuration; until streams D4
// and B land them, a record that names boost lists fails here, and one that names none ships no decoding/.
func (s StoreSources) Decoding(_ context.Context, rec promotions.Record) ([]DecodingFile, error) {
	dec, _ := rec.Body["decoding"].(map[string]any)
	if lists, _ := dec["boostLists"].([]any); len(lists) > 0 {
		return nil, fmt.Errorf("record %s names %d boost list(s); shipping them comes with streams D4 and B: %w", rec.ID, len(lists), errNoSource)
	}
	return nil, nil
}
