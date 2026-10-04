// Package annotation is the annotation workflow (docs/spec/04-blocks.md "Annotation workflow",
// docs/review/2026-10-03-phase-4-plan.md stream A, decision 10, R27): a batch samples segments of one role from a
// frame (a dataset version's segments artifact), stratified by campaign, month, duration and confidence; people
// annotate them — the admin, and reviewers invited to the batch — blind, each item once, a share of them and every
// flagged one twice; disagreements go to adjudication; the inter-annotator WER over the double items decides whether a
// golden-set batch may freeze. Freezing (an approval) writes the accepted items as a draft dataset version, cuts it
// into the content store (dataset_freeze, mode cut) and, for a golden-set batch, freezes the result through
// goldenSets.freeze with the guidelines commit and the agreement in its card.
//
// It also resolves the triage queue's items (triage.accept|correct|reject): a person's decision on a disputed
// pseudo-label becomes the segment's human transcript.
package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity kinds (topics entity.annotation_batch.{id}).
const (
	EntityKind = "annotation_batch"
	ItemKind   = "annotation_item"
)

// Purposes, states and statuses (the contract's BatchPurpose, BatchState, BatchItemState, AnnotationStatus).
const (
	PurposeGolden   = "golden-set"
	PurposeTraining = "training"

	StateOpen     = "open"
	StateFreezing = "freezing"
	StateFrozen   = "frozen"
	StateFailed   = "failed"

	ItemPending     = "pending"
	ItemAgreed      = "agreed"
	ItemDisputed    = "disputed"
	ItemAdjudicated = "adjudicated"
	ItemExcluded    = "excluded"

	StatusDone    = "done"
	StatusSkipped = "skipped"
	StatusFlagged = "flagged"
)

// Tags an annotator may set; an item whose final tags hold one of Excluding is left out of the freeze.
var (
	Tags      = []string{"noise", "crosstalk", "foreign", "unintelligible"}
	Excluding = []string{"foreign", "unintelligible"}
)

// GuidelinesDir is where a project's annotation guidelines live (R27).
const GuidelinesDir = "annotation/guidelines"

// Repo reads the project repository (the guidelines a batch pins).
type Repo interface {
	ReadFile(ctx context.Context, slug, ref, path string) ([]byte, string, error)
}

// Service is the annotation workflow.
type Service struct {
	Pool     *pgxpool.Pool
	CAS      *cas.Store
	Repo     Repo // nil: guidelines cannot be pinned (batches.new fails)
	Defaults func() *defaults.Defaults
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) d() *defaults.Defaults {
	if s.Defaults != nil {
		return s.Defaults()
	}
	return defaults.Get()
}

// Guidelines is the guidelines file a batch pins.
type Guidelines struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Commit string `json:"commit"`
}

// FrameRef is where a batch was sampled from.
type FrameRef struct {
	DatasetVersionID string `json:"datasetVersionId,omitempty"`
	SegmentsHash     string `json:"segmentsHash"`
	Source           string `json:"source"`
	Segments         int    `json:"segments"`
}

// Progress counts a batch's items by state.
type Progress struct {
	Items       int `json:"items"`
	Pending     int `json:"pending"`
	Agreed      int `json:"agreed"`
	Disputed    int `json:"disputed"`
	Adjudicated int `json:"adjudicated"`
	Excluded    int `json:"excluded"`
	Annotations int `json:"annotations"`
	DoubleItems int `json:"doubleItems"`
	DoubleDone  int `json:"doubleDone"`
}

// Agreement is the inter-annotator agreement of a batch.
type Agreement struct {
	Pairs    int      `json:"pairs"`
	RefWords int      `json:"refWords"`
	Edits    int      `json:"edits"`
	IAAWER   *float64 `json:"iaaWer,omitempty"`
	Target   float64  `json:"target"`
	Meets    bool     `json:"meets"`
}

// EOUStats summarises the end-of-utterance gaps of a batch's items.
type EOUStats struct {
	Items    int      `json:"items"`
	P50GapS  *float64 `json:"p50GapS,omitempty"`
	P90GapS  *float64 `json:"p90GapS,omitempty"`
	Overlaps int      `json:"overlaps"`
}

// Reviewer is a person who annotates or adjudicates a batch.
type Reviewer struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Role        string     `json:"role"`
	Annotations int        `json:"annotations"`
	ExpiresAt   *time.Time `json:"expiresAt,omitempty"`
}

