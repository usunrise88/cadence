package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/commands"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// Entity is a span of a transcript (character offsets, end exclusive) entity_score checks in the hypothesis.
type Entity struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Class string `json:"class"`
	Text  string `json:"text,omitempty"`
}

// Annotation is one person's annotation of an item.
type Annotation struct {
	ID          string     `json:"id"`
	ItemID      string     `json:"itemId"`
	Annotator   auth.Actor `json:"annotator"`
	AnnotatorID string     `json:"-"`
	Seq         int        `json:"-"`
	Status      string     `json:"status"`
	Text        string     `json:"text"`
	Tags        []string   `json:"tags"`
	Entities    []Entity   `json:"entities"`
	Note        string     `json:"note,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Segment is the API view of an item's segments row.
type Segment struct {
	Hash       string   `json:"hash"`
	URI        string   `json:"uri"`
	File       string   `json:"file,omitempty"`
	Start      float64  `json:"start"`
	End        float64  `json:"end"`
	Duration   float64  `json:"duration"`
	Channel    int      `json:"channel"`
	Role       string   `json:"role"`
	Language   string   `json:"language,omitempty"`
	Speaker    string   `json:"speaker,omitempty"`
	SourceRate *int     `json:"sourceRate,omitempty"`
	Codec      string   `json:"codec,omitempty"`
	Crosstalk  *float64 `json:"crosstalk,omitempty"`
}

// Window is the audio served under an item's id: the segment plus context, every channel of the source file.
type Window struct {
	File     string   `json:"file"`
	Start    float64  `json:"start"`
	End      float64  `json:"end"`
	Channels int      `json:"channels"`
	Roles    []string `json:"roles,omitempty"`
}

// Prefill is the best hypothesis an annotator starts from.
type Prefill struct {
	Text       string   `json:"text"`
	Origin     string   `json:"origin"`
	Confidence *float64 `json:"confidence,omitempty"`
}

// Turn is the other party's transcribed turn around an item (the bot's TTS script).
type Turn struct {
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Channel int     `json:"channel"`
	Role    string  `json:"role,omitempty"`
	Text    string  `json:"text"`
}

// Context is what an annotator sees around the segment.
type Context struct {
	Turns []Turn `json:"turns"`
}

// EOU is an item's end of utterance: the target's last speech end and the other party's next speech start.
type EOU struct {
	SpeechEnd  float64  `json:"speechEnd"`
	NextSpeech *float64 `json:"nextSpeech,omitempty"`
	GapS       *float64 `json:"gapS,omitempty"`
}

// Final is an item's accepted transcript.
type Final struct {
	Text     string     `json:"text"`
	Tags     []string   `json:"tags"`
	Entities []Entity   `json:"entities"`
	By       auth.Actor `json:"by"`
	At       time.Time  `json:"at"`
	From     string     `json:"from,omitempty"`
}

// Item is a batch item (the contract's BatchItem).
type Item struct {
	ID          string            `json:"id"`
	BatchID     string            `json:"batchId"`
	Position    int               `json:"position"`
	State       string            `json:"state"`
	Rev         int               `json:"rev"`
	Hash        string            `json:"-"`
	Row         Row               `json:"-"`
	Segment     Segment           `json:"segment"`
	Window      Window            `json:"window"`
	Prefill     Prefill           `json:"prefill"`
	Context     Context           `json:"context"`
	Strata      map[string]string `json:"strata"`
	Double      bool              `json:"double"`
	Required    int               `json:"required"`
	EOU         *EOU              `json:"eou,omitempty"`
	Annotations []Annotation      `json:"annotations"`
	WER         *float64          `json:"wer,omitempty"`
	Final       *Final            `json:"final,omitempty"`
}

func segmentOf(r Row) Segment {
	s := Segment{Hash: r.str("hash"), URI: r.str("uri"), File: r.file(), Channel: r.channel(), Role: r.str("role"),
		Language: r.str("language"), Speaker: r.str("speaker"), Codec: r.str("codec")}
	s.Start, s.End = r.span()
	if d, ok := r.num("duration"); ok {
		s.Duration = d
	} else {
		s.Duration = s.End - s.Start
	}
	if v, ok := r.num("sourceRate"); ok {
		n := int(v)
		s.SourceRate = &n
	}
	if v, ok := r.num("crosstalk"); ok {
		s.Crosstalk = &v
	}
	if s.Role == "" {
		s.Role = "mono"
	}
	return s
}

const itemCols = `id, batch_id, position, hash, state, rev, segment, audio_window, prefill, context, strata, double_item, eou, final, wer`

func scanItem(row pgx.CollectableRow) (Item, error) {
	var (
		it  Item
		raw []byte
	)
	err := row.Scan(&it.ID, &it.BatchID, &it.Position, &it.Hash, &it.State, &it.Rev, &raw, &it.Window, &it.Prefill,
		&it.Context, &it.Strata, &it.Double, &it.EOU, &it.Final, &it.WER)
	if err != nil {
		return Item{}, err
	}
	if err := json.Unmarshal(raw, &it.Row); err != nil {
		return Item{}, fmt.Errorf("decode the segment of %s: %w", it.ID, err)
	}
	it.Segment = segmentOf(it.Row)
	if it.Context.Turns == nil {
		it.Context.Turns = []Turn{}
	}
	if it.Strata == nil {
		it.Strata = map[string]string{}
	}
	it.Annotations = []Annotation{}
	it.Required = 1
	if it.Double {
		it.Required = 2
	}
	return it, nil
}

func loadItems(ctx context.Context, q storage.Querier, batchID, state string) ([]Item, error) {
	rows, err := q.Query(ctx, "SELECT "+itemCols+` FROM annotation_items WHERE batch_id = $1 AND ($2 = '' OR state = $2)
		ORDER BY position`, batchID, state)
	if err != nil {
		return nil, fmt.Errorf("read batch items: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanItem)
	if err != nil {
		return nil, fmt.Errorf("read batch items: %w", err)
	}
	return out, nil
}

func getItem(ctx context.Context, q storage.Querier, batchID, id string, lock bool) (Item, error) {
	sql := "SELECT " + itemCols + " FROM annotation_items WHERE id = $1 AND batch_id = $2"
	if lock {
		sql += " FOR UPDATE"
	}
	rows, err := q.Query(ctx, sql, id, batchID)
	if err != nil {
		return Item{}, fmt.Errorf("read item %s: %w", id, err)
	}
	it, err := pgx.CollectExactlyOneRow(rows, scanItem)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, problems.NotFound.New("no item %q in batch %s", id, batchID)
	}
	if err != nil {
		return Item{}, fmt.Errorf("read item %s: %w", id, err)
	}
	return it, nil
}

const annCols = `a.id, a.item_id, a.annotator_id, a.annotator, a.seq, a.status, a.text, a.tags, a.entities, a.note, a.created_at, a.updated_at`

func scanAnnotation(row pgx.CollectableRow) (Annotation, error) {
	var a Annotation
	err := row.Scan(&a.ID, &a.ItemID, &a.AnnotatorID, &a.Annotator, &a.Seq, &a.Status, &a.Text, &a.Tags, &a.Entities,
		&a.Note, &a.CreatedAt, &a.UpdatedAt)
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if a.Entities == nil {
		a.Entities = []Entity{}
	}
	return a, err
}

// loadAnnotations returns a batch's annotations by item, in seq order.
func loadAnnotations(ctx context.Context, q storage.Querier, batchID string) (map[string][]Annotation, error) {
	rows, err := q.Query(ctx, "SELECT "+annCols+` FROM annotations a JOIN annotation_items i ON i.id = a.item_id
		WHERE i.batch_id = $1 ORDER BY a.item_id, a.seq`, batchID)
	if err != nil {
		return nil, fmt.Errorf("read annotations: %w", err)
	}
	list, err := pgx.CollectRows(rows, scanAnnotation)
	if err != nil {
		return nil, fmt.Errorf("read annotations: %w", err)
	}
	out := map[string][]Annotation{}
	for _, a := range list {
		out[a.ItemID] = append(out[a.ItemID], a)
	}
	return out, nil
}

func itemAnnotations(ctx context.Context, q storage.Querier, itemID string) ([]Annotation, error) {
	rows, err := q.Query(ctx, "SELECT "+annCols+" FROM annotations a WHERE a.item_id = $1 ORDER BY a.seq", itemID)
	if err != nil {
		return nil, fmt.Errorf("read annotations of %s: %w", itemID, err)
	}
	list, err := pgx.CollectRows(rows, scanAnnotation)
	if err != nil {
		return nil, fmt.Errorf("read annotations of %s: %w", itemID, err)
	}
	return list, nil
}

// transcripts are an item's annotations with a transcript (done or flagged), in seq order.
func transcripts(list []Annotation) []Annotation {
	var out []Annotation
	for _, a := range list {
		if a.Status != StatusSkipped {
			out = append(out, a)
		}
	}
	return out
}

func hasFlag(list []Annotation) bool {
	return slices.ContainsFunc(list, func(a Annotation) bool { return a.Status == StatusFlagged })
}

func excluding(tags []string) bool {
	return slices.ContainsFunc(tags, func(t string) bool { return slices.Contains(Excluding, t) })
}

// Viewer is who reads or changes items: a reviewer sees only their own annotations (blind); the admin, an
// adjudicator and project credentials see every one.
type Viewer struct {
	Actor    auth.Actor
	UserID   string // the person (annotations are a person's); empty for automation
	Reviewer bool
	Role     string // annotator | adjudicator for a reviewer
}

// SeesAll reports whether v sees every annotation of an item.
func (v Viewer) SeesAll() bool { return !v.Reviewer || v.Role == "adjudicator" }

// Blind is v as an annotator sees their own queue: only their own annotations, whoever they are (the admin included),
// so a second transcript is never written with the first in view.
func (v Viewer) Blind() Viewer {
	v.Reviewer, v.Role = true, "annotator"
	return v
}

// view is it as v may see it with annotations list.
func view(it Item, list []Annotation, v Viewer) Item {
	it.Annotations = []Annotation{}
	if hasFlag(list) && it.Required < 2 {
		it.Required = 2
	}
	if v.SeesAll() {
		it.Annotations = append(it.Annotations, list...)
		return it
	}
	for _, a := range list {
		if a.AnnotatorID == v.UserID {
			it.Annotations = append(it.Annotations, a)
		}
	}
	it.WER, it.Final = nil, nil // blind: another annotator's text shows through neither
	return it
}

// GetItem returns one item as v may see it.
func (s *Service) GetItem(ctx context.Context, q storage.Querier, batchID, id string, v Viewer) (Item, error) {
	it, err := getItem(ctx, q, batchID, id, false)
	if err != nil {
		return Item{}, err
	}
	list, err := itemAnnotations(ctx, q, id)
	if err != nil {
		return Item{}, err
	}
	return view(it, list, v), nil
}

// Queues of batchItems.list.
const (
	QueueMine         = "mine"
	QueueAdjudication = "adjudication"
	QueueAll          = "all"
)

// ListItems returns a queue of batch batchID as v may see it, and for queue mine the item v should annotate next.
func (s *Service) ListItems(ctx context.Context, q storage.Querier, batchID, queue, state string, limit int, v Viewer) ([]Item, string, error) {
	if limit <= 0 {
		limit = 200
	}
	b, err := getBatch(ctx, q, batchID, false)
	if err != nil {
		return nil, "", err
	}
	switch {
	case queue == QueueAll && v.Reviewer:
		return nil, "", problems.Forbidden.New("a reviewer sees their own queue (queue=mine) and, as an adjudicator, the adjudication queue")
	case queue == QueueAdjudication && !v.SeesAll():
		return nil, "", problems.Forbidden.New("only the admin or an adjudicator works the adjudication queue")
	}
	items, err := loadItems(ctx, q, batchID, "")
	if err != nil {
		return nil, "", err
	}
	anns, err := loadAnnotations(ctx, q, batchID)
	if err != nil {
		return nil, "", err
	}
	var out []Item
	next := ""
	switch queue {
	case QueueAdjudication:
		for _, it := range items {
			if it.State == ItemDisputed {
				out = append(out, view(it, anns[it.ID], v))
			}
		}
	case QueueAll:
		for _, it := range items {
			if state == "" || it.State == state {
				out = append(out, view(it, anns[it.ID], v))
			}
		}
	default:
		var todo, done []Item
		for _, it := range items {
			list := anns[it.ID]
			mine := slices.ContainsFunc(list, func(a Annotation) bool { return a.AnnotatorID == v.UserID })
			switch {
			case mine:
				done = append(done, view(it, list, v.Blind()))
			case b.State == StateOpen && wants(it, list):
				todo = append(todo, view(it, list, v.Blind()))
			}
		}
		// Each person meets the open items in their own order, so two annotators rarely take the same item at once.
		sort.SliceStable(todo, func(i, j int) bool { return order(v.UserID, todo[i]) < order(v.UserID, todo[j]) })
		if len(todo) > 0 {
			next = todo[0].ID
		}
		out = append(todo, done...)
		if state != "" {
			out = slices.DeleteFunc(out, func(it Item) bool { return it.State != state })
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	if out == nil {
		out = []Item{}
	}
	return out, next, nil
}

// order is an item's place in a person's queue: double items that already have a first transcript come first (they
// wait for their second), then a per-person shuffle of the rest.
func order(user string, it Item) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(user + "/" + it.ID))
	v := h.Sum64() >> 1
	if len(it.Annotations) > 0 {
		return v >> 8
	}
	return v | 1<<62
}

// wants reports whether an item still needs a transcript from someone who has not annotated it.
func wants(it Item, list []Annotation) bool {
	if it.State == ItemAdjudicated || it.State == ItemExcluded || it.Final != nil && it.State != ItemAgreed {
		return false
	}
	need := it.Required
	if hasFlag(list) {
		need = 2
	}
	return len(transcripts(list)) < need
}

// AnnotateInput is an annotations.new request.
type AnnotateInput struct {
	BatchID, ItemID string
	Viewer          Viewer
	Status          string
	Text            string
	Tags            []string
	Entities        []Entity
	Note            string
}

func checkEntities(text string, list []Entity) ([]Entity, error) {
	runes := []rune(text)
	out := make([]Entity, 0, len(list))
	for i, e := range list {
		if e.Start < 0 || e.End <= e.Start || e.End > len(runes) {
			return nil, problems.Validation([]problems.FieldError{{
				Path:    fmt.Sprintf("/entities/%d", i),
				Message: fmt.Sprintf("the span %d–%d is not inside the %d-character transcript", e.Start, e.End, len(runes)),
			}})
		}
		e.Text = string(runes[e.Start:e.End])
		out = append(out, e)
	}
	return out, nil
}

func checkTags(tags []string) ([]string, error) {
	out := []string{}
	for _, t := range tags {
		if !slices.Contains(Tags, t) {
			return nil, problems.Validation([]problems.FieldError{{Path: "/tags", Message: fmt.Sprintf("%q is not a tag (%v)", t, Tags)}})
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

func openBatch(ctx context.Context, tx pgx.Tx, id string) (Batch, error) {
	b, err := getBatch(ctx, tx, id, true)
	if err != nil {
		return Batch{}, err
	}
	if b.State != StateOpen && b.State != StateFailed {
		return Batch{}, problems.BatchClosed.New("batch %s is %s; its items no longer change", b.Name, b.State)
	}
	return b, nil
}

// Annotate records v's annotation of an item (replacing v's earlier one) and settles the item's state.
func (s *Service) Annotate(ctx context.Context, tx pgx.Tx, in AnnotateInput) (Item, []events.Draft, error) {
	v := in.Viewer
	if v.UserID == "" || v.Actor.Kind != auth.KindUser {
		return Item{}, nil, problems.Forbidden.New("annotations are a person's: sign in (agents and API keys do not annotate)")
	}
	b, err := openBatch(ctx, tx, in.BatchID)
	if err != nil {
		return Item{}, nil, err
	}
	it, err := getItem(ctx, tx, in.BatchID, in.ItemID, true)
	if err != nil {
		return Item{}, nil, err
	}
	if it.State == ItemAdjudicated || (it.State == ItemExcluded && it.Final != nil && it.Final.From == "") {
		return Item{}, nil, problems.Conflict.New("item %d was adjudicated; its transcript no longer changes", it.Position)
	}
	if !slices.Contains([]string{StatusDone, StatusSkipped, StatusFlagged}, in.Status) {
		return Item{}, nil, problems.Validation([]problems.FieldError{{Path: "/status", Message: "done, skipped or flagged"}})
	}
	text := trimmed(in.Text)
	if in.Status != StatusSkipped && text == "" {
		return Item{}, nil, problems.Validation([]problems.FieldError{{Path: "/text", Message: "a transcript is required unless the item is skipped"}})
	}
	if utf8.RuneCountInString(text) > 5000 {
		return Item{}, nil, problems.Validation([]problems.FieldError{{Path: "/text", Message: "at most 5000 characters"}})
	}
	tags, err := checkTags(in.Tags)
	if err != nil {
		return Item{}, nil, err
	}
	ents, err := checkEntities(text, in.Entities)
	if err != nil {
		return Item{}, nil, err
	}
	if in.Status == StatusSkipped {
		text, ents = "", []Entity{}
	}
	list, err := itemAnnotations(ctx, tx, it.ID)
	if err != nil {
		return Item{}, nil, err
	}
	now := time.Now().UTC()
	idx := slices.IndexFunc(list, func(a Annotation) bool { return a.AnnotatorID == v.UserID })
	if idx >= 0 && it.State != ItemPending && slices.ContainsFunc(transcripts(list), func(a Annotation) bool { return a.AnnotatorID != v.UserID }) {
		// Blind double annotation: once another transcript is in, changing one's own would follow the verdict.
		return Item{}, nil, problems.Conflict.New("item %d has its transcripts; it is %s now and goes to adjudication if they differ", it.Position, it.State)
	}
	if idx >= 0 {
		if _, err := tx.Exec(ctx, `UPDATE annotations SET status = $2, text = $3, tags = $4, entities = $5, note = $6,
			annotator = $7, updated_at = $8 WHERE id = $1`, list[idx].ID, in.Status, text, tags, marshal(ents), in.Note, v.Actor, now); err != nil {
			return Item{}, nil, fmt.Errorf("update annotation: %w", err)
		}
	} else {
		if in.Status != StatusSkipped && !wants(it, list) {
			return Item{}, nil, problems.Conflict.New("item %d has the transcripts it needs; take the next item (batchItems.list queue=mine)", it.Position)
		}
		seq := 1
		for _, a := range list {
			seq = max(seq, a.Seq+1)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO annotations (id, item_id, annotator_id, annotator, seq, status, text, tags, entities,
			note, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11)`,
			"ann_"+uuid.Must(uuid.NewV7()).String(), it.ID, v.UserID, v.Actor, seq, in.Status, text, tags, marshal(ents), in.Note, now); err != nil {
			return Item{}, nil, fmt.Errorf("insert annotation: %w", err)
		}
	}
	if list, err = itemAnnotations(ctx, tx, it.ID); err != nil {
		return Item{}, nil, err
	}
	it = settle(it, list, s.d().Annotation.AdjudicateWER.Value, s.d().Annotation.MaxSkips.Value, now)
	if err := saveItem(ctx, tx, it); err != nil {
		return Item{}, nil, err
	}
	it.Rev++
	ev := batchEvent(b, "annotation_batch.item_annotated", map[string]any{"itemId": it.ID, "itemState": it.State, "status": in.Status})
	ev.Entity = &events.EntityRef{Kind: ItemKind, ID: it.ID, Rev: it.Rev}
	return view(it, list, v.Blind()), []events.Draft{ev}, nil
}

