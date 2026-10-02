// Package playbooks is the playbook format and playbook sessions (docs/spec/03-pipelines-defaults.md "Playbooks",
// docs/spec/05-agents.md "Playbooks and schedules as sessions", R16).
//
// A playbook is a template (templates/playbooks/<name>.yaml, a registry template version of kind playbook): inputs
// with defaultRefs into defaults.yaml or project facts, a chain of commands with phase markers for steps not built
// yet, stop conditions, the next-step suggestions and a prompt template. playbooks.run resolves the inputs, sums the
// chain's estimate, renders the prompt and starts an agent session of kind playbook whose plan is the chain. The plan
// ticks on the server only: when a command of the session succeeds (the command pipeline's session hook), when a read
// the chain waits on answers (jobs.wait), never from anything the agent reports.
package playbooks

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
	"text/template"

	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/cli"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
)

// CurrentPhase is the roadmap phase this build ships: a playbook available from a later phase is listed and not run,
// and a step of a later phase is in the plan as skipped.
const CurrentPhase = 2

// Dir is the playbooks directory of the templates tree.
const Dir = "playbooks"

// Input types.
const (
	TypeDataset   = "dataset_version"
	TypeBaseModel = "base_model"
	TypeInteger   = "integer"
	TypeNumber    = "number"
	TypeString    = "string"
	TypeBoolean   = "boolean"
)

// Where an input without a value comes from.
const (
	FromProject  = "project"  // a project fact: the default base model
	FromAdoption = "adoption" // the project's adopted version of a collection (dataset/replay-base)
)

// UntilTerminal marks a step that ticks when the job it waits for has ended.
const UntilTerminal = "terminal"

// Spending operations need a successful dry run of the same operation earlier in a playbook session (05: "dryRun
// before each spending step"). The list is the server's, never the template's, so a template cannot opt out.
var Spending = map[string]bool{
	"runs.new": true, "runs.calibrate": true, "runs.resume": true, "runs.stage": true, "checkpoints.average": true,
	"evals.new": true, "sweeps.run": true,
}

// Pending are operations a chain may name before the contract carries them (a parallel stream builds them);
// Validate accepts them as known. Empty since stream R's runs, checkpoints and metrics merged.
var Pending = []string{}

// Known answers whether op is an implemented operation of the contract (the generated operation table, exempt tags
// aside) or one of Pending.
func Known() func(op string) bool {
	ops := make(map[string]bool, len(cli.Operations)+len(Pending))
	for _, o := range cli.Operations {
		ops[o.ID] = true
	}
	for _, op := range Pending {
		ops[op] = true
	}
	return func(op string) bool { return ops[op] }
}

