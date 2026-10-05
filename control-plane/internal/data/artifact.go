package data

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
)

// The dataset artifact (docs/spec/02-domain-projects-registry.md "The dataset artifact"): a directory artifact in the
// content store whose manifest holds
//
//	dataset.json    the header (Header)
//	manifest.jsonl  one Line per utterance
//	<audio files>   each its own blob; a line's audio is the file's path inside the artifact
//
// An utterance's content hash is the blob hash of its audio file.
const (
	ArtifactType   = "dataset"
	FormatV1       = "cadence.dataset/1"
	HeaderFile     = "dataset.json"
	ManifestFile   = "manifest.jsonl"
	maxHeaderBytes = 1 << 20
	maxManifest    = 256 << 20
)

// HeaderSource is the corpus an import comes from, as the step read it.
type HeaderSource struct {
	Name      string   `json:"name"`
	Licence   string   `json:"licence"`
	Kind      string   `json:"kind"`
	Languages []string `json:"languages"`
	URL       string   `json:"url,omitempty"`
	Revision  string   `json:"revision,omitempty"` // the pinned revision of the corpus (a Hugging Face commit)
	Subset    string   `json:"subset,omitempty"`   // its configuration(s), e.g. he_il
}

// Header is dataset.json.
type Header struct {
	Format      string         `json:"format"`         // cadence.dataset/1
	Name        string         `json:"name,omitempty"` // the collection name without dataset/ (fleurs-he)
	Description string         `json:"description,omitempty"`
	Source      HeaderSource   `json:"source"`
	SplitRule   string         `json:"splitRule"` // speaker-disjoint | source | all-train | all-validation | all-test
	Counts      map[string]int `json:"counts"`    // utterances per split
	Hours       float64        `json:"hours"`     // total audio hours
	Tags        []string       `json:"tags,omitempty"`
	EvalOnly    bool           `json:"evalOnly,omitempty"` // golden and replay test sets: never trained on
	Purpose     string         `json:"purpose,omitempty"`  // speech (empty) or noise: a noise bank, not utterances

	// A freeze's cut (dataset_freeze mode cut, phase 4): the draft version it freezes, the quality checks, statistics
	// and card the step computed, and the shards (Lhotse cuts manifests over the artifact's audio). The source is
	// the draft's; the header names only the source's name.
	DraftVersionID string          `json:"draftVersionId,omitempty"`
	Quality        json.RawMessage `json:"quality,omitempty"`
	Stats          json.RawMessage `json:"stats,omitempty"`
	Card           string          `json:"card,omitempty"` // path of the card (Markdown) inside the artifact
	Shards         []ShardFile     `json:"shards,omitempty"`
	SourceInfo     json.RawMessage `json:"sourceInfo,omitempty"` // the corpus's SOURCE.yaml as the ingest read it
	Steps          []string        `json:"steps,omitempty"`      // the step kinds the segments went through

	// Mined is where a noise bank mined from recordings came from (noise_mine, phase 4 · stream I): the segments
	// artifact, the roles and the clip rules. Such a header names only its source, which must be registered.
	Mined json.RawMessage `json:"mined,omitempty"`
}

// namesSourceOnly reports whether the header names a registered source by name only: a freeze's cut, or a noise
// bank mined from an ingest's recordings.
func (h Header) namesSourceOnly() bool {
	return h.DraftVersionID != "" || (h.Purpose == PurposeNoise && len(h.Mined) > 0)
}

// ShardFile is one shard of a cut dataset artifact: a cuts manifest (gzip JSON lines) inside the artifact.
type ShardFile struct {
	Index      int     `json:"index"`
	Cuts       string  `json:"cuts"` // path inside the artifact, shards/cuts.000000.jsonl.gz
	Utterances int     `json:"utterances"`
	Bytes      int64   `json:"bytes"`
	Seconds    float64 `json:"seconds"`
}

