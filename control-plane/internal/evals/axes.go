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
	"strings"

	"github.com/jackc/pgx/v5"
	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/langpacks"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// The robustness axis and the metrics beside WER (phase 3 stream R; docs/spec/03-pipelines-defaults.md
// "Augmentation", "Scorers and metrics"; docs/spec/04-blocks.md "Task and streaming metrics"; R54). The kinds are
// neutral core kinds every runtime publishes, except the VAD, which is found by what it produces (a family's pack
// carries it; Go never names it).
const (
	AugmentKind       = "augment_dataset"
	EntityScorerKind  = "entity_score"
	LatencyScorerKind = "latency_score"

	TypeAugmentProfile = "augment_profile"
	TypeITN            = "itn"
	TypeVAD            = "vad"
	TypeMetricScores   = "metric_scores"

	MetricEntities = "entities"
	MetricLatency  = "latency"

	AugmentNone          = "none"
	AugmentProfileFormat = "cadence.augment_profile/1"
	ITNFormat            = "cadence.itn/1"
	MetricScoresFormat   = "cadence.metric-scores/1"
)

// AugmentationIn is one requested augmentation: none, or a profile file of the project repository at a commit, with
// an optional seed.
type AugmentationIn struct {
	Profile string
	Seed    *int
}

// Augmentation is one augmentation of an eval (the contract's EvalAugmentation). Index 0 is always none.
type Augmentation struct {
	Index      int             `json:"index"`
	Profile    string          `json:"profile"` // none | augment/<name>.yaml@<commit>
	Name       string          `json:"name,omitempty"`
	Seed       *int            `json:"seed,omitempty"`
	Hash       string          `json:"hash,omitempty"`
	Artifact   string          `json:"artifact,omitempty"`
	NoiseBank  string          `json:"noiseBank,omitempty"`
	Transforms json.RawMessage `json:"transforms,omitempty"`

	ref   *steps.ArtifactRef
	noise *steps.ArtifactRef
}

// none reports whether a is the unaugmented golden set.
func (a Augmentation) none() bool { return a.Profile == "" || a.Profile == AugmentNone }

var augmentPathRe = regexp.MustCompile(`^augment/[A-Za-z0-9._-]+\.ya?ml$`)

// transformFields maps each transform of a profile file to its fields and the defaults.yaml augment.* key that
// gives each its default and range (docs/spec/03 "Profile file"); noise.bank has no default.
var transformFields = map[string]map[string]string{
	"codec":      {"probability": "codec_probability", "codecs": "codecs"},
	"band_limit": {"probability": "band_limit_probability", "cutoff_hz": "band_limit_hz"},
	"level":      {"probability": "level_probability", "gain_db": "level_gain_db"},
	"speed":      {"probability": "speed_probability", "factor": "speed_factor"},
	"noise":      {"probability": "noise_probability", "snr_db": "noise_snr_db", "bank": ""},
}

// profileFile is augment/<name>.yaml; keys outside these stay in the file (the Recipe form keeps them) and are ignored.
type profileFile struct {
	Name       string                    `yaml:"name"`
	Seed       *int                      `yaml:"seed"`
	Transforms map[string]map[string]any `yaml:"transforms"`
}

