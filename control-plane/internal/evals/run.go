package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Create starts a planned eval in tx: the eval and its cells (cached cells linked to their records), then — when a
// cell is missing — the generated pipeline run with the eval's id as its RunID, so the scores hook and the observer
// find the eval while the engine starts it (a reused score step finishes inside Start). An eval whose every cell was
// cached is done at once.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, pl Plan) (Eval, []events.Draft, error) {
	e := Eval{
		ID: "evl_" + uuid.Must(uuid.NewV7()).String(), ProjectID: pl.project.ID, Status: StatusQueued, Subject: pl.Subject,
		Baseline: pl.Baseline, GoldenSets: pl.GoldenSets, Profiles: pl.Profiles, PrimaryProfile: pl.PrimaryProfile,
		Decoding: pl.Decoding, Augmentations: pl.Augmentations, Significance: pl.Significance, Estimate: pl.Estimate, Actor: pl.in.Actor,
	}
	if err := insertEval(ctx, tx, e); err != nil {
		return Eval{}, nil, err
	}
	for i, c := range pl.Cells {
		row := Cell{ID: "evc_" + uuid.Must(uuid.NewV7()).String(), EvalID: e.ID, Position: i, Role: c.Role,
			GoldenSetVersionID: c.GoldenSetVersionID, NormalizerVersionID: c.normalizer, Profile: c.Profile, DecodingIndex: c.DecodingIndex,
			DecodingHash: c.DecodingHash, ModelKey: c.ModelKey, Scorer: c.scorer, State: CellQueued, RecordID: c.RecordID, ScoreStep: c.scoreStep,
			AugmentationIndex: c.AugmentationIndex, Metrics: c.metrics}
		if c.Cached {
			row.State = CellCached
		}
		if err := insertCell(ctx, tx, row); err != nil {
			return Eval{}, nil, err
		}
	}
	var drafts []events.Draft
	if pl.start != nil {
		start := *pl.start
		start.RunID = e.ID
		pr, more, err := s.Engine.Start(ctx, tx, start)
		if err != nil {
			return Eval{}, nil, err
		}
		if _, err := tx.Exec(ctx, "UPDATE evals SET pipeline_run_id = $2 WHERE id = $1", e.ID, pr.ID); err != nil {
			return Eval{}, nil, fmt.Errorf("link eval %s to its pipeline run: %w", e.ID, err)
		}
		drafts = append(drafts, more...)
	} else {
		cur, _, err := getEval(ctx, tx, e.ID, "FOR UPDATE")
		if err != nil {
			return Eval{}, nil, err
		}
		more, err := s.finish(ctx, tx, &cur)
		if err != nil {
			return Eval{}, nil, err
		}
		drafts = append(drafts, more...)
	}
	cur, _, err := getEval(ctx, tx, e.ID, "")
	if err != nil {
		return Eval{}, nil, err
	}
	done, total, err := counts(ctx, tx, cur.ID)
	if err != nil {
		return Eval{}, nil, err
	}
	return cur, append([]events.Draft{entityDraft(cur, EventCreated), progressDraft(cur, done, total)}, drafts...), nil
}

// ---------------------------------------------------------------- the scores hook

