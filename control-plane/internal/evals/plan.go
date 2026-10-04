package evals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/goldensets"
	"github.com/usunrise88/cadence/control-plane/internal/langpacks"
	"github.com/usunrise88/cadence/control-plane/internal/langtag"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// SubjectRef names the model an eval judges: exactly one of the three.
type SubjectRef struct {
	CheckpointID       string
	ModelVersionID     string
	BaseModelVersionID string
}

// DecodingIn is one requested decoding variant: boost none, or a boost list at a commit with a weight.
type DecodingIn struct {
	Boost  string
	Weight *float64
}

// NewInput is evals.new.
type NewInput struct {
	ProjectID  string
	Actor      auth.Actor
	Subject    SubjectRef
	GoldenSets []string
	Profiles   []string
	Decoding   []DecodingIn
	// Augmentations is the robustness axis (phase 3 stream R): none is always included.
	Augmentations []AugmentationIn
	Baseline      string
	Priority      int
	// Languages maps a golden-set locale to the language both models decode it in (a model fine-tuned under a
	// neighbour's prompt, e.g. sr-RS under hr-HR); unmapped locales decode as themselves.
	Languages map[string]string
}

// PlanCell is one cell of a planned eval (the contract's EvalPlanCell).
type PlanCell struct {
	Role               string `json:"role"`
	GoldenSetVersionID string `json:"goldenSetVersionId"`
	Profile            string `json:"profile"`
	DecodingIndex      int    `json:"decodingIndex"`
	AugmentationIndex  int    `json:"augmentationIndex"`
	DecodingHash       string `json:"decodingHash"`
	ModelKey           string `json:"modelKey"`
	Cached             bool   `json:"cached"`
	RecordID           string `json:"recordId,omitempty"`

	normalizer string
	scorer     string
	scoreStep  string
	key        string                // the record key
	metrics    map[string]MetricPlan // entities, latency
}

// PlanStep is one step of the generated pipeline.
type PlanStep struct {
	Step string `json:"step"`
	Kind string `json:"kind"`
}

// Plan is an eval checked and planned, not yet started: what a dry run answers (the contract's EvalPlan) and what
// Create starts.
type Plan struct {
	Subject        Model          `json:"subject"`
	Baseline       Model          `json:"baseline"`
	GoldenSets     []GoldenSet    `json:"goldenSets"`
	Profiles       []Profile      `json:"profiles"`
	PrimaryProfile string         `json:"primaryProfile"`
	Decoding       []Decoding     `json:"decoding"`
	Augmentations  []Augmentation `json:"augmentations"`
	Significance   Significance   `json:"significance"`
	Cells          []PlanCell     `json:"cells"`
	CellsCached    int            `json:"cellsCached"`
	CellsToCompute int            `json:"cellsToCompute"`
	Estimate       Estimate       `json:"estimate"`
	Steps          []PlanStep     `json:"steps"`

	in      NewInput
	project projects.Project
	start   *pipelines.StartInput // nil when every cell is cached
}

// family is a model family descriptor as evals need it.
type family struct {
	runs.Family
	Profiles  []Profile
	Boosting  string // capabilities.boosting: the method, "" when the family cannot boost
	Streaming bool   // capabilities.streaming: its transcribe step writes partial events
}