// renderAugmentations resolves the requested augmentations: none first, then each profile read from the repository
// at its commit, every value it leaves out taken from defaults.yaml augment.*, checked against the ranges there, and
// rendered as an augment_profile artifact with its content hash (transforms and seed).
func (s *Service) renderAugmentations(ctx context.Context, q storage.Querier, p projects.Project, in []AugmentationIn) ([]Augmentation, error) {
	out := []Augmentation{{Index: 0, Profile: AugmentNone}}
	var fields []problems.FieldError
	seen := map[string]int{}
	for i, a := range in {
		at := fmt.Sprintf("/augmentations/%d", i)
		ref := strings.TrimSpace(a.Profile)
		if ref == "" || ref == AugmentNone {
			if a.Seed != nil {
				fields = append(fields, problems.FieldError{Path: at + "/seed", Message: "a seed needs a profile"})
			}
			continue
		}
		path, sha, ok := strings.Cut(ref, "@")
		if !ok || !augmentPathRe.MatchString(path) || !commitRe.MatchString(sha) {
			fields = append(fields, problems.FieldError{Path: at + "/profile",
				Message: fmt.Sprintf("%q is not none or augment/<name>.yaml@<commit sha>", ref)})
			continue
		}
		repo := s.repo()
		if repo == nil || !repo.Exists(p.Slug) {
			fields = append(fields, problems.FieldError{Path: at + "/profile", Message: "the project has no repository to read the profile from"})
			continue
		}
		b, _, err := repo.ReadFile(ctx, p.Slug, sha, path)
		if err != nil {
			msg := fmt.Sprintf("%s cannot be read at %s: %v", path, sha, err)
			if errors.Is(err, repos.ErrNotFound) {
				msg = fmt.Sprintf("%s does not exist at %s", path, sha)
			}
			fields = append(fields, problems.FieldError{Path: at + "/profile", Message: msg})
			continue
		}
		aug, errs, err := s.resolveProfile(ctx, q, p, b, a.Seed)
		if err != nil {
			return nil, err
		}
		for _, e := range errs {
			fields = append(fields, problems.FieldError{Path: at + "/profile", Message: path + ": " + e})
		}
		if len(errs) > 0 {
			continue
		}
		if j, dup := seen[aug.Hash]; dup {
			fields = append(fields, problems.FieldError{Path: at, Message: fmt.Sprintf("the same profile and seed as augmentation %d", j)})
			continue
		}
		seen[aug.Hash] = i
		aug.Index, aug.Profile = len(out), ref
		out = append(out, aug)
	}
	if len(fields) > 0 {
		return nil, problems.Validation(fields)
	}
	return out, nil
}

