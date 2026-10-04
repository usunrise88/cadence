// Package triage is the queue of disputed pseudo-labels (docs/review/2026-10-03-phase-4-plan.md "Decisions taken for
// phase 4" 6): a segment whose pseudo-label members disagree, whose language identification disagrees with the
// source's language, or in which no member heard speech gets origin pseudo-label:disputed in the segments artifact the
// ensemble step writes, and one open triage item here. Disputed segments never reach training; a person resolves
// them (the Triage panel and triage.accept|correct|reject arrive with annotation).
//
// The output hook on segments artifacts indexes the disputed rows in the transaction that marks the step done. Only
// the ensemble's outputs are read (their meta counts disputed segments): a step after it (text_normalise,
// manifest_filter with drop_origins []) keeps the disputed rows under a new segments hash and is not indexed again. A
// segment gets no second item while one for it is open in the project, or from the same pipeline run; a reused output
// indexes nothing twice. It reads only the neutral segments format (cadence.segments/1), never a member's identity
// beyond the labels the rows carry.
package triage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// TypeSegments is the artifact type of a segment manifest (cadence.segments/1, a directory with segments.jsonl).
const TypeSegments = "segments"

// SegmentsFile is the file of a segments artifact that holds one JSON row per segment.
const SegmentsFile = "segments.jsonl"

// OriginDisputed marks a segment whose pseudo-label is disputed.
const OriginDisputed = "pseudo-label:disputed"

// Topic and event type of new items (docs/spec/06-platform.md "Topic scheme").
const (
	Topic          = "triage.new"
	EventItemAdded = "triage.item_added"
)

// Item states and reasons (the contract's TriageState, TriageReason).
const (
	StateOpen = "open"
)

var reasons = []string{"disagreement", "lid-mismatch", "lid-unknown", "no-speech", "too-few-members"}

// maxLine bounds one segments row (the candidates of every member included).
const maxLine = 1 << 20

// Candidate is one member's text for a disputed segment.
type Candidate struct {
	Member     string   `json:"member"`
	Text       string   `json:"text"`
	MeanWER    *float64 `json:"meanWer,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Language   string   `json:"language,omitempty"`
}

// LID is the language identification verdict of a segment.
type LID struct {
	Language   string   `json:"language,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
	Agrees     *bool    `json:"agrees,omitempty"`
}

// Segment is where the disputed audio is.
type Segment struct {
	Hash     string   `json:"hash"`
	URI      string   `json:"uri,omitempty"`
	Start    *float64 `json:"start,omitempty"`
	End      *float64 `json:"end,omitempty"`
	Channel  *int     `json:"channel,omitempty"`
	Role     string   `json:"role,omitempty"`
	Language string   `json:"language,omitempty"`
	Speaker  string   `json:"speaker,omitempty"`
}