func (s *Service) familyOf(ctx context.Context, q storage.Querier, base registry.Version) (family, error) {
	f, err := runs.FamilyOf(ctx, q, base)
	if err != nil {
		return family{}, err
	}
	v, err := registry.GetVersion(ctx, q, registry.KindModelFamily, f.VersionID)
	if err != nil {
		return family{}, err
	}
	var d struct {
		LatencyProfiles []Profile `json:"latencyProfiles"`
		Capabilities    struct {
			Boosting  string `json:"boosting"`
			Streaming bool   `json:"streaming"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(v.Payload, &d); err != nil {
		return family{}, fmt.Errorf("decode model family %s: %w", v.ID, err)
	}
	return family{Family: f, Profiles: d.LatencyProfiles, Boosting: d.Capabilities.Boosting, Streaming: d.Capabilities.Streaming}, nil
}

// roleKind is the newest published step kind of a role of f.
func roleKind(ctx context.Context, q storage.Querier, f family, role string) (pipelines.Kind, error) {
	name, err := f.Role(role)
	if err != nil {
		return pipelines.Kind{}, err
	}
	return runs.RoleKind(ctx, q, name)
}

// Prepare resolves an eval's subject, baseline, golden sets, profiles and decoding, looks every cell up in the eval
// records and plans the pipeline of the missing ones; it writes nothing but rendered blobs (content-addressed).
func (s *Service) Prepare(ctx context.Context, q storage.Querier, in NewInput) (Plan, error) {
	d := s.defaults()
	p, err := projects.GetByID(ctx, q, in.ProjectID)
	if err != nil {
		return Plan{}, err
	}
	gf, err := ReadGate(ctx, s.repo(), p.Slug, d)
	if err != nil {
		return Plan{}, err
	}
	pl := Plan{in: in, project: p, Significance: gf.Gate.Significance, Steps: []PlanStep{}}
	if pl.Subject, err = s.resolveSubject(ctx, q, p, in.Subject); err != nil {
		return Plan{}, err
	}
	if pl.Baseline, err = s.resolveBaseline(ctx, q, p, in.Baseline); err != nil {
		return Plan{}, err
	}
	if pl.GoldenSets, err = s.resolveGoldenSets(ctx, q, p, in.GoldenSets, gf.Gate); err != nil {
		return Plan{}, err
	}
	used := map[string]bool{}
	for i, g := range pl.GoldenSets {
		if l, ok := in.Languages[g.Locale]; ok {
			used[g.Locale] = true
			if l != g.Locale {
				pl.GoldenSets[i].DecodeAs = l
			}
		}
	}
	for _, loc := range sortedKeys(in.Languages) {
		if !used[loc] {
			return Plan{}, problems.Validation([]problems.FieldError{{Path: "/languages/" + loc,
				Message: fmt.Sprintf("no golden set of this eval has locale %s", loc)}})
		}
	}
	if err := checkLanguages(pl.GoldenSets, pl.Subject, pl.Baseline); err != nil {
		return Plan{}, err
	}
	if pl.Profiles, pl.PrimaryProfile, err = chooseProfiles(d, gf.Gate.PrimaryProfile, in.Profiles, pl.Subject.family, pl.Baseline.family); err != nil {
		return Plan{}, err
	}
	if pl.Decoding, err = s.renderDecoding(ctx, p, in.Decoding); err != nil {
		return Plan{}, err
	}
	if pl.Augmentations, err = s.renderAugmentations(ctx, q, p, in.Augmentations); err != nil {
		return Plan{}, err
	}
	scorer, err := runs.RoleKind(ctx, q, ScorerKind)
	if err != nil {
		return Plan{}, err
	}
	b := &builder{s: s, q: q, plan: &pl, scorer: scorer, models: map[string]*modelSteps{}, units: map[string]*unit{},
		perAudioHour: d.Eval.GPUHoursPerAudioHour.Value, cached: map[string]Record{}, slots: map[string]*metricSlot{},
		itn: map[string]itnRender{}, align: map[int]*goldensets.Alignment{}}
	if len(pl.Augmentations) > 1 {
		k, err := runs.RoleKind(ctx, q, AugmentKind)
		if err != nil {
			return Plan{}, err
		}
		b.augment = &k
	}
	if b.mk, err = loadMetricKinds(ctx, q); err != nil {
		return Plan{}, err
	}
	for _, m := range []*Model{&pl.Subject, &pl.Baseline} {
		if err := b.kinds(ctx, m); err != nil {
			return Plan{}, err
		}
	}
	if err := b.cells(ctx); err != nil {
		return Plan{}, err
	}
	if err := b.planMetrics(ctx); err != nil {
		return Plan{}, err
	}
	if err := b.pipeline(ctx); err != nil {
		return Plan{}, err
	}
	return pl, nil
}

// ---------------------------------------------------------------- models

func (s *Service) resolveSubject(ctx context.Context, q storage.Querier, p projects.Project, ref SubjectRef) (Model, error) {
	n := 0
	for _, v := range []string{ref.CheckpointID, ref.ModelVersionID, ref.BaseModelVersionID} {
		if strings.TrimSpace(v) != "" {
			n++
		}
	}
	if n != 1 {
		return Model{}, problems.Validation([]problems.FieldError{{Path: "/subject",
			Message: "name exactly one of checkpointId (ckp_…), modelVersionId or baseModelVersionId"}})
	}
	switch {
	case ref.CheckpointID != "":
		return s.checkpointModel(ctx, q, p, strings.TrimSpace(ref.CheckpointID))
	case ref.ModelVersionID != "":
		v, err := registry.Resolve(ctx, q, p.ID, registry.KindModel, strings.TrimSpace(ref.ModelVersionID))
		if err != nil {
			return Model{}, err
		}
		return s.versionModel(ctx, q, v)
	default:
		v, err := registry.Resolve(ctx, q, p.ID, registry.KindBaseModel, strings.TrimSpace(ref.BaseModelVersionID))
		if err != nil {
			return Model{}, err
		}
		return s.versionModel(ctx, q, v)
	}
}

// checkpointModel is a checkpoint of the project; its family is the family of its run's base model.
func (s *Service) checkpointModel(ctx context.Context, q storage.Querier, p projects.Project, id string) (Model, error) {
	c, err := runs.GetCheckpoint(ctx, q, id)
	if err != nil {
		return Model{}, err
	}
	if c.ProjectID != p.ID {
		return Model{}, problems.NotFound.New("the project has no checkpoint %q", id)
	}
	var baseID string
	if err := q.QueryRow(ctx, "SELECT base_version_id FROM runs WHERE id = $1", c.RunID).Scan(&baseID); err != nil {
		return Model{}, fmt.Errorf("read the base model of run %s: %w", c.RunID, err)
	}
	base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, baseID)
	if err != nil {
		return Model{}, err
	}
	f, err := s.familyOf(ctx, q, base)
	if err != nil {
		return Model{}, err
	}
	ref, err := s.sized(ctx, q, steps.ArtifactRef{Hash: c.Artifact, Type: TypeCheckpoint, Meta: c.Meta})
	if err != nil {
		return Model{}, err
	}
	label := c.RunID
	if c.Step != nil {
		label += " step " + strconv.FormatInt(*c.Step, 10)
	}
	key := c.WeightsHash
	if key == "" {
		key = c.Artifact
	}
	return Model{Kind: "checkpoint", ID: c.ID, Label: label, ModelKey: key, Family: f.Name, RunID: c.RunID, family: f, artifact: ref, base: base}, nil
}

// modelPayload is the part of ModelPayload an eval reads.
type modelPayload struct {
	CheckpointID       string `json:"checkpointId"`
	WeightsHash        string `json:"weightsHash"`
	CheckpointHash     string `json:"checkpointHash"`
	BaseModelVersionID string `json:"baseModelVersionId"`
	Lineage            struct {
		RunID string `json:"runId"`
	} `json:"lineage"`
}

// versionModel is a registered model version or a base model version.
func (s *Service) versionModel(ctx context.Context, q storage.Querier, v registry.Version) (Model, error) {
	label := v.Name + " " + v.Version
	switch v.Kind {
	case registry.KindBaseModel:
		f, err := s.familyOf(ctx, q, v)
		if err != nil {
			return Model{}, err
		}
		ref, err := runs.RenderBaseModel(s.CAS, v, f.Family)
		if err != nil {
			return Model{}, err
		}
		return Model{Kind: "base_model", ID: v.ID, Label: label, ModelKey: "base:" + v.ID, Family: f.Name, family: f, artifact: ref, base: v}, nil
	case registry.KindModel:
		var mp modelPayload
		if err := json.Unmarshal(v.Payload, &mp); err != nil {
			return Model{}, fmt.Errorf("decode model %s: %w", v.ID, err)
		}
		base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, mp.BaseModelVersionID)
		if err != nil {
			return Model{}, err
		}
		f, err := s.familyOf(ctx, q, base)
		if err != nil {
			return Model{}, err
		}
		ref, err := s.sized(ctx, q, steps.ArtifactRef{Hash: mp.CheckpointHash, Type: TypeCheckpoint})
		if err != nil {
			return Model{}, err
		}
		key := mp.WeightsHash
		if key == "" {
			key = mp.CheckpointHash
		}
		return Model{Kind: "model", ID: v.ID, Label: label, ModelKey: key, Family: f.Name, RunID: mp.Lineage.RunID, family: f, artifact: ref, base: base}, nil
	}
	return Model{}, problems.Conflict.New("%s %s is a %s; an eval compares checkpoints, model versions and base models", v.Name, v.Version, registry.Noun(v.Kind))
}

// resolveBaseline finds the baseline: the request's, else the project's @baseline, else its default base model.
func (s *Service) resolveBaseline(ctx context.Context, q storage.Querier, p projects.Project, ref string) (Model, error) {
	ref = strings.TrimSpace(ref)
	var (
		v      registry.Version
		source string
		err    error
	)
	switch {
	case ref != "":
		source = "request"
		switch {
		case strings.HasPrefix(ref, "@"):
			a, aerr := registry.GetAlias(ctx, q, p.ID, strings.TrimPrefix(ref, "@"))
			v, err = a.Version, aerr
		case strings.HasPrefix(ref, "ver_"):
			v, err = registry.GetVersion(ctx, q, "", ref)
		case strings.HasPrefix(ref, "model/"):
			v, err = registry.Latest(ctx, q, registry.KindModel, ref)
		default:
			v, err = registry.Latest(ctx, q, registry.KindBaseModel, ref)
		}
		if err != nil {
			return Model{}, err
		}
	default:
		a, aerr := registry.GetAlias(ctx, q, p.ID, "baseline")
		switch pe, ok := problems.As(aerr); {
		case aerr == nil:
			v, source = a.Version, "alias"
		case ok && pe.Type == problems.NotFound:
			if p.BaseModel == nil || p.BaseModel.VersionID == "" {
				return Model{}, problems.EvalBaselineMissing.New(
					"project %s has no @baseline alias and no default base model; name a baseline (baseline: ver_… or base-model/<name>) or set @baseline (aliases.set, approval)", p.Slug)
			}
			if v, err = registry.GetVersion(ctx, q, registry.KindBaseModel, p.BaseModel.VersionID); err != nil {
				return Model{}, err
			}
			source = "project-default"
		default:
			return Model{}, aerr
		}
	}
	if v.Kind != registry.KindBaseModel && v.Kind != registry.KindModel {
		return Model{}, problems.Validation([]problems.FieldError{{Path: "/baseline",
			Message: fmt.Sprintf("%s %s is a %s; a baseline is a base model or a model version", v.Name, v.Version, registry.Noun(v.Kind))}})
	}
	m, err := s.versionModel(ctx, q, v)
	m.Source = source
	return m, err
}

// sized completes ref with the size and metadata the artifact index holds (for an artifact not indexed yet, the size
// of its blob; recording it as a pipeline input then verifies it).
func (s *Service) sized(ctx context.Context, q storage.Querier, ref steps.ArtifactRef) (steps.ArtifactRef, error) {
	if !steps.ValidHash(ref.Hash) {
		return ref, problems.ArtifactMissing.New("%q is not an artifact hash", ref.Hash)
	}
	a, err := artifacts.Get(ctx, q, ref.Hash)
	if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
		if s.CAS != nil {
			if has, size, err := s.CAS.Has(ref.Hash); err == nil && has {
				ref.Size = size
			}
		}
		return ref, nil
	}
	if err != nil {
		return ref, err
	}
	ref.Size = a.Size
	if len(ref.Meta) == 0 {
		ref.Meta = a.Meta
	}
	return ref, nil
}

// ---------------------------------------------------------------- golden sets

func goldenSet(v registry.Version) (GoldenSet, error) {
	var gp goldensets.Payload
	if err := json.Unmarshal(v.Payload, &gp); err != nil {
		return GoldenSet{}, fmt.Errorf("decode golden set %s: %w", v.ID, err)
	}
	if !steps.ValidHash(gp.DatasetHash) || gp.NormalizerVersionID == "" {
		return GoldenSet{}, problems.Conflict.New("golden set %s %s names no dataset artifact or normalizer version", v.Name, v.Version)
	}
	if gp.Groups == "" {
		gp.Groups = goldensets.GroupsUtterance
	}
	return GoldenSet{VersionID: v.ID, Name: v.Name, Version: v.Version, NormalizerVersionID: gp.NormalizerVersionID, Locale: gp.Locale,
		Domain: gp.Domain, Utterances: gp.Utterances, Hours: gp.Hours, Groups: gp.Groups, datasetHash: gp.DatasetHash}, nil
}

// adoptedGoldenSets resolves gate references (collections, * patterns, ver_ ids) to the project's adopted golden
// set versions, the newest adopted version of each collection.
func adoptedGoldenSets(ctx context.Context, q storage.Querier, projectID string, refs []string) ([]registry.Version, error) {
	adopted, err := registry.ListAdoptions(ctx, q, projectID, registry.KindGoldenSet)
	if err != nil {
		return nil, err
	}
	var out []registry.Version
	seen := map[string]bool{}
	for _, a := range adopted { // newest first: the first version of a collection wins
		v := a.Version
		if seen[v.Name] || (refs != nil && !Matches(refs, v.ID, v.Name)) {
			continue
		}
		seen[v.Name] = true
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) resolveGoldenSets(ctx context.Context, q storage.Querier, p projects.Project, refs []string, g Gate) ([]GoldenSet, error) {
	var versions []registry.Version
	switch {
	case len(refs) > 0:
		var fields []problems.FieldError
		for i, r := range refs {
			r = strings.TrimSpace(r)
			switch {
			case strings.HasPrefix(r, "ver_") || strings.HasPrefix(r, "@"):
				v, err := registry.Resolve(ctx, q, p.ID, registry.KindGoldenSet, r)
				if err != nil {
					return nil, err
				}
				versions = append(versions, v)
			default:
				list, err := adoptedGoldenSets(ctx, q, p.ID, []string{r})
				if err != nil {
					return nil, err
				}
				if len(list) == 0 && !strings.Contains(r, "*") {
					v, err := registry.Latest(ctx, q, registry.KindGoldenSet, r)
					if err != nil {
						return nil, err
					}
					list = []registry.Version{v}
				}
				if len(list) == 0 {
					fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/goldenSets/%d", i),
						Message: fmt.Sprintf("%s matches no golden set the project adopted", r)})
				}
				versions = append(versions, list...)
			}
		}
		if len(fields) > 0 {
			return nil, problems.Validation(fields)
		}
	default:
		var named []string
		named = append(append(named, g.TargetGoldenSets...), g.ReplayGoldenSets...)
		if len(named) == 0 {
			named = nil // every adopted golden set
		}
		list, err := adoptedGoldenSets(ctx, q, p.ID, named)
		if err != nil {
			return nil, err
		}
		versions = list
	}
	var out []GoldenSet
	seen := map[string]bool{}
	for _, v := range versions {
		if seen[v.ID] {
			continue
		}
		seen[v.ID] = true
		gs, err := goldenSet(v)
		if err != nil {
			return nil, err
		}
		gs.Replay = Replay(gs, g, p.Locales)
		out = append(out, gs)
	}
	if len(out) == 0 {
		return nil, problems.Validation([]problems.FieldError{{Path: "/goldenSets",
			Message: "no golden set to evaluate on: the project's gates.yaml names none it adopted and it adopted none; name goldenSets or adopt one (goldenSets.list, projects.adopt)"}})
	}
	return out, nil
}

// Replay reports whether golden set gs is a replay set under gate g: named in its replay list; or, when the gate names
// neither list, of a language outside the project's locales (the replay locales, R17).
func Replay(gs GoldenSet, g Gate, locales []string) bool {
	switch {
	case Matches(g.ReplayGoldenSets, gs.VersionID, gs.Name):
		return true
	case Matches(g.TargetGoldenSets, gs.VersionID, gs.Name), len(g.TargetGoldenSets) > 0 || len(g.ReplayGoldenSets) > 0:
		return false
	case len(locales) == 0 || gs.Locale == "":
		return false
	}
	return !slices.ContainsFunc(locales, func(l string) bool { return language(l) == language(gs.Locale) })
}

// Target reports whether golden set gs is a target set under gate g.
func Target(gs GoldenSet, g Gate, locales []string) bool {
	if len(g.TargetGoldenSets) > 0 {
		return Matches(g.TargetGoldenSets, gs.VersionID, gs.Name)
	}
	return !Replay(gs, g, locales)
}

// checkLanguages refuses, before any GPU time, a golden set whose decode language (its locale, or the language
// evals.new's languages maps it to) a compared model's base model does not know: the base model's locale:<code> tags,
// as runs.CheckLanguages reads them for a training stage. A base model without locale tags is not checked. The answer
// names languages, the fix (decode the set in a close language the model knows).
func checkLanguages(sets []GoldenSet, models ...Model) error {
	var fields []problems.FieldError
	seen := map[string]bool{}
	for _, m := range models {
		known := map[string]bool{}
		var list []string
		for _, t := range m.base.Tags {
			if code, ok := strings.CutPrefix(t, "locale:"); ok && code != "" {
				if l := langtag.Macro(code); !known[l] {
					known[l] = true
					list = append(list, l)
				}
			}
		}
		if len(known) == 0 {
			continue
		}
		slices.Sort(list)
		for _, gs := range sets {
			l := gs.decodeLanguage()
			if l == "" || known[langtag.Macro(l)] || seen[gs.VersionID+"|"+m.base.ID] {
				continue
			}
			seen[gs.VersionID+"|"+m.base.ID] = true
			fields = append(fields, problems.FieldError{Path: "/languages/" + gs.Locale, Message: fmt.Sprintf(
				"golden set %s decodes in %s, a language %s (base model %s) does not know (it knows %s); set languages: {%q: <a close language it knows>} to decode the set in it",
				gs.Name, l, m.Label, m.base.Name, strings.Join(list, ", "), gs.Locale)})
		}
	}
	if len(fields) == 0 {
		return nil
	}
	pe := problems.Validation(fields)
	pe.Detail = fields[0].Message
	return pe
}

func language(locale string) string {
	l, _, _ := strings.Cut(strings.ReplaceAll(locale, "_", "-"), "-")
	return strings.ToLower(strings.TrimSpace(l))
}

// ---------------------------------------------------------------- profiles

var msRe = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)ms$`)

// sameLatency reports whether two profiles are the same column: the same milliseconds when both state them (families
// line up by latency, R43), else the same name.
func sameLatency(a, b Profile) bool {
	if a.LatencyMs > 0 && b.LatencyMs > 0 {
		return a.LatencyMs == b.LatencyMs
	}
	return a.Name == b.Name
}

// profileFor finds the profile name names among ps: by name, else by the milliseconds the name states ("160ms" finds
// the profile of 160 ms whatever a family calls it). The gate's primaryProfile and evals.new's profiles resolve
// through it, in planning and in the verdict alike.
func profileFor(ps []Profile, name string) (Profile, bool) {
	name = strings.TrimSpace(name)
	if i := slices.IndexFunc(ps, func(p Profile) bool { return p.Name == name }); i >= 0 {
		return ps[i], true
	}
	if m := msRe.FindStringSubmatch(name); m != nil {
		ms, _ := strconv.ParseFloat(m[1], 64)
		if i := slices.IndexFunc(ps, func(p Profile) bool { return p.LatencyMs == ms }); i >= 0 {
			return ps[i], true
		}
	}
	return Profile{}, false
}

// own is the name family f gives the matrix column p (its profile at the same latency); p's name when f has none.
// A transcribe step decodes at, and a decoding hash names, the family's own profile.
func (f family) own(p Profile) string {
	if i := slices.IndexFunc(f.Profiles, func(o Profile) bool { return sameLatency(o, p) }); i >= 0 {
		return f.Profiles[i].Name
	}
	return p.Name
}

// chooseProfiles picks the matrix columns: the requested profiles, else eval.matrix_profiles that every model's family
// declares (else all they share), always with the primary profile when the families have it. Families share a
// profile when they declare its latency (R43); a column carries the first family's (the subject's) name, and a
// requested name or the primary profile may be any compared family's name or "<n>ms".
func chooseProfiles(d *defaults.Defaults, primary string, requested []string, fams ...family) ([]Profile, string, error) {
	shared := slices.Clone(fams[0].Profiles)
	for _, f := range fams[1:] {
		shared = slices.DeleteFunc(shared, func(p Profile) bool {
			return !slices.ContainsFunc(f.Profiles, func(o Profile) bool { return sameLatency(o, p) })
		})
	}
	names := make([]string, 0, len(shared))
	for _, p := range shared {
		names = append(names, p.Name)
	}
	find := func(name string) (Profile, bool) {
		if p, ok := profileFor(shared, name); ok {
			return p, true
		}
		for _, f := range fams[1:] { // another family's name for a shared latency
			if fp, ok := profileFor(f.Profiles, name); ok {
				if i := slices.IndexFunc(shared, func(p Profile) bool { return sameLatency(p, fp) }); i >= 0 {
					return shared[i], true
				}
			}
		}
		return Profile{}, false
	}
	if len(shared) == 0 {
		return nil, "", problems.FamilyUnavailable.New("the compared models' families share no latency profile")
	}
	var out []Profile
	if len(requested) > 0 {
		var fields []problems.FieldError
		for i, name := range requested {
			p, ok := find(name)
			if !ok {
				fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/profiles/%d", i),
					Message: fmt.Sprintf("%s is not a latency profile of the compared models' families (they share %s)", name, strings.Join(names, ", "))})
				continue
			}
			if !slices.ContainsFunc(out, func(o Profile) bool { return o.Name == p.Name }) {
				out = append(out, p)
			}
		}
		if len(fields) > 0 {
			return nil, "", problems.Validation(fields)
		}
	} else {
		for _, name := range d.Eval.MatrixProfiles.Value {
			if p, ok := find(name); ok && !slices.ContainsFunc(out, func(o Profile) bool { return o.Name == p.Name }) {
				out = append(out, p)
			}
		}
		if len(out) == 0 {
			out = slices.Clone(shared)
		}
	}
	prim, ok := find(primary)
	if !ok {
		return out, "", nil // the gate reports the missing primary cell
	}
	if !slices.ContainsFunc(out, func(o Profile) bool { return o.Name == prim.Name }) {
		out = append(out, prim)
	}
	return out, prim.Name, nil
}