// resolveProfile turns a profile file into the rendered artifact; errs are the file's problems.
func (s *Service) resolveProfile(ctx context.Context, q storage.Querier, p projects.Project, b []byte, seed *int) (Augmentation, []string, error) {
	var f profileFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return Augmentation{}, []string{fmt.Sprintf("not a profile (name, seed, transforms): %v", err)}, nil
	}
	defs := s.defaults().Augment
	var errs []string
	transforms := map[string]map[string]any{}
	var noise *steps.ArtifactRef
	var bankID string
	for _, name := range sortedKeys(f.Transforms) {
		spec, ok := transformFields[name]
		if !ok {
			errs = append(errs, fmt.Sprintf("transforms.%s is not a transform eval augmentation applies (%s)", name,
				strings.Join(sortedKeys(transformFields), ", ")))
			continue
		}
		t := map[string]any{}
		for k := range f.Transforms[name] {
			if _, known := spec[k]; !known {
				errs = append(errs, fmt.Sprintf("transforms.%s.%s is not a field of %s (%s)", name, k, name, strings.Join(sortedKeys(spec), ", ")))
			}
		}
		for field, key := range spec {
			v, set := f.Transforms[name][field]
			if key == "" { // noise.bank: no default
				continue
			}
			def, rng := defaultOf(defs, key)
			if !set || v == nil {
				v = def
			}
			if msg := checkValue(v, rng); msg != "" {
				errs = append(errs, fmt.Sprintf("transforms.%s.%s: %s", name, field, msg))
			}
			t[field] = v
		}
		if name == "noise" {
			bank, _ := f.Transforms[name]["bank"].(string)
			if strings.TrimSpace(bank) == "" {
				errs = append(errs, "transforms.noise.bank names no noise bank (noise-bank/<name> or ver_…)")
				continue
			}
			v, err := registry.Resolve(ctx, q, p.ID, registry.KindNoiseBank, strings.TrimSpace(bank))
			if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
				errs = append(errs, fmt.Sprintf("transforms.noise.bank: no noise bank %s", bank))
				continue
			}
			if err != nil {
				return Augmentation{}, nil, err
			}
			var np struct {
				Artifact steps.ArtifactRef `json:"artifact"`
			}
			if json.Unmarshal(v.Payload, &np) != nil || !steps.ValidHash(np.Artifact.Hash) {
				errs = append(errs, fmt.Sprintf("transforms.noise.bank: %s %s has no artifact", v.Name, v.Version))
				continue
			}
			ref, err := s.sized(ctx, q, steps.ArtifactRef{Hash: np.Artifact.Hash, Type: TypeDataset})
			if err != nil {
				return Augmentation{}, nil, err
			}
			noise, bankID = &ref, v.ID
			t["bank"] = v.ID
		}
		transforms[name] = t
	}
	if len(errs) > 0 {
		return Augmentation{}, errs, nil
	}
	sd := seed
	if sd == nil {
		sd = f.Seed
	}
	if sd == nil {
		def, _ := defaultOf(defs, "seed")
		if n, ok := def.(int); ok {
			sd = &n
		} else {
			zero := 0
			sd = &zero
		}
	}
	if *sd < 0 || *sd > math.MaxInt32 {
		return Augmentation{}, []string{fmt.Sprintf("seed %d is outside 0 to %d", *sd, math.MaxInt32)}, nil
	}
	name := f.Name
	if name == "" {
		name = "custom"
	}
	sum := sha256.Sum256(mustJSON(map[string]any{"seed": *sd, "transforms": transforms}))
	hash := "sha256:" + hex.EncodeToString(sum[:])
	doc := mustJSON(map[string]any{"format": AugmentProfileFormat, "name": name, "seed": *sd, "hash": hash, "transforms": transforms})
	if s.CAS == nil {
		return Augmentation{}, nil, errors.New("evals: no content store")
	}
	h, err := s.CAS.PutBytes(doc)
	if err != nil {
		return Augmentation{}, nil, err
	}
	ref := steps.ArtifactRef{Hash: h, Type: TypeAugmentProfile, Size: int64(len(doc)), Meta: mustJSON(map[string]any{"name": name, "hash": hash})}
	return Augmentation{Name: name, Seed: sd, Hash: hash, Artifact: h, NoiseBank: bankID, Transforms: mustJSON(transforms), ref: &ref, noise: noise}, nil, nil
}