// Line is one utterance of manifest.jsonl.
type Line struct {
	Audio        string            `json:"audio"`    // relative path of the audio file inside the artifact
	Duration     float64           `json:"duration"` // seconds
	SampleRate   int               `json:"sampleRate"`
	Channels     int               `json:"channels,omitempty"` // 1 when omitted
	Language     string            `json:"language"`
	Speaker      string            `json:"speaker,omitempty"`
	Text         string            `json:"text"`
	Origin       string            `json:"origin"` // human | pseudo-label | model:<id>
	Confidence   *float64          `json:"confidence,omitempty"`
	Split        string            `json:"split"` // train | validation | test
	Fingerprints map[string]string `json:"fingerprints,omitempty"`
	URI          string            `json:"uri,omitempty"`  // where the segment lives on a mount (an ingest's cut)
	Role         string            `json:"role,omitempty"` // caller | bot | mono (an ingest's cut)
	EOU          json.RawMessage   `json:"eou,omitempty"`  // end of utterance from per-channel VAD (sdp_ingest; manifest only)

	Hash string `json:"-"` // the audio file's blob hash (resolved from the artifact's manifest)
	Size int64  `json:"-"`
}

// Artifact is a read dataset artifact.
type Artifact struct {
	Hash   string
	Header Header
	Lines  []Line
}

// ReadArtifact reads and checks the dataset artifact hash from the store: the header and every line must be
// well-formed, every audio path must be a file of the artifact, no audio may appear twice, and the header's counts
// and hours must match the lines.
func ReadArtifact(store *cas.Store, hash string) (Artifact, error) {
	if store == nil {
		return Artifact{}, errors.New("dataset artifact: this control plane has no content store (CADENCE_CAS_DIR)")
	}
	m, err := store.ReadManifest(hash)
	if err != nil {
		return Artifact{}, fmt.Errorf("dataset artifact %s: %w", hash, err)
	}
	files := make(map[string]cas.File, len(m.Files))
	for _, f := range m.Files {
		files[f.Path] = f
	}
	a := Artifact{Hash: hash}
	hb, err := readFile(store, files, HeaderFile, maxHeaderBytes)
	if err != nil {
		return Artifact{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(hb))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a.Header); err != nil {
		return Artifact{}, fmt.Errorf("dataset artifact: %s: %w", HeaderFile, err)
	}
	if err := a.Header.check(); err != nil {
		return Artifact{}, err
	}
	mb, err := readFile(store, files, ManifestFile, maxManifest)
	if err != nil {
		return Artifact{}, err
	}
	if a.Lines, err = parseLines(mb, files); err != nil {
		return Artifact{}, err
	}
	if len(a.Lines) == 0 {
		return Artifact{}, fmt.Errorf("dataset artifact: %s has no utterances", ManifestFile)
	}
	counts, hours := map[string]int{}, 0.0
	for _, l := range a.Lines {
		counts[l.Split]++
		hours += l.Duration / 3600
	}
	for _, s := range Splits {
		if counts[s] != a.Header.Counts[s] {
			return Artifact{}, fmt.Errorf("dataset artifact: %s says %d %s utterances, %s has %d", HeaderFile, a.Header.Counts[s], s, ManifestFile, counts[s])
		}
	}
	if math.Abs(hours-a.Header.Hours) > 0.001+hours*0.001 {
		return Artifact{}, fmt.Errorf("dataset artifact: %s says %.4f hours, the utterances add up to %.4f", HeaderFile, a.Header.Hours, hours)
	}
	return a, nil
}

