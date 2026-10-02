package evals

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/projects"
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
	Message            string    `json:"message"`
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
	v := s.verdict(e, p.Locales, gf, cells, recs)
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

// verdict evaluates the checks at the primary profile and decoding variant 0 (the decoding the project deploys).
func (s *Service) verdict(e Eval, locales []string, gf GateFile, cells []Cell, recs map[string]Record) Verdict {
	g := gf.Gate
	v := Verdict{GatesSHA: gf.Commit, Config: g.Config(), Checks: []Check{}}
	if !gf.Exists {
		v.GatesSHA = ""
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
			if c.Role == role && c.GoldenSetVersionID == gsID && c.Profile == g.PrimaryProfile && c.DecodingIndex == 0 {
				return c, true
			}
		}
		return Cell{}, false
	}
	wer := func(c Cell) *float64 {
		var sm Summary
		if r, ok := recs[c.RecordID]; ok && json.Unmarshal(r.Summary, &sm) == nil {
			w := sm.WER
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
		base := Check{GoldenSetVersionID: gs.VersionID, GoldenSet: gs.Name, Profile: g.PrimaryProfile}
		sc, ok1 := at(gs.VersionID, RoleSubject)
		bc, ok2 := at(gs.VersionID, RoleBaseline)
		var d *Delta
		if ok1 && ok2 {
			base.CandidateWER, base.BaselineWER = wer(sc), wer(bc)
			d = s.deltaAt(sc, cells, recs, e.Significance, g.Significance)
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
				c.State, c.Message = CheckPassed, fmt.Sprintf("WER fell by %.4f, the whole %.0f%% interval below zero", -d.WER.Value, d.Level*100)
			case d.WER.Low > 0:
				c.State, c.Message = CheckFailed, fmt.Sprintf("WER rose by %.4f, the whole interval above zero", d.WER.Value)
			default:
				c.State, c.Message = CheckInconclusive, fmt.Sprintf("WER changed by %+.4f but the interval [%.4f, %.4f] includes zero", d.WER.Value, d.WER.Low, d.WER.High)
			}
			v.Checks = append(v.Checks, c)
			if g.DeletionsInsertions {
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
		c.Message = fmt.Sprintf("WER changed by %+.4f (allowed regression %.4f)", d.WER.Value, mr)
		if d.WER.Value > mr && d.WER.Low > 0 {
			c.State = CheckFailed
			c.Message = fmt.Sprintf("WER rose by %.4f, above the allowed %.4f, the interval excluding zero", d.WER.Value, mr)
		}
		v.Checks = append(v.Checks, c)
	}
	for _, r := range g.TargetGoldenSets {
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
func (s *Service) deltaAt(c Cell, cells []Cell, recs map[string]Record, have, want Significance) *Delta {
	if have == want && len(c.Delta) > 0 && string(c.Delta) != "null" {
		var d Delta
		if json.Unmarshal(c.Delta, &d) == nil {
			return &d
		}
	}
	return s.delta(c, cells, recs, want)
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
