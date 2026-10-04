package annotation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cache"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/credentials"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/goldensets"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Freezing a batch (batches.freeze, an approval): the accepted items become a segments artifact (their rows with the
// final transcript, origin human, the entity spans) and a draft dataset version registered from it through the
// dataset importer — the same draft pipelines/data-ingest ends in — whose cut (dataset_freeze, mode cut) the pipeline
// engine runs at once. When the cut lands, OnDataset freezes a golden-set batch's dataset version as a golden set
// (goldenSets.freeze's path) with the guidelines commit and the agreement in its card.

// FreezeStepKind is the step kind that cuts a draft (mode cut), named as pipelines/data-ingest names it.
const (
	FreezeStepKind    = "dataset_freeze"
	FreezeStepVersion = "1"
)

// FreezeResult is batches.freeze's answer (the contract's BatchFreeze).
type FreezeResult struct {
	Batch            Batch    `json:"batch"`
	Items            int      `json:"items"`
	Excluded         int      `json:"excluded"`
	Hours            float64  `json:"hours"`
	IAAWER           *float64 `json:"iaaWer,omitempty"`
	PipelineRunID    string   `json:"pipelineRunId,omitempty"`
	DatasetVersionID string   `json:"datasetVersionId,omitempty"`
	JobID            string   `json:"-"`
}

// FreezeInput is a batches.freeze request.
type FreezeInput struct {
	BatchID    string
	Rev        int
	Actor      auth.Actor
	ApprovalID string
	DryRun     bool
}

// accepted are the items that freeze: agreed or adjudicated, with a transcript, not tagged foreign or unintelligible.
func accepted(items []Item) []Item {
	var out []Item
	for _, it := range items {
		if (it.State == ItemAgreed || it.State == ItemAdjudicated) && it.Final != nil && strings.TrimSpace(it.Final.Text) != "" &&
			!excluding(it.Final.Tags) {
			out = append(out, it)
		}
	}
	return out
}

// CheckFreeze reads batch id and answers why it cannot freeze, as a problem; nil when it can.
func (s *Service) CheckFreeze(ctx context.Context, q storage.Querier, id string, rev int) (Batch, error) {
	b, err := getBatch(ctx, q, id, false)
	if err != nil {
		return Batch{}, err
	}
	if rev > 0 {
		if err := commands.CheckRev(EntityKind, rev, b.Rev); err != nil {
			return Batch{}, err
		}
	}
	if b, err = s.complete(ctx, q, b); err != nil {
		return Batch{}, err
	}
	return b, freezeProblem(b, s.d().Annotation.MaxIAAWER.Value)
}

func freezeProblem(b Batch, target float64) error {
	switch {
	case b.State == StateFreezing || b.State == StateFrozen:
		return problems.BatchClosed.New("batch %s is %s already", b.Name, b.State)
	case b.Progress.Pending > 0 || b.Progress.Disputed > 0:
		return problems.BatchIncomplete.New("batch %s has %d item(s) that still need annotations and %d waiting for adjudication; finish them (or exclude them with batchItems.accept) before freezing",
			b.Name, b.Progress.Pending, b.Progress.Disputed)
	case b.Progress.Agreed+b.Progress.Adjudicated == 0:
		return problems.BatchIncomplete.New("batch %s has no accepted item", b.Name)
	case b.Purpose == PurposeGolden && b.Agreement.Pairs == 0:
		return problems.AnnotationAgreementLow.New("batch %s has no item with two transcripts, so its inter-annotator WER is unknown; a golden set needs double annotation (doubleShare)", b.Name)
	case b.Purpose == PurposeGolden && !b.Agreement.Meets:
		return problems.AnnotationAgreementLow.New("batch %s: the inter-annotator WER is %.2f %% over %d double item(s), above the %.2f %% target (annotation.max_iaa_wer); re-annotate against the guidelines or tighten them, then adjudicate",
			b.Name, *b.Agreement.IAAWER*100, b.Agreement.Pairs, target*100)
	}
	return nil
}

