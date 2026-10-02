package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Check states and kinds of a gate verdict (04 "Block 3", the gate).
const (
	CheckPassed       = "passed"
	CheckFailed       = "failed"
	CheckInconclusive = "inconclusive"

	CheckTarget         = "target"
	CheckReplay         = "replay"
	CheckDelIns         = "deletionsInsertions"
	CheckPrimaryProfile = "primaryProfile"
	CheckBaseline       = "baseline"

	VerdictPassed = "passed"
	VerdictFailed = "failed"
)

// Check is one check of a verdict (the contract's EvalGateCheck).
type Check struct {
	Kind               string    `json:"kind"`
	GoldenSetVersionID string    `json:"goldenSetVersionId,omitempty"`
	GoldenSet          string    `json:"goldenSet,omitempty"`
	Profile            string    `json:"profile,omitempty"`
	State              string    `json:"state"`
	Delta              *Interval `json:"delta,omitempty"`
	Threshold          *float64  `json:"threshold,omitempty"`
	BaselineWER        *float64  `json:"baselineWer,omitempty"`
	CandidateWER       *float64  `json:"candidateWer,omitempty"`
	// Unit is "char" when the set is gated on CER: Delta, BaselineWER and CandidateWER then hold character error
	// rates (eval.character_error_languages).
	Unit    string `json:"unit,omitempty"`
	Message string `json:"message"`
}

// Verdict is a gate's answer on an eval (the contract's EvalGate).
type Verdict struct {
	Verdict  string     `json:"verdict"`
	GatesSHA string     `json:"gatesSha"`
	Checks   []Check    `json:"checks"`
	Config   GateConfig `json:"config"`
	At       time.Time  `json:"at"`
	By       auth.Actor `json:"by"`
}

// Gate applies the project's gate (gates.yaml at main's head) to done eval id at revision rev and records the
// verdict with the file's SHA (evals.gate).
func (s *Service) Gate(ctx context.Context, tx pgx.Tx, id string, rev int, actor auth.Actor) (View, []events.Draft, error) {
	e, found, err := getEval(ctx, tx, id, "FOR UPDATE")
	if err != nil {
		return View{}, nil, err
	}
	if !found {
		return View{}, nil, problems.NotFound.New("no eval %q", id)
	}
	if err := commands.CheckRev(Kind, rev, e.Rev); err != nil {
		return View{}, nil, err
	}
	if e.Status != StatusDone {
		return View{}, nil, problems.Conflict.New("eval %s is %s; evals.gate reads a done eval (follow it with evals.get)", id, e.Status)
	}
	p, err := projects.GetByID(ctx, tx, e.ProjectID)
	if err != nil {
		return View{}, nil, err
	}
	d := s.defaults()
	gf, err := ReadGate(ctx, s.repo(), p.Slug, d)
	if err != nil {
		return View{}, nil, err
	}
	if err := CheckAdopted(ctx, tx, p.ID, gf.Gate); err != nil {
		return View{}, nil, err
	}
	cells, err := cellsOf(ctx, tx, e.ID)
	if err != nil {
		return View{}, nil, err
	}
	if !slices.ContainsFunc(cells, func(c Cell) bool { return c.Role == RoleBaseline && c.RecordID != "" }) {
		return View{}, nil, problems.EvalBaselineMissing.New("eval %s has no scored baseline cells to compare with; run evals.new again", id)
	}
	recs, err := recordsOf(ctx, tx, cells)
	if err != nil {
		return View{}, nil, err
	}
	st, err := s.standing(ctx, tx, p, gf.Gate, e)
	if err != nil {
		return View{}, nil, err
	}
	v := s.verdict(e, p.Locales, gf, cells, recs, st)
	v.At, v.By = time.Now(), actor
	if err := tx.QueryRow(ctx, `UPDATE evals SET gate = $2, gated_at = $3, rev = rev + 1, updated_at = now() WHERE id = $1
		RETURNING rev, updated_at`, e.ID, mustJSON(v), v.At).Scan(&e.Rev, &e.UpdatedAt); err != nil {
		return View{}, nil, fmt.Errorf("save the verdict of %s: %w", e.ID, err)
	}
	e.Gate, e.GatedAt = mustJSON(v), &v.At
	view, err := s.View(ctx, tx, e, ViewQuery{})
	if err != nil {
		return View{}, nil, err
	}
	ev := entityDraft(e, EventGated)
	ev.Payload.(map[string]any)["eval"].(map[string]any)["gate"] = map[string]any{"verdict": v.Verdict, "gatesSha": v.GatesSHA}
	return view, []events.Draft{ev}, nil
}

// standing is what a verdict needs beyond the eval: whether the eval's baseline is the project's own, and the
// golden set versions gates.yaml names, resolved to the project's adopted versions.
type standing struct {
	baseline *Check             // a failed baseline check, nil when the baseline is the project's
	required []registry.Version // the target and replay golden sets gates.yaml names (adopted versions)
}

