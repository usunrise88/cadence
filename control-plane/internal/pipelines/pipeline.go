package pipelines

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Dir is where a project repository keeps its pipelines; a pipeline named n lives at Dir/n.yaml.
const Dir = "pipelines"

// InputsRef starts a wiring that reads a pipeline input: $inputs.<name>.
const InputsRef = "$inputs."

var (
	nameRe   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	stepRe   = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	kindRe   = regexp.MustCompile(`^([a-z][a-z0-9_]{0,62})@([0-9A-Za-z][0-9A-Za-z._-]{0,19})$`)
	portRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	artTypRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,62}$`)
	// A step input name is a consumed input, or <consumed>.<n> when one input receives several artifacts
	// (checkpoints.0, checkpoints.1 for an average step; docs/spec/03 "Step contract").
	stepInRe = regexp.MustCompile(`^([a-z][a-z0-9_]{0,62})(\.(0|[1-9][0-9]{0,2}))?$`)
)

// ConsumedName is the consumed input a step input name feeds: checkpoints for checkpoints.1, name itself otherwise.
func ConsumedName(in string) string {
	if m := stepInRe.FindStringSubmatch(in); m != nil {
		return m[1]
	}
	return in
}

// acceptsInstead lists the artifact types a consumer accepts in place of the one it declares (R44: a checkpoint is
// a model of its family, so a step that initialises from a base_model also starts from a checkpoint; the step
// reads its input's type to tell them apart).
var acceptsInstead = map[string][]string{"base_model": {"checkpoint"}}

// Accepts reports whether an artifact of type got may feed an input that declares type want.
func Accepts(want, got string) bool {
	if want == got {
		return true
	}
	for _, t := range acceptsInstead[want] {
		if t == got {
			return true
		}
	}
	return false
}

// ValidName reports whether n is a pipeline name.
func ValidName(n string) bool { return nameRe.MatchString(n) }

// Path is the repository path of the pipeline named name.
func Path(name string) string { return Dir + "/" + name + ".yaml" }

// Pipeline is a pipelines/<name>.yaml file (docs/review/2026-09-30-phase-2-plan.md "Pipelines"): its inputs by
// artifact type and its steps, each pinned as kind@version, wired from pipeline inputs or earlier outputs, with
// only the parameters that depart from defaults.
type Pipeline struct {
	Name        string            `yaml:"name" json:"name"`
	Description string            `yaml:"description,omitempty" json:"description,omitempty"`
	Inputs      map[string]string `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Steps       []Step            `yaml:"steps" json:"steps"`
}

