package registry

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Adoption checks (docs/spec/02-domain-projects-registry.md "Registry": "Adoption checks licence and locale";
// docs/review/2026-10-03-phase-4-plan.md "Owner decisions" 2, R26: nothing is adopted that forbids commercial use of
// its outputs). They read only the version (its collection's licence and tags, its payload), so a kind joins them by
// what its payload says, not by code of its own.

// Adoption purposes (the contract's AdoptionNew.purpose).
const (
	PurposeTarget = "target" // the project's own languages: the locale is checked
	PurposeReplay = "replay" // another language kept to measure and limit forgetting: the locale is not checked
)

// kindAuxiliary is the auxiliary-model kind of phase 4 (stream X: LID classifiers, pseudo-label members, aligners);
// named here only so the checks below cover it once it registers.
const kindAuxiliary = "auxiliary"

// Published reports whether kind is published by workers at start (runtime, model family, step kind): such versions
// are pinned by pipelines (kind@version) and never adopted or archived by a person.
func Published(kind string) bool {
	return kind == KindRuntime || kind == KindModelFamily || kind == KindStepKind
}

// licensed lists the kinds whose versions carry data or weights from outside Cadence: their licence is checked at
// adoption. Templates and normalizers are Cadence's own configuration.
var licensed = []string{KindBaseModel, KindDataset, KindGoldenSet, KindModel, KindNoiseBank, kindAuxiliary}

// shipping lists the kinds whose content ends up in what a project trains or ships (a base model, a model, a noise
// bank mixed into training audio, a pseudo-label member): a non-commercial or no-derivatives licence is refused for
// them. A dataset version joins them unless it is eval-only; golden sets are only ever evaluated on.
var shipping = []string{KindBaseModel, KindModel, KindNoiseBank, kindAuxiliary}

// localized lists the kinds whose locale must be one of the project's for a target adoption. Base models and model
// versions are exempt: adapting a model to a new language is what a project does.
var localized = []string{KindDataset, KindGoldenSet, KindNormalizer, kindAuxiliary}

// unusable are licence values that name no licence (case-insensitive), as for sources (no licence, no ingest).
var unusable = []string{"", "unknown", "none", "noassertion", "unlicensed", "n/a", "na", "tbd", "todo", "?"}

// nonCommercial and noDerivatives match licence ids that forbid commercial use or derivative works: CC-BY-NC-4.0,
// CC-BY-NC-SA, CC-BY-ND-4.0, "cc-by-nc", "non-commercial", "research only".
var (
	nonCommercial = regexp.MustCompile(`(?i)(^|[^a-z])nc([^a-z]|$)|non-?commercial|research[ -]only|noncommercial`)
	noDerivatives = regexp.MustCompile(`(?i)(^|[^a-z])nd([^a-z]|$)|no-?derivatives`)
)

// payloadFacts are the fields of a payload the checks read; any kind may carry them.
type payloadFacts struct {
	Licence              string          `json:"licence"`
	EvalOnly             bool            `json:"evalOnly"`
	OutputsCommercialUse *bool           `json:"outputsCommercialUse"`
	Locales              []string        `json:"locales"`
	Locale               string          `json:"locale"`
	Languages            json.RawMessage `json:"languages"`
}

func factsOf(v Version) payloadFacts {
	var f payloadFacts
	_ = json.Unmarshal(v.Payload, &f) // a payload without these fields checks as empty
	return f
}

// licences returns the version's licence terms: the collection's, else the payload's, split on " AND ".
func licences(v Version, f payloadFacts) []string {
	l := strings.TrimSpace(v.Licence)
	if l == "" {
		l = strings.TrimSpace(f.Licence)
	}
	var out []string
	for _, part := range strings.Split(l, " AND ") {
		out = append(out, strings.TrimSpace(part))
	}
	return out
}

func unusableLicence(l string) bool {
	return slices.Contains(unusable, strings.ToLower(strings.TrimSpace(l)))
}

// CheckLicence refuses (licence-forbids-adoption) a version whose licence names no licence, whose payload says its
// outputs may not be used commercially, or whose licence forbids commercial use or derivative works when what it
// holds is trained on or shipped. Kinds that hold no outside data or weights pass.
func CheckLicence(v Version) error {
	if !slices.Contains(licensed, v.Kind) {
		return nil
	}
	f := factsOf(v)
	if f.OutputsCommercialUse != nil && !*f.OutputsCommercialUse {
		return problems.LicenceForbidsAdoption.New("%s %s: its licence forbids commercial use of its outputs (outputsCommercialUse: false); nothing is adopted that does",
			v.Name, v.Version)
	}
	ships := slices.Contains(shipping, v.Kind) || (v.Kind == KindDataset && !f.EvalOnly && !slices.Contains(v.Tags, "eval-only"))
	for _, l := range licences(v, f) {
		switch {
		case unusableLicence(l):
			return problems.LicenceForbidsAdoption.New("%s %s has no usable licence (%q); a version is adopted only under a licence a person has checked",
				v.Name, v.Version, l)
		case ships && nonCommercial.MatchString(l):
			return problems.LicenceForbidsAdoption.New("%s %s is licensed %s, which forbids commercial use; a %s whose content is trained on or shipped must allow it (an eval-only dataset version or a golden set may be adopted)",
				v.Name, v.Version, l, Noun(v.Kind))
		case ships && noDerivatives.MatchString(l):
			return problems.LicenceForbidsAdoption.New("%s %s is licensed %s, which forbids derivative works; a fine-tuned model is one", v.Name, v.Version, l)
		}
	}
	return nil
}

// Locales returns the languages a version declares: its collection's locale:<x> tags and the payload's locales,
// locale and languages (strings, or objects with a language or locale field). Empty when it declares none.
func Locales(v Version) []string {
	var out []string
	add := func(l string) {
		if l = strings.TrimSpace(l); l != "" && !slices.Contains(out, l) {
			out = append(out, l)
		}
	}
	for _, t := range v.Tags {
		if l, ok := strings.CutPrefix(t, "locale:"); ok {
			add(l)
		}
	}
	f := factsOf(v)
	for _, l := range f.Locales {
		add(l)
	}
	add(f.Locale)
	var plain []string
	if json.Unmarshal(f.Languages, &plain) == nil {
		for _, l := range plain {
			add(l)
		}
	} else {
		var objs []struct{ Language, Locale string }
		if json.Unmarshal(f.Languages, &objs) == nil {
			for _, o := range objs {
				add(o.Language)
				add(o.Locale)
			}
		}
	}
	return out
}

// language is a locale's primary subtag in lower case (he-IL → he, sr-Latn-RS → sr).
func language(locale string) string {
	l, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(locale)), "-")
	l, _, _ = strings.Cut(l, "_")
	return l
}