// defaultOf reads augment.<key> of defaults.yaml: its value and range.
func defaultOf(defs map[string]any, key string) (any, map[string]any) {
	e, _ := defs[key].(map[string]any)
	rng, _ := e["range"].(map[string]any)
	return e["value"], rng
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// checkValue checks a profile value against a defaults.yaml range: {min, max} for a number or each number of a pair
// (a pair also ascending), {values} for each string of a list.
func checkValue(v any, rng map[string]any) string {
	lo, hasLo := number(rng["min"])
	hi, hasHi := number(rng["max"])
	inRange := func(x float64) string {
		if (hasLo && x < lo) || (hasHi && x > hi) {
			return fmt.Sprintf("%g is outside %g to %g", x, lo, hi)
		}
		return ""
	}
	switch t := v.(type) {
	case []any:
		if allowed, ok := rng["values"].([]any); ok {
			if len(t) == 0 {
				return "the list is empty"
			}
			for _, x := range t {
				if !slices.Contains(allowed, x) {
					return fmt.Sprintf("%v is not one of %v", x, allowed)
				}
			}
			return ""
		}
		if len(t) != 2 {
			return "a range is two numbers [low, high]"
		}
		a, ok1 := number(t[0])
		b, ok2 := number(t[1])
		if !ok1 || !ok2 {
			return "a range is two numbers [low, high]"
		}
		if a > b {
			return fmt.Sprintf("the low %g is above the high %g", a, b)
		}
		if m := inRange(a); m != "" {
			return m
		}
		return inRange(b)
	default:
		x, ok := number(v)
		if !ok {
			return fmt.Sprintf("%v is not a number", v)
		}
		return inRange(x)
	}
}

// ---------------------------------------------------------------- language pack ITN

// itnRender is a rendered itn artifact, or why there is none.
type itnRender struct {
	ref    *steps.ArtifactRef
	reason string
}

// renderITN renders the itn.yaml of the pack serving locale at main's head (the pack the golden set's locale
// matches, langpacks.Match) as an itn artifact, with the commit that last changed the file. reason says why there is
// none (no repository, no pack, no classes).
func (s *Service) renderITN(ctx context.Context, p projects.Project, locale string) (itnRender, error) {
	repo := s.repo()
	if repo == nil || !repo.Exists(p.Slug) {
		return itnRender{reason: "the project has no repository with a language pack"}, nil
	}
	commit, files, err := repo.ListFiles(ctx, p.Slug, repos.Main, langpacks.Dir)
	if errors.Is(err, repos.ErrNotFound) {
		return itnRender{reason: "the project has no language packs (lang/)"}, nil
	}
	if err != nil {
		return itnRender{}, err
	}
	var dirs []string
	for _, f := range files {
		rest, ok := strings.CutPrefix(f.Path, langpacks.Dir+"/")
		if name, _, nested := strings.Cut(rest, "/"); ok && nested && !slices.Contains(dirs, name) {
			dirs = append(dirs, name)
		}
	}
	dir, ok := langpacks.Match(dirs, locale)
	if !ok {
		return itnRender{reason: fmt.Sprintf("the project has no language pack for %s", locale)}, nil
	}
	path := langpacks.PackPath(dir) + "/itn.yaml"
	b, _, err := repo.ReadFile(ctx, p.Slug, commit, path)
	if errors.Is(err, repos.ErrNotFound) {
		return itnRender{reason: fmt.Sprintf("the %s pack has no itn.yaml", dir)}, nil
	}
	if err != nil {
		return itnRender{}, err
	}
	var itn langpacks.ITN
	if err := yaml.Unmarshal(b, &itn); err != nil {
		return itnRender{reason: fmt.Sprintf("%s does not parse: %v", path, err)}, nil
	}
	type example struct {
		Spoken  string `json:"spoken"`
		Written string `json:"written"`
	}
	type class struct {
		Name     string    `json:"name"`
		Pattern  string    `json:"pattern"`
		Examples []example `json:"examples"`
	}
	var classes []class
	for _, c := range itn.Classes {
		if c.Name == "" || c.Pattern == "" {
			continue
		}
		if _, err := regexp.Compile(c.Pattern); err != nil {
			return itnRender{reason: fmt.Sprintf("%s: the pattern of class %s does not compile: %v", path, c.Name, err)}, nil
		}
		cl := class{Name: c.Name, Pattern: c.Pattern, Examples: []example{}}
		for _, e := range c.Examples {
			cl.Examples = append(cl.Examples, example{Spoken: e.Spoken, Written: e.Written})
		}
		classes = append(classes, cl)
	}
	if len(classes) == 0 {
		return itnRender{reason: fmt.Sprintf("%s has no classes with a pattern", path)}, nil
	}
	fileCommit := commit
	if hist, err := repo.History(ctx, p.Slug, commit, path, 1); err == nil && len(hist) > 0 {
		fileCommit = hist[0].SHA
	}
	doc := mustJSON(map[string]any{"format": ITNFormat, "locale": dir, "path": path, "commit": fileCommit, "version": itn.Version,
		"classes": classes})
	if s.CAS == nil {
		return itnRender{}, errors.New("evals: no content store")
	}
	h, err := s.CAS.PutBytes(doc)
	if err != nil {
		return itnRender{}, err
	}
	ref := steps.ArtifactRef{Hash: h, Type: TypeITN, Size: int64(len(doc)), Meta: mustJSON(map[string]any{"locale": dir, "commit": fileCommit})}
	return itnRender{ref: &ref}, nil
}

// ---------------------------------------------------------------- metric kinds

// metricKinds are the scorers beside WER and the VAD, each with why it is missing.
type metricKinds struct {
	entity, latency, vad          *pipelines.Kind
	noEntity, noLatency, noVADWhy string
}

func optionalKind(ctx context.Context, q storage.Querier, name string) (*pipelines.Kind, string, error) {
	k, err := runs.RoleKind(ctx, q, name)
	if pe, ok := problems.As(err); ok && pe.Type == problems.FamilyUnavailable {
		return nil, fmt.Sprintf("no runtime publishes the step kind %s", name), nil
	}
	if err != nil {
		return nil, "", err
	}
	return &k, "", nil
}

// kindProducing finds the newest published step kind that turns one dataset into an artifact of type typ (the VAD:
// a pack carries it, and Go does not name it).
func kindProducing(ctx context.Context, q storage.Querier, typ string) (*pipelines.Kind, error) {
	list, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindStepKind})
	if err != nil {
		return nil, err
	}
	for _, v := range list { // newest first
		if v.State == registry.StateDeprecated {
			continue
		}
		var k pipelines.Kind
		if json.Unmarshal(v.Payload, &k) != nil {
			continue
		}
		if _, ok := producesType(k, typ); !ok || len(k.Consumes) != 1 {
			continue
		}
		if _, ok := consumesType(k, TypeDataset); !ok {
			continue
		}
		if k.Name == "" {
			k.Name = strings.TrimPrefix(v.Name, "step-kind/")
		}
		k.VersionID = v.ID
		return &k, nil
	}
	return nil, nil
}