// Freeze freezes batch in.BatchID: dry run, the plan; for real (the approved replay), the segments and draft
// artifacts, the draft dataset version, the cut's pipeline run, the batch freezing, and its reviewers' access revoked.
func (s *Service) Freeze(ctx context.Context, tx pgx.Tx, eng *pipelines.Engine, in FreezeInput) (FreezeResult, []events.Draft, error) {
	b, err := getBatch(ctx, tx, in.BatchID, true)
	if err != nil {
		return FreezeResult{}, nil, err
	}
	if err := commands.CheckRev(EntityKind, in.Rev, b.Rev); err != nil {
		return FreezeResult{}, nil, err
	}
	if b, err = s.complete(ctx, tx, b); err != nil {
		return FreezeResult{}, nil, err
	}
	if err := freezeProblem(b, s.d().Annotation.MaxIAAWER.Value); err != nil {
		return FreezeResult{}, nil, err
	}
	items, err := loadItems(ctx, tx, b.ID, "")
	if err != nil {
		return FreezeResult{}, nil, err
	}
	acc := accepted(items)
	res := FreezeResult{Batch: b, Items: len(acc), Excluded: len(items) - len(acc), IAAWER: b.Agreement.IAAWER}
	for _, it := range acc {
		res.Hours += it.Segment.Duration / 3600
	}
	res.Hours = math.Round(res.Hours*1e4) / 1e4
	if in.DryRun {
		return res, nil, nil
	}
	if s.CAS == nil || eng == nil {
		return FreezeResult{}, nil, problems.NotImplemented.New("this control plane has no content store or pipeline engine to freeze with")
	}
	fr, err := ReadFrame(s.CAS, b.Frame.SegmentsHash)
	if err != nil {
		return FreezeResult{}, nil, problems.BadRequest.New("the batch's frame cannot be read: %v", err)
	}
	segRef, rows, err := s.writeSegments(ctx, tx, b, fr, acc)
	if err != nil {
		return FreezeResult{}, nil, err
	}
	draftRef, params, err := s.writeDraft(ctx, tx, b, fr, rows)
	if err != nil {
		return FreezeResult{}, nil, err
	}
	pj, err := json.Marshal(params)
	if err != nil {
		return FreezeResult{}, nil, fmt.Errorf("encode the freeze parameters: %w", err)
	}
	im := &data.Importer{CAS: s.CAS, Now: s.Now}
	v, drafts, err := im.Import(ctx, tx, steps.Output{ProjectID: b.ProjectID, Name: data.ArtifactType, Artifact: draftRef,
		Spec: steps.Spec{ProjectID: b.ProjectID, Kind: FreezeStepKind, KindVersion: FreezeStepVersion, Params: pj,
			Inputs: map[string]steps.ArtifactRef{SegmentsType: segRef}}})
	if err != nil {
		return FreezeResult{}, nil, err
	}
	res.DatasetVersionID = v.ID
	now := s.now()
	actor := in.Actor
	fz := &FreezeState{ApprovalID: in.ApprovalID, DatasetVersionID: v.ID, IAAWER: b.Agreement.IAAWER, Pairs: b.Agreement.Pairs,
		Items: len(acc), Adjudicated: b.Progress.Adjudicated, Actor: &actor, StartedAt: &now}
	if err := setFreeze(ctx, tx, b.ID, StateFreezing, fz); err != nil {
		return FreezeResult{}, nil, err
	}
	plan, err := data.PlanFreeze(ctx, tx, v.ID)
	if err != nil {
		return FreezeResult{}, nil, err
	}
	if !plan.Frozen && plan.Running == "" {
		adding, err := data.CutBytes(ctx, tx, v.ID)
		if err != nil {
			return FreezeResult{}, nil, err
		}
		if err := cache.CheckQuota(ctx, tx, s.d(), b.ProjectID, adding); err != nil {
			return FreezeResult{}, nil, err
		}
		r, more, err := eng.Start(ctx, tx, pipelines.StartInput{
			ProjectID: b.ProjectID, Actor: actor,
			Pipeline: &pipelines.Pipeline{Name: "batch-freeze", Description: "Freeze annotation batch " + b.Name + ": cut its accepted items into the content store",
				Inputs: map[string]string{SegmentsType: SegmentsType},
				Steps: []pipelines.Step{{ID: "freeze", Kind: plan.Kind, In: map[string]string{SegmentsType: pipelines.InputsRef + SegmentsType},
					Params: plan.Params}}},
			Inputs: map[string]steps.ArtifactRef{SegmentsType: plan.Segments},
		})
		if err != nil {
			return FreezeResult{}, nil, err
		}
		drafts = append(drafts, more...)
		if err := data.MarkFreezing(ctx, tx, v.ID, r.ID, actor, now); err != nil {
			return FreezeResult{}, nil, err
		}
		res.PipelineRunID = r.ID
		for _, st := range r.Steps {
			if st.JobID != "" && st.State == pipelines.StepQueued {
				res.JobID = st.JobID
			}
		}
	} else {
		res.PipelineRunID = plan.Running
	}
	if _, err := tx.Exec(ctx, `UPDATE annotation_batches SET freeze_state = jsonb_set(freeze_state, '{pipelineRunId}', to_jsonb($2::text))
		WHERE id = $1 AND state = 'freezing'`, b.ID, res.PipelineRunID); err != nil {
		return FreezeResult{}, nil, fmt.Errorf("record the freeze run: %w", err)
	}
	if plan.Frozen { // the same draft was cut before (a retry after a refused golden set): finish now
		fb, err := getBatch(ctx, tx, b.ID, true)
		if err != nil {
			return FreezeResult{}, nil, err
		}
		if fb.State == StateFreezing {
			fv, err := registry.GetVersion(ctx, tx, registry.KindDataset, v.ID)
			if err != nil {
				return FreezeResult{}, nil, err
			}
			more, err := s.finish(ctx, tx, fb, fv)
			if err != nil {
				return FreezeResult{}, nil, err
			}
			drafts = append(drafts, more...)
		}
	}
	if _, err := credentials.RevokeBatch(ctx, tx, b.ID); err != nil {
		return FreezeResult{}, nil, err
	}
	if res.Batch, err = s.Get(ctx, tx, b.ID); err != nil {
		return FreezeResult{}, nil, err
	}
	drafts = append(drafts, batchEvent(res.Batch, "annotation_batch.freezing", map[string]any{
		"datasetVersionId": v.ID, "pipelineRunId": res.PipelineRunID, "items": len(acc)}))
	return res, drafts, nil
}