// ---------------------------------------------------------------- decoding

var (
	boostPathRe = regexp.MustCompile(`^lang/[A-Za-z0-9_-]+/boost/[A-Za-z0-9._-]+\.txt$`)
	commitRe    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

func (s *Service) renderDecoding(ctx context.Context, p projects.Project, in []DecodingIn) ([]Decoding, error) {
	if len(in) == 0 {
		in = []DecodingIn{{Boost: "none"}}
	}
	out := make([]Decoding, 0, len(in))
	var fields []problems.FieldError
	seen := map[string]int{}
	for i, v := range in {
		at := fmt.Sprintf("/decoding/%d/boost", i)
		boost := strings.TrimSpace(v.Boost)
		if boost == "" || boost == "none" {
			if v.Weight != nil {
				fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/decoding/%d/weight", i), Message: "a weight needs a boost list"})
			}
			out = append(out, Decoding{Index: i, Boost: "none"})
			continue
		}
		path, sha, ok := strings.Cut(boost, "@")
		if !ok || !boostPathRe.MatchString(path) || !commitRe.MatchString(sha) {
			fields = append(fields, problems.FieldError{Path: at,
				Message: fmt.Sprintf("%q is not none or lang/<locale>/boost/<file>.txt@<commit sha>", boost)})
			continue
		}
		repo := s.repo()
		if repo == nil || !repo.Exists(p.Slug) {
			fields = append(fields, problems.FieldError{Path: at, Message: "the project has no repository to read the boost list from"})
			continue
		}
		b, _, err := repo.ReadFile(ctx, p.Slug, sha, path)
		if err != nil { // a missing file, or a commit the repository does not have
			msg := fmt.Sprintf("%s cannot be read at %s: %v", path, sha, err)
			if errors.Is(err, repos.ErrNotFound) {
				msg = fmt.Sprintf("%s does not exist at %s", path, sha)
			}
			fields = append(fields, problems.FieldError{Path: at, Message: msg})
			continue
		}
		domain := strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".txt")
		bl, err := langpacks.ParseBoost(domain, b, s.defaults().Langpacks.BoostMaxTerms.Value)
		if err != nil {
			fields = append(fields, problems.FieldError{Path: at, Message: fmt.Sprintf("%s: %v", path, err)})
			continue
		}
		if v.Weight != nil { // the request's weight wins over the list's header
			bl.Weight = *v.Weight
		}
		ref, err := s.putBoost(bl, map[string]any{"path": path, "commit": sha, "terms": len(bl.Terms), "sha256": bl.SHA256()})
		if err != nil {
			return nil, err
		}
		if j, dup := seen[ref.Hash]; dup {
			fields = append(fields, problems.FieldError{Path: at, Message: fmt.Sprintf("the same list and weight as decoding %d", j)})
			continue
		}
		seen[ref.Hash] = i
		weight := bl.Weight
		out = append(out, Decoding{Index: i, Boost: boost, Weight: &weight, Terms: len(bl.Terms), Artifact: ref.Hash, ref: &ref})
	}
	if n := countNone(out); n > 1 {
		fields = append(fields, problems.FieldError{Path: "/decoding", Message: "boost none is listed twice"})
	}
	if len(fields) > 0 {
		return nil, problems.Validation(fields)
	}
	return out, nil
}

