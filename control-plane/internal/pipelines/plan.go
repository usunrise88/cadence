package pipelines

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

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
	// Auxiliaries are the registry versions the step's parameters name (x-cadence.registryRef), resolved for the
	// project: parameter → version with its payload.
	Auxiliaries map[string]steps.RegistryRef `json:"-"`
	// Deprecation repeats the kind's deprecation; Locked lists the parameters that name registry versions, as the
	// project's data.lock resolves them (lock.go).
	Deprecation *Deprecation `json:"deprecation,omitempty"`
	Locked      []Locked     `json:"locked,omitempty"`
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
	Warnings []Warning // what does not stop the run (deprecated step kinds)
	// Skipped are the optional steps that cannot run (Warnings says why, code step-kind-unavailable): Start records
	// them skipped, with the steps that read them.
	Skipped []SkippedStep
	// Materialize are the dataset versions a training step would read that the cache evicted (a needs-materialize
	// warning each): Start refuses the run (artifact-missing) while there is one.
	Materialize []Materialize
}

// SkippedStep is an optional step the plan skips: its kind is not published, or no worker that publishes it is alive.
type SkippedStep struct {
	Step          string
	Position      int
	Name, Version string
	In            map[string]string
}

// optional reports whether the plan's step id is marked optional.
func (p Plan) optional(id string) bool {
	for _, s := range p.Pipeline.Steps {
		if s.ID == id {
			return s.Optional
		}
	}
	return false
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
	// ProjectID resolves registry references (x-cadence.registryRef) to the versions the project adopted; empty (a
	// file checked on save) resolves them to the newest frozen version without the adoption check.
	ProjectID string
	// SkipInputs plans a pipeline file without artifacts (Validate: a file is saved before anyone has them).
	SkipInputs bool
	// lock reads the project's data.lock at the pipeline's commit (Prepare, for a repository or template pipeline):
	// registry references resolve through it (resolveRef). Nil resolves them through the adoptions.
	lock *lockSet
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
		if in.SkipInputs {
			break
		}
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
	// unavailable holds why an optional step cannot run (its kind is unknown, no longer published, or no worker that
	// publishes it is alive): it is skipped at start with a warning instead of refusing the plan or waiting for ever.
	unavailable := map[int]string{}
	for i, s := range p.Steps {
		name, version, _ := s.KindRef()
		k, found, err := e.o.Kinds.Lookup(ctx, q, name, version)
		if err != nil {
			return Plan{}, err
		}
		if !found {
			if s.Optional {
				unavailable[i] = fmt.Sprintf("no runtime publishes step kind %s", s.Kind)
				continue
			}
			errs.Add(fmt.Sprintf("steps[%d].kind", i), "no runtime publishes step kind %s: check stepKinds.list for the kinds and versions workers offer", s.Kind)
			continue
		}
		if pub, ok := e.o.Kinds.(Publisher); ok {
			current, versions, err := pub.Published(ctx, q, k)
			if err != nil {
				return Plan{}, err
			}
			if !current && s.Optional {
				unavailable[i] = fmt.Sprintf("no registered %s worker publishes step kind %s any more (they publish %s)",
					k.Runtime, s.Kind, listOr(versions, "no version of "+name))
				continue
			}
			if !current {
				errs.Add(fmt.Sprintf("steps[%d].kind", i), "no registered %s worker publishes step kind %s any more (they publish %s), so its step would wait for ever; pin a published version in the pipeline file (projects.sync brings the bundled pipelines up to date)",
					k.Runtime, s.Kind, listOr(versions, "no version of "+name))
				continue
			}
		}
		if lv, ok := e.o.Kinds.(Liveness); ok && s.Optional && !in.SkipInputs {
			live, err := lv.Live(ctx, q, k)
			if err != nil {
				return Plan{}, err
			}
			if !live {
				unavailable[i] = fmt.Sprintf("no %s worker that publishes step kind %s has been seen for %s, so its step would wait in the queue",
					k.Runtime, s.Kind, LiveWindow)
				continue
			}
		}
		kinds[i] = &k
	}

	order, _ := p.Order()
	plan := Plan{Pipeline: p, Estimate: Estimate{Known: true, UnknownSteps: []string{}}}
	var seconds, gpuHours float64
	for pos, i := range order {
		s, k := p.Steps[i], kinds[i]
		if why, ok := unavailable[i]; ok {
			name, version, _ := s.KindRef()
			plan.Skipped = append(plan.Skipped, SkippedStep{Step: s.ID, Position: pos, Name: name, Version: version, In: s.In})
			plan.Warnings = append(plan.Warnings, Warning{Code: WarningStepKindUnavailable, Step: s.ID, Kind: s.Kind,
				Message: why + " (the step is optional: it is skipped and the run goes on without it)"})
			continue
		}
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
		refErrs := Errors{}
		refs, err := resolveRefs(ctx, q, *k, params, in.ProjectID, in.lock, path+".params", &refErrs)
		if err != nil {
			return Plan{}, err
		}
		if len(refErrs) > 0 && s.Optional {
			// An optional member whose auxiliary the project has not adopted (or may not use) is skipped like one whose
			// worker is gone: the run goes on without it instead of refusing the whole plan.
			name, version, _ := s.KindRef()
			plan.Skipped = append(plan.Skipped, SkippedStep{Step: s.ID, Position: pos, Name: name, Version: version, In: s.In})
			plan.Warnings = append(plan.Warnings, Warning{Code: WarningAuxiliaryUnavailable, Step: s.ID, Kind: s.Kind,
				Message: refErrs[0].Message + " (the step is optional: it is skipped and the run goes on without it)"})
			continue
		}
		errs = append(errs, refErrs...)
		ps := PlanStep{Step: s.ID, Position: pos, Kind: *k, Params: params, Departures: deps, In: s.In, Auxiliaries: refs,
			Deprecation: k.Deprecation}
		if k.Deprecation != nil {
			plan.Warnings = append(plan.Warnings, deprecationWarning(s.ID, *k))
		}
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
// anyone has artifacts for it). file is the name it is stored under; previous is the file as it is on main (nil for a
// new file): a pin of a step kind whose deprecation is closed that previous did not have is refused
// (step-kind-deprecated). It answers nil, one pipeline-invalid with every problem, or step-kind-deprecated.
func (e *Engine) Validate(ctx context.Context, q storage.Querier, content []byte, file string, previous []byte) error {
	p, err := Parse(content, file)
	if err != nil {
		return err
	}
	plan, err := e.Plan(ctx, q, p, PlanInput{SkipInputs: true})
	if err != nil {
		return err
	}
	return newDeprecatedPins(plan, pins(previous, file), e.now())
}
