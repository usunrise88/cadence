// Package goldensets freezes golden sets and keeps them out of training (docs/spec/04-blocks.md Block 3,
// docs/spec/02-domain-projects-registry.md "Adoption re-runs the leakage check", docs/review/2026-10-02-phase-3-plan.md
// stream G).
//
// A golden set is a registry version (kind golden_set, collection golden-set/<name>) that ties a frozen dataset
// version registered eval-only to one scoring normalizer version. Freezing it is a registry-scope approval the admin
// decides (preset rule golden-set-freeze); the approved request runs Freeze. The leakage checks are three: at freeze
// the dataset may share no utterance (by content hash or by any fingerprint) with a dataset version not registered
// eval-only; data.Trainable refuses for training any dataset version that shares one with a golden set; and
// projects.adopt of a golden set is refused when a dataset version the project trained on overlaps it (CheckAdoption).
package goldensets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Operation is the command that freezes a golden set.
const Operation = "goldenSets.freeze"

// Resampling units of the bootstrap (R54), as the contract's GoldenSetPayload.groups.
const (
	GroupsCall      = "call"
	GroupsSpeaker   = "speaker"
	GroupsUtterance = "utterance"
)

// Payload is a golden set version's registry payload (the contract's GoldenSetPayload).
type Payload struct {
	DatasetVersionID    string  `json:"datasetVersionId"`
	DatasetHash         string  `json:"datasetHash"`
	NormalizerVersionID string  `json:"normalizerVersionId"`
	Locale              string  `json:"locale"`
	Domain              string  `json:"domain,omitempty"`
	Utterances          int     `json:"utterances"`
	Hours               float64 `json:"hours"`
	Fingerprint         string  `json:"fingerprint"`
	Groups              string  `json:"groups"`
	ApprovalID          string  `json:"approvalId,omitempty"`
	// Annotation is set on a golden set frozen from an annotation batch (batches.freeze, phase 4 · stream A, R27).
	Annotation *Annotation `json:"annotation,omitempty"`
}

// Annotation is the card of a golden set an annotation batch froze: the batch, the guidelines commit it was annotated
// under and the inter-annotator agreement (the contract's GoldenSetPayload.annotation).
type Annotation struct {
	BatchID    string `json:"batchId"`
	Guidelines struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		Commit string `json:"commit"`
	} `json:"guidelines"`
	IAAWER      *float64 `json:"iaaWer,omitempty"`
	Pairs       int      `json:"pairs"`
	Items       int      `json:"items"`
	Adjudicated int      `json:"adjudicated"`
}

// FreezeInput is a goldenSets.freeze request.
type FreezeInput struct {
	Dataset    string // ver_… or a dataset collection name (its newest frozen version)
	Normalizer string // ver_… or a normalizer collection name; empty: defaults.yaml eval.normalizer
	Name       string // golden-set/<name> or <name>; empty: the dataset collection's name
	Domain     string // empty: the dataset collection's domain:<x> tag
	Groups     string // call | speaker | utterance; empty: speaker when every utterance names one, else utterance
	Actor      auth.Actor
	ApprovalID string // the approval the request was replayed under
	// Annotation is the card of a batch-made golden set (batches.freeze); nil for goldenSets.freeze.
	Annotation *Annotation
}

// Plan is what a freeze registers, checked.
type Plan struct {
	Dataset    registry.Version
	Normalizer registry.Version
	Payload    Payload
	Register   registry.RegisterInput
}

// resolve finds a version by ver_ id or by collection name (prefix added to a bare name; names are lowercase).
func resolve(ctx context.Context, q storage.Querier, kind, prefix, ref string) (registry.Version, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "ver_") {
		return registry.GetVersion(ctx, q, kind, ref)
	}
	ref = strings.ToLower(ref)
	if !strings.Contains(ref, "/") {
		ref = prefix + ref
	}
	return registry.Latest(ctx, q, kind, ref)
}

// datasetPayload is what a freeze reads of a dataset version's payload.
type datasetPayload struct {
	EvalOnly bool     `json:"evalOnly"`
	Locales  []string `json:"locales"`
	Artifact struct {
		Hash string `json:"hash"`
	} `json:"artifact"`
}

// normalizerPayload is what a freeze reads of a normalizer version's payload.
type normalizerPayload struct {
	Locale string `json:"locale"`
}

