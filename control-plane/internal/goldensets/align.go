package goldensets

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/langtag"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Aligning many golden sets in one run (goldenSets.align, phase 4 tail): one pipeline run with one aligning step per
// dataset artifact, so a batch of golden sets is one GPU-spend decision with a known estimate instead of one
// pipelines.run (and one approval of an unknown cost) per set. The aligning kind is the newest published step kind
// that turns one dataset into an alignment (Go does not name it); the aligner auxiliary its parameter names is
// resolved for the project by the engine's plan. Sets are skipped, with the reason, when their dataset artifact is
// already aligned, when an aligning step on it has not finished, or when the aligner does not cover their language.

// AlignOperation is the command that aligns golden sets.
const AlignOperation = "goldenSets.align"

// AlignPipeline names the generated pipeline (an inline source).
const AlignPipeline = "align-golden-sets"

// optionalNote ends the plan's warning about an optional step it skips (internal/pipelines plan.go).
const optionalNote = " (the step is optional: it is skipped and the run goes on without it)"

// Skip reasons (the contract's GoldenSetAlignSkip.reason).
const (
	SkipAligned  = "aligned"
	SkipRunning  = "running"
	SkipLanguage = "language"
)

// AlignInput is a goldenSets.align request.
type AlignInput struct {
	ProjectID  string
	Actor      auth.Actor
	GoldenSets []string // ver_…, @alias or collection names (* patterns); empty: every golden set the project adopted
	Aligner    string   // the aligner auxiliary; empty: the aligning kind's default
	Priority   int
}

// AlignSet is a golden set the run aligns (the contract's GoldenSetAlignSet).
type AlignSet struct {
	GoldenSetVersionID string  `json:"goldenSetVersionId"`
	Name               string  `json:"name"`
	Version            string  `json:"version"`
	Locale             string  `json:"locale"`
	Utterances         int     `json:"utterances"`
	Hours              float64 `json:"hours"`
	Step               string  `json:"step"`
	EstimateSeconds    float64 `json:"estimateSeconds"`

	datasetHash string
}

