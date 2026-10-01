package data

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// AudioFingerprint is the fingerprint kind every import writes: the audio's content hash.
const AudioFingerprint = "audio-b3"

// TagEvalOnly marks dataset collections registered for evaluation only, or from a source not cleared for training
// at registration.
const TagEvalOnly = "eval-only"

var fingerprintKind = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// batchSize bounds the statements sent to Postgres in one round trip.
const batchSize = 500

// Importer registers dataset versions from dataset artifacts (the "dataset" output hook).
type Importer struct {
	CAS *cas.Store
	Now func() time.Time // time.Now when nil
}

// Register installs the importer as the output hook of the "dataset" artifact type.
func (im *Importer) Register(h *steps.Hooks) { h.On(ArtifactType, im.Hook) }

// Hook imports out's dataset artifact inside the transaction that marks the step done: it creates or reuses the
// source, inserts utterances by content hash (an utterance already in the registry is reused), transcripts with
// their origin and the audio fingerprints, and registers a frozen dataset version. Re-importing the same content
// into the same collection returns the version already there.
func (im *Importer) Hook(ctx context.Context, tx pgx.Tx, out steps.Output) ([]events.Draft, error) {
	_, drafts, err := im.Import(ctx, tx, out)
	return drafts, err
}

// Import is Hook that also returns the registered (or existing) version.
func (im *Importer) Import(ctx context.Context, tx pgx.Tx, out steps.Output) (registry.Version, []events.Draft, error) {
	now := time.Now().UTC()
	if im.Now != nil {
		now = im.Now().UTC()
	}
	a, err := ReadArtifact(im.CAS, out.Artifact.Hash)
	if err != nil {
		return registry.Version{}, nil, err
	}
	actor := auth.Actor{Kind: auth.KindAutomation, ID: out.PipelineRunID, Name: "pipeline run " + out.PipelineRunID}
	if out.PipelineRunID == "" {
		actor = registry.Bundled()
	}
	h := a.Header
	src, _, drafts, err := Ensure(ctx, tx, SourceInput{Name: h.Source.Name, Licence: h.Source.Licence, Kind: h.Source.Kind,
		Languages: languagesOf(a.Lines, h.Source.Languages), URL: h.Source.URL}, actor, now)
	if err != nil {
		return registry.Version{}, nil, err
	}
	if h.Purpose == PurposeNoise {
		v, more, err := importNoise(ctx, tx, a, src, out, actor, now)
		return v, append(drafts, more...), err
	}
	uttIDs, err := upsertUtterances(ctx, tx, src.ID, a.Lines, now)
	if err != nil {
		return registry.Version{}, nil, err
	}
	trnIDs, err := upsertTranscripts(ctx, tx, uttIDs, a.Lines, now)
	if err != nil {
		return registry.Version{}, nil, err
	}

	name := collectionName(out, h)
	payload := buildPayload(a, src, out)
	evalOnly := h.EvalOnly || !src.TrainingCleared
	tags := collectionTags(a, src, evalOnly)
	body, err := json.Marshal(payload)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("encode dataset payload: %w", err)
	}
	desc := h.Description
	if desc == "" {
		desc = fmt.Sprintf("Imported from source %s (%s)", src.Name, strings.Join(payload.Locales, ", "))
	}
	v, created, regDrafts, err := registry.Register(ctx, tx, registry.RegisterInput{
		Kind: registry.KindDataset, Name: "dataset/" + name, Description: desc, Tags: tags, Licence: src.Licence,
		Payload: body, Actor: actor, Freeze: true, Fingerprint: ContentFingerprint(a.Lines),
	}, now)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("register dataset version: %w", err)
	}
	drafts = append(drafts, regDrafts...)
	if created {
		if err := insertMembership(ctx, tx, v.ID, uttIDs, trnIDs, a.Lines); err != nil {
			return registry.Version{}, nil, err
		}
	}
	return v, drafts, nil
}

// collectionName is the step's name parameter, else the header's name, else the source's name.
func collectionName(out steps.Output, h Header) string {
	var p struct {
		Name string `json:"name"`
	}
	if len(out.Spec.Params) > 0 && json.Unmarshal(out.Spec.Params, &p) == nil && p.Name != "" {
		return strings.TrimPrefix(p.Name, "dataset/")
	}
	if h.Name != "" {
		return h.Name
	}
	return h.Source.Name
}