func countNone(list []Decoding) int {
	n := 0
	for _, d := range list {
		if d.Boost == "none" {
			n++
		}
	}
	return n
}

// putBoost renders a boost_list artifact (the language pack's {terms, weight}) into the content store.
func (s *Service) putBoost(bl langpacks.Boost, meta map[string]any) (steps.ArtifactRef, error) {
	if s.CAS == nil {
		return steps.ArtifactRef{}, errors.New("evals: no content store")
	}
	b := bl.Artifact()
	h, err := s.CAS.PutBytes(b)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	return steps.ArtifactRef{Hash: h, Type: TypeBoostList, Size: int64(len(b)), Meta: mustJSON(meta)}, nil
}

// RenderNormalizer puts the normalizer artifact of a normalizer version in the store (03 "Artifact types"): the
// NormalizerPayload's fields plus versionId, collection and version — a step reads the file, not the artifact's meta
// ({versionId}).
func RenderNormalizer(s *Service, v registry.Version) (steps.ArtifactRef, error) {
	if s.CAS == nil {
		return steps.ArtifactRef{}, errors.New("evals: no content store")
	}
	doc := map[string]any{}
	if err := json.Unmarshal(v.Payload, &doc); err != nil {
		return steps.ArtifactRef{}, fmt.Errorf("decode normalizer %s: %w", v.ID, err)
	}
	doc["versionId"], doc["collection"], doc["version"] = v.ID, v.Name, v.Version
	b := mustJSON(doc) // map keys marshal sorted: one version, one artifact
	h, err := s.CAS.PutBytes(b)
	if err != nil {
		return steps.ArtifactRef{}, err
	}
	return steps.ArtifactRef{Hash: h, Type: TypeNormalizer, Size: int64(len(b)), Meta: mustJSON(map[string]string{"versionId": v.ID})}, nil
}

