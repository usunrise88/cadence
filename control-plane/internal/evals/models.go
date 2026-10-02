package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// ModelPayload is the payload of a model version (registry kind model, R22; the contract's ModelPayload).
type ModelPayload struct {
	CheckpointID       string       `json:"checkpointId"`
	WeightsHash        string       `json:"weightsHash"`
	CheckpointHash     string       `json:"checkpointHash"`
	FamilyID           string       `json:"familyId"`
	BaseModelVersionID string       `json:"baseModelVersionId"`
	ProjectID          string       `json:"projectId"`
	EvalID             string       `json:"evalId"`
	Gate               ModelGate    `json:"gate"`
	Lineage            ModelLineage `json:"lineage"`
	Card               string       `json:"card"`
}

// ModelGate is the verdict a model version was registered with.
type ModelGate struct {
	Verdict  string `json:"verdict"`
	GatesSHA string `json:"gatesSha"`
}

// ModelLineage is where a model version came from.
type ModelLineage struct {
	RunID             string   `json:"runId,omitempty"`
	MixSHA            string   `json:"mixSha,omitempty"`
	RecipeSHA         string   `json:"recipeSha,omitempty"`
	DatasetVersionIDs []string `json:"datasetVersionIds"`
}

// RegisterInput is models.register.
type RegisterInput struct {
	ProjectID    string
	CheckpointID string
	Name         string // model/<name>; default model/<project slug>
	EvalID       string // default the checkpoint's latest gated eval
	Description  string
	Actor        auth.Actor
}

// Registration is a model version about to be registered (what a dry run answers: the contract's ModelRegistration).
type Registration struct {
	Name  string       `json:"name"`
	Model ModelPayload `json:"model"`

	description string
	tags        []string
	licence     string
}

var modelNameRe = regexp.MustCompile(`^model/[a-z0-9][a-z0-9._-]{0,98}[a-z0-9]$`)