func languagesOf(lines []Line, declared []string) []string {
	out := append([]string{}, declared...)
	for _, l := range lines {
		if !containsFold(out, l.Language) {
			out = append(out, l.Language)
		}
	}
	return out
}

func upsertUtterances(ctx context.Context, tx pgx.Tx, sourceID string, lines []Line, now time.Time) ([]string, error) {
	ids := make([]string, len(lines))
	err := inBatches(ctx, tx, len(lines), func(b *pgx.Batch, i int) {
		l := lines[i]
		b.Queue(`WITH ins AS (INSERT INTO utterances (id, content_hash, source_id, duration_s, language, speaker, sample_rate,
				channels, bytes, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
				ON CONFLICT (content_hash) DO NOTHING RETURNING id)
			SELECT id FROM ins UNION ALL SELECT id FROM utterances WHERE content_hash = $2 LIMIT 1`,
			"utt_"+uuid.Must(uuid.NewV7()).String(), l.Hash, sourceID, l.Duration, l.Language, l.Speaker, l.SampleRate,
			l.Channels, l.Size, now).QueryRow(func(row pgx.Row) error { return row.Scan(&ids[i]) })
	})
	if err != nil {
		return nil, fmt.Errorf("insert utterances: %w", err)
	}
	return ids, nil
}

func upsertTranscripts(ctx context.Context, tx pgx.Tx, uttIDs []string, lines []Line, now time.Time) ([]string, error) {
	ids := make([]string, len(lines))
	err := inBatches(ctx, tx, len(lines), func(b *pgx.Batch, i int) {
		l := lines[i]
		b.Queue(`WITH ins AS (INSERT INTO transcripts (id, utterance_id, text, origin, confidence, created_at)
				VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (utterance_id, origin, text) DO NOTHING RETURNING id)
			SELECT id FROM ins UNION ALL SELECT id FROM transcripts WHERE utterance_id = $2 AND origin = $4 AND text = $3 LIMIT 1`,
			"trn_"+uuid.Must(uuid.NewV7()).String(), uttIDs[i], l.Text, l.Origin, l.Confidence, now).
			QueryRow(func(row pgx.Row) error { return row.Scan(&ids[i]) })
		b.Queue(`INSERT INTO utterance_fingerprints (utterance_id, kind, value) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
			uttIDs[i], AudioFingerprint, l.Hash)
		kinds := make([]string, 0, len(l.Fingerprints))
		for k := range l.Fingerprints {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			b.Queue(`INSERT INTO utterance_fingerprints (utterance_id, kind, value) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
				uttIDs[i], k, l.Fingerprints[k])
		}
	})
	if err != nil {
		return nil, fmt.Errorf("insert transcripts: %w", err)
	}
	return ids, nil
}

func insertMembership(ctx context.Context, tx pgx.Tx, versionID string, uttIDs, trnIDs []string, lines []Line) error {
	err := inBatches(ctx, tx, len(lines), func(b *pgx.Batch, i int) {
		b.Queue(`INSERT INTO dataset_utterances (version_id, utterance_id, split, transcript_id) VALUES ($1, $2, $3, $4)`,
			versionID, uttIDs[i], lines[i].Split, trnIDs[i])
	})
	if err != nil {
		return fmt.Errorf("insert dataset membership: %w", err)
	}
	return nil
}

// inBatches queues n items, batchSize at a time, and sends each batch.
func inBatches(ctx context.Context, tx pgx.Tx, n int, queue func(b *pgx.Batch, i int)) error {
	for start := 0; start < n; start += batchSize {
		b := &pgx.Batch{}
		for i := start; i < min(n, start+batchSize); i++ {
			queue(b, i)
		}
		if err := tx.SendBatch(ctx, b).Close(); err != nil {
			return err
		}
	}
	return nil
}