// Step is one step of a pipeline.
type Step struct {
	ID     string            `yaml:"id" json:"id"`
	Kind   string            `yaml:"kind" json:"kind"` // name@version
	In     map[string]string `yaml:"in,omitempty" json:"in,omitempty"`
	Params map[string]any    `yaml:"params,omitempty" json:"params,omitempty"`
	// Optional marks a step whose failure does not fail the run: the steps that read its outputs (optional too) are
	// skipped and the run ends done without them (an eval's reported-only metrics, R54).
	Optional bool `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// KindRef splits the pinned kind into name and version.
func (s Step) KindRef() (name, version string, ok bool) {
	m := kindRe.FindStringSubmatch(s.Kind)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// Wire is a parsed wiring: a pipeline input, or an output of another step.
type Wire struct {
	Input  string // pipeline input name, when the wiring reads $inputs.<name>
	Step   string // producing step id otherwise
	Output string
}

// ParseWire parses "$inputs.<name>" or "<step>.<output>".
func ParseWire(s string) (Wire, bool) {
	if name, ok := strings.CutPrefix(s, InputsRef); ok {
		return Wire{Input: name}, portRe.MatchString(name)
	}
	step, out, ok := strings.Cut(s, ".")
	if !ok || !stepRe.MatchString(step) || !portRe.MatchString(out) {
		return Wire{}, false
	}
	return Wire{Step: step, Output: out}, true
}

// Errors collects field problems of a pipeline; it renders as pipeline-invalid.
type Errors []problems.FieldError

// Add records one problem at path.
func (e *Errors) Add(path, format string, args ...any) {
	*e = append(*e, problems.FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
}

// Err returns the pipeline-invalid problem, or nil when there is none.
func (e Errors) Err(name string) error {
	if len(e) == 0 {
		return nil
	}
	pe := problems.PipelineInvalid.New("pipeline %q has %d problem(s): %s", name, len(e), e[0].Path+": "+e[0].Message)
	pe.Errors = e
	return pe
}

// Parse decodes a pipeline file strictly (unknown keys fail) and checks its structure: names, pinned kinds,
// wiring syntax and references, and that the steps form no cycle. file, when not empty, is the name the file
// is stored under; the pipeline's name must equal it.
func Parse(b []byte, file string) (Pipeline, error) {
	var p Pipeline
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		name := file
		if name == "" {
			name = "(unnamed)"
		}
		pe := problems.PipelineInvalid.New("pipeline %q does not parse: %v", name, err)
		pe.Errors = []problems.FieldError{{Path: "", Message: err.Error()}}
		return Pipeline{}, pe
	}
	for i := range p.Steps {
		norm, err := normalize(p.Steps[i].Params)
		if err != nil {
			pe := problems.PipelineInvalid.New("pipeline %q: step %q: params: %v", p.Name, p.Steps[i].ID, err)
			pe.Errors = []problems.FieldError{{Path: fmt.Sprintf("steps[%d].params", i), Message: err.Error()}}
			return Pipeline{}, pe
		}
		p.Steps[i].Params = norm
	}
	if err := p.Check(file); err != nil {
		return Pipeline{}, err
	}
	return p, nil
}

// normalize turns YAML-decoded parameters into their JSON form (numbers as float64, maps keyed by string).
func normalize(params map[string]any) (map[string]any, error) {
	if len(params) == 0 {
		return nil, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Check validates the structure of p (see Parse).
func (p Pipeline) Check(file string) error {
	var errs Errors
	switch {
	case !nameRe.MatchString(p.Name):
		errs.Add("name", "%q is not a pipeline name (lowercase letters, digits and -, starting with a letter)", p.Name)
	case file != "" && p.Name != file:
		errs.Add("name", "the pipeline is named %q but stored as %s", p.Name, Path(file))
	}
	for _, in := range sortedKeys(p.Inputs) {
		if !portRe.MatchString(in) {
			errs.Add("inputs."+in, "%q is not an input name (lowercase letters, digits and _)", in)
		}
		if !artTypRe.MatchString(p.Inputs[in]) {
			errs.Add("inputs."+in, "%q is not an artifact type", p.Inputs[in])
		}
	}
	if len(p.Steps) == 0 {
		errs.Add("steps", "a pipeline needs at least one step")
	}
	ids := map[string]int{}
	for i, s := range p.Steps {
		path := fmt.Sprintf("steps[%d]", i)
		if !stepRe.MatchString(s.ID) {
			errs.Add(path+".id", "%q is not a step id (lowercase letters, digits, _ and -, starting with a letter)", s.ID)
		} else if j, dup := ids[s.ID]; dup {
			errs.Add(path+".id", "step id %q is already used by steps[%d]", s.ID, j)
		} else {
			ids[s.ID] = i
		}
		if _, _, ok := s.KindRef(); !ok {
			errs.Add(path+".kind", "%q must pin a step kind as name@version (e.g. echo@1)", s.Kind)
		}
	}
	for i, s := range p.Steps {
		for _, name := range sortedKeys(s.In) {
			path := fmt.Sprintf("steps[%d].in.%s", i, name)
			w, ok := ParseWire(s.In[name])
			switch {
			case !stepInRe.MatchString(name):
				errs.Add(path, "%q is not an input name (a name, or name.<n> when one input takes several artifacts)", name)
			case !ok:
				errs.Add(path, "%q must be $inputs.<name> or <step>.<output>", s.In[name])
			case w.Input != "":
				if _, declared := p.Inputs[w.Input]; !declared {
					errs.Add(path, "the pipeline declares no input %q", w.Input)
				}
			case w.Step == s.ID:
				errs.Add(path, "step %q cannot read its own output", s.ID)
			default:
				j, known := ids[w.Step]
				switch {
				case !known:
					errs.Add(path, "no step %q in this pipeline", w.Step)
				case p.Steps[j].Optional && !s.Optional:
					errs.Add(path, "step %q reads the optional step %q: mark it optional too (an optional step's failure skips the steps that read it)", s.ID, w.Step)
				}
			}
		}
	}
	if len(errs) == 0 {
		if _, cycle := p.Order(); cycle != nil {
			errs.Add("steps", "the steps form a cycle: %s", strings.Join(cycle, " → "))
		}
	}
	return errs.Err(p.Name)
}

// Order returns the steps' indexes in execution order: a step comes after every step it reads from, and file
// order breaks ties. When the steps form a cycle it returns the steps on it instead.
func (p Pipeline) Order() ([]int, []string) {
	idx := map[string]int{}
	for i, s := range p.Steps {
		idx[s.ID] = i
	}
	deps := make([]map[int]bool, len(p.Steps))
	indeg := make([]int, len(p.Steps))
	for i, s := range p.Steps {
		deps[i] = map[int]bool{}
		for _, v := range s.In {
			if w, ok := ParseWire(v); ok && w.Step != "" {
				if j, ok := idx[w.Step]; ok && j != i && !deps[i][j] {
					deps[i][j] = true
					indeg[i]++
				}
			}
		}
	}
	done := make([]bool, len(p.Steps))
	var order []int
	for len(order) < len(p.Steps) {
		next := -1
		for i := range p.Steps {
			if !done[i] && indeg[i] == 0 {
				next = i
				break
			}
		}
		if next < 0 {
			var cycle []string
			for i, s := range p.Steps {
				if !done[i] {
					cycle = append(cycle, s.ID)
				}
			}
			return nil, cycle
		}
		done[next] = true
		order = append(order, next)
		for i := range p.Steps {
			if deps[i][next] {
				indeg[i]--
			}
		}
	}
	return order, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
