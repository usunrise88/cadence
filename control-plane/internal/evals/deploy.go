package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Deployment (phase 5 · stream D1, internal/modelexports) reads model versions and golden sets as evals do: an
// export transcribes nothing, but its parity check decodes a golden set with the model's own checkpoint in the
// language its gating eval used, so the facade below exposes exactly those pieces.

// DeployModel is a registered model version resolved for a project: its payload, its family descriptor and the
// checkpoint artifact the family's export and parity-reference steps read.
type DeployModel struct {
	Version    registry.Version
	Payload    ModelPayload
	Family     runs.Family
	Descriptor json.RawMessage // the family descriptor (model-family registry payload)
	Profiles   []Profile
	Checkpoint steps.ArtifactRef
	Base       registry.Version
}

// Label is how plans name the version: model/<name> <version>.
func (m DeployModel) Label() string { return m.Version.Name + " " + m.Version.Version }

// ResolveModelVersion resolves ref (ver_…, @alias or model/<name>) to a model version the project can use, with its
// family and checkpoint.
func (s *Service) ResolveModelVersion(ctx context.Context, q storage.Querier, projectID, ref string) (DeployModel, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return DeployModel{}, problems.Validation([]problems.FieldError{{Path: "/version", Message: "name a model version: ver_…, @alias or model/<name>"}})
	}
	v, err := registry.Resolve(ctx, q, projectID, registry.KindModel, ref)
	if err != nil {
		return DeployModel{}, err
	}
	m, err := s.versionModel(ctx, q, v)
	if err != nil {
		return DeployModel{}, err
	}
	var mp ModelPayload
	if err := json.Unmarshal(v.Payload, &mp); err != nil {
		return DeployModel{}, fmt.Errorf("decode model %s: %w", v.ID, err)
	}
	fv, err := registry.GetVersion(ctx, q, registry.KindModelFamily, m.family.VersionID)
	if err != nil {
		return DeployModel{}, err
	}
	return DeployModel{Version: v, Payload: mp, Family: m.family.Family, Descriptor: fv.Payload, Profiles: m.family.Profiles,
		Checkpoint: m.artifact, Base: m.base}, nil
}

// PrimaryProfile is the project's primary latency profile: gates.yaml primaryProfile, else eval.primary_profile.
func (s *Service) PrimaryProfile(ctx context.Context, slug string) (string, error) {
	gf, err := ReadGate(ctx, s.repo(), slug, s.defaults())
	if err != nil {
		return "", err
	}
	return gf.Gate.PrimaryProfile, nil
}

// SampleSet is a golden set a parity sample is drawn from, with the language the model decodes it in.
type SampleSet struct {
	GoldenSet
	DatasetHash string
	Language    string
}

// SampleGoldenSet resolves the golden set a parity check or benchmark samples: ref when given (ver_…, @alias or
// golden-set/<name>), else the project's first target golden set (gates.yaml's target list in order, else the
// golden sets it adopted that are not replay sets, newest first). The decode language is the one the model's gating
// eval (evalID) decoded that golden set (or another of its locale) in, else the set's locale.
func (s *Service) SampleGoldenSet(ctx context.Context, q storage.Querier, projectID, ref, evalID string) (SampleSet, error) {
	p, err := projects.GetByID(ctx, q, projectID)
	if err != nil {
		return SampleSet{}, err
	}
	gf, err := ReadGate(ctx, s.repo(), p.Slug, s.defaults())
	if err != nil {
		return SampleSet{}, err
	}
	var refs []string
	if strings.TrimSpace(ref) != "" {
		refs = []string{strings.TrimSpace(ref)}
	}
	sets, err := s.resolveGoldenSets(ctx, q, p, refs, gf.Gate)
	if err != nil {
		return SampleSet{}, err
	}
	var pick *GoldenSet
	if refs != nil {
		pick = &sets[0]
	} else {
		for _, name := range gf.Gate.TargetGoldenSets { // the gate's order
			for i := range sets {
				if pick == nil && Matches([]string{name}, sets[i].VersionID, sets[i].Name) {
					pick = &sets[i]
				}
			}
		}
		for i := range sets {
			if pick == nil && Target(sets[i], gf.Gate, p.Locales) {
				pick = &sets[i]
			}
		}
	}
	if pick == nil {
		return SampleSet{}, problems.Validation([]problems.FieldError{{Path: "/goldenSet",
			Message: "the project has no target golden set to sample (gates.yaml targets, or an adopted golden set of a project locale); name goldenSet"}})
	}
	if !steps.ValidHash(pick.datasetHash) {
		return SampleSet{}, problems.Conflict.New("golden set %s %s names no dataset artifact", pick.Name, pick.Version)
	}
	out := SampleSet{GoldenSet: *pick, DatasetHash: pick.datasetHash, Language: pick.Locale}
	if evalID != "" {
		if e, found, err := getEval(ctx, q, evalID, ""); err != nil {
			return SampleSet{}, err
		} else if found {
			out.Language = decodedAs(e.GoldenSets, *pick)
		}
	}
	return out, nil
}

// decodedAs is the language an eval decoded golden set g in: its own entry's, else one of the same locale's.
func decodedAs(sets []GoldenSet, g GoldenSet) string {
	for _, pass := range []func(GoldenSet) bool{
		func(x GoldenSet) bool { return x.VersionID == g.VersionID },
		func(x GoldenSet) bool { return x.Locale == g.Locale },
	} {
		for _, x := range sets {
			if pass(x) {
				return x.decodeLanguage()
			}
		}
	}
	return g.Locale
}

// LocaleParam is the parameter a kind takes the decode language in ("" when none): the naming convention of role
// kinds (target_lang, language, lang, locale).
func LocaleParam(k json.RawMessage) string {
	var sch struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	_ = json.Unmarshal(k, &sch)
	for _, n := range localeParams {
		if _, ok := sch.Properties[n]; ok {
			return n
		}
	}
	return ""
}

// GPUHoursPerAudioHour is the eval decode's estimate rate (eval.gpu_hours_per_audio_hour).
func (s *Service) GPUHoursPerAudioHour() float64 { return s.defaults().Eval.GPUHoursPerAudioHour.Value }