// ---------------------------------------------------------------- cells and the pipeline

// modelSteps are the kinds that compute a model's cells and, once planned, how its checkpoint is wired.
type modelSteps struct {
	model       *Model
	transcribe  pipelines.Kind
	materialize *pipelines.Kind // base models only
	localeParam string
	index       int    // m<index> in step and input names
	wire        string // the checkpoint transcribe steps read
}

// unit is one record to compute: a transcribe and a score step.
type unit struct {
	key       Key
	ms        *modelSteps
	gs        int
	dec       int
	aug       int
	profile   Profile // the column; the transcribe step decodes at its family's own profile
	score     string
	transcode string
}

type builder struct {
	s            *Service
	q            storage.Querier
	plan         *Plan
	scorer       pipelines.Kind
	models       map[string]*modelSteps // by model key
	units        map[string]*unit       // by record key
	order        []*unit
	perAudioHour float64

	augment   *pipelines.Kind        // the augment step kind when the eval has augmentations
	mk        metricKinds            // the metric scorers and the VAD
	cached    map[string]Record      // cached records by record key
	slots     map[string]*metricSlot // metrics per record key
	slotOrder []string
	itn       map[string]itnRender // the rendered itn.yaml per golden-set locale
	// align is the newest reference alignment of each golden set's dataset artifact (nil: none), by golden set index;
	// the latency scorer reads it for emission delay.
	align map[int]*goldensets.Alignment
}

// localeParams name the language a transcribe kind decodes in, first match wins (a naming convention of role kinds,
// as in internal/runs; not a family name).
var localeParams = []string{"target_lang", "language", "lang", "locale"}

func kindParams(k pipelines.Kind) map[string]json.RawMessage {
	var sch struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(k.Params, &sch)
	return sch.Properties
}

func consumesType(k pipelines.Kind, typ string) (string, bool) {
	for name, t := range k.Consumes {
		if t == typ {
			return name, true
		}
	}
	return "", false
}

func producesType(k pipelines.Kind, typ string) (string, bool) {
	for _, name := range sortedKeys(k.Produces) {
		if k.Produces[name] == typ && !slices.Contains(k.OptionalOutputs, name) {
			return name, true
		}
	}
	return "", false
}

func (b *builder) kinds(ctx context.Context, m *Model) error {
	if _, ok := b.models[m.ModelKey]; ok {
		return nil
	}
	ms := &modelSteps{model: m, index: len(b.models) + 1}
	var err error
	if ms.transcribe, err = roleKind(ctx, b.q, m.family, RoleTranscribe); err != nil {
		return err
	}
	if m.Kind == "base_model" {
		k, err := roleKind(ctx, b.q, m.family, RoleMaterialize)
		if err != nil {
			return err
		}
		ms.materialize = &k
	}
	props := kindParams(ms.transcribe)
	for _, n := range localeParams {
		if _, ok := props[n]; ok {
			ms.localeParam = n
			break
		}
	}
	// A boost list is decoded only by a family that declares a boosting method and a transcribe kind that reads one.
	_, boosts := consumesType(ms.transcribe, TypeBoostList)
	for i, d := range b.plan.Decoding {
		if d.Boost != "none" && (!boosts || m.family.Boosting == "") {
			return problems.Validation([]problems.FieldError{{Path: fmt.Sprintf("/decoding/%d/boost", i),
				Message: fmt.Sprintf("model family %s (transcribe step %s) does not decode with a boost list", m.Family, ms.transcribe.Ref())}})
		}
	}
	b.models[m.ModelKey] = ms
	return nil
}

// outputNeutralParams are transcribe parameters that change how a decode runs, never what it writes (the memory kept
// back for the CUDA context): the decoding hash leaves them out, so tuning them keeps the eval records. The batch size
// is not one: at 80 ms the card's batched matrix products flip the base model's near-ties, so a batch of 8 and a
// batch of 1 can differ by a word (the streaming pack's batched decode, 2026-10-02 GPU check).
var outputNeutralParams = []string{"cuda_context_reserve_mb"}

// transcribeParams are the parameters an eval writes for a model's transcribe step: the family's own profile at the
// column and the language the golden set decodes in.
func (ms *modelSteps) transcribeParams(prof Profile, gs GoldenSet) map[string]any {
	tp := map[string]any{}
	if _, ok := kindParams(ms.transcribe)["profile"]; ok {
		tp["profile"] = ms.model.family.own(prof)
	}
	if ms.localeParam != "" && gs.decodeLanguage() != "" {
		tp[ms.localeParam] = gs.decodeLanguage()
	}
	return tp
}