// FreezeState is how a batch's freeze went.
type FreezeState struct {
	ApprovalID         string      `json:"approvalId,omitempty"`
	PipelineRunID      string      `json:"pipelineRunId,omitempty"`
	DatasetVersionID   string      `json:"datasetVersionId,omitempty"`
	GoldenSetVersionID string      `json:"goldenSetVersionId,omitempty"`
	IAAWER             *float64    `json:"iaaWer,omitempty"`
	Pairs              int         `json:"pairs,omitempty"`
	Items              int         `json:"items,omitempty"`
	Adjudicated        int         `json:"adjudicated,omitempty"`
	Error              string      `json:"error,omitempty"`
	Actor              *auth.Actor `json:"actor,omitempty"`
	StartedAt          *time.Time  `json:"startedAt,omitempty"`
	FrozenAt           *time.Time  `json:"frozenAt,omitempty"`
}

// CanFreeze says whether a batch can freeze now and, if not, why.
type CanFreeze struct {
	OK      bool     `json:"ok"`
	Reasons []string `json:"reasons"`
}

// Batch is an annotation batch (the contract's Batch).
type Batch struct {
	ID           string     `json:"id"`
	ProjectID    string     `json:"projectId"`
	Name         string     `json:"name"`
	Description  string     `json:"description,omitempty"`
	Purpose      string     `json:"purpose"`
	State        string     `json:"state"`
	Rev          int        `json:"rev"`
	Role         string     `json:"role"`
	Stratify     []string   `json:"stratify"`
	DoubleShare  float64    `json:"doubleShare"`
	Seed         int64      `json:"seed"`
	ContextS     float64    `json:"contextS"`
	DueAt        *time.Time `json:"dueAt,omitempty"`
	Guidelines   Guidelines `json:"guidelines"`
	Frame        FrameRef   `json:"frame"`
	Strata       []Stratum  `json:"strata"`
	Progress     Progress   `json:"progress"`
	Agreement    Agreement  `json:"agreement"`
	Adjudication struct {
		Queue int `json:"queue"`
	} `json:"adjudication"`
	EOU       EOUStats     `json:"eou"`
	Reviewers []Reviewer   `json:"reviewers"`
	GoldenSet string       `json:"goldenSet"`
	Freeze    *FreezeState `json:"freeze,omitempty"`
	CanFreeze CanFreeze    `json:"canFreeze"`
	Sample    []Item       `json:"sample,omitempty"`
	CreatedBy auth.Actor   `json:"createdBy"`
	CreatedAt time.Time    `json:"createdAt"`
	UpdatedAt time.Time    `json:"updatedAt"`
}

// Topic is a batch's entity topic.
func Topic(id string) string { return events.EntityTopic(EntityKind, id) }

func batchEvent(b Batch, typ string, payload map[string]any) events.Draft {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["batchId"], payload["state"] = b.ID, b.State
	return events.Draft{Topic: Topic(b.ID), Type: typ, ProjectID: b.ProjectID,
		Entity: &events.EntityRef{Kind: EntityKind, ID: b.ID, Rev: b.Rev}, Payload: payload}
}

const batchCols = `id, project_id, name, description, purpose, state, rev, role, stratify, double_share, seed, context_s,
	due_at, guidelines, frame, strata, golden_set, freeze_state, created_by, created_at, updated_at`

func scanBatch(row pgx.CollectableRow) (Batch, error) {
	var b Batch
	err := row.Scan(&b.ID, &b.ProjectID, &b.Name, &b.Description, &b.Purpose, &b.State, &b.Rev, &b.Role, &b.Stratify,
		&b.DoubleShare, &b.Seed, &b.ContextS, &b.DueAt, &b.Guidelines, &b.Frame, &b.Strata, &b.GoldenSet, &b.Freeze,
		&b.CreatedBy, &b.CreatedAt, &b.UpdatedAt)
	if b.Strata == nil {
		b.Strata = []Stratum{}
	}
	if b.Stratify == nil {
		b.Stratify = []string{}
	}
	b.Reviewers = []Reviewer{}
	b.CanFreeze.Reasons = []string{}
	return b, err
}