// AlignSkip is a golden set the run leaves out, and why (the contract's GoldenSetAlignSkip).
type AlignSkip struct {
	GoldenSetVersionID string `json:"goldenSetVersionId"`
	Name               string `json:"name"`
	Version            string `json:"version"`
	Locale             string `json:"locale"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
	AlignmentID        string `json:"alignmentId,omitempty"`
	PipelineRunID      string `json:"pipelineRunId,omitempty"`
}

// AlignerView is the aligner auxiliary the steps resolve to.
type AlignerView struct {
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	VersionID string   `json:"versionId"`
	Languages []string `json:"languages"`
}

// AlignPlan is a goldenSets.align request checked and planned (the contract's GoldenSetAlignPlan); Start starts it.
type AlignPlan struct {
	Kind                string              `json:"kind"`
	Aligner             *AlignerView        `json:"aligner,omitempty"`
	Sets                []AlignSet          `json:"sets"`
	Skipped             []AlignSkip         `json:"skipped"`
	SecondsPerAudioHour float64             `json:"secondsPerAudioHour"`
	Estimate            pipelines.Estimate  `json:"estimate"`
	Warnings            []pipelines.Warning `json:"warnings"`
	PipelineRun         *pipelines.Run      `json:"pipelineRun,omitempty"`

	start *pipelines.StartInput // nil when nothing is left to align
}

// Aligner plans and starts goldenSets.align.
type Aligner struct {
	Engine   *pipelines.Engine
	CAS      *cas.Store
	Defaults func() *defaults.Defaults // defaults.Get when nil
}

func (a *Aligner) defaults() *defaults.Defaults {
	if a.Defaults != nil {
		return a.Defaults()
	}
	return defaults.Get()
}

// Prepare resolves the golden sets, skips what needs no alignment and plans one aligning step per dataset artifact
// left; it writes nothing (a dry run answers it).
func (a *Aligner) Prepare(ctx context.Context, q storage.Querier, in AlignInput) (AlignPlan, error) {
	versions, err := alignTargets(ctx, q, in)
	if err != nil {
		return AlignPlan{}, err
	}
	kind, err := aligningKind(ctx, q)
	if err != nil {
		return AlignPlan{}, err
	}
	d := a.defaults()
	perHour, overhead := d.Eval.AlignSecondsPerAudioHour.Value, d.Eval.AlignStepOverheadS.Value
	pl := AlignPlan{Kind: kind.Ref(), Sets: []AlignSet{}, Skipped: []AlignSkip{}, SecondsPerAudioHour: perHour,
		Estimate: pipelines.Estimate{Known: true, UnknownSteps: []string{}}, Warnings: []pipelines.Warning{}}
	var sets []AlignSet
	var hashes []string
	for _, v := range versions {
		var gp Payload
		if err := json.Unmarshal(v.Payload, &gp); err != nil {
			return AlignPlan{}, fmt.Errorf("decode golden set %s: %w", v.ID, err)
		}
		if !steps.ValidHash(gp.DatasetHash) {
			return AlignPlan{}, problems.Conflict.New("golden set %s %s names no dataset artifact", v.Name, v.Version)
		}
		sets = append(sets, AlignSet{GoldenSetVersionID: v.ID, Name: v.Name, Version: v.Version, Locale: gp.Locale,
			Utterances: gp.Utterances, Hours: gp.Hours, datasetHash: gp.DatasetHash})
		hashes = append(hashes, gp.DatasetHash)
	}
	aligned, err := Alignments(ctx, q, hashes)
	if err != nil {
		return AlignPlan{}, err
	}
	running, err := runningAligns(ctx, q, kind.Name, hashes)
	if err != nil {
		return AlignPlan{}, err
	}
	var todo []AlignSet
	for _, s := range sets {
		switch al, isAligned := aligned[s.datasetHash]; {
		case isAligned:
			pl.Skipped = append(pl.Skipped, s.skip(SkipAligned, fmt.Sprintf("already aligned (%s: %d of %d utterances by %s)",
				al.ID, al.Aligned, al.Utterances, al.Aligner), func(k *AlignSkip) { k.AlignmentID = al.ID }))
		case running[s.datasetHash] != "":
			run := running[s.datasetHash]
			pl.Skipped = append(pl.Skipped, s.skip(SkipRunning, fmt.Sprintf("an aligning step on its dataset has not finished (pipeline run %s)", run),
				func(k *AlignSkip) { k.PipelineRunID = run }))
		default:
			todo = append(todo, s)
		}
	}
	if len(todo) == 0 {
		return pl, nil
	}
	refParam, err := alignerParam(kind, in.Aligner)
	if err != nil {
		return AlignPlan{}, err
	}
	// Plan everything once to learn the aligner the project resolves to, then leave out the languages it lacks.
	start, plan, err := a.plan(ctx, q, in, kind, refParam, todo, perHour, overhead)
	if err != nil {
		return AlignPlan{}, err
	}
	view, err := alignerOf(plan, refParam)
	if err != nil {
		return AlignPlan{}, err
	}
	pl.Aligner = view
	var kept []AlignSet
	for _, s := range todo {
		if view != nil && !covers(view.Languages, s.Locale) {
			pl.Skipped = append(pl.Skipped, s.skip(SkipLanguage, fmt.Sprintf("%s does not cover %s (it lists %s)",
				view.Name, s.Locale, strings.Join(view.Languages, ", ")), nil))
			continue
		}
		kept = append(kept, s)
	}
	if len(kept) == 0 {
		return pl, nil
	}
	if len(kept) < len(todo) {
		if start, plan, err = a.plan(ctx, q, in, kind, refParam, kept, perHour, overhead); err != nil {
			return AlignPlan{}, err
		}
	}
	pl.Sets, pl.Estimate, pl.Warnings, pl.start = stepsOf(kept, start), plan.Estimate, plan.Warnings, start
	if pl.Warnings == nil {
		pl.Warnings = []pipelines.Warning{}
	}
	return pl, nil
}

// Start starts the planned run; a plan with nothing to align starts nothing.
func (a *Aligner) Start(ctx context.Context, tx pgx.Tx, pl AlignPlan, actor auth.Actor) (AlignPlan, []events.Draft, error) {
	if pl.start == nil {
		return pl, nil, nil
	}
	in := *pl.start
	in.Actor = actor
	r, drafts, err := a.Engine.Start(ctx, tx, in)
	if err != nil {
		return AlignPlan{}, nil, err
	}
	pl.PipelineRun = &r
	return pl, drafts, nil
}

// Started reports whether the plan has a run to start.
func (pl AlignPlan) Started() bool { return pl.start != nil }

// GPUHours is the plan's GPU estimate for the policy (0 when nothing is left to align).
func (pl AlignPlan) GPUHours() float64 {
	if pl.Estimate.GPUHours == nil {
		return 0
	}
	return *pl.Estimate.GPUHours
}

// Unknown reports a GPU step without an estimate (the policy then asks a person).
func (pl AlignPlan) Unknown() bool { return pl.Estimate.UnknownGPU }

func (s AlignSet) skip(reason, msg string, with func(*AlignSkip)) AlignSkip {
	k := AlignSkip{GoldenSetVersionID: s.GoldenSetVersionID, Name: s.Name, Version: s.Version, Locale: s.Locale,
		Reason: reason, Message: msg}
	if with != nil {
		with(&k)
	}
	return k
}

// plan builds the pipeline (one optional aligning step per dataset artifact of sets, golden sets on one artifact
// sharing it) and plans it for the project.
func (a *Aligner) plan(ctx context.Context, q storage.Querier, in AlignInput, kind pipelines.Kind, refParam string, sets []AlignSet,
	perHour, overhead float64) (*pipelines.StartInput, pipelines.Plan, error) {
	consumed := ""
	for name := range kind.Consumes {
		consumed = name
	}
	p := pipelines.Pipeline{Name: AlignPipeline, Description: "Word timings for the reference texts of golden sets (goldenSets.align)",
		Inputs: map[string]string{}}
	start := &pipelines.StartInput{ProjectID: in.ProjectID, Pipeline: &p, Inputs: map[string]steps.ArtifactRef{},
		Params: map[string]map[string]any{}, Estimates: map[string]float64{}, Actor: in.Actor, Priority: in.Priority}
	stepOf := map[string]string{}
	hours := map[string]float64{}
	for _, s := range sets {
		if _, ok := stepOf[s.datasetHash]; ok {
			hours[stepOf[s.datasetHash]] = math.Max(hours[stepOf[s.datasetHash]], s.Hours)
			continue
		}
		n := len(stepOf) + 1
		input, id := fmt.Sprintf("data%d", n), fmt.Sprintf("align-%d", n)
		stepOf[s.datasetHash], hours[id] = id, s.Hours
		ref, err := a.sized(ctx, q, steps.ArtifactRef{Hash: s.datasetHash, Type: TypeDataset})
		if err != nil {
			return nil, pipelines.Plan{}, err
		}
		p.Inputs[input] = TypeDataset
		p.Steps = append(p.Steps, pipelines.Step{ID: id, Kind: kind.Ref(), In: map[string]string{consumed: "$inputs." + input},
			Optional: true})
		start.Inputs[input] = ref
		if in.Aligner != "" {
			start.Params[id] = map[string]any{refParam: in.Aligner}
		}
	}
	for id, h := range hours {
		start.Estimates[id] = math.Round(overhead + h*perHour)
	}
	_, plan, err := a.Engine.Prepare(ctx, q, *start)
	if err != nil {
		return nil, pipelines.Plan{}, err
	}
	// The steps are optional so that one set's failure leaves the others going; a step the plan would skip outright
	// (an aligner the project has not adopted, no live worker of the kind's runtime) refuses the request instead.
	for _, w := range plan.Warnings {
		msg := strings.TrimSuffix(w.Message, optionalNote)
		switch w.Code {
		case pipelines.WarningAuxiliaryUnavailable:
			return nil, pipelines.Plan{}, problems.Validation([]problems.FieldError{{Path: "/aligner", Message: msg}})
		case pipelines.WarningStepKindUnavailable:
			return nil, pipelines.Plan{}, problems.FamilyUnavailable.New("%s", msg)
		}
	}
	if len(plan.Skipped) > 0 {
		return nil, pipelines.Plan{}, problems.FamilyUnavailable.New("%s cannot run now (step %s would be skipped)", kind.Ref(), plan.Skipped[0].Step)
	}
	return start, plan, nil
}

// stepsOf gives each set the step that aligns its dataset artifact and that step's estimate.
func stepsOf(sets []AlignSet, start *pipelines.StartInput) []AlignSet {
	byInput := map[string]string{}
	for _, st := range start.Pipeline.Steps {
		for _, wire := range st.In {
			byInput[strings.TrimPrefix(wire, "$inputs.")] = st.ID
		}
	}
	byHash := map[string]string{}
	for input, ref := range start.Inputs {
		byHash[ref.Hash] = byInput[input]
	}
	out := make([]AlignSet, 0, len(sets))
	for _, s := range sets {
		s.Step = byHash[s.datasetHash]
		s.EstimateSeconds = start.Estimates[s.Step]
		out = append(out, s)
	}
	return out
}

// sized completes ref with the size the artifact index (else the content store) holds; the engine then checks it.
func (a *Aligner) sized(ctx context.Context, q storage.Querier, ref steps.ArtifactRef) (steps.ArtifactRef, error) {
	art, err := artifacts.Get(ctx, q, ref.Hash)
	if pe, ok := problems.As(err); ok && pe.Type == problems.NotFound {
		if a.CAS != nil {
			if has, size, err := a.CAS.Has(ref.Hash); err == nil && has {
				ref.Size = size
			}
		}
		return ref, nil
	}
	if err != nil {
		return ref, err
	}
	ref.Size, ref.Meta = art.Size, art.Meta
	return ref, nil
}

// alignTargets resolves the request's golden sets: named ones (ver_…, @alias, collection names and * patterns over
// the adopted ones; a collection the project did not adopt resolves to its newest frozen version) or every golden
// set the project adopted, the newest adopted version of each collection.
func alignTargets(ctx context.Context, q storage.Querier, in AlignInput) ([]registry.Version, error) {
	adopted := func(refs []string) ([]registry.Version, error) {
		list, err := registry.ListAdoptions(ctx, q, in.ProjectID, registry.KindGoldenSet)
		if err != nil {
			return nil, err
		}
		var out []registry.Version
		seen := map[string]bool{}
		for _, ad := range list { // newest first
			v := ad.Version
			if v.Kind != registry.KindGoldenSet || seen[v.Name] || (refs != nil && !matches(refs, v.ID, v.Name)) {
				continue
			}
			seen[v.Name] = true
			out = append(out, v)
		}
		return out, nil
	}
	var versions []registry.Version
	if len(in.GoldenSets) == 0 {
		list, err := adopted(nil)
		if err != nil {
			return nil, err
		}
		if len(list) == 0 {
			return nil, problems.Validation([]problems.FieldError{{Path: "/goldenSets",
				Message: "the project adopted no golden set; adopt one (projects.adopt) or name golden sets"}})
		}
		versions = list
	} else {
		var fields []problems.FieldError
		for i, r := range in.GoldenSets {
			r = strings.TrimSpace(r)
			if strings.HasPrefix(r, "ver_") || strings.HasPrefix(r, "@") {
				v, err := registry.Resolve(ctx, q, in.ProjectID, registry.KindGoldenSet, r)
				if err != nil {
					return nil, err
				}
				versions = append(versions, v)
				continue
			}
			list, err := adopted([]string{r})
			if err != nil {
				return nil, err
			}
			if len(list) == 0 && !strings.Contains(r, "*") {
				v, err := registry.Latest(ctx, q, registry.KindGoldenSet, r)
				if err != nil {
					return nil, err
				}
				list = []registry.Version{v}
			}
			if len(list) == 0 {
				fields = append(fields, problems.FieldError{Path: fmt.Sprintf("/goldenSets/%d", i),
					Message: fmt.Sprintf("%s matches no golden set the project adopted", r)})
			}
			versions = append(versions, list...)
		}
		if len(fields) > 0 {
			return nil, problems.Validation(fields)
		}
	}
	seen := map[string]bool{}
	out := versions[:0:0]
	for _, v := range versions {
		if !seen[v.ID] {
			seen[v.ID] = true
			out = append(out, v)
		}
	}
	return out, nil
}

func matches(list []string, versionID, name string) bool {
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

// aligningKind is the newest published step kind that turns one dataset into an alignment.
func aligningKind(ctx context.Context, q storage.Querier) (pipelines.Kind, error) {
	list, err := registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindStepKind})
	if err != nil {
		return pipelines.Kind{}, err
	}
	for _, v := range list { // newest first
		if v.State == registry.StateDeprecated {
			continue
		}
		var k pipelines.Kind
		if json.Unmarshal(v.Payload, &k) != nil || len(k.Consumes) != 1 || len(k.Produces) != 1 {
			continue
		}
		if !slices.Contains(mapValues(k.Consumes), TypeDataset) || !slices.Contains(mapValues(k.Produces), TypeAlignment) {
			continue
		}
		if k.Name == "" {
			k.Name = strings.TrimPrefix(v.Name, "step-kind/")
		}
		k.VersionID = v.ID
		return k, nil
	}
	return pipelines.Kind{}, problems.FamilyUnavailable.New(
		"no runtime publishes a step kind that aligns a dataset (consumes dataset, produces alignment); start the worker whose runtime carries it (stepKinds.list)")
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// alignerParam is the aligning kind's parameter that names the aligner auxiliary (x-cadence.registryRef).
func alignerParam(k pipelines.Kind, requested string) (string, error) {
	refs := pipelines.RegistryRefs(k)
	var names []string
	for name, spec := range refs {
		if spec.Kind == "auxiliary" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) == 0 {
		if requested != "" {
			return "", problems.Validation([]problems.FieldError{{Path: "/aligner",
				Message: fmt.Sprintf("%s takes no aligner auxiliary", k.Ref())}})
		}
		return "", nil
	}
	return names[0], nil
}

// alignerOf reads the aligner the plan's first step resolved (nil when the kind names none).
func alignerOf(plan pipelines.Plan, param string) (*AlignerView, error) {
	if param == "" || len(plan.Steps) == 0 {
		return nil, nil
	}
	ref, ok := plan.Steps[0].Auxiliaries[param]
	if !ok {
		return nil, nil
	}
	var p struct {
		Languages []string `json:"languages"`
	}
	if err := json.Unmarshal(ref.Payload, &p); err != nil {
		return nil, fmt.Errorf("aligner %s payload: %w", ref.VersionID, err)
	}
	if p.Languages == nil {
		p.Languages = []string{}
	}
	return &AlignerView{Name: ref.Name, Version: ref.Version, VersionID: ref.VersionID, Languages: p.Languages}, nil
}

// covers reports whether an aligner's languages (primary subtags, or *) include a locale, as the aligning step decides
// per utterance (its primary subtag).
func covers(languages []string, locale string) bool {
	if slices.Contains(languages, "*") {
		return true
	}
	want := langtag.Primary(locale)
	if want == "" {
		return false
	}
	for _, l := range languages {
		if langtag.Primary(l) == want {
			return true
		}
	}
	return false
}

// runningAligns maps each dataset artifact of hashes that an unfinished step of kind reads to that step's run.
func runningAligns(ctx context.Context, q storage.Querier, kind string, hashes []string) (map[string]string, error) {
	out := map[string]string{}
	if len(hashes) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `SELECT DISTINCT ON (e.value->>'hash') e.value->>'hash', s.pipeline_run_id
		FROM pipeline_steps s, jsonb_each(s.inputs) e
		WHERE s.kind = $1 AND s.state IN ('waiting', 'queued', 'running') AND s.inputs IS NOT NULL
			AND jsonb_typeof(s.inputs) = 'object' AND e.value->>'hash' = ANY($2)
		ORDER BY e.value->>'hash', s.created_at DESC`, kind, hashes)
	if err != nil {
		return nil, fmt.Errorf("unfinished aligning steps: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h, run string
		if err := rows.Scan(&h, &run); err != nil {
			return nil, fmt.Errorf("unfinished aligning steps: %w", err)
		}
		out[h] = run
	}
	return out, rows.Err()
}