// Prepare resolves and checks a freeze without writing: the dataset version is frozen and registered eval-only
// (golden-set-not-eval-only), the normalizer exists (normalizer-unknown) and fits the dataset's locale, the request's
// fields are valid, and the dataset shares nothing with a dataset version not registered eval-only
// (golden-set-leakage).
func Prepare(ctx context.Context, q storage.Querier, in FreezeInput, d *defaults.Defaults) (Plan, error) {
	ds, err := resolve(ctx, q, registry.KindDataset, "dataset/", in.Dataset)
	if err != nil {
		return Plan{}, err
	}
	if ds.State != registry.StateFrozen {
		return Plan{}, problems.Conflict.New("%s %s is %s; only a frozen dataset version can become a golden set", ds.Name, ds.Version, ds.State)
	}
	var dp datasetPayload
	if err := json.Unmarshal(ds.Payload, &dp); err != nil {
		return Plan{}, fmt.Errorf("decode dataset %s: %w", ds.ID, err)
	}
	if !dp.EvalOnly {
		return Plan{}, problems.GoldenSetNotEvalOnly.New(
			"%s %s is not registered eval-only; a golden set is frozen only from a dataset version imported for evaluation (dataset.evalOnly: true), so it was never trainable",
			ds.Name, ds.Version)
	}
	if dp.Artifact.Hash == "" {
		return Plan{}, problems.Conflict.New("%s %s has no dataset artifact (a fixture); only an imported dataset version can become a golden set", ds.Name, ds.Version)
	}

	ref := in.Normalizer
	if strings.TrimSpace(ref) == "" {
		ref = d.Eval.Normalizer.Value
	}
	nl, err := resolve(ctx, q, registry.KindNormalizer, "normalizer/", ref)
	if err != nil {
		if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
			return Plan{}, problems.NormalizerUnknown.New("no frozen scoring normalizer %q in the registry; normalizers.list shows the ones there are", ref)
		}
		return Plan{}, err
	}
	if nl.State != registry.StateFrozen {
		return Plan{}, problems.NormalizerUnknown.New("%s %s is %s; a golden set is frozen only with a frozen normalizer version", nl.Name, nl.Version, nl.State)
	}
	var np normalizerPayload
	if err := json.Unmarshal(nl.Payload, &np); err != nil {
		return Plan{}, fmt.Errorf("decode normalizer %s: %w", nl.ID, err)
	}

	st, err := stats(ctx, q, ds.ID)
	if err != nil {
		return Plan{}, err
	}
	var bad []problems.FieldError
	fail := func(path, format string, args ...any) {
		bad = append(bad, problems.FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
	}
	if st.utterances == 0 {
		return Plan{}, problems.Conflict.New("%s %s holds no utterances", ds.Name, ds.Version)
	}
	locale := ""
	switch langs := st.languages; {
	case len(langs) == 1:
		locale = langs[0]
	case len(langs) == 0 && len(dp.Locales) == 1:
		locale = dp.Locales[0]
	default:
		if len(langs) == 0 {
			langs = dp.Locales
		}
		fail("/datasetVersionId", "a golden set holds one locale; %s %s holds %s", ds.Name, ds.Version, strings.Join(langs, ", "))
	}
	if locale != "" && np.Locale != "*" && primary(np.Locale) != primary(locale) {
		fail("/normalizerVersionId", "%s %s is for locale %s; the dataset's locale is %s", nl.Name, nl.Version, np.Locale, locale)
	}

	groups := in.Groups
	switch groups {
	case "":
		groups = GroupsUtterance
		if st.speakers == st.utterances {
			groups = GroupsSpeaker
		}
	case GroupsSpeaker:
		if st.speakers < st.utterances {
			fail("/groups", "%d of %d utterances name no speaker; resample by utterance", st.utterances-st.speakers, st.utterances)
		}
	case GroupsCall:
		fail("/groups", "no utterance of %s %s names a call (call ids arrive with production imports); resample by speaker or utterance", ds.Name, ds.Version)
	case GroupsUtterance:
	default:
		fail("/groups", "must be call, speaker or utterance")
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = strings.TrimPrefix(ds.Name, "dataset/")
	}
	if !strings.HasPrefix(name, "golden-set/") {
		name = "golden-set/" + name
	}
	if !registry.ValidName(registry.KindGoldenSet, name) {
		fail("/name", "%q is not a collection name: golden-set/ then lowercase letters, digits, dots, dashes and underscores", name)
	}
	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		for _, t := range ds.Tags {
			if v, ok := strings.CutPrefix(t, "domain:"); ok {
				domain = v
				break
			}
		}
	}
	if len(bad) > 0 {
		pe := problems.Validation(bad)
		pe.Detail = "the golden set is not valid: " + bad[0].Path + " " + bad[0].Message
		return Plan{}, pe
	}

	if err := freezeLeakage(ctx, q, ds); err != nil {
		return Plan{}, err
	}

	p := Payload{
		DatasetVersionID: ds.ID, DatasetHash: dp.Artifact.Hash, NormalizerVersionID: nl.ID, Locale: locale, Domain: domain,
		Utterances: st.utterances, Hours: math.Round(st.hours*1e4) / 1e4, Fingerprint: ds.Fingerprint, Groups: groups,
		ApprovalID: in.ApprovalID, Annotation: in.Annotation,
	}
	body, err := json.Marshal(p)
	if err != nil {
		return Plan{}, fmt.Errorf("encode golden set payload: %w", err)
	}
	tags := []string{"golden", "locale:" + locale}
	if domain != "" {
		tags = append(tags, "domain:"+domain)
	}
	desc := fmt.Sprintf("Golden set frozen from %s %s, scored with %s %s", ds.Name, ds.Version, nl.Name, nl.Version)
	if a := in.Annotation; a != nil {
		tags = append(tags, "annotated")
		desc += fmt.Sprintf("; annotated in batch %s under %s at %.12s", a.BatchID, a.Guidelines.Path, a.Guidelines.Commit)
		if a.IAAWER != nil {
			desc += fmt.Sprintf(", inter-annotator WER %.2f %%", *a.IAAWER*100)
		}
	}
	slices.Sort(tags)
	return Plan{Dataset: ds, Normalizer: nl, Payload: p, Register: registry.RegisterInput{
		Kind: registry.KindGoldenSet, Name: name, Tags: tags, Licence: ds.Licence, Payload: body, Actor: in.Actor, Freeze: true,
		Description: desc,
		Fingerprint: identity(p),
	}}, nil
}