// Item is one triage item.
type Item struct {
	ID            string      `json:"id"`
	ProjectID     string      `json:"projectId"`
	State         string      `json:"state"`
	Reason        string      `json:"reason"`
	PipelineRunID string      `json:"pipelineRunId"`
	StepID        string      `json:"stepId"`
	SegmentsHash  string      `json:"segmentsHash"`
	Segment       Segment     `json:"segment"`
	Candidates    []Candidate `json:"candidates"`
	Best          string      `json:"best"`
	LID           *LID        `json:"lid,omitempty"`
	Confidence    float64     `json:"confidence"`
	Rev           int         `json:"rev"`
	// Resolution is how a person resolved the item (triage.accept|correct|reject, phase 4 · stream A).
	Resolution json.RawMessage `json:"resolution,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// row is a disputed segments row as the ensemble writes it: the segment's fields plus its dispute.
type row struct {
	Segment
	Origin     string   `json:"origin"`
	Text       string   `json:"text"`
	Confidence *float64 `json:"confidence"`
	Dispute    *struct {
		Reason     string      `json:"reason"`
		Candidates []Candidate `json:"candidates"`
		LID        *LID        `json:"lid"`
	} `json:"dispute"`
}

// Hook indexes the disputed rows of segments artifacts.
type Hook struct {
	CAS *cas.Store
}

// Register installs the hook on segments outputs.
func (h *Hook) Register(hooks *steps.Hooks) { hooks.On(TypeSegments, h.onSegments) }

func (h *Hook) onSegments(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	if out.ProjectID == "" || h.CAS == nil {
		return nil, nil
	}
	var meta struct {
		Disputed *int `json:"disputed"`
	}
	if len(out.Artifact.Meta) == 0 || json.Unmarshal(out.Artifact.Meta, &meta) != nil || meta.Disputed == nil || *meta.Disputed == 0 {
		// Not the ensemble's output (it counts disputed segments in its meta), or nothing is disputed: a later step
		// that carries the disputed rows on is not indexed again.
		return nil, nil
	}
	rows, err := h.disputed(out.Artifact.Hash)
	if err != nil {
		return nil, err
	}
	added := 0
	for _, r := range rows {
		ok, err := insert(ctx, tx, out, r)
		if err != nil {
			return nil, err
		}
		if ok {
			added++
		}
	}
	if added == 0 {
		return nil, nil
	}
	return []events.Draft{{
		Topic: Topic, Type: EventItemAdded, ProjectID: out.ProjectID,
		Payload: map[string]any{"pipelineRunId": out.PipelineRunID, "stepId": out.StepID,
			"segmentsHash": out.Artifact.Hash, "added": added},
	}}, nil
}

// disputed reads the disputed rows of a segments artifact.
func (h *Hook) disputed(hash string) ([]row, error) {
	m, err := h.CAS.ReadManifest(hash)
	if err != nil {
		return nil, fmt.Errorf("segments artifact %s: %w", hash, err)
	}
	i := slices.IndexFunc(m.Files, func(f cas.File) bool { return f.Path == SegmentsFile })
	if i < 0 {
		return nil, fmt.Errorf("segments artifact %s has no %s", hash, SegmentsFile)
	}
	f, err := h.CAS.Open(m.Files[i].Hash)
	if err != nil {
		return nil, fmt.Errorf("segments artifact %s: %w", hash, err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLine)
	var out []row
	for n := 1; sc.Scan(); n++ {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r row
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("segments artifact %s: line %d: %w", hash, n, err)
		}
		if r.Origin != OriginDisputed {
			continue
		}
		if r.Hash == "" || !steps.ValidHash(r.Hash) {
			return nil, fmt.Errorf("segments artifact %s: line %d: a disputed segment needs its b3 hash", hash, n)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("segments artifact %s: %w", hash, err)
	}
	return out, nil
}

func insert(ctx context.Context, tx pgx.Tx, out steps.Output, r row) (bool, error) {
	reason, candidates, best := "disagreement", []Candidate{}, r.Text
	var lid *LID
	if r.Dispute != nil {
		if slices.Contains(reasons, r.Dispute.Reason) {
			reason = r.Dispute.Reason
		}
		if r.Dispute.Candidates != nil {
			candidates = r.Dispute.Candidates
		}
		lid = r.Dispute.LID
	}
	conf := 0.0
	if r.Confidence != nil {
		conf = min(1, max(0, *r.Confidence))
	}
	tag, err := tx.Exec(ctx, `INSERT INTO triage_items (id, project_id, reason, pipeline_run_id, step_id, segments_hash,
		segment_hash, segment, candidates, best, lid, confidence)
		SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		WHERE NOT EXISTS (SELECT 1 FROM triage_items WHERE project_id = $2 AND segment_hash = $7
			AND (state = 'open' OR pipeline_run_id = $4))
		ON CONFLICT (project_id, segments_hash, segment_hash) DO NOTHING`,
		"tri_"+uuid.Must(uuid.NewV7()).String(), out.ProjectID, reason, out.PipelineRunID, out.StepID, out.Artifact.Hash,
		r.Hash, r.Segment, candidates, best, lid, conf)
	if err != nil {
		return false, fmt.Errorf("insert triage item: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// Filter narrows List; zero fields do not filter.
type Filter struct {
	State         string
	Reason        string
	PipelineRunID string
	Limit         int // 100 when zero
}

const itemCols = `id, project_id, state, reason, pipeline_run_id, step_id, segments_hash, segment, candidates, best, lid,
	confidence, rev, created_at, resolution`

func scanItem(r pgx.CollectableRow) (Item, error) {
	var it Item
	err := r.Scan(&it.ID, &it.ProjectID, &it.State, &it.Reason, &it.PipelineRunID, &it.StepID, &it.SegmentsHash,
		&it.Segment, &it.Candidates, &it.Best, &it.LID, &it.Confidence, &it.Rev, &it.CreatedAt, &it.Resolution)
	if it.Candidates == nil {
		it.Candidates = []Candidate{}
	}
	return it, err
}

// List returns the project's triage items, newest first.
func List(ctx context.Context, q storage.Querier, projectID string, f Filter) ([]Item, error) {
	if f.Limit <= 0 {
		f.Limit = 100
	}
	rows, err := q.Query(ctx, `SELECT `+itemCols+` FROM triage_items
		WHERE project_id = $1 AND ($2 = '' OR state = $2) AND ($3 = '' OR reason = $3) AND ($4 = '' OR pipeline_run_id = $4)
		ORDER BY created_at DESC, id DESC LIMIT $5`, projectID, f.State, f.Reason, f.PipelineRunID, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("query triage items: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanItem)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("read triage items: %w", err)
	}
	return out, nil
}

// Get returns triage item id (phase 4 · stream A: the resolutions).
func Get(ctx context.Context, q storage.Querier, id string) (Item, error) {
	rows, err := q.Query(ctx, `SELECT `+itemCols+` FROM triage_items WHERE id = $1`, id)
	if err != nil {
		return Item{}, fmt.Errorf("query triage item %s: %w", id, err)
	}
	it, err := pgx.CollectExactlyOneRow(rows, scanItem)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, problems.NotFound.New("no triage item %q", id)
	}
	if err != nil {
		return Item{}, fmt.Errorf("read triage item %s: %w", id, err)
	}
	return it, nil
}