// standing reads the project's baseline (@baseline, and its default base model) and the golden sets gates.yaml
// names. An eval whose baseline the request chose is gated against nothing the project agreed on, and one that
// leaves out a set the gate names (a replay set that would regress) proves nothing about it: both fail.
func (s *Service) standing(ctx context.Context, q storage.Querier, p projects.Project, g Gate, e Eval) (standing, error) {
	var st standing
	var allowed, names []string
	a, err := registry.GetAlias(ctx, q, p.ID, "baseline")
	switch pe, ok := problems.As(err); {
	case err == nil:
		allowed, names = append(allowed, a.Version.ID), append(names, "@baseline ("+a.Version.Name+" "+a.Version.Version+")")
	case ok && pe.Type == problems.NotFound:
	default:
		return standing{}, err
	}
	if p.BaseModel != nil && p.BaseModel.VersionID != "" {
		allowed, names = append(allowed, p.BaseModel.VersionID), append(names, "the default base model ("+p.BaseModel.VersionID+")")
	}
	if !slices.Contains(allowed, e.Baseline.ID) {
		want := "the project has neither @baseline nor a default base model; set @baseline (aliases.set, approval)"
		if len(names) > 0 {
			want = "the gate compares against " + strings.Join(names, " or ") + "; run evals.new without baseline"
		}
		st.baseline = &Check{Kind: CheckBaseline, State: CheckFailed,
			Message: fmt.Sprintf("the eval's baseline %s is not the project's baseline: %s", e.Baseline.Label, want)}
	}
	if named := append(append([]string{}, g.TargetGoldenSets...), g.ReplayGoldenSets...); len(named) > 0 {
		if st.required, err = adoptedGoldenSets(ctx, q, p.ID, named); err != nil {
			return standing{}, err
		}
	}
	return st, nil
}