func setFreeze(ctx context.Context, tx pgx.Tx, id, state string, fz *FreezeState) error {
	if _, err := tx.Exec(ctx, `UPDATE annotation_batches SET state = $2, freeze_state = $3, rev = rev + 1, updated_at = now() WHERE id = $1`,
		id, state, fz); err != nil {
		return fmt.Errorf("update batch %s: %w", id, err)
	}
	return nil
}

func putFile(store *cas.Store, path string, b []byte) (cas.File, error) {
	h, err := store.PutBytes(b)
	if err != nil {
		return cas.File{}, fmt.Errorf("store %s: %w", path, err)
	}
	return cas.File{Path: path, Hash: h, Size: int64(len(b))}, nil
}

func jsonLines(rows []map[string]any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}

func recordDir(ctx context.Context, tx pgx.Tx, store *cas.Store, typ, projectID string, files []cas.File, meta map[string]any) (steps.ArtifactRef, error) {
	h, err := store.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		return steps.ArtifactRef{}, fmt.Errorf("store the %s artifact: %w", typ, err)
	}
	size := int64(0)
	for _, f := range files {
		size += f.Size
	}
	ref := steps.ArtifactRef{Hash: h, Type: typ, Size: size, Meta: marshal(meta)}
	if _, err := artifacts.Record(ctx, tx, store, ref, projectID, nil); err != nil {
		return steps.ArtifactRef{}, err
	}
	return ref, nil
}

// split is the split the freeze gives an item: test for a golden set, the row's own (else train) for training data.
func (b Batch) split(r Row) string {
	if b.Purpose == PurposeGolden {
		return "test"
	}
	if s := r.str("split"); slices.Contains(data.Splits, s) {
		return s
	}
	return "train"
}