// decoding is the decoding configuration a record key hashes (R22): the transcribe kind, the family's profile at the
// column, the decode language, every other transcribe parameter as the run resolves it against defaults.yaml (a
// changed default, e.g. the end-of-utterance history, makes new records; outputNeutralParams excepted), the boost
// list, the materialize kind of a base model (its weights come from that step), and for an augmented cell the
// augmentation (the kind that applies it, the profile's content hash and the seed).
func (b *builder) decoding(ms *modelSteps, prof Profile, gs GoldenSet, d Decoding, a Augmentation) (map[string]any, string) {
	own := ms.model.family.own(prof)
	dec := map[string]any{"transcribe": ms.transcribe.Ref(), "profile": own}
	if ms.localeParam != "" && gs.decodeLanguage() != "" {
		dec[ms.localeParam] = gs.decodeLanguage()
	}
	written := ms.transcribeParams(prof, gs)
	resolved, _ := pipelines.ResolveParams(ms.transcribe, written, b.s.defaults()) // problems surface when the pipeline is planned
	params := map[string]any{}
	for k, v := range resolved {
		if _, set := written[k]; !set && !slices.Contains(outputNeutralParams, k) {
			params[k] = v
		}
	}
	if len(params) > 0 {
		dec["params"] = params
	}
	if ms.materialize != nil {
		dec["materialize"] = ms.materialize.Ref()
	}
	if d.Boost != "none" {
		dec["boost"] = map[string]any{"list": d.Artifact, "weight": *d.Weight}
	}
	if !a.none() && b.augment != nil {
		dec["augment"] = map[string]any{"kind": b.augment.Ref(), "profile": a.Hash, "seed": *a.Seed}
	}
	sum := sha256.Sum256(mustJSON(dec)) // map keys marshal sorted: canonical
	return dec, "sha256:" + hex.EncodeToString(sum[:])
}

func (b *builder) cells(ctx context.Context) error {
	pl := b.plan
	for gi, gs := range pl.GoldenSets {
		for _, prof := range pl.Profiles {
			if gs.Replay && pl.PrimaryProfile != "" && prof.Name != pl.PrimaryProfile {
				continue // replay golden sets at the primary profile only (03 "The eval pipeline")
			}
			for di, d := range pl.Decoding {
				for ai, a := range pl.Augmentations {
					if ai > 0 && gs.Replay {
						continue // the robustness axis covers the target golden sets; replay sets guard against forgetting
					}
					for _, role := range []string{RoleSubject, RoleBaseline} {
						m := &pl.Subject
						if role == RoleBaseline {
							m = &pl.Baseline
						}
						ms := b.models[m.ModelKey]
						_, dhash := b.decoding(ms, prof, gs, d, a)
						k := Key{ModelKey: m.ModelKey, GoldenSetVersionID: gs.VersionID, NormalizerVersionID: gs.NormalizerVersionID,
							DecodingHash: dhash, Scorer: b.scorer.Ref()}
						c := PlanCell{Role: role, GoldenSetVersionID: gs.VersionID, Profile: prof.Name, DecodingIndex: di, AugmentationIndex: ai,
							DecodingHash: dhash, ModelKey: m.ModelKey, normalizer: gs.NormalizerVersionID, scorer: b.scorer.Ref(), key: k.String()}
						rec, found, err := findRecord(ctx, b.q, k)
						if err != nil {
							return err
						}
						// A record whose per-utterance artifacts the age retention evicted is computed again: a delta, the
						// worst utterances and the metric steps need them (the hook refreshes the record).
						if found && !rec.Stale() {
							c.Cached, c.RecordID = true, rec.ID
							b.cached[k.String()] = rec
							pl.CellsCached++
						} else {
							u, ok := b.units[k.String()]
							if !ok {
								u = &unit{key: k, ms: ms, gs: gi, dec: di, aug: ai, profile: prof}
								b.units[k.String()] = u
								b.order = append(b.order, u)
							}
							c.scoreStep = fmt.Sprintf("score-u%d", slices.Index(b.order, u)+1)
						}
						if _, ok := b.slots[k.String()]; !ok {
							b.slots[k.String()] = &metricSlot{key: k, ms: ms, gs: gi, aug: ai}
							b.slotOrder = append(b.slotOrder, k.String())
						}
						pl.Cells = append(pl.Cells, c)
					}
				}
			}
		}
	}
	pl.CellsToCompute = len(b.order)
	return nil
}

// metricSlot is one record key's metrics beside WER: entity accuracy and latency to final.
type metricSlot struct {
	key     Key
	ms      *modelSteps
	gs, aug int
	plans   map[string]MetricPlan
}

// planMetrics decides, per record key, which metrics its cells get: a stored eval_metrics row is linked, a missing
// one gets a step (the transcribe step's hypotheses, or the cached record's), anything impossible its reason.
func (b *builder) planMetrics(ctx context.Context) error {
	pl := b.plan
	for _, ks := range b.slotOrder {
		sl := b.slots[ks]
		gs := pl.GoldenSets[sl.gs]
		sl.plans = map[string]MetricPlan{}
		hypAvailable := true
		if rec, ok := b.cached[ks]; ok && !steps.ValidHash(rec.Hypotheses) {
			hypAvailable = false
		}
		// Entity accuracy: the pack's ITN classes of the golden set's locale.
		ep := MetricPlan{}
		switch {
		case b.mk.entity == nil:
			ep.Unavailable = b.mk.noEntity
		case !hypAvailable:
			ep.Unavailable = "the cached eval record keeps no hypotheses to score"
		default:
			it, ok := b.itn[gs.Locale]
			if !ok {
				r, err := b.s.renderITN(ctx, pl.project, gs.Locale)
				if err != nil {
					return err
				}
				b.itn[gs.Locale], it = r, r
			}
			if it.ref == nil {
				ep.Unavailable = it.reason
			} else {
				ep.Scorer, ep.Config = b.mk.entity.Ref(), it.ref.Hash
			}
		}
		// Latency to final: a streaming family's partials and utterance ends from a VAD.
		lp := MetricPlan{}
		switch {
		case !sl.ms.model.family.Streaming:
			lp.Unavailable = fmt.Sprintf("model family %s does not stream: its decodes carry no partial events", sl.ms.model.Family)
		case b.mk.latency == nil:
			lp.Unavailable = b.mk.noLatency
		case b.mk.vad == nil:
			lp.Unavailable = b.mk.noVADWhy
		case !hypAvailable:
			lp.Unavailable = "the cached eval record keeps no hypotheses to score"
		default:
			lp.Scorer, lp.Config = b.mk.latency.Ref(), b.mk.vad.Ref()+"#"+b.mk.vad.VersionID
			a, err := b.alignment(ctx, sl.gs, sl.aug)
			if err != nil {
				return err
			}
			if a != nil { // emission delay: the alignment is part of the metric's configuration
				lp.Config += "|alignment:" + a.Artifact
			}
		}
		for metric, mp := range map[string]MetricPlan{MetricEntities: ep, MetricLatency: lp} {
			if mp.Scorer != "" {
				_, _, _, found, err := findMetric(ctx, b.q, metricKey{sl.key.ModelKey, sl.key.GoldenSetVersionID, sl.key.DecodingHash, mp.Scorer, mp.Config})
				if err != nil {
					return err
				}
				if !found {
					mp.Step = "pending" // named when the pipeline is generated
				}
			}
			sl.plans[metric] = mp
		}
	}
	return nil
}