// ProjectOf returns the project of batch id (not-found when there is none).
func ProjectOf(ctx context.Context, q storage.Querier, id string) (string, error) {
	var p string
	err := q.QueryRow(ctx, "SELECT project_id FROM annotation_batches WHERE id = $1", id).Scan(&p)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", problems.NotFound.New("no annotation batch %q", id)
	}
	if err != nil {
		return "", fmt.Errorf("read batch %s: %w", id, err)
	}
	return p, nil
}

func getBatch(ctx context.Context, q storage.Querier, id string, lock bool) (Batch, error) {
	sql := "SELECT " + batchCols + " FROM annotation_batches WHERE id = $1"
	if lock {
		sql += " FOR UPDATE"
	}
	rows, err := q.Query(ctx, sql, id)
	if err != nil {
		return Batch{}, fmt.Errorf("read batch %s: %w", id, err)
	}
	b, err := pgx.CollectExactlyOneRow(rows, scanBatch)
	if errors.Is(err, pgx.ErrNoRows) {
		return Batch{}, problems.NotFound.New("no annotation batch %q", id)
	}
	if err != nil {
		return Batch{}, fmt.Errorf("read batch %s: %w", id, err)
	}
	return b, nil
}

// Get returns batch id with its progress, agreement, adjudication queue, end-of-utterance gaps, reviewers and
// whether it can freeze.
func (s *Service) Get(ctx context.Context, q storage.Querier, id string) (Batch, error) {
	b, err := getBatch(ctx, q, id, false)
	if err != nil {
		return Batch{}, err
	}
	return s.complete(ctx, q, b)
}

// List returns the project's batches, newest first.
func (s *Service) List(ctx context.Context, q storage.Querier, projectID, state string, limit int) ([]Batch, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.Query(ctx, "SELECT "+batchCols+` FROM annotation_batches WHERE project_id = $1 AND ($2 = '' OR state = $2)
		ORDER BY created_at DESC, id DESC LIMIT $3`, projectID, state, limit)
	if err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanBatch)
	if err != nil {
		return nil, fmt.Errorf("list batches: %w", err)
	}
	for i := range list {
		if list[i], err = s.complete(ctx, q, list[i]); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// complete fills what a batch derives from its items: progress, agreement, queue, gaps, reviewers, canFreeze; a
// freeze whose cut failed shows as failed.
func (s *Service) complete(ctx context.Context, q storage.Querier, b Batch) (Batch, error) {
	items, err := loadItems(ctx, q, b.ID, "")
	if err != nil {
		return Batch{}, err
	}
	anns, err := loadAnnotations(ctx, q, b.ID)
	if err != nil {
		return Batch{}, err
	}
	d := s.d()
	b.Progress, b.Agreement, b.EOU = summarise(items, anns, d.Annotation.MaxIAAWER.Value)
	b.Adjudication.Queue = b.Progress.Disputed
	if b.Reviewers, err = reviewers(ctx, q, b, anns); err != nil {
		return Batch{}, err
	}
	if b.State == StateFreezing && b.Freeze != nil && b.Freeze.PipelineRunID != "" {
		var st string
		err := q.QueryRow(ctx, "SELECT state FROM pipeline_runs WHERE id = $1", b.Freeze.PipelineRunID).Scan(&st)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Batch{}, fmt.Errorf("read the freeze pipeline run: %w", err)
		}
		if st == "failed" || st == "cancelled" {
			b.State = StateFailed
			b.Freeze.Error = fmt.Sprintf("the cut (pipeline run %s) %s; batches.freeze again to retry", b.Freeze.PipelineRunID, st)
		}
	}
	b.CanFreeze = canFreeze(b, d)
	return b, nil
}