// writeSegments writes the accepted items as a segments artifact: each row as sampled, with the final transcript
// (origin human), its tags, entity spans, end-of-utterance gap and the batch item it came from.
func (s *Service) writeSegments(ctx context.Context, tx pgx.Tx, b Batch, fr Frame, acc []Item) (steps.ArtifactRef, []Row, error) {
	lang, _ := fr.Header["language"].(string)
	rows := make([]Row, 0, len(acc))
	hours := 0.0
	for _, it := range acc {
		r := Row{}
		for k, v := range it.Row {
			r[k] = v
		}
		for _, k := range []string{"confidence", "dispute", "hypotheses"} {
			delete(r, k)
		}
		r["text"], r["origin"], r["split"] = it.Final.Text, "human", b.split(it.Row)
		r["tags"], r["entities"] = it.Final.Tags, it.Final.Entities
		if r.str("language") == "" {
			r["language"] = lang
		}
		if r.str("language") == "" {
			return steps.ArtifactRef{}, nil, problems.BadRequest.New("item %d has no language and the frame names none; ingest with a language", it.Position)
		}
		if it.EOU != nil {
			r["eou"] = it.EOU
		}
		r["annotation"] = map[string]any{"batchId": b.ID, "itemId": it.ID}
		rows = append(rows, r)
		hours += it.Segment.Duration / 3600
	}
	header := map[string]any{}
	for k, v := range fr.Header {
		header[k] = v
	}
	stepsDone, _ := header["steps"].([]any)
	header["format"], header["counts"], header["hours"] = SegmentsFormat, map[string]int{"segments": len(rows)}, math.Round(hours*1e6)/1e6
	header["steps"] = append(stepsDone, "batches.freeze")
	header["annotation"] = map[string]any{"batchId": b.ID, "guidelines": b.Guidelines, "iaaWer": b.Agreement.IAAWER}
	hb, err := json.Marshal(header)
	if err != nil {
		return steps.ArtifactRef{}, nil, fmt.Errorf("encode segments.json: %w", err)
	}
	plain := make([]map[string]any, len(rows))
	for i, r := range rows {
		plain[i] = r
	}
	lb, err := jsonLines(plain)
	if err != nil {
		return steps.ArtifactRef{}, nil, fmt.Errorf("encode segments.jsonl: %w", err)
	}
	var files []cas.File
	for _, f := range []struct {
		p string
		b []byte
	}{{SegmentsHeader, hb}, {SegmentsRows, lb}} {
		cf, err := putFile(s.CAS, f.p, f.b)
		if err != nil {
			return steps.ArtifactRef{}, nil, err
		}
		files = append(files, cf)
	}
	ref, err := recordDir(ctx, tx, s.CAS, SegmentsType, b.ProjectID, files, map[string]any{
		"format": SegmentsFormat, "segments": len(rows), "batchId": b.ID, "disputed": 0})
	return ref, rows, err
}