// scoresHook records the scores output of an eval's score step as an eval record (idempotent: the key is unique, and a
// record computed meanwhile by another eval is linked instead) and links the eval's cells that wait for that step. A
// scores output outside an eval (a plain pipelines.run) records nothing. A malformed scores artifact fails the step.
func (s *Service) scoresHook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	if !strings.HasPrefix(out.RunID, "evl_") {
		return nil, nil
	}
	e, found, err := getEval(ctx, tx, out.RunID, "FOR UPDATE")
	if err != nil || !found {
		return nil, err
	}
	var step string
	if err := tx.QueryRow(ctx, "SELECT step FROM pipeline_steps WHERE id = $1", out.StepID).Scan(&step); err != nil {
		return nil, fmt.Errorf("read step %s: %w", out.StepID, err)
	}
	cells, err := cellsOf(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	var waiting []Cell
	for _, c := range cells {
		if c.ScoreStep == step {
			waiting = append(waiting, c)
		}
	}
	if len(waiting) == 0 {
		return nil, nil
	}
	summary, sum, err := ReadSummary(s.CAS, out.Artifact.Hash)
	if err != nil {
		return nil, err
	}
	c := waiting[0]
	var hyp string
	for _, ref := range out.Spec.Inputs {
		if ref.Type == TypeHypotheses {
			hyp = ref.Hash
		}
	}
	decoding := map[string]any{"profile": c.Profile}
	for _, gs := range e.GoldenSets {
		if gs.VersionID == c.GoldenSetVersionID && gs.decodeLanguage() != "" {
			decoding["language"] = gs.decodeLanguage() // the language both models decoded in (evals.new languages)
		}
	}
	if c.DecodingIndex < len(e.Decoding) {
		d := e.Decoding[c.DecodingIndex]
		decoding["boost"] = d.Boost
		if d.Boost != "none" {
			decoding["list"], decoding["weight"] = d.Artifact, d.Weight
		}
	}
	var augmentation []byte // NULL: none
	if c.AugmentationIndex > 0 && c.AugmentationIndex < len(e.Augmentations) {
		a := e.Augmentations[c.AugmentationIndex]
		decoding["augmentation"] = map[string]any{"profile": a.Profile, "name": a.Name, "hash": a.Hash, "seed": a.Seed}
		augmentation = mustJSON(map[string]any{"index": a.Index, "profile": a.Profile, "name": a.Name, "hash": a.Hash, "seed": a.Seed})
	}
	dec := json.RawMessage(mustJSON(decoding))
	var family string
	for _, m := range []Model{e.Subject, e.Baseline} {
		if m.ModelKey == c.ModelKey {
			family = m.Family
		}
	}
	if sum.Scorer != "" && sum.Scorer != c.Scorer {
		return nil, fmt.Errorf("the scores of step %s say scorer %s, the cell expects %s", step, sum.Scorer, c.Scorer)
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO eval_records (id, model_key, golden_set_version_id, normalizer_version_id, decoding_hash, scorer,
			profile, decoding, scores_hash, hypotheses_hash, summary, family, project_id, eval_id, pipeline_run_id, step_id, augmentation)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULLIF($10, ''), $11, $12, $13, $14, $15, $16, $17)
		ON CONFLICT (model_key, golden_set_version_id, normalizer_version_id, decoding_hash, scorer) DO NOTHING RETURNING id`,
		"erc_"+uuid.Must(uuid.NewV7()).String(), c.ModelKey, c.GoldenSetVersionID, c.NormalizerVersionID, c.DecodingHash, c.Scorer,
		c.Profile, dec, out.Artifact.Hash, hyp, summary, family, e.ProjectID, e.ID, out.PipelineRunID, out.StepID, augmentation).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		rec, found, ferr := findRecord(ctx, tx, Key{c.ModelKey, c.GoldenSetVersionID, c.NormalizerVersionID, c.DecodingHash, c.Scorer})
		if ferr != nil {
			return nil, ferr
		}
		if !found {
			return nil, fmt.Errorf("eval record of step %s vanished", step)
		}
		id, err = rec.ID, nil
	}
	if err != nil {
		return nil, fmt.Errorf("insert eval record: %w", err)
	}
	tag, err := tx.Exec(ctx, `UPDATE eval_cells SET record_id = $3, state = 'done' WHERE eval_id = $1 AND score_step = $2 AND record_id IS NULL`,
		e.ID, step, id)
	if err != nil {
		return nil, fmt.Errorf("link eval cells: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, nil
	}
	done, total, err := counts(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	return []events.Draft{progressDraft(e, done, total)}, nil
}

// ---------------------------------------------------------------- the observer

// Observe mirrors a change of an eval's pipeline run onto the eval (pipelines.RunObserver): done finishes the eval
// (deltas, status done); failed or cancelled fails it with the step's error; otherwise it is running once a step was
// leased or finished, queued before. A retried pipeline run reopens a failed eval.
func (s *Service) Observe(ctx context.Context, tx pgx.Tx, r pipelines.Run) ([]events.Draft, error) {
	if !strings.HasPrefix(r.RunID, "evl_") {
		return nil, nil
	}
	e, found, err := getEval(ctx, tx, r.RunID, "FOR UPDATE")
	if err != nil || !found || e.Status == StatusDone {
		return nil, err
	}
	switch r.State {
	case pipelines.RunDone:
		return s.finish(ctx, tx, &e)
	case pipelines.RunFailed, pipelines.RunCancelled:
		msg := r.Error
		if r.State == pipelines.RunCancelled {
			msg = "cancelled: " + r.Error
		}
		if e.Status == StatusFailed && e.Error == msg {
			return nil, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE eval_cells SET state = 'failed' WHERE eval_id = $1 AND record_id IS NULL`, e.ID); err != nil {
			return nil, fmt.Errorf("fail eval cells: %w", err)
		}
		now := time.Now()
		e.Status, e.Error, e.FinishedAt = StatusFailed, msg, &now
		return s.changed(ctx, tx, &e)
	}
	full, err := pipelines.Get(ctx, tx, r.ID)
	if err != nil {
		return nil, err
	}
	status := StatusQueued
	for _, st := range full.Steps {
		if st.State == pipelines.StepRunning || st.State == pipelines.StepDone || st.State == pipelines.StepReused {
			status = StatusRunning
		}
	}
	if e.Status == StatusFailed { // a retry reopened the pipeline run
		if _, err := tx.Exec(ctx, `UPDATE eval_cells SET state = 'queued' WHERE eval_id = $1 AND record_id IS NULL`, e.ID); err != nil {
			return nil, fmt.Errorf("reopen eval cells: %w", err)
		}
	}
	if status == e.Status {
		return nil, nil
	}
	e.Status, e.Error, e.FinishedAt = status, "", nil
	return s.changed(ctx, tx, &e)
}