// summarise counts items and computes the agreement over the double items' first two transcripts.
func summarise(items []Item, anns map[string][]Annotation, target float64) (Progress, Agreement, EOUStats) {
	var p Progress
	a := Agreement{Target: target}
	var gaps []float64
	e := EOUStats{}
	for _, it := range items {
		p.Items++
		switch it.State {
		case ItemPending:
			p.Pending++
		case ItemAgreed:
			p.Agreed++
		case ItemDisputed:
			p.Disputed++
		case ItemAdjudicated:
			p.Adjudicated++
		case ItemExcluded:
			p.Excluded++
		}
		list := anns[it.ID]
		p.Annotations += len(list)
		tr := transcripts(list)
		if it.Double || hasFlag(list) {
			p.DoubleItems++
			if len(tr) >= 2 {
				p.DoubleDone++
			}
		}
		if len(tr) >= 2 {
			ref, hyp := Words(tr[0].Text), Words(tr[1].Text)
			a.Pairs++
			a.RefWords += len(ref)
			a.Edits += Edits(ref, hyp)
		}
		if it.EOU != nil && it.EOU.GapS != nil {
			gaps = append(gaps, *it.EOU.GapS)
			if *it.EOU.GapS < 0 {
				e.Overlaps++
			}
		}
	}
	if a.Pairs > 0 {
		w := 0.0
		if a.RefWords > 0 {
			w = float64(a.Edits) / float64(a.RefWords)
		} else if a.Edits > 0 {
			w = 1
		}
		w = math.Round(w*1e4) / 1e4
		a.IAAWER = &w
		a.Meets = w <= target
	}
	if len(gaps) > 0 {
		sort.Float64s(gaps)
		p50, p90 := quantile(gaps, 0.5), quantile(gaps, 0.9)
		e.Items, e.P50GapS, e.P90GapS = len(gaps), &p50, &p90
	}
	return p, a, e
}

func quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := min(lo+1, len(sorted)-1)
	v := sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
	return math.Round(v*1000) / 1000
}

// canFreeze lists why b cannot freeze now (an open or failed batch with every item resolved and, for a golden set,
// the agreement target met).
func canFreeze(b Batch, d *defaults.Defaults) CanFreeze {
	var r []string
	switch b.State {
	case StateFreezing:
		r = append(r, "the batch is freezing")
	case StateFrozen:
		r = append(r, "the batch is frozen")
	}
	if b.Progress.Pending > 0 {
		r = append(r, fmt.Sprintf("%d item(s) still need annotations", b.Progress.Pending))
	}
	if b.Progress.Disputed > 0 {
		r = append(r, fmt.Sprintf("%d item(s) wait for adjudication", b.Progress.Disputed))
	}
	if b.Progress.Agreed+b.Progress.Adjudicated == 0 {
		r = append(r, "no item is accepted yet")
	}
	if b.Purpose == PurposeGolden {
		switch {
		case b.Agreement.Pairs == 0:
			r = append(r, "no item has two transcripts yet: the inter-annotator WER is unknown")
		case !b.Agreement.Meets:
			r = append(r, fmt.Sprintf("the inter-annotator WER %.2f %% is above the %.2f %% target (annotation.max_iaa_wer)",
				*b.Agreement.IAAWER*100, d.Annotation.MaxIAAWER.Value*100))
		}
	}
	if r == nil {
		r = []string{}
	}
	return CanFreeze{OK: len(r) == 0, Reasons: r}
}

func reviewers(ctx context.Context, q storage.Querier, b Batch, anns map[string][]Annotation) ([]Reviewer, error) {
	count := map[string]int{}
	names := map[string]string{}
	for _, list := range anns {
		for _, a := range list {
			count[a.AnnotatorID]++
			names[a.AnnotatorID] = a.Annotator.Name
		}
	}
	rows, err := q.Query(ctx, `SELECT u.id, u.name, coalesce(c.scope->>'batchRole', ''), c.expires_at FROM credentials c
		JOIN users u ON u.id = c.user_id
		WHERE c.kind = 'invitation' AND c.subject = $1 AND c.revoked_at IS NULL ORDER BY c.created_at`, b.ID)
	if err != nil {
		return nil, fmt.Errorf("read reviewers: %w", err)
	}
	out := []Reviewer{}
	seen := map[string]bool{}
	var (
		id, name, role string
		exp            *time.Time
	)
	_, err = pgx.ForEachRow(rows, []any{&id, &name, &role, &exp}, func() error {
		if seen[id] {
			return nil
		}
		seen[id] = true
		out = append(out, Reviewer{ID: id, Name: name, Role: role, Annotations: count[id], ExpiresAt: exp})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read reviewers: %w", err)
	}
	ids := make([]string, 0, len(count))
	for id := range count {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		out = append(out, Reviewer{ID: id, Name: names[id], Role: "admin", Annotations: count[id]})
	}
	return out, nil
}

func marshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func trimmed(s string) string { return strings.TrimSpace(s) }
