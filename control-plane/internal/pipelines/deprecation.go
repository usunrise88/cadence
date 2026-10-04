package pipelines

import (
	"fmt"
	"strings"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// Step-kind deprecation (docs/spec/02-domain-projects-registry.md "Registry": "a version can be deprecated but not
// removed while any pipeline template or project pipeline pins it; deprecation shows as a warning on the Pipeline run
// and in the Recipe document"). A pack deprecates a kind version in what it publishes (StepKindDescriptor
// .deprecation: after, replacedBy, note); a deprecated kind keeps running. Every plan that pins it carries a warning,
// and from the day After a pipeline file that did not pin it yet is refused when it is saved (step-kind-deprecated):
// pins already on main keep working until their file changes them.

// Deprecation is the contract's StepKindDeprecation.
type Deprecation struct {
	After      string `json:"after"` // YYYY-MM-DD (UTC)
	ReplacedBy string `json:"replacedBy,omitempty"`
	Note       string `json:"note,omitempty"`
}

func (e *Engine) now() time.Time {
	if e.o.Clock != nil {
		return e.o.Clock()
	}
	return time.Now()
}

// Warning is the contract's PipelineWarning.
type Warning struct {
	Code    string `json:"code"`
	Step    string `json:"step,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Message string `json:"message"`
}

// WarningStepKindDeprecated is the code of a deprecated step kind's warning.
const WarningStepKindDeprecated = "step-kind-deprecated"

// Closed reports whether new pins of the deprecated kind are refused at now: from the day After (UTC) on. A
// deprecation whose date does not parse is closed (the pack meant to retire it).
func (d Deprecation) Closed(now time.Time) bool {
	after, err := time.Parse(time.DateOnly, d.After)
	if err != nil {
		return true
	}
	return !now.UTC().Before(after)
}

func (d Deprecation) advice() string {
	var b strings.Builder
	if d.ReplacedBy != "" {
		fmt.Fprintf(&b, "; pin %s instead", d.ReplacedBy)
	}
	if d.Note != "" {
		fmt.Fprintf(&b, " (%s)", d.Note)
	}
	return b.String()
}

func deprecationWarning(step string, k Kind) Warning {
	d := *k.Deprecation
	return Warning{Code: WarningStepKindDeprecated, Step: step, Kind: k.Ref(),
		Message: fmt.Sprintf("step %s pins %s, which its pack deprecates: new pins are refused from %s%s", step, k.Ref(), d.After, d.advice())}
}

// pins returns the kind@version pins of a pipeline file's content; nil when it does not parse (a broken file on main
// pins nothing anyone can run).
func pins(content []byte, file string) map[string]bool {
	if len(content) == 0 {
		return nil
	}
	p, err := Parse(content, file)
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, s := range p.Steps {
		out[s.Kind] = true
	}
	return out
}

// newDeprecatedPins refuses (step-kind-deprecated) the steps of plan that pin a kind whose deprecation is closed at
// now and that previous (the file as it is on main; nil for a new file) did not pin.
func newDeprecatedPins(plan Plan, previous map[string]bool, now time.Time) error {
	var pe *problems.Error
	for _, ps := range plan.Steps {
		d := ps.Kind.Deprecation
		if d == nil || !d.Closed(now) || previous[ps.Kind.Ref()] {
			continue
		}
		msg := fmt.Sprintf("step %s pins %s, deprecated since %s%s; a pipeline may not newly pin it", ps.Step, ps.Kind.Ref(), d.After, d.advice())
		if pe == nil {
			pe = problems.StepKindDeprecated.New("%s", msg)
		}
		pe.Errors = append(pe.Errors, problems.FieldError{Path: fmt.Sprintf("steps[%d].kind", stepIndex(plan.Pipeline, ps.Step)), Message: msg})
	}
	if pe == nil {
		return nil
	}
	return pe
}

func stepIndex(p Pipeline, id string) int {
	for i, s := range p.Steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}