// PlanRegister checks that checkpoint in.CheckpointID may be published — its latest gated eval (or in.EvalID) passed
// (gate-not-passed otherwise) — and builds the version's payload with its lineage and model card.
func (s *Service) PlanRegister(ctx context.Context, q storage.Querier, in RegisterInput) (Registration, error) {
	p, err := projects.GetByID(ctx, q, in.ProjectID)
	if err != nil {
		return Registration{}, err
	}
	c, err := runs.GetCheckpoint(ctx, q, strings.TrimSpace(in.CheckpointID))
	if err != nil {
		return Registration{}, err
	}
	if c.ProjectID != p.ID {
		return Registration{}, problems.NotFound.New("the project has no checkpoint %q", in.CheckpointID)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "model/" + p.Slug
	}
	if !modelNameRe.MatchString(name) {
		return Registration{}, problems.Validation([]problems.FieldError{{Path: "/name",
			Message: fmt.Sprintf("%q is not a model collection (model/<name>: lowercase letters, digits, ., _ and -)", name)}})
	}
	e, err := s.gatedEval(ctx, q, p, c.ID, strings.TrimSpace(in.EvalID))
	if err != nil {
		return Registration{}, err
	}
	var v Verdict
	if err := json.Unmarshal(e.Gate, &v); err != nil {
		return Registration{}, fmt.Errorf("decode the verdict of %s: %w", e.ID, err)
	}
	if v.Verdict != VerdictPassed {
		return Registration{}, problems.GateNotPassed.New("the gate failed on eval %s of checkpoint %s (%s); only a checkpoint whose latest gated eval passed is registered",
			e.ID, c.ID, failedChecks(v))
	}
	run, err := s.Runs.Get(ctx, q, c.RunID)
	if err != nil {
		return Registration{}, err
	}
	var baseID string
	if err := q.QueryRow(ctx, "SELECT base_version_id FROM runs WHERE id = $1", c.RunID).Scan(&baseID); err != nil {
		return Registration{}, fmt.Errorf("read the base model of run %s: %w", c.RunID, err)
	}
	base, err := registry.GetVersion(ctx, q, registry.KindBaseModel, baseID)
	if err != nil {
		return Registration{}, err
	}
	lineage := ModelLineage{RunID: run.ID, MixSHA: run.Mix.Hash, RecipeSHA: run.Recipe.Commit, DatasetVersionIDs: []string{}}
	var mixHours float64
	if a, err := artifacts.Get(ctx, q, run.Mix.Hash); err == nil {
		var m struct {
			Datasets []string `json:"datasets"`
			Hours    float64  `json:"hours"`
		}
		if json.Unmarshal(a.Meta, &m) == nil {
			lineage.DatasetVersionIDs = append(lineage.DatasetVersionIDs, m.Datasets...)
			mixHours = m.Hours
		}
	}
	weights := c.WeightsHash
	if weights == "" {
		weights = c.Artifact
	}
	mp := ModelPayload{
		CheckpointID: c.ID, WeightsHash: weights, CheckpointHash: c.Artifact, FamilyID: run.Family["name"], BaseModelVersionID: base.ID,
		ProjectID: p.ID, EvalID: e.ID, Gate: ModelGate{Verdict: v.Verdict, GatesSHA: v.GatesSHA}, Lineage: lineage,
	}
	view, err := s.View(ctx, q, e, ViewQuery{})
	if err != nil {
		return Registration{}, err
	}
	datasets, err := registry.ListVersions(ctx, q, registry.Filter{IDs: lineage.DatasetVersionIDs})
	if err != nil {
		return Registration{}, err
	}
	mp.Card = Card(CardInput{Name: name, Project: p, Checkpoint: c, Run: run, Base: base, Eval: view, Verdict: v, Datasets: datasets,
		MixHours: mixHours, CharLanguages: s.defaults().Eval.CharacterErrorLanguages.Value})
	desc := strings.TrimSpace(in.Description)
	if desc == "" {
		desc = fmt.Sprintf("Models of project %s, registered from checkpoints whose gate passed", p.Name)
	}
	var tags []string
	for _, t := range base.Tags {
		if strings.HasPrefix(t, "locale:") {
			tags = append(tags, t)
		}
	}
	tags = append(tags, "family:"+mp.FamilyID)
	return Registration{Name: name, Model: mp, description: desc, tags: tags, licence: base.Licence}, nil
}

// gatedEval is the eval models.register publishes with: evalID, or the checkpoint's latest gated eval.
func (s *Service) gatedEval(ctx context.Context, q storage.Querier, p projects.Project, checkpointID, evalID string) (Eval, error) {
	if evalID == "" {
		err := q.QueryRow(ctx, `SELECT id FROM evals WHERE project_id = $1 AND subject_id = $2 AND gate IS NOT NULL
			ORDER BY gated_at DESC, id DESC LIMIT 1`, p.ID, checkpointID).Scan(&evalID)
		if errors.Is(err, pgx.ErrNoRows) {
			return Eval{}, problems.GateNotPassed.New("checkpoint %s has no gated eval; run evals.new on it, then evals.gate", checkpointID)
		}
		if err != nil {
			return Eval{}, fmt.Errorf("find the gated eval of %s: %w", checkpointID, err)
		}
	}
	e, err := Get(ctx, q, evalID)
	if err != nil {
		return Eval{}, err
	}
	switch {
	case e.ProjectID != p.ID || e.Subject.ID != checkpointID:
		return Eval{}, problems.Validation([]problems.FieldError{{Path: "/evalId", Message: fmt.Sprintf("%s is not an eval of checkpoint %s", evalID, checkpointID)}})
	case len(e.Gate) == 0 || string(e.Gate) == "null":
		return Eval{}, problems.GateNotPassed.New("eval %s was not gated; run evals.gate on it", evalID)
	}
	return e, nil
}

