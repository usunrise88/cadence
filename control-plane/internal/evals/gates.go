package evals

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/repos"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// GatesFile is the project's gate in its repository (docs/review/2026-10-02-phase-3-plan.md "Decisions" 3).
const GatesFile = "gates.yaml"

// DefaultsETag is the ETag of gates.get for a project without gates.yaml: gates.edit takes it to create the file.
const DefaultsETag = "defaults"

// TargetRuleBeatBaseline is the one target rule: a WER delta whose whole interval lies below zero.
const TargetRuleBeatBaseline = "beat-baseline"

// GateConfig is gates.yaml; a value left out takes defaults.yaml gate.* and eval.*.
type GateConfig struct {
	PrimaryProfile      string           `yaml:"primaryProfile,omitempty" json:"primaryProfile,omitempty"`
	Target              *TargetGate      `yaml:"target,omitempty" json:"target,omitempty"`
	Replay              *ReplayGate      `yaml:"replay,omitempty" json:"replay,omitempty"`
	DeletionsInsertions *bool            `yaml:"deletionsInsertions,omitempty" json:"deletionsInsertions,omitempty"`
	Significance        *SignificanceCfg `yaml:"significance,omitempty" json:"significance,omitempty"`
}

// TargetGate is what the target golden sets must show.
type TargetGate struct {
	GoldenSets []string `yaml:"goldenSets,omitempty" json:"goldenSets,omitempty"`
	Rule       string   `yaml:"rule,omitempty" json:"rule,omitempty"`
}

// ReplayGate bounds the regression of the replay golden sets.
type ReplayGate struct {
	GoldenSets    []string `yaml:"goldenSets,omitempty" json:"goldenSets,omitempty"`
	MaxRegression *float64 `yaml:"maxRegression,omitempty" json:"maxRegression,omitempty"`
}

// SignificanceCfg is the bootstrap of gates.yaml.
type SignificanceCfg struct {
	Samples *int     `yaml:"samples,omitempty" json:"samples,omitempty"`
	Level   *float64 `yaml:"level,omitempty" json:"level,omitempty"`
	Seed    *int     `yaml:"seed,omitempty" json:"seed,omitempty"`
}

// Gate is a gate with every value resolved.
type Gate struct {
	PrimaryProfile      string
	TargetGoldenSets    []string // empty: the eval's golden sets in the project's locales
	TargetRule          string
	ReplayGoldenSets    []string // empty: the eval's other golden sets
	MaxRegression       float64
	DeletionsInsertions bool
	Significance        Significance
}