// writeDraft writes the draft dataset artifact (cadence.dataset-draft/1) of the rows and the parameters its cut runs
// with.
func (s *Service) writeDraft(ctx context.Context, tx pgx.Tx, b Batch, fr Frame, rows []Row) (steps.ArtifactRef, map[string]any, error) {
	name := b.GoldenSet + "-annotated"
	evalOnly := b.Purpose == PurposeGolden
	tags := []string{"annotated"}
	if b.Purpose == PurposeGolden {
		tags = append(tags, data.TagEvalOnly)
	}
	if tg, ok := fr.Header["tags"].([]any); ok {
		for _, t := range tg {
			if ts, ok := t.(string); ok && !slices.Contains(tags, ts) {
				tags = append(tags, ts)
			}
		}
	}
	desc := fmt.Sprintf("Annotated in batch %s (%s, guidelines %s at %.12s)", b.Name, b.ID, b.Guidelines.Path, b.Guidelines.Commit)
	counts := map[string]int{"train": 0, "validation": 0, "test": 0}
	hours := 0.0
	lines := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		dur, ok := r.num("duration")
		if !ok {
			a, e := r.span()
			dur = e - a
		}
		size, _ := r.num("bytes")
		if size <= 0 {
			size = math.Round(dur*16000)*2 + 44
		}
		sp := r.str("split")
		counts[sp]++
		hours += dur / 3600
		l := map[string]any{"uri": r.str("uri"), "hash": r.str("hash"), "bytes": int64(size), "duration": dur, "sampleRate": 16000,
			"channels": 1, "language": r.str("language"), "text": r.str("text"), "origin": "human", "split": sp, "role": r.str("role")}
		if sk := r.str("speaker"); sk != "" {
			l["speaker"] = sk
		}
		lines = append(lines, l)
	}
	rule := "all-test"
	if b.Purpose == PurposeTraining {
		rule = "all-train"
		if counts["validation"]+counts["test"] > 0 {
			rule = "source"
		}
	}
	src := fr.Source()
	header := map[string]any{"format": data.FormatDraft, "name": name, "description": desc, "source": map[string]string{"name": src},
		"splitRule": rule, "counts": counts, "hours": math.Round(hours*1e6) / 1e6, "tags": tags, "evalOnly": evalOnly,
		"card": "card.md", "steps": []string{"batches.freeze"}}
	hb, err := json.Marshal(header)
	if err != nil {
		return steps.ArtifactRef{}, nil, fmt.Errorf("encode dataset.json: %w", err)
	}
	lb, err := jsonLines(lines)
	if err != nil {
		return steps.ArtifactRef{}, nil, fmt.Errorf("encode manifest.jsonl: %w", err)
	}
	var files []cas.File
	for _, f := range []struct {
		p string
		b []byte
	}{{data.HeaderFile, hb}, {data.ManifestFile, lb}, {"card.md", []byte(card(b, len(rows), hours))}} {
		cf, err := putFile(s.CAS, f.p, f.b)
		if err != nil {
			return steps.ArtifactRef{}, nil, err
		}
		files = append(files, cf)
	}
	ref, err := recordDir(ctx, tx, s.CAS, data.ArtifactType, b.ProjectID, files, map[string]any{"format": data.FormatDraft, "batchId": b.ID})
	params := map[string]any{"mode": "draft", "name": name, "description": desc, "tags": tags, "eval_only": evalOnly}
	return ref, params, err
}

// card is the draft's dataset card: where the transcripts came from, under which guidelines, with what agreement.
func card(b Batch, items int, hours float64) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s — annotated\n\n", b.GoldenSet)
	fmt.Fprintf(&sb, "Human transcripts of %d segment(s) (%.3f h) of role %s, annotated in batch **%s** (%s), purpose %s.\n\n",
		items, hours, b.Role, b.Name, b.ID, b.Purpose)
	fmt.Fprintf(&sb, "- Guidelines: `%s` at commit `%s`\n", b.Guidelines.Path, b.Guidelines.Commit)
	fmt.Fprintf(&sb, "- Frame: segments `%s`", b.Frame.SegmentsHash)
	if b.Frame.DatasetVersionID != "" {
		fmt.Fprintf(&sb, " (dataset version %s)", b.Frame.DatasetVersionID)
	}
	fmt.Fprintf(&sb, ", source %s; sample stratified by %s, seed %d\n", b.Frame.Source, strings.Join(b.Stratify, ", "), b.Seed)
	if b.Agreement.IAAWER != nil {
		fmt.Fprintf(&sb, "- Inter-annotator WER: %.2f %% over %d double-annotated item(s) (target ≤ %.2f %%)\n",
			*b.Agreement.IAAWER*100, b.Agreement.Pairs, b.Agreement.Target*100)
	} else {
		sb.WriteString("- Inter-annotator WER: not measured (single annotation)\n")
	}
	fmt.Fprintf(&sb, "- Items: %d agreed, %d adjudicated, %d excluded\n", b.Progress.Agreed, b.Progress.Adjudicated, b.Progress.Excluded)
	if b.EOU.P50GapS != nil {
		fmt.Fprintf(&sb, "- End of utterance (target speech end → other party's next speech): p50 %.2f s, p90 %.2f s over %d item(s)\n",
			*b.EOU.P50GapS, *b.EOU.P90GapS, b.EOU.Items)
	}
	return sb.String()
}

// Install registers OnDataset on dataset outputs; call it after the dataset importer is registered, so a cut's
// draft is frozen when OnDataset reads it.
func (s *Service) Install(hooks *steps.Hooks) { hooks.On(data.ArtifactType, s.OnDataset) }