// pipeline generates the eval's pipeline: per model that needs it a materialize step (base models); per golden set
// and augmentation an augment step (augmented cells) and a VAD step (latency); per record to compute a transcribe and
// a score step; per record key missing a metric its metric step (03 "The eval pipeline").
func (b *builder) pipeline(ctx context.Context) error {
	pl := b.plan
	audio := 0.0
	for _, u := range b.order {
		audio += pl.GoldenSets[u.gs].Hours
	}
	pl.Estimate = Estimate{GPUHours: round3(audio * b.perAudioHour), AudioHours: round3(audio), CellsToCompute: len(b.order),
		GPUHoursPerAudioHour: b.perAudioHour, Basis: "table"}
	metricSteps := 0
	for _, ks := range b.slotOrder {
		for _, mp := range b.slots[ks].plans {
			if mp.Step != "" {
				metricSteps++
			}
		}
	}
	if len(b.order) == 0 && metricSteps == 0 {
		b.cellMetrics()
		return nil
	}
	g := &gen{b: b, p: pipelines.Pipeline{Name: "eval", Description: fmt.Sprintf("Eval of %s against %s: %d cell(s) to compute (evals.new)",
		pl.Subject.Label, pl.Baseline.Label, len(b.order)), Inputs: map[string]string{}},
		inputs: map[string]steps.ArtifactRef{}, params: map[string]map[string]any{}, estimates: map[string]float64{},
		data: map[[2]int]string{}, vad: map[[2]int]string{}, hyp: map[string]string{}}
	for _, u := range b.order {
		if err := g.unit(ctx, u); err != nil {
			return err
		}
	}
	for _, ks := range b.slotOrder {
		if err := g.metrics(ctx, ks); err != nil {
			return err
		}
	}
	if len(g.fields) > 0 {
		return problems.Validation(g.fields)
	}
	b.cellMetrics()
	start := pipelines.StartInput{ProjectID: pl.project.ID, Pipeline: &g.p, Inputs: g.inputs, Params: g.params, Estimates: g.estimates,
		Actor: pl.in.Actor, Priority: pl.in.Priority}
	if _, _, err := b.s.Engine.Prepare(ctx, b.q, start); err != nil {
		return err
	}
	pl.start = &start
	return nil
}

// cellMetrics copies each record key's metric plans onto its cells.
func (b *builder) cellMetrics() {
	for i := range b.plan.Cells {
		if sl, ok := b.slots[b.plan.Cells[i].key]; ok {
			b.plan.Cells[i].metrics = sl.plans
		}
	}
}

// gen is one generated eval pipeline under construction.
type gen struct {
	b         *builder
	p         pipelines.Pipeline
	inputs    map[string]steps.ArtifactRef
	params    map[string]map[string]any
	estimates map[string]float64
	data      map[[2]int]string // (golden set, augmentation) → the wire of its dataset
	vad       map[[2]int]string // (golden set, augmentation) → the wire of its VAD
	hyp       map[string]string // record key → the wire of its hypotheses
	fields    []problems.FieldError
}

func (g *gen) step(id string, k pipelines.Kind, in map[string]string) {
	g.p.Steps = append(g.p.Steps, pipelines.Step{ID: id, Kind: k.Ref(), In: in})
	g.b.plan.Steps = append(g.b.plan.Steps, PlanStep{Step: id, Kind: k.Ref()})
}

// optional marks the step just added optional: a metric reported beside WER (and the VAD it reads) never fails the
// eval; the cell shows that metric unavailable with the step's error (finish).
func (g *gen) optional() { g.p.Steps[len(g.p.Steps)-1].Optional = true }

func (g *gen) input(name, typ string, ref steps.ArtifactRef) string {
	if _, ok := g.inputs[name]; !ok {
		g.p.Inputs[name], g.inputs[name] = typ, ref
	}
	return pipelines.InputsRef + name
}

// golden wires golden set gi's dataset and normalizer as pipeline inputs.
func (g *gen) golden(ctx context.Context, gi int) (data, norm string, err error) {
	data, norm = fmt.Sprintf("data_g%d", gi+1), fmt.Sprintf("norm_g%d", gi+1)
	if _, ok := g.inputs[data]; ok {
		return pipelines.InputsRef + data, pipelines.InputsRef + norm, nil
	}
	gs := g.b.plan.GoldenSets[gi]
	ref, err := g.b.s.sized(ctx, g.b.q, steps.ArtifactRef{Hash: gs.datasetHash, Type: TypeDataset})
	if err != nil {
		return "", "", err
	}
	nv, err := registry.GetVersion(ctx, g.b.q, registry.KindNormalizer, gs.NormalizerVersionID)
	if err != nil {
		return "", "", err
	}
	nref, err := RenderNormalizer(g.b.s, nv)
	if err != nil {
		return "", "", err
	}
	return g.input(data, TypeDataset, ref), g.input(norm, TypeNormalizer, nref), nil
}

// dataset is the wire of golden set gi's dataset under augmentation ai: the golden dataset, or the output of the
// augment step that applies the profile to it (one per golden set and augmentation).
func (g *gen) dataset(ctx context.Context, gi, ai int) (string, error) {
	if w, ok := g.data[[2]int{gi, ai}]; ok {
		return w, nil
	}
	golden, _, err := g.golden(ctx, gi)
	if err != nil {
		return "", err
	}
	a := g.b.plan.Augmentations[ai]
	if a.none() {
		g.data[[2]int{gi, ai}] = golden
		return golden, nil
	}
	k := *g.b.augment
	in := map[string]string{}
	for port, typ := range k.Consumes {
		switch {
		case typ == TypeAugmentProfile:
			in[port] = g.input(fmt.Sprintf("aug_a%d", ai), TypeAugmentProfile, *a.ref)
		case typ == TypeDataset && slices.Contains(k.OptionalInputs, port):
			if a.noise != nil {
				in[port] = g.input(fmt.Sprintf("noise_a%d", ai), TypeDataset, *a.noise)
			}
		case typ == TypeDataset:
			in[port] = golden
		default:
			return "", problems.RecipeMismatch.New("the augment step kind %s consumes %s (%s), which an eval cannot fill", k.Ref(), port, typ)
		}
	}
	out, ok := producesType(k, TypeDataset)
	if !ok {
		return "", problems.RecipeMismatch.New("the augment step kind %s produces no dataset", k.Ref())
	}
	id := fmt.Sprintf("augment-g%da%d", gi+1, ai)
	g.step(id, k, in)
	w := id + "." + out
	g.data[[2]int{gi, ai}] = w
	return w, nil
}

// vadOf is the wire of the VAD of golden set gi under augmentation ai.
func (g *gen) vadOf(ctx context.Context, gi, ai int) (string, error) {
	if w, ok := g.vad[[2]int{gi, ai}]; ok {
		return w, nil
	}
	data, err := g.dataset(ctx, gi, ai)
	if err != nil {
		return "", err
	}
	k := *g.b.mk.vad
	port, _ := consumesType(k, TypeDataset)
	out, _ := producesType(k, TypeVAD)
	id := fmt.Sprintf("vad-g%da%d", gi+1, ai)
	g.step(id, k, map[string]string{port: data})
	g.optional()
	w := id + "." + out
	g.vad[[2]int{gi, ai}] = w
	return w, nil
}

// checkpoint wires a model's checkpoint: the input, or its materialize step.
func (g *gen) checkpoint(ms *modelSteps) error {
	if ms.wire != "" {
		return nil
	}
	if ms.materialize == nil {
		ms.wire = g.input(fmt.Sprintf("model_m%d", ms.index), TypeCheckpoint, ms.model.artifact)
		return nil
	}
	in := g.input(fmt.Sprintf("base_m%d", ms.index), TypeBaseModel, ms.model.artifact)
	port, ok := consumesType(*ms.materialize, TypeBaseModel)
	out, ok2 := producesType(*ms.materialize, TypeCheckpoint)
	if !ok || !ok2 || len(ms.materialize.Consumes) != 1 {
		return problems.RecipeMismatch.New("the materialize step kind %s must consume one base_model and produce a checkpoint (it consumes %v, produces %v)",
			ms.materialize.Ref(), ms.materialize.Consumes, ms.materialize.Produces)
	}
	id := fmt.Sprintf("materialize-m%d", ms.index)
	g.step(id, *ms.materialize, map[string]string{port: in})
	ms.wire = id + "." + out
	return nil
}