func failedChecks(v Verdict) string {
	var out []string
	for _, c := range v.Checks {
		if c.State != CheckPassed {
			s := c.Kind + " " + c.State
			if c.GoldenSet != "" {
				s += " on " + c.GoldenSet
			}
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "no check passed"
	}
	return strings.Join(out, "; ")
}

// Register publishes a planned registration in tx: a frozen model version (registered once per content), adopted by
// the project. The registry emits model.registered on entity.model.{id}.
func (s *Service) Register(ctx context.Context, tx pgx.Tx, r Registration, actor auth.Actor) (registry.Version, []events.Draft, error) {
	b, err := json.Marshal(r.Model)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("encode model payload: %w", err)
	}
	v, _, drafts, err := registry.Register(ctx, tx, registry.RegisterInput{
		Kind: registry.KindModel, Name: r.Name, Description: r.description, Tags: r.tags, Licence: r.licence, Payload: b,
		Actor: actor, Freeze: true,
	}, time.Now())
	if err != nil {
		return registry.Version{}, nil, err
	}
	if _, err := registry.AdoptQuietly(ctx, tx, r.Model.ProjectID, []string{v.ID}, actor); err != nil {
		return registry.Version{}, nil, err
	}
	return v, drafts, nil
}

// CardInput is what a model card is written from.
type CardInput struct {
	Name       string
	Project    projects.Project
	Checkpoint runs.Checkpoint
	Run        runs.View
	Base       registry.Version
	Eval       View
	Verdict    Verdict
	Datasets   []registry.Version
	MixHours   float64
	// CharLanguages are eval.character_error_languages: their golden sets are reported on CER.
	CharLanguages []string
}