// payload is a dataset version's registry payload (the contract's DatasetPayload).
type payload struct {
	Source         string            `json:"source"`
	SourceRevision string            `json:"sourceRevision,omitempty"`
	Subset         string            `json:"subset,omitempty"`
	Locales        []string          `json:"locales"`
	SampleRateHz   int               `json:"sampleRateHz,omitempty"`
	Splits         []splitStats      `json:"splits"`
	Hours          float64           `json:"hours"`
	Utterances     int               `json:"utterances"`
	Bytes          int64             `json:"bytes"`
	Fixture        bool              `json:"fixture"`
	SourceIDs      []string          `json:"sourceIds"`
	Licence        string            `json:"licence"`
	Languages      []languageStats   `json:"languages"`
	Artifact       steps.ArtifactRef `json:"artifact"`
	EvalOnly       bool              `json:"evalOnly"`
	SplitRule      string            `json:"splitRule"`
	Speakers       int               `json:"speakers"`
	Tags           []string          `json:"tags"`
	Lineage        lineage           `json:"lineage"`
}

type splitStats struct {
	Name       string  `json:"name"`
	Utterances int     `json:"utterances"`
	Hours      float64 `json:"hours"`
	Speakers   int     `json:"speakers"`
}

type languageStats struct {
	Language   string  `json:"language"`
	Utterances int     `json:"utterances"`
	Hours      float64 `json:"hours"`
}

type lineage struct {
	PipelineRunID string `json:"pipelineRunId,omitempty"`
	StepID        string `json:"stepId,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	StepKind      string `json:"stepKind,omitempty"`
}

func buildPayload(a Artifact, src Source, out steps.Output) payload {
	h := a.Header
	p := payload{
		Source: h.Source.URL, SourceRevision: h.Source.Revision, Subset: h.Source.Subset, SourceIDs: []string{src.ID},
		Licence: src.Licence, EvalOnly: h.EvalOnly, SplitRule: h.SplitRule, Tags: append([]string{}, h.Tags...),
		Artifact: steps.ArtifactRef{Hash: a.Hash, Type: ArtifactType, Size: out.Artifact.Size},
		Lineage:  lineage{PipelineRunID: out.PipelineRunID, StepID: out.StepID, ProjectID: out.ProjectID},
	}
	if p.Source == "" {
		p.Source = "source:" + src.Name
	}
	if out.Spec.Kind != "" {
		p.Lineage.StepKind = out.Spec.KindRef()
	}
	splits := map[string]*splitStats{}
	splitSpeakers := map[string]map[string]bool{}
	langs := map[string]*languageStats{}
	speakers := map[string]bool{}
	rates := map[int]bool{}
	for _, l := range a.Lines {
		s := splits[l.Split]
		if s == nil {
			s = &splitStats{Name: l.Split}
			splits[l.Split], splitSpeakers[l.Split] = s, map[string]bool{}
		}
		s.Utterances++
		s.Hours += l.Duration / 3600
		lg := langs[l.Language]
		if lg == nil {
			lg = &languageStats{Language: l.Language}
			langs[l.Language] = lg
		}
		lg.Utterances++
		lg.Hours += l.Duration / 3600
		if l.Speaker != "" {
			splitSpeakers[l.Split][l.Speaker], speakers[l.Speaker] = true, true
		}
		rates[l.SampleRate] = true
		p.Utterances++
		p.Hours += l.Duration / 3600
		p.Bytes += l.Size
	}
	for _, name := range Splits {
		if s := splits[name]; s != nil {
			s.Hours, s.Speakers = round(s.Hours, 4), len(splitSpeakers[name])
			p.Splits = append(p.Splits, *s)
		}
	}
	for _, lg := range langs {
		lg.Hours = round(lg.Hours, 4)
		p.Languages = append(p.Languages, *lg)
		p.Locales = append(p.Locales, lg.Language)
	}
	sort.Slice(p.Languages, func(i, j int) bool { return p.Languages[i].Language < p.Languages[j].Language })
	sort.Strings(p.Locales)
	if len(rates) == 1 {
		for r := range rates {
			p.SampleRateHz = r
		}
	}
	p.Hours, p.Speakers = round(p.Hours, 4), len(speakers)
	return p
}

func collectionTags(a Artifact, src Source, evalOnly bool) []string {
	tags := []string{"source:" + src.Name}
	for _, l := range languagesOf(a.Lines, nil) {
		tags = append(tags, "locale:"+l)
	}
	for _, t := range a.Header.Tags {
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	if evalOnly && !slices.Contains(tags, TagEvalOnly) {
		tags = append(tags, TagEvalOnly)
	}
	sort.Strings(tags)
	return tags
}

func round(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Round(v*p) / p
}
