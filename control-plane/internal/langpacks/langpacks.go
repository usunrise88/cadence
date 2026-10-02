// Package langpacks reads and checks language packs: one directory per locale in a project repository,
// lang/<locale>/, holding everything a locale needs — the text rules (normalizer.yaml: the scoring normalizer it
// names and the training text style), inverse normalisation (itn.yaml), script maps (translit.yaml), language-ID
// settings (lid.yaml), boost lists (boost/<domain>.txt), the golden-set recipe (golden-recipe.yaml) and README.md
// (docs/spec/03-pipelines-defaults.md "Language packs and hot words"; docs/spec/08-resolutions.md R21, R24).
//
// Cadence ships starter packs in control-plane/templates/lang/<locale>/; the bootstrap copies the pack of every
// project locale and projects.sync offers updates. Packs are project configuration, not registry data: entities pin
// a pack by the commit that last changed it.
package langpacks

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/textnorm"
)

// Layout of a pack.
const (
	Dir              = "lang" // in the repository and in the templates tree
	FileNormalizer   = "normalizer.yaml"
	FileITN          = "itn.yaml"
	FileTranslit     = "translit.yaml"
	FileLID          = "lid.yaml"
	FileGoldenRecipe = "golden-recipe.yaml"
	FileReadme       = "README.md"
	BoostDir         = "boost"
)

var (
	localeRe     = regexp.MustCompile(`^[A-Za-z]{2,3}(-[A-Za-z0-9]{2,8}){0,3}$`)
	domainRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	normalizerRe = regexp.MustCompile(`^(normalizer/[a-z0-9][a-z0-9._-]{0,62}|ver_[A-Za-z0-9-]{1,64})$`)
	scriptRe     = regexp.MustCompile(`^[A-Z][a-z]{3}$`)
)

// ValidLocale reports whether s is a locale a pack directory may be named after.
func ValidLocale(s string) bool { return localeRe.MatchString(s) }

// ValidDomain reports whether s is a boost list's domain.
func ValidDomain(s string) bool { return domainRe.MatchString(s) }

// PackPath is the pack's directory in a repository: lang/<locale>.
func PackPath(locale string) string { return Dir + "/" + locale }

// BoostPath is a boost list's path inside a pack.
func BoostPath(domain string) string { return BoostDir + "/" + domain + ".txt" }

// Language returns the primary language subtag of a locale, lower case (he for he-IL).
func Language(locale string) string {
	l, _, _ := strings.Cut(locale, "-")
	return strings.ToLower(l)
}

// Match picks the pack among available (pack directory names) that serves locale: the exact name (case
// insensitive), else the longest available name that is a prefix of the locale on a subtag boundary (sr for
// sr-Latn-RS), else a pack of the same language (he-IL for he). ok is false when none serves it.
func Match(available []string, locale string) (string, bool) {
	lc := strings.ToLower(locale)
	best := ""
	for _, a := range available {
		al := strings.ToLower(a)
		switch {
		case al == lc:
			return a, true
		case strings.HasPrefix(lc, al+"-") && len(a) > len(best):
			best = a
		}
	}
	if best != "" {
		return best, true
	}
	lang := Language(locale)
	sorted := slices.Clone(available)
	sort.Strings(sorted)
	for _, a := range sorted {
		if Language(a) == lang {
			return a, true
		}
	}
	return "", false
}

// ---------------------------------------------------------------- file shapes

// Normalizer is normalizer.yaml.
type Normalizer struct {
	Version  int       `yaml:"version"`
	Locale   string    `yaml:"locale"`
	Scoring  Scoring   `yaml:"scoring"`
	Training TextStyle `yaml:"training"`
}

// Scoring names the registry normalizer WER is computed after: a collection (normalizer/<name>, its newest frozen
// version) or a pinned version id.
type Scoring struct {
	Normalizer string `yaml:"normalizer"`
}

// TextStyle is the text style transcripts are trained in: the NormalizerPayload's steps plus an optional
// transliteration scheme of translit.yaml.
type TextStyle struct {
	Unicode       string             `yaml:"unicode"`
	RemoveMarks   bool               `yaml:"removeMarks"`
	Casefold      bool               `yaml:"casefold"`
	Punctuation   string             `yaml:"punctuation"`
	Numbers       string             `yaml:"numbers"`
	Transliterate string             `yaml:"transliterate,omitempty"`
	Mappings      []textnorm.Mapping `yaml:"mappings"`
}

// Spec returns the style as a normalizer spec for locale.
func (t TextStyle) Spec(locale string) textnorm.Spec {
	m := t.Mappings
	if m == nil {
		m = []textnorm.Mapping{}
	}
	return textnorm.Spec{Locale: locale, Unicode: t.Unicode, Casefold: t.Casefold, Punctuation: t.Punctuation,
		RemoveMarks: t.RemoveMarks, Mappings: m, Numbers: t.Numbers}
}