// Card writes the model card (Markdown): what the model is, the gate it passed, its eval cells, composition, lineage
// and its departures from defaults.
func Card(in CardInput) string {
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format, a...) }
	step := ""
	if in.Checkpoint.Step != nil {
		step = fmt.Sprintf(", step %d", *in.Checkpoint.Step)
	}
	w("# %s\n\n", in.Name)
	w("Checkpoint `%s` (run `%s`%s) of project **%s**, fine-tuned from %s %s (model family %s); gated %s.\n\n",
		in.Checkpoint.ID, in.Run.ID, step, in.Project.Name, in.Base.Name, in.Base.Version, in.Run.Family["name"], in.Verdict.At.UTC().Format("2006-01-02"))
	w("## Gate\n\n")
	sha := in.Verdict.GatesSHA
	if sha == "" {
		sha = "the defaults (no gates.yaml)"
	} else {
		sha = "`gates.yaml` at " + shortSHA(sha)
	}
	w("Verdict **%s** on eval `%s` against the baseline %s, with %s.\n\n", in.Verdict.Verdict, in.Eval.ID, in.Eval.Baseline.Label, sha)
	// A golden set of a language written without spaces is scored on CER: its rows say so (the metric column).
	chars := map[string]bool{}
	names := map[string]string{}
	for _, g := range in.Eval.GoldenSets {
		names[g.VersionID] = g.Name
		chars[g.VersionID] = CharScored(g.Locale, in.CharLanguages)
	}
	metric := func(gsID string) string {
		if chars[gsID] {
			return "CER"
		}
		return "WER"
	}
	rate := func(sm Summary, gsID string) float64 {
		if chars[gsID] {
			return sm.CER
		}
		return sm.WER
	}
	w("| Check | Golden set | Profile | Metric | Delta | Interval | State |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, c := range in.Verdict.Checks {
		delta, ci, m := "—", "—", "WER"
		if c.Unit == UnitChar {
			m = "CER"
		}
		if c.Delta != nil {
			delta, ci = fmt.Sprintf("%+.4f", c.Delta.Value), fmt.Sprintf("[%+.4f, %+.4f]", c.Delta.Low, c.Delta.High)
		}
		w("| %s | %s | %s | %s | %s | %s | %s |\n", c.Kind, or(c.GoldenSet, "—"), or(c.Profile, "—"), m, delta, ci, c.State)
	}
	w("\n## Eval cells\n\n")
	baseWER := map[string]float64{}
	for _, c := range in.Eval.Cells {
		if c.Role == RoleBaseline {
			var sm Summary
			if json.Unmarshal(c.Summary, &sm) == nil {
				baseWER[fmt.Sprintf("%s|%s|%d|%d", c.GoldenSetVersionID, c.Profile, c.DecodingIndex, c.AugmentationIndex)] = rate(sm, c.GoldenSetVersionID)
			}
		}
	}
	w("| Golden set | Profile | Decoding | Metric | Rate | Baseline | Delta [interval] |\n| --- | --- | --- | --- | --- | --- | --- |\n")
	for _, c := range in.Eval.Cells {
		if c.Role != RoleSubject {
			continue
		}
		var sm Summary
		_ = json.Unmarshal(c.Summary, &sm)
		dec := "none"
		if c.DecodingIndex < len(in.Eval.Decoding) {
			dec = in.Eval.Decoding[c.DecodingIndex].Boost
		}
		if c.AugmentationIndex > 0 && c.AugmentationIndex < len(in.Eval.Augmentations) {
			dec += " · augmented " + in.Eval.Augmentations[c.AugmentationIndex].Name
		}
		delta := "—"
		var d Delta
		if len(c.Delta) > 0 && json.Unmarshal(c.Delta, &d) == nil && d.Error == "" {
			delta = fmt.Sprintf("%+.4f [%+.4f, %+.4f]", d.WER.Value, d.WER.Low, d.WER.High)
		}
		bw := "—"
		if v, ok := baseWER[fmt.Sprintf("%s|%s|%d|%d", c.GoldenSetVersionID, c.Profile, c.DecodingIndex, c.AugmentationIndex)]; ok {
			bw = fmt.Sprintf("%.4f", v)
		}
		w("| %s | %s | %s | %s | %.4f | %s | %s |\n", or(names[c.GoldenSetVersionID], c.GoldenSetVersionID), c.Profile, dec,
			metric(c.GoldenSetVersionID), rate(sm, c.GoldenSetVersionID), bw, delta)
	}
	w("\n## Composition\n\n")
	w("Mix **%s** revision %d (`%s`), %.3g hours:\n\n", in.Run.Mix.Name, in.Run.Mix.Revision, shortSHA(in.Run.Mix.Hash), in.MixHours)
	if len(in.Datasets) == 0 {
		w("- (no dataset versions recorded)\n")
	}
	for _, d := range in.Datasets {
		w("- %s %s (`%s`)\n", d.Name, d.Version, d.ID)
	}
	w("\n## Lineage\n\n")
	w("- Run `%s`: recipe `%s` at %s, %d steps, precision %s\n", in.Run.ID, in.Run.Recipe.Pipeline, shortSHA(in.Run.Recipe.Commit),
		in.Run.Steps, in.Run.Precision)
	if in.Run.ParentRunID != "" {
		w("- Parent run `%s`\n", in.Run.ParentRunID)
	}
	w("- Base model %s %s (`%s`)\n", in.Base.Name, in.Base.Version, in.Base.ID)
	w("- Checkpoint artifact `%s`, weights `%s`\n", in.Checkpoint.Artifact, or(in.Checkpoint.WeightsHash, "—"))
	w("\n## Departures from defaults\n\n")
	if len(in.Run.Departures) == 0 {
		w("None: every parameter of the run took its default.\n")
	}
	for _, d := range in.Run.Departures {
		w("- %s.%s = %v (default %v)\n", d.Step, d.Param, d.Value, d.Default)
	}
	return b.String()
}

func shortSHA(s string) string {
	s = strings.TrimPrefix(s, "b3:")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