// Config renders g as a complete GateConfig.
func (g Gate) Config() GateConfig {
	di, mr := g.DeletionsInsertions, g.MaxRegression
	s, l, seed := g.Significance.Samples, g.Significance.Level, g.Significance.Seed
	return GateConfig{
		PrimaryProfile:      g.PrimaryProfile,
		Target:              &TargetGate{GoldenSets: nonNil(g.TargetGoldenSets), Rule: g.TargetRule},
		Replay:              &ReplayGate{GoldenSets: nonNil(g.ReplayGoldenSets), MaxRegression: &mr},
		DeletionsInsertions: &di,
		Significance:        &SignificanceCfg{Samples: &s, Level: &l, Seed: &seed},
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// DefaultGate is the gate of a project without gates.yaml.
func DefaultGate(d *defaults.Defaults) Gate {
	return Gate{
		PrimaryProfile: d.Eval.PrimaryProfile.Value, TargetRule: d.Gate.TargetRule.Value,
		MaxRegression: d.Gate.ReplayMaxRegression.Value, DeletionsInsertions: d.Gate.DeletionsInsertions.Value,
		Significance: Significance{Samples: d.Eval.BootstrapSamples.Value, Level: d.Eval.Confidence.Value, Seed: d.Eval.BootstrapSeed.Value},
	}
}

var (
	profileRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	goldenRefRe = regexp.MustCompile(`^(golden-set/[a-z0-9*][a-z0-9*._-]{0,99}|ver_[0-9a-f-]{36})$`)
)

// ParseGate reads gates.yaml (strict: unknown keys fail) and resolves it against the defaults. Every problem comes
// back together as one gate-config-invalid.
func ParseGate(content []byte, d *defaults.Defaults) (GateConfig, Gate, error) {
	var c GateConfig
	if len(bytes.TrimSpace(content)) > 0 {
		dec := yaml.NewDecoder(bytes.NewReader(content))
		dec.KnownFields(true)
		if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) { // only comments: the defaults
			return GateConfig{}, Gate{}, gateInvalid([]problems.FieldError{{Path: "/", Message: strings.TrimPrefix(err.Error(), "yaml: ")}})
		}
	}
	g, err := Resolve(c, d)
	return c, g, err
}

// CheckAdopted refuses (gate-config-invalid) a gate naming a golden-set collection the project has not adopted:
// gates.yaml resolves golden sets to the project's adopted versions (a * pattern may match none).
func CheckAdopted(ctx context.Context, q storage.Querier, projectID string, g Gate) error {
	adopted, err := registry.ListAdoptions(ctx, q, projectID, registry.KindGoldenSet)
	if err != nil {
		return err
	}
	var errs []problems.FieldError
	for _, l := range []struct {
		path string
		refs []string
	}{{"/target/goldenSets", g.TargetGoldenSets}, {"/replay/goldenSets", g.ReplayGoldenSets}} {
		for i, r := range l.refs {
			if strings.Contains(r, "*") {
				continue
			}
			found := false
			for _, a := range adopted {
				found = found || a.Version.ID == r || a.Version.Name == r
			}
			if !found {
				errs = append(errs, problems.FieldError{Path: fmt.Sprintf("%s/%d", l.path, i),
					Message: fmt.Sprintf("the project has not adopted %s; adopt a frozen version of it first (projects.adopt)", r)})
			}
		}
	}
	if len(errs) > 0 {
		return gateInvalid(errs)
	}
	return nil
}

// Resolve checks a GateConfig against the safe ranges of defaults.yaml and fills what it leaves out.
func Resolve(c GateConfig, d *defaults.Defaults) (Gate, error) {
	g := DefaultGate(d)
	var errs []problems.FieldError
	bad := func(p, format string, a ...any) {
		errs = append(errs, problems.FieldError{Path: p, Message: fmt.Sprintf(format, a...)})
	}
	if c.PrimaryProfile != "" {
		if !profileRe.MatchString(c.PrimaryProfile) {
			bad("/primaryProfile", "%q is not a latency profile name (like 160ms)", c.PrimaryProfile)
		}
		g.PrimaryProfile = c.PrimaryProfile
	}
	refs := func(p string, list []string) []string {
		for i, r := range list {
			if !goldenRefRe.MatchString(r) {
				bad(fmt.Sprintf("%s/%d", p, i), "%q is not a golden set (golden-set/<name>, * matches several, or ver_…)", r)
			}
		}
		return list
	}
	if t := c.Target; t != nil {
		g.TargetGoldenSets = refs("/target/goldenSets", t.GoldenSets)
		if t.Rule != "" {
			if !d.Gate.TargetRule.Range.Allows(t.Rule) {
				bad("/target/rule", "%q is not one of %v", t.Rule, d.Gate.TargetRule.Range.Values)
			}
			g.TargetRule = t.Rule
		}
	}
	if r := c.Replay; r != nil {
		g.ReplayGoldenSets = refs("/replay/goldenSets", r.GoldenSets)
		if r.MaxRegression != nil {
			if err := d.Gate.ReplayMaxRegression.Range.Check(*r.MaxRegression); err != nil {
				bad("/replay/maxRegression", "%v", err)
			}
			g.MaxRegression = *r.MaxRegression
		}
	}
	if c.DeletionsInsertions != nil {
		g.DeletionsInsertions = *c.DeletionsInsertions
	}
	if s := c.Significance; s != nil {
		if s.Samples != nil {
			if err := d.Eval.BootstrapSamples.Range.Check(float64(*s.Samples)); err != nil {
				bad("/significance/samples", "%v", err)
			}
			g.Significance.Samples = *s.Samples
		}
		if s.Level != nil {
			if err := d.Eval.Confidence.Range.Check(*s.Level); err != nil {
				bad("/significance/level", "%v", err)
			}
			g.Significance.Level = *s.Level
		}
		if s.Seed != nil {
			if err := d.Eval.BootstrapSeed.Range.Check(float64(*s.Seed)); err != nil {
				bad("/significance/seed", "%v", err)
			}
			g.Significance.Seed = *s.Seed
		}
	}
	for _, t := range g.TargetGoldenSets {
		for _, r := range g.ReplayGoldenSets {
			if t == r {
				bad("/replay/goldenSets", "%s is a target golden set too; a set is either target or replay", t)
			}
		}
	}
	if len(errs) > 0 {
		return Gate{}, gateInvalid(errs)
	}
	return g, nil
}

func gateInvalid(errs []problems.FieldError) error {
	return &problems.Error{Type: problems.GateConfigInvalid, Detail: "gates.yaml: " + errs[0].Message, Errors: errs}
}

// Departure is one gate value that differs from defaults.yaml.
type Departure struct {
	Param   string `json:"param"`
	Value   any    `json:"value"`
	Default any    `json:"default"`
}

// Departures lists where g departs from the defaults.
func Departures(g Gate, d *defaults.Defaults) []Departure {
	def := DefaultGate(d)
	out := []Departure{}
	add := func(param string, v, dv any) {
		if !reflect.DeepEqual(v, dv) {
			out = append(out, Departure{Param: param, Value: v, Default: dv})
		}
	}
	add("primaryProfile", g.PrimaryProfile, def.PrimaryProfile)
	add("target.goldenSets", nonNil(g.TargetGoldenSets), []string{})
	add("target.rule", g.TargetRule, def.TargetRule)
	add("replay.goldenSets", nonNil(g.ReplayGoldenSets), []string{})
	add("replay.maxRegression", g.MaxRegression, def.MaxRegression)
	add("deletionsInsertions", g.DeletionsInsertions, def.DeletionsInsertions)
	add("significance.samples", g.Significance.Samples, def.Significance.Samples)
	add("significance.level", g.Significance.Level, def.Significance.Level)
	add("significance.seed", g.Significance.Seed, def.Significance.Seed)
	return out
}

// RenderGate writes c as gates.yaml.
func RenderGate(c GateConfig) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("# written by Cadence — the project's gate (docs/review/2026-10-02-phase-3-plan.md \"Gate\"). Keys are stable; a\n")
	b.WriteString("# value left out takes defaults.yaml gate.* and eval.* (gates.get shows the effective gate).\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(c); err != nil {
		return nil, fmt.Errorf("render gates.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render gates.yaml: %w", err)
	}
	return b.Bytes(), nil
}