// ITN is itn.yaml: the written forms output uses, by class, with a pattern that finds each in a reference.
type ITN struct {
	Version int        `yaml:"version"`
	Locale  string     `yaml:"locale"`
	Classes []ITNClass `yaml:"classes"`
}

// ITNClass is one class of written forms (number, phone, date, …).
type ITNClass struct {
	Name        string       `yaml:"name"`
	Description string       `yaml:"description"`
	Pattern     string       `yaml:"pattern"`
	Examples    []ITNExample `yaml:"examples"`
}

// ITNExample is one spoken form and its written form.
type ITNExample struct {
	Spoken  string `yaml:"spoken"`
	Written string `yaml:"written"`
}

// Translit is translit.yaml.
type Translit struct {
	Version int      `yaml:"version"`
	Locale  string   `yaml:"locale"`
	Schemes []Scheme `yaml:"schemes"`
}

// Scheme is one script map; its name is the worker transliteration that applies it.
type Scheme struct {
	Name       string             `yaml:"name"`
	From       string             `yaml:"from"`
	To         string             `yaml:"to"`
	Reversible bool               `yaml:"reversible"`
	Map        []textnorm.Mapping `yaml:"map"`
}

// LID is lid.yaml.
type LID struct {
	Version       int      `yaml:"version"`
	Locale        string   `yaml:"locale"`
	Accept        []string `yaml:"accept"`
	CodeSwitch    string   `yaml:"codeSwitch"`
	MinConfidence float64  `yaml:"minConfidence"`
}

// GoldenRecipe is golden-recipe.yaml.
type GoldenRecipe struct {
	Version          int     `yaml:"version"`
	Locale           string  `yaml:"locale"`
	Normalizer       string  `yaml:"normalizer"`
	Transliterate    string  `yaml:"transliterate,omitempty"`
	TargetHours      float64 `yaml:"targetHours"`
	MinUtterances    int     `yaml:"minUtterances"`
	UtteranceSeconds struct {
		Min float64 `yaml:"min"`
		Max float64 `yaml:"max"`
	} `yaml:"utteranceSeconds"`
	Groups          string   `yaml:"groups"`
	SplitRule       string   `yaml:"splitRule"`
	MaxSpeakerShare float64  `yaml:"maxSpeakerShare"`
	Stratify        []string `yaml:"stratify"`
	Domains         []string `yaml:"domains"`
}

