package pipelines

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// PlanStep is one validated step: its kind as published, resolved parameters and departures.
type PlanStep struct {
	Step            string            `json:"step"`
	Position        int               `json:"-"`
	Kind            Kind              `json:"-"`
	Params          map[string]any    `json:"params"`
	Departures      []Departure       `json:"departures"`
	In              map[string]string `json:"in"`
	EstimateSeconds *float64          `json:"estimateSeconds,omitempty"`
}

// Estimate sums the steps' estimates (R12); a step without one makes it partial.
type Estimate struct {
	Known        bool     `json:"known"`
	Seconds      *float64 `json:"seconds,omitempty"`
	GPUHours     *float64 `json:"gpuHours,omitempty"`
	UnknownSteps []string `json:"unknownSteps"`
	// UnknownGPU: a step that needs a card has no estimate, so GPUHours understates the cost (the policy then waits
	// for a person rather than weighing a partial sum against the budget).
	UnknownGPU bool `json:"-"`
}

// Plan is a pipeline validated against the published step kinds, in execution order.
type Plan struct {
	Pipeline Pipeline
	Steps    []PlanStep
	Estimate Estimate
}

// PlanInput is what Plan checks a pipeline against besides the step registry.
type PlanInput struct {
	// Inputs are the artifacts the run starts from, by pipeline input; every declared input is required and its
	// type must match.
	Inputs map[string]steps.ArtifactRef
	// Params overrides parameters per step id.
	Params map[string]map[string]any
	// Estimates are per-step wall-time estimates in seconds from the facade that starts the run (R12); they win
	// over a kind's published estimate.
	Estimates map[string]float64
}