// verdict evaluates the checks at the primary profile and decoding variant 0 (the decoding the project deploys).
func (s *Service) verdict(e Eval, locales []string, gf GateFile, cells []Cell, recs map[string]Record, st standing) Verdict {
	g := gf.Gate
	v := Verdict{GatesSHA: gf.Commit, Config: g.Config(), Checks: []Check{}}
	if !gf.Exists {
		v.GatesSHA = ""
	}
	if st.baseline != nil {
		v.Checks = append(v.Checks, *st.baseline)
	}
	if !slices.ContainsFunc(e.Profiles, func(p Profile) bool { return p.Name == g.PrimaryProfile }) {
		v.Checks = append(v.Checks, Check{Kind: CheckPrimaryProfile, Profile: g.PrimaryProfile, State: CheckFailed,
			Message: fmt.Sprintf("the eval has no %s cells (it ran %s); run evals.new with that profile, or set primaryProfile in gates.yaml to a profile of the model's family",
				g.PrimaryProfile, profileNames(e.Profiles))})
		v.Verdict = VerdictFailed
		return v
	}
	at := func(gsID, role string) (Cell, bool) {
		for _, c := range cells {
			if c.Role == role && c.GoldenSetVersionID == gsID && c.Profile == g.PrimaryProfile && c.DecodingIndex == 0 && c.AugmentationIndex == 0 {
				return c, true
			}
		}
		return Cell{}, false
	}
	rate := func(c Cell, chars bool) *float64 {
		var sm Summary
		if r, ok := recs[c.RecordID]; ok && json.Unmarshal(r.Summary, &sm) == nil {
			w := sm.WER
			if chars {
				w = sm.CER
			}
			return &w
		}
		return nil
	}
	targets := 0
	for _, gs := range e.GoldenSets {
		target, replay := Target(gs, g, locales), Replay(gs, g, locales)
		if !target && !replay {
			continue
		}
		chars := CharScored(gs.Locale, s.defaults().Eval.CharacterErrorLanguages.Value)
		metric := "WER"
		base := Check{GoldenSetVersionID: gs.VersionID, GoldenSet: gs.Name, Profile: g.PrimaryProfile}
		if chars {
			metric, base.Unit = "CER", UnitChar
		}
		sc, ok1 := at(gs.VersionID, RoleSubject)
		bc, ok2 := at(gs.VersionID, RoleBaseline)
		var d *Delta
		if ok1 && ok2 {
			base.CandidateWER, base.BaselineWER = rate(sc, chars), rate(bc, chars)
			d = s.deltaAt(sc, cells, recs, e.Significance, g.Significance, chars)
		}
		missing := func(kind string) Check {
			c := base
			c.Kind, c.State = kind, CheckInconclusive
			c.Message = "no delta against the baseline at the primary profile"
			if d != nil && d.Error != "" {
				c.Message += ": " + d.Error
			}
			return c
		}
		if target {
			targets++
			if d == nil || d.Error != "" {
				v.Checks = append(v.Checks, missing(CheckTarget))
				continue
			}
			c := base
			c.Kind, c.Delta = CheckTarget, &d.WER
			switch {
			case d.WER.Value < 0 && d.WER.High < 0:
				c.State, c.Message = CheckPassed, fmt.Sprintf("%s fell by %.4f, the whole %.0f%% interval below zero", metric, -d.WER.Value, d.Level*100)
			case d.WER.Low > 0:
				c.State, c.Message = CheckFailed, fmt.Sprintf("%s rose by %.4f, the whole interval above zero", metric, d.WER.Value)
			default:
				c.State, c.Message = CheckInconclusive, fmt.Sprintf("%s changed by %+.4f but the interval [%.4f, %.4f] includes zero", metric, d.WER.Value, d.WER.Low, d.WER.High)
			}
			v.Checks = append(v.Checks, c)
			if g.DeletionsInsertions && chars {
				di := base
				di.Kind, di.State = CheckDelIns, CheckPassed
				di.Message = "not applicable: the set is scored on characters, whose deletions and insertions the scores do not count"
				v.Checks = append(v.Checks, di)
			} else if g.DeletionsInsertions {
				di := base
				di.Kind, di.Delta, di.State = CheckDelIns, &d.Ins, CheckPassed
				di.Message = "deletions were not traded for insertions"
				if d.Del.High < 0 && d.Ins.Low > 0 {
					di.State = CheckFailed
					di.Message = fmt.Sprintf("the deletion rate fell (%+.4f) while the insertion rate rose (%+.4f), both intervals excluding zero", d.Del.Value, d.Ins.Value)
				}
				v.Checks = append(v.Checks, di)
			}
			continue
		}
		if d == nil || d.Error != "" {
			v.Checks = append(v.Checks, missing(CheckReplay))
			continue
		}
		c := base
		mr := g.MaxRegression
		c.Kind, c.Delta, c.Threshold, c.State = CheckReplay, &d.WER, &mr, CheckPassed
		c.Message = fmt.Sprintf("%s changed by %+.4f (allowed regression %.4f)", metric, d.WER.Value, mr)
		if d.WER.Value > mr && d.WER.Low > 0 {
			c.State = CheckFailed
			c.Message = fmt.Sprintf("%s rose by %.4f, above the allowed %.4f, the interval excluding zero", metric, d.WER.Value, mr)
		}
		v.Checks = append(v.Checks, c)
	}
	in := func(id string) bool {
		return slices.ContainsFunc(e.GoldenSets, func(gs GoldenSet) bool { return gs.VersionID == id })
	}
	for _, rv := range st.required {
		if in(rv.ID) {
			continue
		}
		kind := CheckTarget
		if Matches(g.ReplayGoldenSets, rv.ID, rv.Name) && !Matches(g.TargetGoldenSets, rv.ID, rv.Name) {
			kind = CheckReplay
		}
		msg := fmt.Sprintf("the %s golden set %s %s (named in gates.yaml, adopted by the project) is not in this eval; run evals.new with it", kind, rv.Name, rv.Version)
		if i := slices.IndexFunc(e.GoldenSets, func(gs GoldenSet) bool { return gs.Name == rv.Name }); i >= 0 {
			msg = fmt.Sprintf("the eval scored %s %s, not the version the project adopted (%s); run evals.new with it", rv.Name, e.GoldenSets[i].Version, rv.Version)
		}
		v.Checks = append(v.Checks, Check{Kind: kind, GoldenSetVersionID: rv.ID, GoldenSet: rv.Name, Profile: g.PrimaryProfile, State: CheckFailed, Message: msg})
		if kind == CheckTarget {
			targets++
		}
	}
	for _, r := range g.TargetGoldenSets {
		if slices.ContainsFunc(st.required, func(rv registry.Version) bool { return Matches([]string{r}, rv.ID, rv.Name) }) {
			continue // reported above
		}
		if !slices.ContainsFunc(e.GoldenSets, func(gs GoldenSet) bool { return Matches([]string{r}, gs.VersionID, gs.Name) }) {
			v.Checks = append(v.Checks, Check{Kind: CheckTarget, GoldenSet: r, Profile: g.PrimaryProfile, State: CheckFailed,
				Message: fmt.Sprintf("the target golden set %s is not in this eval; run evals.new with it", r)})
			targets++
		}
	}
	if targets == 0 {
		v.Checks = append(v.Checks, Check{Kind: CheckTarget, Profile: g.PrimaryProfile, State: CheckFailed,
			Message: "the eval has no target golden set; name target goldenSets in gates.yaml or evaluate on a golden set of the project's locales"})
	}
	v.Verdict = VerdictPassed
	for _, c := range v.Checks {
		if c.State != CheckPassed {
			v.Verdict = VerdictFailed
		}
	}
	return v
}

// deltaAt is subject cell c's delta at significance want: the stored one when the eval computed it at want.
func (s *Service) deltaAt(c Cell, cells []Cell, recs map[string]Record, have, want Significance, chars bool) *Delta {
	if have == want && len(c.Delta) > 0 && string(c.Delta) != "null" {
		var d Delta
		if json.Unmarshal(c.Delta, &d) == nil && (d.Unit == UnitChar) == chars {
			return &d
		}
	}
	return s.delta(c, cells, recs, want, chars)
}

func profileNames(ps []Profile) string {
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		names = append(names, p.Name)
	}
	if len(names) == 0 {
		return "none"
	}
	return fmt.Sprint(names)
}