func (h Header) check() error {
	var bad []string
	if h.Format != FormatV1 {
		bad = append(bad, fmt.Sprintf("format %q is not %s", h.Format, FormatV1))
	}
	if !h.namesSourceOnly() {
		if err := (SourceInput{Name: h.Source.Name, Licence: h.Source.Licence, Kind: h.Source.Kind}).validate(); err != nil {
			bad = append(bad, err.Error())
		}
	} else if !sourceName.MatchString(h.Source.Name) {
		bad = append(bad, fmt.Sprintf("source name %q must be 2–100 lowercase letters, digits, dots, dashes or underscores", h.Source.Name))
	}
	if h.Name != "" && !sourceName.MatchString(h.Name) {
		bad = append(bad, fmt.Sprintf("name %q must be lowercase letters, digits, dots, dashes or underscores", h.Name))
	}
	if h.Purpose != "" && h.Purpose != PurposeSpeech && h.Purpose != PurposeNoise {
		bad = append(bad, fmt.Sprintf("purpose %q is not speech or noise", h.Purpose))
	}
	if !slices.Contains(SplitRules, h.SplitRule) {
		bad = append(bad, fmt.Sprintf("splitRule %q is not one of %s", h.SplitRule, strings.Join(SplitRules, ", ")))
	}
	for s := range h.Counts {
		if !slices.Contains(Splits, s) {
			bad = append(bad, fmt.Sprintf("counts has an unknown split %q", s))
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("dataset artifact: %s: %s", HeaderFile, strings.Join(bad, "; "))
	}
	return nil
}

// SplitRules are the ways an import assigns splits.
var SplitRules = []string{"speaker-disjoint", "source", "all-train", "all-validation", "all-test"}

func readFile(store *cas.Store, files map[string]cas.File, name string, limit int64) ([]byte, error) {
	f, ok := files[name]
	if !ok {
		return nil, fmt.Errorf("dataset artifact: no %s", name)
	}
	r, err := store.Open(f.Hash)
	if err != nil {
		return nil, fmt.Errorf("dataset artifact: %s: %w", name, err)
	}
	defer func() { _ = r.Close() }()
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, fmt.Errorf("dataset artifact: read %s: %w", name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("dataset artifact: %s is larger than %d bytes", name, limit)
	}
	return b, nil
}

func parseLines(b []byte, files map[string]cas.File) ([]Line, error) {
	var (
		out  []Line
		seen = map[string]int{}
	)
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	n := 0
	for sc.Scan() {
		n++
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var l Line
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&l); err != nil {
			return nil, fmt.Errorf("dataset artifact: %s line %d: %w", ManifestFile, n, err)
		}
		if l.Channels == 0 {
			l.Channels = 1
		}
		if err := l.check(); err != nil {
			return nil, fmt.Errorf("dataset artifact: %s line %d: %w", ManifestFile, n, err)
		}
		f, ok := files[l.Audio]
		if !ok || l.Audio == HeaderFile || l.Audio == ManifestFile {
			return nil, fmt.Errorf("dataset artifact: %s line %d: audio %q is not a file of the artifact", ManifestFile, n, l.Audio)
		}
		if prev, dup := seen[f.Hash]; dup {
			return nil, fmt.Errorf("dataset artifact: %s line %d: the same audio as line %d (%s); an utterance appears once", ManifestFile, n, prev, f.Hash)
		}
		seen[f.Hash] = n
		l.Hash, l.Size = f.Hash, f.Size
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("dataset artifact: read %s: %w", ManifestFile, err)
	}
	return out, nil
}

func (l Line) check() error {
	switch {
	case l.Audio == "":
		return errors.New("audio is required")
	case !(l.Duration > 0) || math.IsInf(l.Duration, 0):
		return fmt.Errorf("duration %v must be above 0 seconds", l.Duration)
	case l.SampleRate <= 0:
		return fmt.Errorf("sampleRate %d must be above 0", l.SampleRate)
	case l.Channels < 1:
		return fmt.Errorf("channels %d must be at least 1", l.Channels)
	case strings.TrimSpace(l.Language) == "":
		return errors.New("language is required")
	case !ValidOrigin(l.Origin):
		return fmt.Errorf("origin %q must be human, pseudo-label or model:<id>", l.Origin)
	case !slices.Contains(Splits, l.Split):
		return fmt.Errorf("split %q must be train, validation or test", l.Split)
	case l.Confidence != nil && (*l.Confidence < 0 || *l.Confidence > 1):
		return fmt.Errorf("confidence %v must lie in [0, 1]", *l.Confidence)
	}
	for k := range l.Fingerprints {
		if !fingerprintKind.MatchString(k) || k == AudioFingerprint {
			return fmt.Errorf("fingerprint kind %q must be lowercase letters, digits and dashes (and not %s, which Cadence writes)", k, AudioFingerprint)
		}
	}
	return nil
}

// ContentFingerprint is a dataset version's identity (R18): the sha256 (hex) of the sorted JSON tuples
// [audio hash, split, transcript text], one per line. Lineage, statistics and order do not change it.
func ContentFingerprint(lines []Line) string {
	enc := make([]string, 0, len(lines))
	for _, l := range lines {
		b, _ := json.Marshal([]string{l.Hash, l.Split, l.Text}) // strings always marshal
		enc = append(enc, string(b))
	}
	sort.Strings(enc)
	h := sha256.New()
	for _, e := range enc {
		h.Write([]byte(e))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}