// unit generates a record's transcribe and score steps.
func (g *gen) unit(ctx context.Context, u *unit) error {
	b, pl, ms := g.b, g.b.plan, u.ms
	if err := g.checkpoint(ms); err != nil {
		return err
	}
	gs := pl.GoldenSets[u.gs]
	data, err := g.dataset(ctx, u.gs, u.aug)
	if err != nil {
		return err
	}
	_, norm, err := g.golden(ctx, u.gs)
	if err != nil {
		return err
	}
	n := slices.Index(b.order, u) + 1
	tid, sid := fmt.Sprintf("transcribe-u%d", n), fmt.Sprintf("score-u%d", n)
	u.transcode, u.score = tid, sid
	tin := map[string]string{}
	for port, typ := range ms.transcribe.Consumes {
		switch typ {
		case TypeCheckpoint, TypeBaseModel:
			tin[port] = ms.wire
		case TypeDataset:
			tin[port] = data
		case TypeBoostList:
			ref := pl.Decoding[u.dec].ref
			if ref == nil && slices.Contains(ms.transcribe.OptionalInputs, port) {
				continue // boost none: the optional list stays unwired
			}
			in := fmt.Sprintf("boost_d%d", u.dec+1)
			if ref == nil { // a kind that always reads a list: an empty one
				in = "boost_none"
				empty, err := b.s.putBoost(langpacks.Boost{Terms: []string{}}, map[string]any{"terms": 0})
				if err != nil {
					return err
				}
				ref = &empty
			}
			tin[port] = g.input(in, TypeBoostList, *ref)
		default:
			g.fields = append(g.fields, problems.FieldError{Path: "/subject", Message: fmt.Sprintf(
				"the transcribe step kind %s consumes %s (%s), which an eval cannot fill", ms.transcribe.Ref(), port, typ)})
		}
	}
	hyp, ok := producesType(ms.transcribe, TypeHypotheses)
	if !ok {
		return problems.RecipeMismatch.New("the transcribe step kind %s produces no hypotheses", ms.transcribe.Ref())
	}
	g.params[tid] = ms.transcribeParams(u.profile, gs)
	g.estimates[tid] = gs.Hours * b.perAudioHour * 3600
	g.step(tid, ms.transcribe, tin)
	g.hyp[u.key.String()] = tid + "." + hyp
	sin := map[string]string{}
	for port, typ := range b.scorer.Consumes {
		switch typ {
		case TypeHypotheses:
			sin[port] = tid + "." + hyp
		case TypeDataset:
			sin[port] = data
		case TypeNormalizer:
			sin[port] = norm
		default:
			g.fields = append(g.fields, problems.FieldError{Path: "/", Message: fmt.Sprintf(
				"the scorer %s consumes %s (%s), which an eval cannot fill", b.scorer.Ref(), port, typ)})
		}
	}
	if _, ok := producesType(b.scorer, TypeScores); !ok {
		return problems.RecipeMismatch.New("the scorer %s produces no scores", b.scorer.Ref())
	}
	g.step(sid, b.scorer, sin)
	return nil
}

// metrics generates the metric steps one record key misses.
func (g *gen) metrics(ctx context.Context, ks string) error {
	b := g.b
	sl := b.slots[ks]
	n := slices.Index(b.slotOrder, ks) + 1
	for _, metric := range []string{MetricEntities, MetricLatency} {
		mp := sl.plans[metric]
		if mp.Step == "" {
			continue
		}
		hyp, ok := g.hyp[ks]
		if !ok { // a cached record: its hypotheses are an input
			rec := b.cached[ks]
			ref, err := b.s.sized(ctx, b.q, steps.ArtifactRef{Hash: rec.Hypotheses, Type: TypeHypotheses})
			if err != nil {
				return err
			}
			hyp = g.input(fmt.Sprintf("hyp_r%d", n), TypeHypotheses, ref)
			g.hyp[ks] = hyp
		}
		data, err := g.dataset(ctx, sl.gs, sl.aug)
		if err != nil {
			return err
		}
		var k pipelines.Kind
		var id string
		in := map[string]string{}
		switch metric {
		case MetricEntities:
			k, id = *b.mk.entity, fmt.Sprintf("entities-k%d", n)
		default:
			k, id = *b.mk.latency, fmt.Sprintf("latency-k%d", n)
		}
		for port, typ := range k.Consumes {
			switch typ {
			case TypeHypotheses:
				in[port] = hyp
			case TypeDataset:
				in[port] = data
			case TypeITN:
				in[port] = g.input(fmt.Sprintf("itn_g%d", sl.gs+1), TypeITN, *b.itn[b.plan.GoldenSets[sl.gs].Locale].ref)
			case TypeNormalizer:
				_, norm, err := g.golden(ctx, sl.gs)
				if err != nil {
					return err
				}
				in[port] = norm
			case goldensets.TypeAlignment:
				a, err := b.alignment(ctx, sl.gs, sl.aug)
				if err != nil {
					return err
				}
				if a == nil {
					if slices.Contains(k.OptionalInputs, port) {
						continue // no aligned references: the scorer reports emission delay unavailable
					}
					return problems.RecipeMismatch.New("the metric scorer %s needs aligned references, which golden set %s has none of",
						k.Ref(), b.plan.GoldenSets[sl.gs].Name)
				}
				ref, err := b.s.sized(ctx, b.q, steps.ArtifactRef{Hash: a.Artifact, Type: goldensets.TypeAlignment})
				if err != nil {
					return err
				}
				in[port] = g.input(fmt.Sprintf("align_g%d", sl.gs+1), goldensets.TypeAlignment, ref)
			case TypeVAD:
				w, err := g.vadOf(ctx, sl.gs, sl.aug)
				if err != nil {
					return err
				}
				in[port] = w
			default:
				return problems.RecipeMismatch.New("the metric scorer %s consumes %s (%s), which an eval cannot fill", k.Ref(), port, typ)
			}
		}
		if _, ok := producesType(k, TypeMetricScores); !ok {
			return problems.RecipeMismatch.New("the metric scorer %s produces no metric_scores", k.Ref())
		}
		g.step(id, k, in)
		g.optional()
		mp.Step = id
		sl.plans[metric] = mp
	}
	return nil
}

// alignment is the reference alignment latency to final's scorer reads for golden set gi under augmentation ai: the
// newest alignment of the golden set's dataset artifact, for the unaugmented audio only (an augmentation may change
// the timing), and only when the scorer consumes one. nil when there is none.
func (b *builder) alignment(ctx context.Context, gi, ai int) (*goldensets.Alignment, error) {
	if b.mk.latency == nil || !b.plan.Augmentations[ai].none() {
		return nil, nil
	}
	if _, ok := consumesType(*b.mk.latency, goldensets.TypeAlignment); !ok {
		return nil, nil
	}
	if a, ok := b.align[gi]; ok {
		return a, nil
	}
	a, err := goldensets.LatestAlignment(ctx, b.q, b.plan.GoldenSets[gi].datasetHash)
	if err != nil {
		return nil, err
	}
	b.align[gi] = a
	return a, nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