func loadMetricKinds(ctx context.Context, q storage.Querier) (metricKinds, error) {
	var mk metricKinds
	var err error
	if mk.entity, mk.noEntity, err = optionalKind(ctx, q, EntityScorerKind); err != nil {
		return mk, err
	}
	if mk.latency, mk.noLatency, err = optionalKind(ctx, q, LatencyScorerKind); err != nil {
		return mk, err
	}
	if mk.vad, err = kindProducing(ctx, q, TypeVAD); err != nil {
		return mk, err
	}
	if mk.vad == nil {
		mk.noVADWhy = "no runtime publishes a VAD step kind (a dataset → vad step, e.g. a framework pack's frame VAD); utterance ends are unknown"
	}
	return mk, nil
}

// MetricPlan is what a cell expects of one metric: the scorer and configuration that key it and the step computing
// it, or why it is unavailable.
type MetricPlan struct {
	Scorer      string `json:"scorer,omitempty"`
	Config      string `json:"config,omitempty"`
	Step        string `json:"step,omitempty"`
	Unavailable string `json:"unavailable,omitempty"`
}

// metricKey is the identity of an eval_metrics row.
type metricKey struct {
	ModelKey, GoldenSetVersionID, DecodingHash, Scorer, Config string
}

func findMetric(ctx context.Context, q storage.Querier, k metricKey) (string, json.RawMessage, string, bool, error) {
	var id, scores string
	var summary json.RawMessage
	err := q.QueryRow(ctx, `SELECT id, summary, scores_hash FROM eval_metrics WHERE model_key = $1 AND golden_set_version_id = $2
		AND decoding_hash = $3 AND scorer = $4 AND config = $5`, k.ModelKey, k.GoldenSetVersionID, k.DecodingHash, k.Scorer, k.Config).
		Scan(&id, &summary, &scores)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, "", false, nil
	}
	if err != nil {
		return "", nil, "", false, fmt.Errorf("find eval metric: %w", err)
	}
	return id, summary, scores, true, nil
}