// OnDataset completes a batch whose cut just landed: a training batch is frozen; a golden-set batch's dataset version
// is frozen as a golden set (goldenSets.freeze's path, under the batch's approval) — a refusal (leakage, normalizer)
// marks the batch failed with the reason instead of failing the cut.
func (s *Service) OnDataset(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	var p struct {
		Mode         string `json:"mode"`
		DraftVersion string `json:"draft_version"`
	}
	if out.Spec.Kind != FreezeStepKind || json.Unmarshal(out.Spec.Params, &p) != nil || p.Mode != "cut" || p.DraftVersion == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, "SELECT "+batchCols+` FROM annotation_batches WHERE state = 'freezing'
		AND freeze_state->>'datasetVersionId' = $1 FOR UPDATE`, p.DraftVersion)
	if err != nil {
		return nil, fmt.Errorf("find the batch of %s: %w", p.DraftVersion, err)
	}
	b, err := pgx.CollectExactlyOneRow(rows, scanBatch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find the batch of %s: %w", p.DraftVersion, err)
	}
	v, err := registry.GetVersion(ctx, tx, registry.KindDataset, p.DraftVersion)
	if err != nil {
		return nil, err
	}
	if v.State != registry.StateFrozen {
		return nil, nil // the importer refused or has not frozen it; the batch stays freezing and shows the run's failure
	}
	return s.finish(ctx, tx, b, v)
}

// finish completes a freezing batch whose dataset version v is frozen.
func (s *Service) finish(ctx context.Context, tx pgx.Tx, b Batch, v registry.Version) ([]events.Draft, error) {
	fz := b.Freeze
	if fz == nil {
		fz = &FreezeState{}
	}
	now := s.now()
	if b.Purpose == PurposeTraining {
		fz.FrozenAt = &now
		if err := setFreeze(ctx, tx, b.ID, StateFrozen, fz); err != nil {
			return nil, err
		}
		b.State, b.Rev = StateFrozen, b.Rev+1
		return []events.Draft{batchEvent(b, "annotation_batch.frozen", map[string]any{"datasetVersionId": v.ID})}, nil
	}
	actor := auth.Actor{Kind: auth.KindAutomation, ID: b.ID, Name: "annotation batch " + b.Name}
	if fz.Actor != nil {
		actor = *fz.Actor
	}
	ann := &goldensets.Annotation{BatchID: b.ID, IAAWER: fz.IAAWER, Pairs: fz.Pairs, Items: fz.Items, Adjudicated: fz.Adjudicated}
	ann.Guidelines.Name, ann.Guidelines.Path, ann.Guidelines.Commit = b.Guidelines.Name, b.Guidelines.Path, b.Guidelines.Commit
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("savepoint: %w", err)
	}
	gv, _, drafts, ferr := goldensets.Freeze(ctx, sp, goldensets.FreezeInput{Dataset: v.ID, Name: b.GoldenSet, Actor: actor,
		ApprovalID: fz.ApprovalID, Annotation: ann}, s.d(), now)
	if ferr != nil {
		if err := sp.Rollback(ctx); err != nil {
			return nil, fmt.Errorf("roll back the golden set: %w", err)
		}
		fz.Error = "the golden set was not frozen: " + problemText(ferr)
		if err := setFreeze(ctx, tx, b.ID, StateFailed, fz); err != nil {
			return nil, err
		}
		b.State, b.Rev = StateFailed, b.Rev+1
		return []events.Draft{batchEvent(b, "annotation_batch.failed", map[string]any{"datasetVersionId": v.ID, "error": fz.Error})}, nil
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, fmt.Errorf("release savepoint: %w", err)
	}
	fz.GoldenSetVersionID, fz.FrozenAt, fz.Error = gv.ID, &now, ""
	if err := setFreeze(ctx, tx, b.ID, StateFrozen, fz); err != nil {
		return nil, err
	}
	b.State, b.Rev = StateFrozen, b.Rev+1
	return append(drafts, batchEvent(b, "annotation_batch.frozen", map[string]any{"datasetVersionId": v.ID, "goldenSetVersionId": gv.ID})), nil
}

func problemText(err error) string {
	if pe, ok := problems.As(err); ok {
		return pe.Detail
	}
	return err.Error()
}