// identity is a golden set version's fingerprint: what it holds and how it is scored, not who approved it, so
// freezing the same content again answers the version already there.
func identity(p Payload) string {
	b, _ := json.Marshal(map[string]string{
		"datasetVersionId": p.DatasetVersionID, "normalizerVersionId": p.NormalizerVersionID, "groups": p.Groups, "domain": p.Domain,
	})
	c, _ := registry.Canonical(b)
	return registry.Fingerprint(c)
}

// primary is the language subtag of a BCP 47 tag (he-IL → he).
func primary(tag string) string {
	tag = strings.ToLower(strings.ReplaceAll(tag, "_", "-"))
	lang, _, _ := strings.Cut(tag, "-")
	return lang
}

type datasetStats struct {
	utterances int
	hours      float64
	speakers   int // utterances that name a speaker
	languages  []string
}

func stats(ctx context.Context, q storage.Querier, versionID string) (datasetStats, error) {
	var s datasetStats
	var seconds float64
	err := q.QueryRow(ctx, `SELECT count(*)::int, coalesce(sum(u.duration_s), 0), (count(*) FILTER (WHERE u.speaker <> ''))::int,
			coalesce(array_agg(DISTINCT u.language) FILTER (WHERE u.language <> ''), '{}')
		FROM dataset_utterances du JOIN utterances u ON u.id = du.utterance_id WHERE du.version_id = $1`, versionID).
		Scan(&s.utterances, &seconds, &s.speakers, &s.languages)
	if err != nil {
		return s, fmt.Errorf("read the utterances of %s: %w", versionID, err)
	}
	s.hours = seconds / 3600
	return s, nil
}

// freezeLeakage refuses a dataset version that shares an utterance with a dataset version not registered eval-only,
// or with one any run of any project has trained on (spec 02 "Leakage and training exclusion").
func freezeLeakage(ctx context.Context, q storage.Querier, ds registry.Version) error {
	list, err := data.TrainableOverlaps(ctx, q, ds.ID)
	if err != nil {
		return err
	}
	trained, err := TrainedDatasets(ctx, q, "")
	if err != nil {
		return err
	}
	more, err := data.Overlaps(ctx, q, []string{ds.ID}, trained)
	if err != nil {
		return err
	}
	for _, o := range more {
		if !slices.ContainsFunc(list, func(x data.Overlap) bool { return x.OtherID == o.OtherID }) {
			list = append(list, o)
		}
	}
	if len(list) == 0 {
		return nil
	}
	leaks := make([]data.Leak, 0, len(list))
	for _, o := range list {
		leaks = append(leaks, data.Leak{DatasetID: o.OtherID, OtherID: o.VersionID, Overlap: o})
	}
	return data.LeakageError(ctx, q, fmt.Sprintf("%s %s cannot be frozen as a golden set: trainable dataset versions hold its audio; "+
		"re-freeze them without these utterances or freeze a disjoint test set", ds.Name, ds.Version), leaks)
}