func (s *Service) changed(ctx context.Context, tx pgx.Tx, e *Eval) ([]events.Draft, error) {
	if err := saveStatus(ctx, tx, e); err != nil {
		return nil, err
	}
	done, total, err := counts(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	return []events.Draft{entityDraft(*e, EventStatus), progressDraft(*e, done, total)}, nil
}

// finish completes an eval whose cells all have scores: each subject cell gets its delta against the baseline's cell
// of the same golden set, profile and decoding, and the eval is done. A cell left without scores fails the eval.
func (s *Service) finish(ctx context.Context, tx pgx.Tx, e *Eval) ([]events.Draft, error) {
	cells, err := cellsOf(ctx, tx, e.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	for _, c := range cells {
		if c.RecordID == "" {
			e.Status, e.Error, e.FinishedAt = StatusFailed, fmt.Sprintf("cell %s (%s, %s) has no scores", c.ID, c.Role, c.Profile), &now
			return s.changed(ctx, tx, e)
		}
	}
	if err := s.computeDeltas(ctx, tx, e, cells); err != nil {
		return nil, err
	}
	if err := failedMetrics(ctx, tx, e, cells); err != nil {
		return nil, err
	}
	e.Status, e.Error, e.FinishedAt = StatusDone, "", &now
	return s.changed(ctx, tx, e)
}

// failedMetrics marks unavailable, with the step's error, each metric whose step failed or was skipped: metric steps
// (and the VAD) are optional in the eval's pipeline, so their failure leaves the WER cells and the gate alone.
func failedMetrics(ctx context.Context, tx pgx.Tx, e *Eval, cells []Cell) error {
	if e.PipelineRunID == "" {
		return nil
	}
	r, err := pipelines.Get(ctx, tx, e.PipelineRunID)
	if err != nil {
		return err
	}
	why := map[string]string{}
	for _, st := range r.Steps {
		switch {
		case st.State == pipelines.StepFailed && st.Error != nil:
			why[st.Step] = fmt.Sprintf("step %s failed (%s): %s", st.Step, st.Error.Type, st.Error.Message)
		case st.State == pipelines.StepFailed:
			why[st.Step] = fmt.Sprintf("step %s failed", st.Step)
		case st.State == pipelines.StepSkipped:
			why[st.Step] = fmt.Sprintf("step %s was skipped: a step it reads (the VAD) failed", st.Step)
		}
	}
	for _, c := range cells {
		changed := false
		for metric, mp := range c.Metrics {
			if reason, ok := why[mp.Step]; ok && mp.Step != "" && mp.Unavailable == "" {
				mp.Unavailable = reason
				c.Metrics[metric], changed = mp, true
			}
		}
		if !changed {
			continue
		}
		if _, err := tx.Exec(ctx, "UPDATE eval_cells SET metrics = $2 WHERE id = $1", c.ID, mustJSON(c.Metrics)); err != nil {
			return fmt.Errorf("mark the metrics of %s unavailable: %w", c.ID, err)
		}
	}
	return nil
}

// computeDeltas stores each subject cell's delta at the eval's significance.
func (s *Service) computeDeltas(ctx context.Context, tx pgx.Tx, e *Eval, cells []Cell) error {
	recs, err := recordsOf(ctx, tx, cells)
	if err != nil {
		return err
	}
	for _, c := range cells {
		if c.Role != RoleSubject {
			continue
		}
		d := s.delta(c, cells, recs, e.Significance, s.charScored(*e, c.GoldenSetVersionID))
		if d == nil {
			continue
		}
		if _, err := tx.Exec(ctx, "UPDATE eval_cells SET delta = $2 WHERE id = $1", c.ID, mustJSON(d)); err != nil {
			return fmt.Errorf("save delta of %s: %w", c.ID, err)
		}
	}
	return nil
}

func recordsOf(ctx context.Context, tx pgx.Tx, cells []Cell) (map[string]Record, error) {
	var ids []string
	for _, c := range cells {
		if c.RecordID != "" {
			ids = append(ids, c.RecordID)
		}
	}
	return recordsByID(ctx, tx, ids)
}

// Delta is a subject cell's comparison with the baseline (the contract's EvalDelta).
type Delta struct {
	BaselineCellID string `json:"baselineCellId"`
	// Unit is "char" when the golden set is scored on CER (eval.character_error_languages): WER then holds the
	// character error rate's delta and Del and Ins are not computed.
	Unit        string   `json:"unit,omitempty"`
	WER         Interval `json:"wer"`
	Del         Interval `json:"del"`
	Ins         Interval `json:"ins"`
	Significant bool     `json:"significant"`
	Groups      int      `json:"groups"`
	Samples     int      `json:"samples"`
	Level       float64  `json:"level"`
	Error       string   `json:"error,omitempty"`
}

// delta compares subject cell c with the baseline's cell at the same place; nil when the eval has none.
func (s *Service) delta(c Cell, cells []Cell, recs map[string]Record, sig Significance, chars bool) *Delta {
	var base *Cell
	for i := range cells {
		if cells[i].Role == RoleBaseline && cells[i].sameCell(c) {
			base = &cells[i]
		}
	}
	if base == nil {
		return nil
	}
	d := &Delta{BaselineCellID: base.ID, Samples: sig.Samples, Level: sig.Level}
	pairFn := Pair
	if chars {
		d.Unit, pairFn = UnitChar, PairChars
	}
	sr, ok1 := recs[c.RecordID]
	br, ok2 := recs[base.RecordID]
	if !ok1 || !ok2 {
		d.Error = "a side has no scores yet"
		return d
	}
	su, err := ReadUtterances(s.CAS, sr.Scores)
	if err == nil {
		var bu []Utterance
		if bu, err = ReadUtterances(s.CAS, br.Scores); err == nil {
			var groups []Group
			if groups, err = pairFn(su, bu); err == nil {
				var cmp Comparison
				if cmp, err = Compare(groups, sig); err == nil {
					d.WER, d.Del, d.Ins, d.Groups = cmp.WER, cmp.Del, cmp.Ins, cmp.Groups
					d.Significant = cmp.WER.Excludes()
				}
			}
		}
	}
	if err != nil {
		d.Error = err.Error()
	}
	return d
}

// UnitChar marks a delta and a check computed on characters.
const UnitChar = "char"

// CharScored reports whether a locale's golden sets are scored on CER: its language (primary subtag) is one of
// langs (eval.character_error_languages).
func CharScored(locale string, langs []string) bool {
	return locale != "" && slices.Contains(langs, language(locale))
}

// charScored reports whether the eval's golden set gsID is scored on CER.
func (s *Service) charScored(e Eval, gsID string) bool {
	for _, g := range e.GoldenSets {
		if g.VersionID == gsID {
			return CharScored(g.Locale, s.defaults().Eval.CharacterErrorLanguages.Value)
		}
	}
	return false
}