// GateFile is gates.yaml as read at the head of main.
type GateFile struct {
	Exists  bool
	Head    string // main's head
	Commit  string // the commit that last changed the file
	Content []byte
	Config  GateConfig // as written
	Gate    Gate       // resolved
}

// ETag is the file's commit, or DefaultsETag without a file.
func (f GateFile) ETag() string {
	if !f.Exists {
		return DefaultsETag
	}
	return f.Commit
}

// ReadGate reads gates.yaml of project slug at the head of main; a project without the file (or without a
// repository) gets the defaults.
func ReadGate(ctx context.Context, repo pipelines.Repo, slug string, d *defaults.Defaults) (GateFile, error) {
	f := GateFile{Gate: DefaultGate(d)}
	if repo == nil || !repo.Exists(slug) {
		return f, nil
	}
	b, head, err := repo.ReadFile(ctx, slug, repos.Main, GatesFile)
	f.Head = head
	if errors.Is(err, repos.ErrNotFound) {
		return f, nil
	}
	if err != nil {
		return GateFile{}, fmt.Errorf("read %s: %w", GatesFile, err)
	}
	hist, err := repo.History(ctx, slug, repos.Main, GatesFile, 1)
	if err != nil {
		return GateFile{}, fmt.Errorf("history of %s: %w", GatesFile, err)
	}
	f.Exists, f.Content = true, b
	if len(hist) > 0 {
		f.Commit = hist[0].SHA
	}
	if f.Config, f.Gate, err = ParseGate(b, d); err != nil {
		return GateFile{}, err
	}
	return f, nil
}

// Matches reports whether golden set (versionID, name) is one a gate's list names: its version id, its collection
// name, or a pattern with * over collection names.
func Matches(list []string, versionID, name string) bool {
	for _, r := range list {
		if r == versionID || r == name {
			return true
		}
		if strings.Contains(r, "*") {
			if ok, _ := path.Match(r, name); ok {
				return true
			}
		}
	}
	return false
}