// Freeze registers the golden set the plan checks, frozen, and records which dataset version and normalizer it
// holds. Freezing content the collection already holds answers that version with created false and no event.
func Freeze(ctx context.Context, tx pgx.Tx, in FreezeInput, d *defaults.Defaults, now time.Time) (registry.Version, bool, []events.Draft, error) {
	plan, err := Prepare(ctx, tx, in, d)
	if err != nil {
		return registry.Version{}, false, nil, err
	}
	v, created, _, err := registry.Register(ctx, tx, plan.Register, now)
	if err != nil {
		return registry.Version{}, false, nil, fmt.Errorf("register golden set: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO golden_sets (version_id, dataset_version_id, normalizer_version_id, created_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT (version_id) DO NOTHING`, v.ID, plan.Dataset.ID, plan.Normalizer.ID, now); err != nil {
		return registry.Version{}, false, nil, fmt.Errorf("record golden set: %w", err)
	}
	if !created {
		return v, false, nil, nil
	}
	return v, true, []events.Draft{registry.VersionEvent(v, "golden_set.frozen")}, nil
}

// DatasetOf returns the dataset version a golden set holds.
func DatasetOf(ctx context.Context, q storage.Querier, v registry.Version) (string, error) {
	var id string
	err := q.QueryRow(ctx, "SELECT dataset_version_id FROM golden_sets WHERE version_id = $1", v.ID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var p Payload
		if err := json.Unmarshal(v.Payload, &p); err != nil {
			return "", fmt.Errorf("decode golden set %s: %w", v.ID, err)
		}
		return p.DatasetVersionID, nil
	}
	if err != nil {
		return "", fmt.Errorf("read golden set %s: %w", v.ID, err)
	}
	return id, nil
}

// TrainedDatasets returns the dataset versions the project (every project when projectID is empty) has trained on,
// or queued to train on: the datasets of the mix revisions its runs used, and the dataset versions (by artifact hash)
// and mixes (meta.datasets) fed to its pipelines' training steps.
func TrainedDatasets(ctx context.Context, q storage.Querier, projectID string) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT d FROM runs r JOIN mix_revisions mr ON mr.mix_id = r.mix_id AND mr.rev = r.mix_rev
			CROSS JOIN LATERAL jsonb_array_elements(coalesce(mr.content->'groups', '[]')) g
			CROSS JOIN LATERAL jsonb_array_elements_text(coalesce(g->'datasets', '[]')) d
		WHERE ($1 = '' OR r.project_id = $1)
		UNION
		SELECT v.id FROM pipeline_runs pr JOIN pipeline_steps ps ON ps.pipeline_run_id = pr.id
			CROSS JOIN LATERAL jsonb_each(coalesce(ps.inputs, '{}')) i(name, ref)
			JOIN registry_versions v ON v.payload->'artifact'->>'hash' = i.ref->>'hash'
			JOIN registry_collections c ON c.id = v.collection_id AND c.kind = 'dataset_version'
		WHERE ($1 = '' OR pr.project_id = $1) AND coalesce(ps.resources->>'jobKind', '') IN ('', 'training') AND i.ref->>'type' = 'dataset'
		UNION
		SELECT d FROM pipeline_runs pr JOIN pipeline_steps ps ON ps.pipeline_run_id = pr.id
			CROSS JOIN LATERAL jsonb_each(coalesce(ps.inputs, '{}')) i(name, ref)
			CROSS JOIN LATERAL jsonb_array_elements_text(coalesce(i.ref->'meta'->'datasets', '[]')) d
		WHERE ($1 = '' OR pr.project_id = $1) AND coalesce(ps.resources->>'jobKind', '') IN ('', 'training') AND i.ref->>'type' = 'mix'
		ORDER BY 1`, projectID)
	if err != nil {
		return nil, fmt.Errorf("find the datasets project %s trained on: %w", projectID, err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("find the datasets project %s trained on: %w", projectID, err)
	}
	return out, nil
}

// CheckAdoption re-runs the leakage check when a project adopts a golden set (spec 02): it is refused while any
// dataset version the project trained on shares an utterance with it, until those datasets are re-frozen without the
// overlap. Other kinds pass.
func CheckAdoption(ctx context.Context, q storage.Querier, projectID string, v registry.Version) error {
	if v.Kind != registry.KindGoldenSet {
		return nil
	}
	ds, err := DatasetOf(ctx, q, v)
	if err != nil {
		return err
	}
	trained, err := TrainedDatasets(ctx, q, projectID)
	if err != nil {
		return err
	}
	list, err := data.Overlaps(ctx, q, []string{ds}, trained)
	if err != nil || len(list) == 0 {
		return err
	}
	leaks := make([]data.Leak, 0, len(list))
	for _, o := range list {
		leaks = append(leaks, data.Leak{DatasetID: o.OtherID, OtherID: v.ID, Overlap: o})
	}
	return data.LeakageError(ctx, q, fmt.Sprintf("the project trained on dataset versions that hold audio of %s %s, so its scores there would not be held out; "+
		"re-freeze those datasets without the overlap before adopting it", v.Name, v.Version), leaks)
}