// strict decodes one YAML document into v, refusing unknown keys.
func strict(b []byte, v any) error {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("not the expected YAML shape: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- packs

// Pack is a pack's files by path inside the pack (normalizer.yaml, boost/names.txt).
type Pack struct {
	Locale string
	Files  map[string][]byte
}

// FromTree reads the pack at dir (lang/<name>) of a file system: the templates tree or a checkout.
func FromTree(tree fs.FS, dir, locale string) (Pack, error) {
	p := Pack{Locale: locale, Files: map[string][]byte{}}
	err := fs.WalkDir(tree, dir, func(fp string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(tree, fp)
		if err != nil {
			return err
		}
		p.Files[strings.TrimPrefix(fp, dir+"/")] = b
		return nil
	})
	if err != nil {
		return Pack{}, fmt.Errorf("read pack %s: %w", dir, err)
	}
	return p, nil
}

// Bundled lists the starter packs in the templates tree (the names under lang/), sorted.
func Bundled(tree fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(tree, Dir)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read templates/%s: %w", Dir, err)
	}
	out := []string{}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Paths returns the pack's file paths in order.
func (p Pack) Paths() []string {
	out := make([]string, 0, len(p.Files))
	for k := range p.Files {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Normalizer parses normalizer.yaml.
func (p Pack) Normalizer() (Normalizer, error) {
	b, ok := p.Files[FileNormalizer]
	if !ok {
		return Normalizer{}, fmt.Errorf("the pack has no %s", FileNormalizer)
	}
	var n Normalizer
	if err := strict(b, &n); err != nil {
		return Normalizer{}, err
	}
	return n, nil
}

// BoostLists parses every boost list of the pack, by domain; a list that does not parse is left out (Check
// reports it).
func (p Pack) BoostLists(maxTerms int) []Boost {
	var out []Boost
	for _, fp := range p.Paths() {
		domain, ok := boostDomain(fp)
		if !ok {
			continue
		}
		if b, err := ParseBoost(domain, p.Files[fp], maxTerms); err == nil {
			out = append(out, b)
		}
	}
	return out
}

func boostDomain(fp string) (string, bool) {
	name, ok := strings.CutPrefix(fp, BoostDir+"/")
	if !ok || strings.Contains(name, "/") {
		return "", false
	}
	d, ok := strings.CutSuffix(name, ".txt")
	return d, ok
}

// Limits bound what Check accepts (defaults.yaml langpacks).
type Limits struct {
	BoostMaxTerms  int
	BoostMinWeight float64
	BoostMaxWeight float64
}

// Check validates every file of the pack against its shape and returns the problems, each with the file's
// repository path (lang/<locale>/<file>) and what is wrong. An empty result means the pack checks out.
func (p Pack) Check(lim Limits) []problems.FieldError {
	var out []problems.FieldError
	at := func(file string) string { return PackPath(p.Locale) + "/" + file }
	bad := func(file, format string, a ...any) {
		out = append(out, problems.FieldError{Path: at(file), Message: fmt.Sprintf(format, a...)})
	}
	if _, ok := p.Files[FileNormalizer]; !ok {
		bad(FileNormalizer, "a language pack needs %s (the scoring normalizer it names and the training text style)", FileNormalizer)
	}
	var schemes []string
	if b, ok := p.Files[FileTranslit]; ok {
		var t Translit
		if err := strict(b, &t); err != nil {
			bad(FileTranslit, "%v", err)
		} else {
			for _, msg := range p.checkTranslit(t) {
				bad(FileTranslit, "%s", msg)
			}
			for _, s := range t.Schemes {
				schemes = append(schemes, s.Name)
			}
		}
	}
	for _, fp := range p.Paths() {
		b := p.Files[fp]
		if !utf8.Valid(b) {
			bad(fp, "the file must be UTF-8 text")
			continue
		}
		var msgs []string
		switch fp {
		case FileNormalizer:
			var n Normalizer
			if err := strict(b, &n); err != nil {
				msgs = []string{err.Error()}
				break
			}
			msgs = p.checkNormalizer(n, schemes)
		case FileITN:
			var n ITN
			if err := strict(b, &n); err != nil {
				msgs = []string{err.Error()}
				break
			}
			msgs = p.checkITN(n)
		case FileTranslit:
			continue // checked above
		case FileLID:
			var n LID
			if err := strict(b, &n); err != nil {
				msgs = []string{err.Error()}
				break
			}
			msgs = p.checkLID(n)
		case FileGoldenRecipe:
			var n GoldenRecipe
			if err := strict(b, &n); err != nil {
				msgs = []string{err.Error()}
				break
			}
			msgs = p.checkGolden(n, schemes)
		default:
			if domain, ok := boostDomain(fp); ok {
				if !ValidDomain(domain) {
					msgs = []string{fmt.Sprintf("a boost list is boost/<domain>.txt with a domain of lower-case letters, digits and dashes, not %q", domain)}
					break
				}
				bl, err := ParseBoost(domain, b, lim.BoostMaxTerms)
				if err != nil {
					msgs = []string{err.Error()}
					break
				}
				if bl.Weight < lim.BoostMinWeight || bl.Weight > lim.BoostMaxWeight {
					msgs = []string{fmt.Sprintf("weight %v is outside %v–%v (defaults.yaml langpacks.boost_weight)", bl.Weight, lim.BoostMinWeight, lim.BoostMaxWeight)}
				}
				break
			}
			if path.Ext(fp) == ".md" {
				break
			}
			msgs = []string{fmt.Sprintf("a language pack holds %s, %s, %s, %s, %s, Markdown notes and boost/<domain>.txt; not this file",
				FileNormalizer, FileITN, FileTranslit, FileLID, FileGoldenRecipe)}
		}
		for _, m := range msgs {
			bad(fp, "%s", m)
		}
	}
	return out
}

func (p Pack) checkHeader(version int, locale string) []string {
	var out []string
	if version != 1 {
		out = append(out, fmt.Sprintf("version must be 1, not %d", version))
	}
	if !ValidLocale(locale) {
		out = append(out, fmt.Sprintf("locale %q is not a locale (he-IL, sr)", locale))
	} else if Language(locale) != Language(p.Locale) {
		out = append(out, fmt.Sprintf("locale %s is not the pack's language (%s)", locale, p.Locale))
	}
	return out
}

func checkNormalizerRef(field, ref string) []string {
	if !normalizerRe.MatchString(ref) {
		return []string{fmt.Sprintf("%s must name a registry normalizer: normalizer/<name> (its newest frozen version) or a version id ver_…, not %q", field, ref)}
	}
	return nil
}

func checkScheme(field, name string, schemes []string) []string {
	if name != "" && !slices.Contains(schemes, name) {
		return []string{fmt.Sprintf("%s %q is not a scheme of %s (%s)", field, name, FileTranslit, strings.Join(schemes, ", "))}
	}
	return nil
}

func (p Pack) checkNormalizer(n Normalizer, schemes []string) []string {
	out := p.checkHeader(n.Version, n.Locale)
	out = append(out, checkNormalizerRef("scoring.normalizer", n.Scoring.Normalizer)...)
	if err := n.Training.Spec(n.Locale).Check(); err != nil {
		for _, e := range strings.Split(err.Error(), "\n") {
			out = append(out, "training: "+e)
		}
	}
	return append(out, checkScheme("training.transliterate", n.Training.Transliterate, schemes)...)
}

func (p Pack) checkITN(n ITN) []string {
	out := p.checkHeader(n.Version, n.Locale)
	seen := map[string]bool{}
	for i, c := range n.Classes {
		where := fmt.Sprintf("classes[%d]", i)
		if !domainRe.MatchString(c.Name) {
			out = append(out, fmt.Sprintf("%s.name must be lower-case letters, digits and dashes, not %q", where, c.Name))
		}
		if seen[c.Name] {
			out = append(out, fmt.Sprintf("%s.name %q appears twice", where, c.Name))
		}
		seen[c.Name] = true
		re, err := regexp.Compile(`^(?:` + c.Pattern + `)$`)
		if c.Pattern == "" || err != nil {
			out = append(out, fmt.Sprintf("%s.pattern is not a regular expression: %v", where, err))
			continue
		}
		for j, ex := range c.Examples {
			if ex.Spoken == "" || ex.Written == "" {
				out = append(out, fmt.Sprintf("%s.examples[%d] needs spoken and written", where, j))
			} else if !re.MatchString(ex.Written) {
				out = append(out, fmt.Sprintf("%s.examples[%d].written %q does not match the class's pattern", where, j, ex.Written))
			}
		}
	}
	return out
}

func (p Pack) checkTranslit(t Translit) []string {
	out := p.checkHeader(t.Version, t.Locale)
	seen := map[string]bool{}
	for i, s := range t.Schemes {
		where := fmt.Sprintf("schemes[%d]", i)
		if s.Name == "" || seen[s.Name] {
			out = append(out, fmt.Sprintf("%s.name must be set and unique", where))
		}
		seen[s.Name] = true
		if !scriptRe.MatchString(s.From) || !scriptRe.MatchString(s.To) {
			out = append(out, fmt.Sprintf("%s.from and .to are ISO 15924 script codes (Cyrl, Latn), not %q and %q", where, s.From, s.To))
		}
		if len(s.Map) == 0 {
			out = append(out, where+".map is empty")
		}
		for j, m := range s.Map {
			if m.From == "" {
				out = append(out, fmt.Sprintf("%s.map[%d].from is empty", where, j))
			}
		}
	}
	return out
}

func (p Pack) checkLID(n LID) []string {
	out := p.checkHeader(n.Version, n.Locale)
	if len(n.Accept) == 0 {
		out = append(out, "accept lists no locale")
	}
	for _, l := range n.Accept {
		if !ValidLocale(l) {
			out = append(out, fmt.Sprintf("accept: %q is not a locale", l))
		}
	}
	if !slices.Contains([]string{"keep", "drop", "flag"}, n.CodeSwitch) {
		out = append(out, fmt.Sprintf("codeSwitch must be keep, drop or flag, not %q", n.CodeSwitch))
	}
	if n.MinConfidence < 0 || n.MinConfidence > 1 {
		out = append(out, fmt.Sprintf("minConfidence %v is outside 0–1", n.MinConfidence))
	}
	return out
}

func (p Pack) checkGolden(n GoldenRecipe, schemes []string) []string {
	out := p.checkHeader(n.Version, n.Locale)
	out = append(out, checkNormalizerRef("normalizer", n.Normalizer)...)
	out = append(out, checkScheme("transliterate", n.Transliterate, schemes)...)
	if n.TargetHours <= 0 {
		out = append(out, "targetHours must be above zero")
	}
	if n.MinUtterances < 1 {
		out = append(out, "minUtterances must be at least 1")
	}
	if n.UtteranceSeconds.Min < 0 || n.UtteranceSeconds.Max <= n.UtteranceSeconds.Min {
		out = append(out, "utteranceSeconds needs 0 ≤ min < max")
	}
	if !slices.Contains([]string{"call", "speaker", "utterance"}, n.Groups) {
		out = append(out, fmt.Sprintf("groups must be call, speaker or utterance, not %q", n.Groups))
	}
	if !slices.Contains([]string{"speaker-disjoint", "source", "all-train", "all-validation", "all-test"}, n.SplitRule) {
		out = append(out, fmt.Sprintf("splitRule must be speaker-disjoint, source, all-train, all-validation or all-test, not %q", n.SplitRule))
	}
	if n.MaxSpeakerShare <= 0 || n.MaxSpeakerShare > 1 {
		out = append(out, "maxSpeakerShare must lie in (0, 1]")
	}
	return out
}