// anyLanguage reports a locale that stands for every language (a normalizer for all locales).
func anyLanguage(l string) bool { return l == "*" || strings.EqualFold(l, "mul") }

// SharesLanguage reports whether any of have is in the language of any of want (he matches he-IL).
func SharesLanguage(have, want []string) bool {
	for _, h := range have {
		if anyLanguage(h) {
			return true
		}
		for _, w := range want {
			if language(h) == language(w) {
				return true
			}
		}
	}
	return false
}

// CheckLocale refuses (locale-mismatch) a target adoption of a dataset version, golden set, normalizer or auxiliary
// model that declares languages, none of them one of the project's. A version that declares none, a project without
// locales and a replay adoption pass.
func CheckLocale(v Version, projectLocales []string, purpose string) error {
	if purpose == PurposeReplay || len(projectLocales) == 0 || !slices.Contains(localized, v.Kind) {
		return nil
	}
	have := Locales(v)
	if len(have) == 0 || SharesLanguage(have, projectLocales) {
		return nil
	}
	return problems.LocaleMismatch.New("%s %s is in %s, none of the project's languages (%s); adopt it with purpose replay if it is kept to measure forgetting",
		v.Name, v.Version, strings.Join(have, ", "), strings.Join(projectLocales, ", "))
}

// TrainingForbidden says why a licence forbids training on what it covers, or "" when it allows it: the licence
// names no licence, or one of its terms (split on " AND ") forbids commercial use or derivative works — a fine-tuned
// model is a derivative work that a project ships (R26). Adoption checks a version's own licence (CheckLicence);
// training checks the licence of every source a dataset version is built from too (data.Trainable), so a version
// that was never adopted (a mix may name ver_… directly) or a source cleared before the rule cannot slip through.
func TrainingForbidden(licence string) string {
	for _, l := range strings.Split(licence, " AND ") {
		l = strings.TrimSpace(l)
		switch {
		case unusableLicence(l):
			return "names no licence"
		case nonCommercial.MatchString(l):
			return "forbids commercial use"
		case noDerivatives.MatchString(l):
			return "forbids derivative works"
		}
	}
	return ""
}