// Input is one input of a playbook, in file order.
type Input struct {
	Name        string `yaml:"-"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
	Required    bool   `yaml:"required"`
	Multiple    bool   `yaml:"multiple"`
	DefaultRef  string `yaml:"defaultRef"`
	From        string `yaml:"from"`
	Collection  string `yaml:"collection"`
}

// Hint is a step's own estimate, used when its operation has no dry-run estimator here.
type Hint struct {
	GPUHours  float64 `yaml:"gpuHours"`
	Minutes   float64 `yaml:"minutes"`
	PlusMinus float64 `yaml:"plusMinus"`
}

// Step is one step of the chain.
type Step struct {
	ID       string         `yaml:"id"`
	Title    string         `yaml:"title"`
	Command  string         `yaml:"command"`
	Accepts  []string       `yaml:"accepts"`
	Until    string         `yaml:"until"`
	Phase    int            `yaml:"phase"`
	With     map[string]any `yaml:"with"`
	Estimate *Hint          `yaml:"estimate"`
}

// Available reports whether the step's phase has shipped.
func (s Step) Available() bool { return s.Phase <= CurrentPhase }

// Operations are the operations that tick the step: its command and what it accepts.
func (s Step) Operations() []string { return append([]string{s.Command}, s.Accepts...) }

// Stop is one stop condition: on (gate, budget, step, approval) when (failed, exceeded, denied).
type Stop struct {
	On   string `json:"on"`
	When string `json:"when"`
}

// Next are the next-step suggestions written when the chain is done or stopped.
type Next struct {
	Done    string `yaml:"done"`
	Stopped string `yaml:"stopped"`
}

// Playbook is one parsed playbook template.
type Playbook struct {
	Name          string
	Title         string
	Description   string
	TypicalCost   string
	AvailableFrom int
	Inputs        []Input
	Chain         []Step
	Stop          []Stop
	Next          Next
	Prompt        string
	// File is the template's path in the templates tree (playbooks/<name>.yaml).
	File string
}

// Runnable reports whether the playbook can run in this build.
func (p Playbook) Runnable() bool { return p.AvailableFrom <= CurrentPhase }

// Input returns the input named name.
func (p Playbook) Input(name string) (Input, bool) {
	for _, in := range p.Inputs {
		if in.Name == name {
			return in, true
		}
	}
	return Input{}, false
}

// Stops reports whether the playbook stops on on.
func (p Playbook) Stops(on string) bool {
	return slices.ContainsFunc(p.Stop, func(s Stop) bool { return s.On == on })
}

type file struct {
	Name          string              `yaml:"name"`
	Title         string              `yaml:"title"`
	Description   string              `yaml:"description"`
	TypicalCost   string              `yaml:"typicalCost"`
	AvailableFrom int                 `yaml:"availableFrom"`
	Inputs        yaml.Node           `yaml:"inputs"`
	Chain         []Step              `yaml:"chain"`
	Stop          []map[string]string `yaml:"stop"`
	Next          Next                `yaml:"next"`
	Prompt        string              `yaml:"prompt"`
}

// Parse reads one playbook file; unknown keys are refused.
func Parse(name string, raw []byte) (Playbook, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return Playbook{}, fmt.Errorf("playbook %s: %w", name, err)
	}
	p := Playbook{Name: f.Name, Title: f.Title, Description: f.Description, TypicalCost: f.TypicalCost,
		AvailableFrom: f.AvailableFrom, Chain: f.Chain, Next: f.Next, Prompt: f.Prompt, File: path.Join(Dir, name+".yaml")}
	if f.Inputs.Kind != 0 {
		if f.Inputs.Kind != yaml.MappingNode {
			return Playbook{}, fmt.Errorf("playbook %s: inputs must be a mapping of name to input", name)
		}
		for i := 0; i+1 < len(f.Inputs.Content); i += 2 {
			var in Input
			node := f.Inputs.Content[i+1]
			var b bytes.Buffer
			enc := yaml.NewEncoder(&b)
			if err := enc.Encode(node); err != nil {
				return Playbook{}, fmt.Errorf("playbook %s: input %s: %w", name, f.Inputs.Content[i].Value, err)
			}
			d := yaml.NewDecoder(&b)
			d.KnownFields(true)
			if err := d.Decode(&in); err != nil {
				return Playbook{}, fmt.Errorf("playbook %s: input %s: %w", name, f.Inputs.Content[i].Value, err)
			}
			in.Name = f.Inputs.Content[i].Value
			p.Inputs = append(p.Inputs, in)
		}
	}
	for i, m := range f.Stop {
		if len(m) != 1 {
			return Playbook{}, fmt.Errorf("playbook %s: stop[%d] must be one `on: when` pair", name, i)
		}
		for on, when := range m {
			p.Stop = append(p.Stop, Stop{On: on, When: when})
		}
	}
	return p, nil
}

var (
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	idRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	opRe   = regexp.MustCompile(`^[a-z][A-Za-z]*\.[a-z]+$`)
	refRe  = regexp.MustCompile(`^\$inputs\.([A-Za-z][A-Za-z0-9]*)$`)
)

var stopWhen = map[string]string{"gate": "failed", "budget": "exceeded", "step": "failed", "approval": "denied"}

// Validate checks a parsed playbook: names, input types and sources (defaultRefs resolve in d), the chain (unique
// ids; every operation of a step that can run now is one known reports; later-phase steps only need the
// <entity>.<verb> form), stop conditions, the `with` references and the prompt template (it must render with every
// input). It returns every problem found.
func Validate(p Playbook, d *defaults.Defaults, known func(op string) bool) error {
	var errs []error
	fail := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }
	if !nameRe.MatchString(p.Name) {
		fail("name %q is not lowercase letters, digits and dashes", p.Name)
	}
	if want := strings.TrimSuffix(path.Base(p.File), ".yaml"); p.File != "" && want != p.Name {
		fail("name %q differs from the file name %s", p.Name, path.Base(p.File))
	}
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Description) == "" || strings.TrimSpace(p.Prompt) == "" {
		fail("title, description and prompt are required")
	}
	if p.AvailableFrom < 1 {
		fail("availableFrom must name a roadmap phase (1 or later)")
	}
	seen := map[string]bool{}
	for _, in := range p.Inputs {
		at := "inputs." + in.Name
		if seen[in.Name] {
			fail("%s: declared twice", at)
		}
		seen[in.Name] = true
		switch in.Type {
		case TypeDataset, TypeBaseModel, TypeInteger, TypeNumber, TypeString, TypeBoolean:
		default:
			fail("%s: unknown type %q", at, in.Type)
		}
		sources := 0
		if in.Required {
			sources++
		}
		if in.DefaultRef != "" {
			sources++
			if _, ok := d.Lookup(in.DefaultRef); !ok {
				fail("%s: defaultRef %q does not resolve in defaults.yaml", at, in.DefaultRef)
			}
		}
		if in.From != "" {
			sources++
		}
		if sources != 1 {
			fail("%s: exactly one of required, defaultRef or from", at)
		}
		switch in.From {
		case "":
		case FromProject:
			if in.Type != TypeBaseModel {
				fail("%s: from project is the project's base model; the type must be base_model", at)
			}
		case FromAdoption:
			if in.Collection == "" || (in.Type != TypeDataset && in.Type != TypeBaseModel) {
				fail("%s: from adoption needs a collection and a registry type", at)
			}
		default:
			fail("%s: from must be project or adoption", at)
		}
		if in.Multiple && in.Type != TypeDataset {
			fail("%s: only dataset_version inputs take several values", at)
		}
	}
	ids := map[string]bool{}
	for i, s := range p.Chain {
		at := fmt.Sprintf("chain[%d]", i)
		if !idRe.MatchString(s.ID) || ids[s.ID] {
			fail("%s: id %q is missing, malformed or a duplicate", at, s.ID)
		}
		ids[s.ID] = true
		if strings.TrimSpace(s.Title) == "" {
			fail("%s: title is required", at)
		}
		if s.Phase < 0 {
			fail("%s: phase must be a roadmap phase", at)
		}
		for _, op := range s.Operations() {
			switch {
			case !opRe.MatchString(op):
				fail("%s: %q is not an operation <entity>.<verb>", at, op)
			case s.Available() && p.Runnable() && !known(op):
				fail("%s: unknown operation %q (not in the contract; a step of a later phase needs phase:)", at, op)
			}
		}
		if s.Until != "" && s.Until != UntilTerminal {
			fail("%s: until must be terminal", at)
		}
		if s.Estimate != nil && (s.Estimate.GPUHours < 0 || s.Estimate.Minutes < 0 || s.Estimate.PlusMinus < 0 || s.Estimate.PlusMinus > 1) {
			fail("%s: the estimate hint must be non-negative (plusMinus at most 1)", at)
		}
		for key, v := range s.With {
			for _, ref := range withRefs(v) {
				m := refRe.FindStringSubmatch(ref)
				if m == nil {
					fail("%s: with.%s: %q is not $inputs.<name>", at, key, ref)
				} else if _, ok := p.Input(m[1]); !ok {
					fail("%s: with.%s: no input %q", at, key, m[1])
				}
			}
		}
	}
	if len(p.Chain) == 0 {
		fail("chain: a playbook needs at least one step")
	}
	for i, s := range p.Stop {
		if want, ok := stopWhen[s.On]; !ok || s.When != want {
			fail("stop[%d]: %s: %s is not one of gate: failed, budget: exceeded, step: failed, approval: denied", i, s.On, s.When)
		}
	}
	if _, err := Render(p, PromptData{Inputs: sampleInputs(p)}); err != nil {
		fail("prompt: %v", err)
	}
	return errors.Join(errs...)
}

// withRefs lists the $inputs references of a `with` value (a string or a list of strings); literals are skipped.
func withRefs(v any) []string {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "$") {
			return []string{x}
		}
	case []any:
		var out []string
		for _, e := range x {
			out = append(out, withRefs(e)...)
		}
		return out
	}
	return nil
}

func sampleInputs(p Playbook) map[string]string {
	out := make(map[string]string, len(p.Inputs))
	for _, in := range p.Inputs {
		out[in.Name] = "<" + in.Name + ">"
	}
	return out
}

// PromptData is what a prompt template sees: .Inputs (name → the value as text), .Project (Name, Slug, Locales).
type PromptData struct {
	Inputs  map[string]string
	Project PromptProject
}

// PromptProject are the project facts a prompt names.
type PromptProject struct {
	Name, Slug, Locales string
}

// Render renders the prompt template of p; an input the template names but the playbook lacks is an error.
func Render(p Playbook, data PromptData) (string, error) {
	t, err := template.New(p.Name).Option("missingkey=error").Parse(p.Prompt)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}

// Library is the bundled playbooks by name.
type Library struct {
	byName map[string]Playbook
	names  []string
}

// Load parses and validates every playbooks/*.yaml of the templates tree.
func Load(tree fs.FS, d *defaults.Defaults, known func(op string) bool) (*Library, error) {
	entries, err := fs.ReadDir(tree, Dir)
	if err != nil {
		return nil, fmt.Errorf("read templates/%s: %w", Dir, err)
	}
	lib := &Library{byName: map[string]Playbook{}}
	var errs []error
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".yaml")
		raw, err := fs.ReadFile(tree, path.Join(Dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		p, err := Parse(name, raw)
		if err == nil {
			err = Validate(p, d, known)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("playbook %s: %w", name, err))
			continue
		}
		lib.byName[name] = p
		lib.names = append(lib.names, name)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	sort.Slice(lib.names, func(i, j int) bool {
		a, b := lib.byName[lib.names[i]], lib.byName[lib.names[j]]
		if a.Runnable() != b.Runnable() {
			return a.Runnable()
		}
		return a.Name < b.Name
	})
	return lib, nil
}

// List returns the playbooks, runnable ones first, then by name.
func (l *Library) List() []Playbook {
	out := make([]Playbook, 0, len(l.names))
	for _, n := range l.names {
		out = append(out, l.byName[n])
	}
	return out
}

// Get returns the playbook named name.
func (l *Library) Get(name string) (Playbook, bool) {
	p, ok := l.byName[name]
	return p, ok
}

// Operations lists every operation any chain names (the server observes the read ones).
func (l *Library) Operations() map[string]bool {
	out := map[string]bool{}
	for _, p := range l.byName {
		for _, s := range p.Chain {
			for _, op := range s.Operations() {
				out[op] = true
			}
		}
	}
	return out
}
