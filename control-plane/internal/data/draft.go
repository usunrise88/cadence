package data

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// Draft dataset versions (docs/review/2026-10-03-phase-4-plan.md, decisions 3–4). pipelines/data-ingest ends in
// dataset_freeze (mode draft), whose output is a `dataset` artifact of format cadence.dataset-draft/1: the header and
// one line per segment with its mount URI and canonical hash, and no audio. The hook registers it as a dataset
// version in state draft (payload frozen: false): utterances by canonical hash, transcripts, fingerprints and
// memberships, so the leakage check and previews work before any audio is copied. datasets.freeze runs the same step
// kind in mode cut; its output (cadence.dataset/1 with draftVersionId) makes that draft frozen.

// FormatDraft is the format of a draft's dataset artifact.
const FormatDraft = "cadence.dataset-draft/1"

// SegmentsType is the artifact type of sdp_ingest's output (cadence.segments/1), what a draft was made from.
const SegmentsType = "segments"

// OriginDisputed is the origin the pseudo-label ensemble gives a segment its members disagree on: it goes to the
// triage queue, never into a dataset version.
const OriginDisputed = "pseudo-label:disputed"

var canonicalHash = regexp.MustCompile(`^b3:[0-9a-f]{64}$`)

// DraftHeader is dataset.json of a draft.
type DraftHeader struct {
	Format      string `json:"format"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	Source      struct {
		Name string `json:"name"`
	} `json:"source"`
	SplitRule  string          `json:"splitRule"`
	Counts     map[string]int  `json:"counts"`
	Hours      float64         `json:"hours"`
	Tags       []string        `json:"tags,omitempty"`
	EvalOnly   bool            `json:"evalOnly,omitempty"`
	Quality    json.RawMessage `json:"quality,omitempty"`
	Stats      json.RawMessage `json:"stats,omitempty"`
	Card       string          `json:"card,omitempty"`
	SourceInfo json.RawMessage `json:"sourceInfo,omitempty"`
	Steps      []string        `json:"steps,omitempty"`
}

// DraftLine is one segment of a draft's manifest.jsonl.
type DraftLine struct {
	URI          string            `json:"uri"`
	Hash         string            `json:"hash"`  // b3 of the canonical 16 kHz PCM16 WAV of the segment
	Bytes        int64             `json:"bytes"` // the size of that WAV
	Duration     float64           `json:"duration"`
	SampleRate   int               `json:"sampleRate"`
	Channels     int               `json:"channels,omitempty"`
	Language     string            `json:"language"`
	Speaker      string            `json:"speaker,omitempty"`
	Text         string            `json:"text"`
	Origin       string            `json:"origin"`
	Confidence   *float64          `json:"confidence,omitempty"`
	Split        string            `json:"split"`
	Role         string            `json:"role,omitempty"`
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
}

// Draft is a read draft artifact: its header, its lines as phase-2 Lines (Audio holds the mount URI) and its files.
type Draft struct {
	Hash   string
	Header DraftHeader
	Lines  []Line
	Files  map[string]cas.File
}

// artifactFormat reads only the format of a dataset artifact's header.
func artifactFormat(store *cas.Store, hash string) (string, error) {
	files, err := artifactFiles(store, hash)
	if err != nil {
		return "", err
	}
	hb, err := readFile(store, files, HeaderFile, maxHeaderBytes)
	if err != nil {
		return "", err
	}
	var h struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(hb, &h); err != nil {
		return "", fmt.Errorf("dataset artifact: %s: %w", HeaderFile, err)
	}
	return h.Format, nil
}

func artifactFiles(store *cas.Store, hash string) (map[string]cas.File, error) {
	if store == nil {
		return nil, errors.New("dataset artifact: this control plane has no content store (CADENCE_CAS_DIR)")
	}
	m, err := store.ReadManifest(hash)
	if err != nil {
		return nil, fmt.Errorf("dataset artifact %s: %w", hash, err)
	}
	files := make(map[string]cas.File, len(m.Files))
	for _, f := range m.Files {
		files[f.Path] = f
	}
	return files, nil
}

// ReadDraft reads and checks a draft artifact: every line names a canonical hash once, a mount URI, a positive
// duration, a language, a split, text with a known origin (a disputed pseudo-label is refused), and the header's
// counts and hours match the lines.
func ReadDraft(store *cas.Store, hash string) (Draft, error) {
	files, err := artifactFiles(store, hash)
	if err != nil {
		return Draft{}, err
	}
	d := Draft{Hash: hash, Files: files}
	hb, err := readFile(store, files, HeaderFile, maxHeaderBytes)
	if err != nil {
		return Draft{}, err
	}
	if err := json.Unmarshal(hb, &d.Header); err != nil {
		return Draft{}, fmt.Errorf("draft dataset artifact: %s: %w", HeaderFile, err)
	}
	h := d.Header
	switch {
	case h.Format != FormatDraft:
		return Draft{}, fmt.Errorf("draft dataset artifact: format %q is not %s", h.Format, FormatDraft)
	case !sourceName.MatchString(h.Source.Name):
		return Draft{}, fmt.Errorf("draft dataset artifact: source name %q must be 2–100 lowercase letters, digits, dots, dashes or underscores", h.Source.Name)
	case h.Name != "" && !sourceName.MatchString(h.Name):
		return Draft{}, fmt.Errorf("draft dataset artifact: name %q must be lowercase letters, digits, dots, dashes or underscores", h.Name)
	case !slices.Contains(SplitRules, h.SplitRule):
		return Draft{}, fmt.Errorf("draft dataset artifact: splitRule %q is not one of %s", h.SplitRule, strings.Join(SplitRules, ", "))
	case h.Card != "":
		if _, ok := files[h.Card]; !ok {
			return Draft{}, fmt.Errorf("draft dataset artifact: card %q is not a file of the artifact", h.Card)
		}
	}
	mb, err := readFile(store, files, ManifestFile, maxManifest)
	if err != nil {
		return Draft{}, err
	}
	sc := bufio.NewScanner(bytes.NewReader(mb))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	seen := map[string]int{}
	n := 0
	for sc.Scan() {
		n++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var dl DraftLine
		if err := json.Unmarshal(raw, &dl); err != nil {
			return Draft{}, fmt.Errorf("draft dataset artifact: %s line %d: %w", ManifestFile, n, err)
		}
		if dl.Channels == 0 {
			dl.Channels = 1
		}
		l := Line{Audio: dl.URI, Duration: dl.Duration, SampleRate: dl.SampleRate, Channels: dl.Channels, Language: dl.Language,
			Speaker: dl.Speaker, Text: dl.Text, Origin: dl.Origin, Confidence: dl.Confidence, Split: dl.Split,
			Fingerprints: dl.Fingerprints, URI: dl.URI, Role: dl.Role, Hash: dl.Hash, Size: dl.Bytes}
		if err := checkDraftLine(dl, l); err != nil {
			return Draft{}, fmt.Errorf("draft dataset artifact: %s line %d: %w", ManifestFile, n, err)
		}
		if prev, dup := seen[dl.Hash]; dup {
			return Draft{}, fmt.Errorf("draft dataset artifact: %s line %d: the same audio as line %d (%s); a segment appears once", ManifestFile, n, prev, dl.Hash)
		}
		seen[dl.Hash] = n
		d.Lines = append(d.Lines, l)
	}
	if err := sc.Err(); err != nil {
		return Draft{}, fmt.Errorf("draft dataset artifact: read %s: %w", ManifestFile, err)
	}
	if len(d.Lines) == 0 {
		return Draft{}, fmt.Errorf("draft dataset artifact: %s has no segments", ManifestFile)
	}
	counts, hours := map[string]int{}, 0.0
	for _, l := range d.Lines {
		counts[l.Split]++
		hours += l.Duration / 3600
	}
	for _, s := range Splits {
		if counts[s] != h.Counts[s] {
			return Draft{}, fmt.Errorf("draft dataset artifact: %s says %d %s segments, %s has %d", HeaderFile, h.Counts[s], s, ManifestFile, counts[s])
		}
	}
	if math.Abs(hours-h.Hours) > 0.001+hours*0.001 {
		return Draft{}, fmt.Errorf("draft dataset artifact: %s says %.4f hours, the segments add up to %.4f", HeaderFile, h.Hours, hours)
	}
	return d, nil
}

func checkDraftLine(dl DraftLine, l Line) error {
	switch {
	case !strings.HasPrefix(dl.URI, "mount://"):
		return fmt.Errorf("uri %q is not a mount:// URI", dl.URI)
	case !canonicalHash.MatchString(dl.Hash):
		return fmt.Errorf("hash %q is not a b3 hash", dl.Hash)
	case dl.Bytes <= 0:
		return fmt.Errorf("bytes %d must be the size of the segment's canonical WAV", dl.Bytes)
	case strings.TrimSpace(dl.Text) == "":
		return errors.New("text is empty; pseudo-label or filter (manifest_filter) untranscribed segments before the draft")
	case dl.Origin == OriginDisputed:
		return fmt.Errorf("origin %s: a disputed pseudo-label goes to triage, never into a dataset version (manifest_filter drops it)", OriginDisputed)
	}
	return l.check()
}

// recipeOf reads the project, pipeline and commit of the pipeline run that produced out.
func recipeOf(ctx context.Context, tx pgx.Tx, out steps.Output) (*recipe, error) {
	r := &recipe{ProjectID: out.ProjectID, Params: out.Spec.Params}
	if out.Spec.Kind != "" {
		r.StepKind = out.Spec.KindRef()
	}
	if out.PipelineRunID == "" {
		return r, nil
	}
	err := tx.QueryRow(ctx, "SELECT project_id, pipeline, commit_sha FROM pipeline_runs WHERE id = $1", out.PipelineRunID).
		Scan(&r.ProjectID, &r.Pipeline, &r.Commit)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the pipeline run of a draft: %w", err)
	}
	return r, nil
}

// DraftFingerprint is a draft version's identity: its members (ContentFingerprint) and the recipe commit it was
// ingested from (docs/spec/04-blocks.md Block 1: "fingerprint over members and recipe SHA").
func DraftFingerprint(content, commit string) string {
	h := sha256.Sum256([]byte(FormatDraft + "\n" + content + "\nrecipe:" + commit))
	return hex.EncodeToString(h[:])
}

// importDraft registers out's draft artifact as a draft dataset version.
func (im *Importer) importDraft(ctx context.Context, tx pgx.Tx, out steps.Output, actor auth.Actor, now time.Time) (registry.Version, []events.Draft, error) {
	d, err := ReadDraft(im.CAS, out.Artifact.Hash)
	if err != nil {
		return registry.Version{}, nil, err
	}
	src, err := IngestAllowed(ctx, tx, d.Header.Source.Name)
	if err != nil {
		return registry.Version{}, nil, err
	}
	uttIDs, err := upsertUtterances(ctx, tx, src.ID, d.Lines, now)
	if err != nil {
		return registry.Version{}, nil, err
	}
	// Where each utterance's audio also lives: its segment on the mount (utterance_uris, utterances.get uris).
	uris := make([]string, len(d.Lines))
	for i, l := range d.Lines {
		uris[i] = l.URI
	}
	if err := mounts.RecordURIsBatch(ctx, tx, uttIDs, uris); err != nil {
		return registry.Version{}, nil, err
	}
	trnIDs, err := upsertTranscripts(ctx, tx, uttIDs, d.Lines, now)
	if err != nil {
		return registry.Version{}, nil, err
	}
	rec, err := recipeOf(ctx, tx, out)
	if err != nil {
		return registry.Version{}, nil, err
	}
	h := Header{Format: FormatDraft, Name: d.Header.Name, Description: d.Header.Description, SplitRule: d.Header.SplitRule,
		Counts: d.Header.Counts, Hours: d.Header.Hours, Tags: d.Header.Tags, EvalOnly: d.Header.EvalOnly}
	h.Source.Name = src.Name
	a := Artifact{Hash: d.Hash, Header: h, Lines: d.Lines}
	payload := buildPayload(a, src, out)
	frozen := false
	content := ContentFingerprint(d.Lines)
	payload.Frozen, payload.Quality, payload.Stats, payload.Recipe, payload.ContentFingerprint = &frozen, d.Header.Quality,
		d.Header.Stats, rec, content
	payload.Shards = []shard{}
	if f, ok := d.Files[d.Header.Card]; ok && d.Header.Card != "" {
		payload.Card = &cardRef{Hash: f.Hash, Bytes: f.Size}
	}
	if in, ok := out.Spec.Inputs[SegmentsType]; ok {
		in.Meta = nil
		payload.Segments = &in
	}
	evalOnly := h.EvalOnly || !src.TrainingCleared
	body, err := json.Marshal(payload)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("encode dataset payload: %w", err)
	}
	desc := h.Description
	if desc == "" {
		desc = fmt.Sprintf("Ingested from source %s (%s)", src.Name, strings.Join(payload.Locales, ", "))
	}
	v, created, drafts, err := registry.Register(ctx, tx, registry.RegisterInput{
		Kind: registry.KindDataset, Name: "dataset/" + collectionName(out, h), Description: desc, Tags: collectionTags(a, src, evalOnly),
		Licence: src.Licence, Payload: body, Actor: actor, Fingerprint: DraftFingerprint(content, rec.Commit),
	}, now)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("register draft dataset version: %w", err)
	}
	if created {
		if err := insertMembership(ctx, tx, v.ID, uttIDs, trnIDs, d.Lines); err != nil {
			return registry.Version{}, nil, err
		}
		if err := recordIngest(ctx, tx, v.ID, payload.Lineage, now); err != nil {
			return registry.Version{}, nil, err
		}
	}
	return v, drafts, nil
}

// completeFreeze makes the draft a cut artifact names frozen: the cut must hold exactly the draft's members (same
// canonical hashes, splits and texts), and the draft must still share nothing with a golden set (one may have been
// frozen since datasets.freeze checked). Freezing the same cut twice is a no-op.
func (im *Importer) completeFreeze(ctx context.Context, tx pgx.Tx, out steps.Output, a Artifact, now time.Time) (registry.Version, []events.Draft, error) {
	id := a.Header.DraftVersionID
	v, err := registry.GetVersion(ctx, tx, registry.KindDataset, id)
	if err != nil {
		return registry.Version{}, nil, err
	}
	if _, err := tx.Exec(ctx, "SELECT 1 FROM registry_versions WHERE id = $1 FOR UPDATE", id); err != nil {
		return registry.Version{}, nil, fmt.Errorf("lock draft %s: %w", id, err)
	}
	var p payload
	if err := json.Unmarshal(v.Payload, &p); err != nil {
		return registry.Version{}, nil, fmt.Errorf("decode dataset %s: %w", id, err)
	}
	if v.State != registry.StateDraft {
		if p.Artifact.Hash == a.Hash {
			return v, nil, nil
		}
		return registry.Version{}, nil, problems.Conflict.New("%s %s is %s already (artifact %s); the cut %s is not its content", v.Name, v.Version, v.State, p.Artifact.Hash, a.Hash)
	}
	if got := ContentFingerprint(a.Lines); got != p.ContentFingerprint {
		return registry.Version{}, nil, fmt.Errorf("the cut %s does not hold the draft %s's segments (content fingerprint %s, the draft's %s); re-run the ingest and freeze the new draft",
			a.Hash, id, got, p.ContentFingerprint)
	}
	if err := NotGolden(ctx, tx, v); err != nil {
		return registry.Version{}, nil, err
	}
	files, err := artifactFiles(im.CAS, a.Hash)
	if err != nil {
		return registry.Version{}, nil, err
	}
	frozen := true
	p.Frozen = &frozen
	p.Artifact = steps.ArtifactRef{Hash: a.Hash, Type: ArtifactType, Size: out.Artifact.Size}
	if len(a.Header.Quality) > 0 {
		p.Quality = a.Header.Quality
	}
	if len(a.Header.Stats) > 0 {
		p.Stats = a.Header.Stats
	}
	if f, ok := files[a.Header.Card]; ok && a.Header.Card != "" {
		p.Card = &cardRef{Hash: f.Hash, Bytes: f.Size}
	}
	p.Shards = []shard{}
	for _, s := range a.Header.Shards {
		f, ok := files[s.Cuts]
		if !ok {
			return registry.Version{}, nil, fmt.Errorf("dataset artifact: shard %d names %q, which is not a file of the artifact", s.Index, s.Cuts)
		}
		p.Shards = append(p.Shards, shard{Index: s.Index, Hash: f.Hash, Path: s.Cuts, Utterances: s.Utterances, Bytes: s.Bytes,
			Seconds: s.Seconds, Location: "cas"})
	}
	p.Bytes = 0
	for _, l := range a.Lines {
		p.Bytes += l.Size
	}
	if p.Freeze == nil {
		p.Freeze = &freezeState{}
	}
	if p.Freeze.PipelineRunID == "" {
		p.Freeze.PipelineRunID = out.PipelineRunID
	}
	p.Freeze.FrozenAt = &now
	body, err := json.Marshal(p)
	if err != nil {
		return registry.Version{}, nil, fmt.Errorf("encode dataset payload: %w", err)
	}
	canonical, err := registry.Canonical(body)
	if err != nil {
		return registry.Version{}, nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE registry_versions SET payload = $2, state = 'frozen', frozen_at = $3 WHERE id = $1 AND state = 'draft'`,
		id, canonical, now); err != nil {
		return registry.Version{}, nil, fmt.Errorf("freeze %s: %w", id, err)
	}
	if v, err = registry.GetVersion(ctx, tx, registry.KindDataset, id); err != nil {
		return registry.Version{}, nil, err
	}
	return v, []events.Draft{registry.VersionEvent(v, v.Kind+".frozen")}, nil
}