// settle derives an item's state from its annotations: pending until it has the transcripts it needs (two for a double
// or flagged item) — excluded after maxSkips skips without one; agreed with the first transcript when one is needed
// or the two agree (WER ≤ adjudicateWER); disputed otherwise. An agreed item tagged foreign or unintelligible is
// excluded. Adjudicated items do not change.
func settle(it Item, list []Annotation, adjudicateWER float64, maxSkips int, now time.Time) Item {
	if it.State == ItemAdjudicated || (it.State == ItemExcluded && it.Final != nil && it.Final.From == "") {
		return it
	}
	tr := transcripts(list)
	skips := len(list) - len(tr)
	need := it.Required
	if hasFlag(list) {
		need = 2
	}
	it.WER, it.Final = nil, nil
	if len(tr) >= 2 {
		w := WER(tr[0].Text, tr[1].Text)
		it.WER = &w
	}
	switch {
	case len(tr) < need:
		it.State = ItemPending
		if len(tr) == 0 && skips >= maxSkips {
			it.State = ItemExcluded
		}
	case need == 1 || (it.WER != nil && *it.WER <= adjudicateWER):
		a := tr[0]
		it.Final = &Final{Text: a.Text, Tags: a.Tags, Entities: a.Entities, By: a.Annotator, At: now, From: a.ID}
		it.State = ItemAgreed
		if excluding(a.Tags) {
			it.State = ItemExcluded
		}
	default:
		it.State = ItemDisputed
	}
	return it
}

