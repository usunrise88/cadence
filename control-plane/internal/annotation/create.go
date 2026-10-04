package annotation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// NewInput is a batches.new request.
type NewInput struct {
	ProjectID, ProjectSlug string
	Name, Description      string
	Purpose                string
	Dataset, Segments      string
	Size                   int
	Role                   string
	Stratify               []string
	DoubleShare            *float64
	Guidelines             string
	DueAt                  *time.Time
	Seed                   int64
	GoldenSet              string
	ContextS               *float64
	Actor                  auth.Actor
}

// Plan is a checked batches.new: the batch and its items, not written yet.
type Plan struct {
	Batch Batch
	Items []Item
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}[a-z0-9]$`)

// lookAround is how far after a target's speech end the other party's next speech is looked for (seconds).
const lookAround = 10.0

// Prepare checks a batches.new request and draws the sample: the frame (a dataset version's segments artifact, or a
// segments artifact), the guidelines file at the head of the project repository (its commit is pinned), the sample
// over the role's rows, the double share and each item's window, context, prefill and end-of-utterance gap.
func (s *Service) Prepare(ctx context.Context, q storage.Querier, in NewInput) (Plan, error) {
	d := s.d().Annotation
	var bad []problems.FieldError
	fail := func(p, m string) { bad = append(bad, problems.FieldError{Path: p, Message: m}) }
	if !nameRe.MatchString(in.Name) {
		fail("/name", "2–64 lowercase letters, digits, dots, dashes or underscores")
	}
	if in.Purpose == "" {
		in.Purpose = PurposeGolden
	}
	if in.Purpose != PurposeGolden && in.Purpose != PurposeTraining {
		fail("/purpose", "golden-set or training")
	}
	if in.Role == "" {
		in.Role = d.TargetRole.Value
	}
	if in.Size == 0 {
		in.Size = d.BatchSize.Value
	}
	if in.Size < 1 || in.Size > 5000 {
		fail("/size", "1–5000")
	}
	if in.Stratify == nil {
		in.Stratify = slices.Clone(AllStrata)
	}
	for _, x := range in.Stratify {
		if !slices.Contains(AllStrata, x) {
			fail("/stratify", fmt.Sprintf("%q is not a stratum (%s)", x, strings.Join(AllStrata, ", ")))
		}
	}
	share := d.DoubleShare.Value
	if in.DoubleShare != nil {
		share = *in.DoubleShare
	}
	if share < 0 || share > 1 {
		fail("/doubleShare", "0–1")
	}
	ctxS := d.ContextSeconds.Value
	if in.ContextS != nil {
		ctxS = *in.ContextS
	}
	if in.Guidelines == "" {
		in.Guidelines = d.Guidelines.Value
	}
	if in.GoldenSet == "" {
		in.GoldenSet = in.Name
	}
	if !nameRe.MatchString(in.GoldenSet) {
		fail("/goldenSet", "2–64 lowercase letters, digits, dots, dashes or underscores")
	}
	if (in.Dataset == "") == (in.Segments == "") {
		fail("/dataset", "name the frame: a dataset version (dataset) or a segments artifact (segments), one of them")
	}
	now := s.now()
	due := in.DueAt
	if due == nil {
		t := now.Add(time.Duration(d.DueDays.Value) * 24 * time.Hour).Truncate(time.Second)
		due = &t
	}
	if len(bad) > 0 {
		pe := problems.Validation(bad)
		pe.Detail = "the batch is not valid: " + bad[0].Path + " " + bad[0].Message
		return Plan{}, pe
	}
	var exists bool
	if err := q.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM annotation_batches WHERE project_id = $1 AND name = $2)",
		in.ProjectID, in.Name).Scan(&exists); err != nil {
		return Plan{}, fmt.Errorf("check batch name: %w", err)
	}
	if exists {
		return Plan{}, problems.Conflict.New("the project has a batch named %q already", in.Name)
	}

	ref, err := s.frame(ctx, q, in)
	if err != nil {
		return Plan{}, err
	}
	fr, err := ReadFrame(s.CAS, ref.SegmentsHash)
	if err != nil {
		return Plan{}, problems.BadRequest.New("the frame cannot be read: %v", err)
	}
	ref.Source = fr.Source()
	g, err := s.guidelines(ctx, in.ProjectSlug, in.Guidelines)
	if err != nil {
		return Plan{}, err
	}

	e := Edges{Duration: d.DurationEdges.Value, Confidence: d.ConfidenceEdges.Value}
	seed := uint64(max(0, in.Seed)) //nolint:gosec // seed is ≥ 0
	smp := Draw(fr, in.Role, in.Stratify, in.Size, seed, e)
	if smp.Candidates == 0 {
		return Plan{}, problems.BadRequest.New("the frame %s has no segments of role %s with a mount URI and a hash; choose another role (caller, bot, mono) or frame",
			ref.SegmentsHash, in.Role)
	}
	ref.Segments = smp.Candidates
	dbl := doubles(smp.Rows, share, seed)
	byFile := map[string][]Row{}
	for _, r := range fr.Rows {
		byFile[r.file()] = append(byFile[r.file()], r)
	}
	b := Batch{
		ID: "anb_" + uuid.Must(uuid.NewV7()).String(), ProjectID: in.ProjectID, Name: in.Name, Description: in.Description,
		Purpose: in.Purpose, State: StateOpen, Rev: 1, Role: in.Role, Stratify: in.Stratify, DoubleShare: share, Seed: in.Seed,
		ContextS: ctxS, DueAt: due, Guidelines: g, Frame: ref, Strata: smp.Strata, GoldenSet: in.GoldenSet,
		Reviewers: []Reviewer{}, CreatedBy: in.Actor, CreatedAt: now, UpdatedAt: now,
	}
	items := make([]Item, 0, len(smp.Rows))
	for i, r := range smp.Rows {
		it := buildItem(r, fr, byFile[r.file()], ctxS, e, in.Stratify)
		it.ID, it.BatchID, it.Position, it.State, it.Rev = "bit_"+uuid.Must(uuid.NewV7()).String(), b.ID, i+1, ItemPending, 1
		it.Double = dbl[r.str("hash")]
		if it.Double {
			it.Required = 2
		}
		items = append(items, it)
	}
	b.Progress, b.Agreement, b.EOU = summarise(items, nil, d.MaxIAAWER.Value)
	b.CanFreeze = canFreeze(b, s.d())
	return Plan{Batch: b, Items: items}, nil
}

// frame resolves the request's frame to a segments artifact.
func (s *Service) frame(ctx context.Context, q storage.Querier, in NewInput) (FrameRef, error) {
	if in.Segments != "" {
		a, err := artifacts.Get(ctx, q, in.Segments)
		if err != nil {
			return FrameRef{}, err
		}
		if a.Type != SegmentsType || !a.Directory {
			return FrameRef{}, problems.BadRequest.New("%s is a %s artifact, not segments (cadence.segments/1)", in.Segments, a.Type)
		}
		return FrameRef{SegmentsHash: a.Hash}, nil
	}
	ref := strings.TrimSpace(in.Dataset)
	var v registry.Version
	var err error
	if strings.HasPrefix(ref, "ver_") {
		v, err = registry.GetVersion(ctx, q, registry.KindDataset, ref)
	} else {
		if !strings.Contains(ref, "/") {
			ref = "dataset/" + ref
		}
		var list []registry.Version
		list, err = registry.ListVersions(ctx, q, registry.Filter{Kind: registry.KindDataset, Collection: strings.ToLower(ref), Limit: 1})
		if err == nil && len(list) == 0 {
			err = problems.NotFound.New("no dataset version in collection %q", ref)
		}
		if err == nil {
			v = list[0]
		}
	}
	if err != nil {
		return FrameRef{}, err
	}
	var p struct {
		Segments *struct {
			Hash string `json:"hash"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(v.Payload, &p); err != nil {
		return FrameRef{}, fmt.Errorf("decode dataset %s: %w", v.ID, err)
	}
	if p.Segments == nil || p.Segments.Hash == "" {
		return FrameRef{}, problems.BadRequest.New("%s %s has no segments artifact (it was not ingested by pipelines/data-ingest); name the segments artifact instead",
			v.Name, v.Version)
	}
	return FrameRef{DatasetVersionID: v.ID, SegmentsHash: p.Segments.Hash}, nil
}