// Plan validates p for a run: every kind@version is published, every step input is wired to an artifact of the
// type the kind consumes, parameters resolve against defaults.yaml and fit each kind's schema, and the pipeline
// inputs are given with their declared types. All problems come back together as one pipeline-invalid.
func (e *Engine) Plan(ctx context.Context, q storage.Querier, p Pipeline, in PlanInput) (Plan, error) {
	if err := p.Check(""); err != nil {
		return Plan{}, err
	}
	var errs Errors
	for _, name := range sortedKeys(p.Inputs) {
		ref, given := in.Inputs[name]
		switch {
		case !given:
			errs.Add("inputs."+name, "the pipeline needs input %q (an artifact of type %s)", name, p.Inputs[name])
		case !Accepts(p.Inputs[name], ref.Type):
			errs.Add("inputs."+name, "input %q must be a %s artifact, not %s", name, p.Inputs[name], ref.Type)
		case !steps.ValidHash(ref.Hash):
			errs.Add("inputs."+name, "%q is not an artifact hash (b3:<64 hex>)", ref.Hash)
		}
	}
	for _, name := range sortedKeys(in.Inputs) {
		if _, declared := p.Inputs[name]; !declared {
			errs.Add("inputs."+name, "the pipeline declares no input %q (it declares %s)", name, listOr(sortedKeys(p.Inputs), "none"))
		}
	}
	ids := map[string]int{}
	for i, s := range p.Steps {
		ids[s.ID] = i
	}
	for _, id := range sortedKeys(in.Params) {
		if _, ok := ids[id]; !ok {
			errs.Add("params."+id, "the pipeline has no step %q", id)
		}
	}

	kinds := make([]*Kind, len(p.Steps))
	for i, s := range p.Steps {
		name, version, _ := s.KindRef()
		k, found, err := e.o.Kinds.Lookup(ctx, q, name, version)
		if err != nil {
			return Plan{}, err
		}
		if !found {
			errs.Add(fmt.Sprintf("steps[%d].kind", i), "no runtime publishes step kind %s: check stepKinds.list for the kinds and versions workers offer", s.Kind)
			continue
		}
		kinds[i] = &k
	}

	order, _ := p.Order()
	plan := Plan{Pipeline: p, Estimate: Estimate{Known: true, UnknownSteps: []string{}}}
	var seconds, gpuHours float64
	for pos, i := range order {
		s, k := p.Steps[i], kinds[i]
		if k == nil {
			continue
		}
		path := fmt.Sprintf("steps[%d]", i)
		wired := map[string]bool{}
		for name := range s.In {
			wired[ConsumedName(name)] = true
		}
		for _, name := range sortedKeys(k.Consumes) {
			if !wired[name] && !slices.Contains(k.OptionalInputs, name) {
				errs.Add(path+".in."+name, "%s consumes %q (%s); wire it from $inputs.<name> or <step>.<output>", s.Kind, name, k.Consumes[name])
			}
		}
		for _, name := range sortedKeys(s.In) {
			want, consumed := k.Consumes[ConsumedName(name)]
			if !consumed {
				errs.Add(path+".in."+name, "%s has no input %q (it consumes %s)", s.Kind, name, listOr(sortedKeys(k.Consumes), "nothing"))
				continue
			}
			w, _ := ParseWire(s.In[name])
			var got string
			if w.Input != "" {
				got = p.Inputs[w.Input]
			} else {
				pk := kinds[ids[w.Step]]
				if pk == nil {
					continue // the producer's kind is unknown; reported on its own step
				}
				t, ok := pk.Produces[w.Output]
				if !ok {
					errs.Add(path+".in."+name, "step %q (%s) produces no %q (it produces %s)", w.Step, pk.Ref(), w.Output, listOr(sortedKeys(pk.Produces), "nothing"))
					continue
				}
				if slices.Contains(pk.OptionalOutputs, w.Output) {
					errs.Add(path+".in."+name, "step %q (%s) may finish without %q (an optional output); wire an output it always writes", w.Step, pk.Ref(), w.Output)
					continue
				}
				got = t
			}
			if !Accepts(want, got) {
				errs.Add(path+".in."+name, "%s consumes a %s artifact as %q, but %s is a %s", s.Kind, want, name, s.In[name], got)
			}
		}
		params, deps, probs := resolveParams(*k, s.Params, in.Params[s.ID], e.defaults())
		for _, pr := range probs {
			fp := path + ".params"
			if pr.param != "" {
				fp += "." + pr.param
			}
			errs.Add(fp, "%s", pr.message)
		}
		if deps == nil {
			deps = []Departure{}
		}
		ps := PlanStep{Step: s.ID, Position: pos, Kind: *k, Params: params, Departures: deps, In: s.In}
		if est, ok := in.Estimates[s.ID]; ok {
			ps.EstimateSeconds = &est
		} else if k.EstimateSeconds != nil {
			est := *k.EstimateSeconds
			ps.EstimateSeconds = &est
		}
		if ps.EstimateSeconds == nil {
			plan.Estimate.Known = false
			plan.Estimate.UnknownSteps = append(plan.Estimate.UnknownSteps, s.ID)
			plan.Estimate.UnknownGPU = plan.Estimate.UnknownGPU || k.Resources.GPU
		} else {
			seconds += *ps.EstimateSeconds
			if k.Resources.GPU {
				gpuHours += *ps.EstimateSeconds / 3600 * float64(max(1, k.Resources.GPUs))
			}
		}
		if ps.In == nil {
			ps.In = map[string]string{}
		}
		plan.Steps = append(plan.Steps, ps)
	}
	if err := errs.Err(p.Name); err != nil {
		return Plan{}, err
	}
	if len(plan.Steps) > len(plan.Estimate.UnknownSteps) {
		plan.Estimate.Seconds, plan.Estimate.GPUHours = &seconds, &gpuHours
	}
	sort.Strings(plan.Estimate.UnknownSteps)
	return plan, nil
}

func listOr(items []string, none string) string {
	if len(items) == 0 {
		return none
	}
	return strings.Join(items, ", ")
}

// Validate checks a pipeline file's content the way a run would plan it — strict parsing, structure, pinned kinds
// published by a worker, wiring and parameters against each kind's schema — without inputs (a file is saved before
// anyone has artifacts for it, so "needs input" problems are dropped). file is the name it is stored under. It answers
// nil or one pipeline-invalid with every remaining problem.
func (e *Engine) Validate(ctx context.Context, q storage.Querier, content []byte, file string) error {
	p, err := Parse(content, file)
	if err != nil {
		return err
	}
	_, err = e.Plan(ctx, q, p, PlanInput{})
	var pe *problems.Error
	if err == nil || !errors.As(err, &pe) || pe.Type != problems.PipelineInvalid {
		return err
	}
	var left Errors
	for _, f := range pe.Errors {
		if !strings.HasPrefix(f.Path, "inputs.") {
			left = append(left, f)
		}
	}
	return left.Err(p.Name)
}