func saveItem(ctx context.Context, tx pgx.Tx, it Item) error {
	var final any
	if it.Final != nil {
		final = marshal(it.Final)
	}
	if _, err := tx.Exec(ctx, `UPDATE annotation_items SET state = $2, final = $3, wer = $4, rev = rev + 1, updated_at = now()
		WHERE id = $1`, it.ID, it.State, final, it.WER); err != nil {
		return fmt.Errorf("update item %s: %w", it.ID, err)
	}
	return nil
}

// AdjudicateInput is a batchItems.accept request.
type AdjudicateInput struct {
	BatchID, ItemID string
	Rev             int
	Viewer          Viewer
	From            string
	Text            *string
	Tags            []string
	Entities        []Entity
	Exclude         bool
}

// Adjudicate sets an item's final transcript (from an annotation, or a text of its own) or excludes it: the admin or
// an adjudicator, never an agent.
func (s *Service) Adjudicate(ctx context.Context, tx pgx.Tx, in AdjudicateInput) (Item, []events.Draft, error) {
	v := in.Viewer
	if v.UserID == "" || v.Actor.Kind != auth.KindUser {
		return Item{}, nil, problems.Forbidden.New("adjudication is a person's: sign in (agents and API keys do not adjudicate)")
	}
	if !v.SeesAll() {
		return Item{}, nil, problems.Forbidden.New("only the admin or an adjudicator decides an item's transcript")
	}
	b, err := openBatch(ctx, tx, in.BatchID)
	if err != nil {
		return Item{}, nil, err
	}
	it, err := getItem(ctx, tx, in.BatchID, in.ItemID, true)
	if err != nil {
		return Item{}, nil, err
	}
	if err := commands.CheckRev(ItemKind, in.Rev, it.Rev); err != nil {
		return Item{}, nil, err
	}
	list, err := itemAnnotations(ctx, tx, it.ID)
	if err != nil {
		return Item{}, nil, err
	}
	now := time.Now().UTC()
	f := Final{By: v.Actor, At: now, Tags: []string{}, Entities: []Entity{}}
	switch {
	case in.Exclude:
		it.State = ItemExcluded
	case in.From != "":
		i := slices.IndexFunc(list, func(a Annotation) bool { return a.ID == in.From })
		if i < 0 || list[i].Status == StatusSkipped {
			return Item{}, nil, problems.Validation([]problems.FieldError{{Path: "/from", Message: "not a transcript of this item"}})
		}
		f.Text, f.Tags, f.Entities = list[i].Text, list[i].Tags, list[i].Entities
		it.State = ItemAdjudicated
	case in.Text != nil && trimmed(*in.Text) != "":
		f.Text = trimmed(*in.Text)
		it.State = ItemAdjudicated
	default:
		return Item{}, nil, problems.Validation([]problems.FieldError{{Path: "/text", Message: "name an annotation (from), give the text, or exclude the item"}})
	}
	if in.Tags != nil {
		if f.Tags, err = checkTags(in.Tags); err != nil {
			return Item{}, nil, err
		}
	}
	if in.Entities != nil {
		if f.Entities, err = checkEntities(f.Text, in.Entities); err != nil {
			return Item{}, nil, err
		}
	}
	if it.State == ItemAdjudicated && excluding(f.Tags) {
		it.State = ItemExcluded
	}
	it.Final = &f
	if err := saveItem(ctx, tx, it); err != nil {
		return Item{}, nil, err
	}
	it.Rev++
	ev := batchEvent(b, "annotation_batch.item_adjudicated", map[string]any{"itemId": it.ID, "itemState": it.State})
	ev.Entity = &events.EntityRef{Kind: ItemKind, ID: it.ID, Rev: it.Rev}
	return view(it, list, v), []events.Draft{ev}, nil
}