// SegmentsType is the artifact type of a frame.
const SegmentsType = "segments"

// guidelines pins annotation/guidelines/<name>.md at the head of the project repository.
func (s *Service) guidelines(ctx context.Context, slug, name string) (Guidelines, error) {
	g := Guidelines{Name: name, Path: GuidelinesDir + "/" + name + ".md"}
	if s.Repo == nil {
		return Guidelines{}, problems.RepositoryUnavailable.New("this control plane reaches no project repositories; the batch cannot pin its guidelines")
	}
	b, commit, err := s.Repo.ReadFile(ctx, slug, "main", g.Path)
	if err != nil || commit == "" {
		return Guidelines{}, problems.NotFound.New("%s is not on the project's main branch; write the guidelines first (the bootstrap writes %s/default.md; recipes.new adds one)",
			g.Path, GuidelinesDir)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return Guidelines{}, problems.BadRequest.New("%s is empty", g.Path)
	}
	g.Commit = commit
	return g, nil
}

// buildItem derives an item from its row: the window (segment ± ctxS, every channel), the other party's turns in it,
// the prefill and the end-of-utterance gap.
func buildItem(r Row, fr Frame, sameFile []Row, ctxS float64, e Edges, dims []string) Item {
	start, end := r.span()
	file := r.file()
	fi := fr.Files[file]
	w := Window{File: file, Start: round3(math.Max(0, start-ctxS)), End: round3(end + ctxS), Channels: fi.Channels}
	if fi.Duration > 0 {
		w.End = round3(math.Min(w.End, fi.Duration))
	}
	if len(fi.Roles) == fi.Channels && fi.Channels > 1 {
		w.Roles = fi.Roles
	}
	ch := r.channel()
	it := Item{Hash: r.str("hash"), Row: r, Segment: segmentOf(r), Window: w, Strata: strataOf(r, dims, e), Required: 1,
		Context: Context{Turns: []Turn{}}}
	for _, o := range sameFile {
		oc := o.channel()
		if oc == ch || o.str("hash") == r.str("hash") || strings.TrimSpace(o.str("text")) == "" {
			continue
		}
		a, b := o.span()
		if b <= w.Start || a >= w.End {
			continue
		}
		it.Context.Turns = append(it.Context.Turns, Turn{Start: a, End: b, Channel: oc, Role: o.str("role"), Text: o.str("text")})
	}
	slices.SortFunc(it.Context.Turns, func(x, y Turn) int { return cmpFloat(x.Start, y.Start) })
	it.Prefill = Prefill{Text: strings.TrimSpace(r.str("text")), Origin: r.str("origin")}
	if it.Prefill.Text != "" && it.Prefill.Origin == "" {
		it.Prefill.Origin = "unknown"
	}
	if it.Prefill.Text == "" {
		it.Prefill.Origin = "none"
	}
	if c, ok := r.num("confidence"); ok {
		it.Prefill.Confidence = &c
	}
	it.EOU = eouOf(r, fi, sameFile)
	return it
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// eouOf is the gap between the target's last speech end in its segment and the other channels' next speech start
// (files.jsonl speech per channel; else the other channels' segments), nil for a single-channel file.
func eouOf(r Row, fi FileInfo, sameFile []Row) *EOU {
	ch := r.channel()
	if ch < 0 {
		return nil
	}
	_, end := r.span()
	speechEnd := end
	if sp := r.speech(); len(sp) > 0 {
		speechEnd = sp[len(sp)-1][1]
	}
	var other [][2]float64
	if len(fi.Speech) > 1 && len(fi.Speech) == max(fi.Channels, len(fi.Roles)) {
		for c, list := range fi.Speech {
			if c != ch {
				other = append(other, list...)
			}
		}
	} else {
		for _, o := range sameFile {
			if o.channel() >= 0 && o.channel() != ch {
				if sp := o.speech(); len(sp) > 0 {
					other = append(other, sp...)
				} else {
					a, b := o.span()
					other = append(other, [2]float64{a, b})
				}
			}
		}
	}
	if len(other) == 0 {
		return nil
	}
	out := &EOU{SpeechEnd: round3(speechEnd)}
	best := math.Inf(1)
	for _, iv := range other {
		if iv[1] > speechEnd && iv[0] < speechEnd+lookAround && iv[0] < best {
			best = iv[0]
		}
	}
	if !math.IsInf(best, 1) {
		next, gap := round3(best), round3(best-speechEnd)
		out.NextSpeech, out.GapS = &next, &gap
	}
	return out
}

// Create writes a prepared batch and its items.
func (s *Service) Create(ctx context.Context, tx pgx.Tx, p Plan) (Batch, []events.Draft, error) {
	b := p.Batch
	if _, err := tx.Exec(ctx, `INSERT INTO annotation_batches (id, project_id, name, description, purpose, state, rev, role,
		stratify, double_share, seed, context_s, due_at, guidelines, frame, strata, golden_set, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, 1, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $18)`,
		b.ID, b.ProjectID, b.Name, b.Description, b.Purpose, b.State, b.Role, b.Stratify, b.DoubleShare, b.Seed, b.ContextS,
		b.DueAt, b.Guidelines, b.Frame, b.Strata, b.GoldenSet, b.CreatedBy, b.CreatedAt); err != nil {
		var pe interface{ SQLState() string }
		if errors.As(err, &pe) && pe.SQLState() == "23505" {
			return Batch{}, nil, problems.Conflict.New("the project has a batch named %q already", b.Name)
		}
		return Batch{}, nil, fmt.Errorf("insert batch: %w", err)
	}
	batch := &pgx.Batch{}
	for _, it := range p.Items {
		batch.Queue(`INSERT INTO annotation_items (id, batch_id, position, hash, state, segment, audio_window, prefill, context,
			strata, double_item, eou) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			it.ID, b.ID, it.Position, it.Hash, it.State, marshal(it.Row), it.Window, it.Prefill, it.Context, it.Strata, it.Double, it.EOU)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return Batch{}, nil, fmt.Errorf("insert batch items: %w", err)
	}
	b.Sample = nil
	return b, []events.Draft{batchEvent(b, "annotation_batch.created", map[string]any{"items": len(p.Items), "purpose": b.Purpose})}, nil
}
