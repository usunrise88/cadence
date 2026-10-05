package modelexports

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/data"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// SelectionFirstByHash names how a parity sample is drawn: the first utterances of the golden set's dataset by
// the BLAKE3 hash of their audio file (R31), so every export of every model is compared on the same audio.
const SelectionFirstByHash = "first-by-audio-hash"

// Sample is a sample dataset artifact drawn from a golden set's dataset artifact.
type Sample struct {
	Ref        steps.ArtifactRef
	Utterances int
	Hours      float64
	Hashes     []string // the audio hashes, in the sample's (manifest) order
}

const maxManifestBytes = 256 << 20

// BuildSample writes (content-addressed, so it is the same artifact every time) the dataset artifact holding the
// first n utterances of the dataset artifact datasetHash by audio hash: the same audio blobs, a manifest.jsonl of
// their lines in hash order and a dataset.json with the counts. A dataset with n or fewer utterances is taken whole,
// still in hash order.
func BuildSample(store *cas.Store, datasetHash string, n int, label string) (Sample, error) {
	m, err := store.ReadManifest(datasetHash)
	if err != nil {
		return Sample{}, problems.Conflict.New("the golden set's dataset %s is not a dataset directory in the content store: %v", datasetHash, err)
	}
	files := map[string]cas.File{}
	for _, f := range m.Files {
		files[f.Path] = f
	}
	hdrFile, ok1 := files[data.HeaderFile]
	manFile, ok2 := files[data.ManifestFile]
	if !ok1 || !ok2 {
		return Sample{}, problems.Conflict.New("the dataset %s has no %s or %s", datasetHash, data.HeaderFile, data.ManifestFile)
	}
	hb, err := readBlob(store, hdrFile.Hash, 1<<20)
	if err != nil {
		return Sample{}, err
	}
	var header map[string]any
	if err := json.Unmarshal(hb, &header); err != nil {
		return Sample{}, fmt.Errorf("decode %s of %s: %w", data.HeaderFile, datasetHash, err)
	}
	mb, err := readBlob(store, manFile.Hash, maxManifestBytes)
	if err != nil {
		return Sample{}, err
	}
	type line struct {
		raw   []byte
		audio cas.File
		dur   float64
		split string
	}
	var lines []line
	sc := bufio.NewScanner(bytes.NewReader(mb))
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for n := 1; sc.Scan(); n++ {
		raw := bytes.TrimSpace(sc.Bytes())
		if len(raw) == 0 {
			continue
		}
		var row struct {
			Audio    string  `json:"audio"`
			Duration float64 `json:"duration"`
			Split    string  `json:"split"`
		}
		if err := json.Unmarshal(raw, &row); err != nil {
			return Sample{}, fmt.Errorf("%s line %d of %s: %w", data.ManifestFile, n, datasetHash, err)
		}
		f, ok := files[strings.TrimPrefix(row.Audio, "./")]
		if !ok {
			return Sample{}, problems.Conflict.New("%s line %d of %s names audio %q the artifact lacks", data.ManifestFile, n, datasetHash, row.Audio)
		}
		lines = append(lines, line{raw: append([]byte(nil), raw...), audio: f, dur: row.Duration, split: row.Split})
	}
	if err := sc.Err(); err != nil {
		return Sample{}, fmt.Errorf("read %s of %s: %w", data.ManifestFile, datasetHash, err)
	}
	if len(lines) == 0 {
		return Sample{}, problems.Conflict.New("the dataset %s has no utterances", datasetHash)
	}
	slices.SortStableFunc(lines, func(a, b line) int { return strings.Compare(a.audio.Hash, b.audio.Hash) })
	// One utterance per audio file: a dataset that lists one file twice (two windows of it) keeps its first line.
	var picked []line
	seen := map[string]bool{}
	for _, l := range lines {
		if len(picked) == n {
			break
		}
		if seen[l.audio.Hash] {
			continue
		}
		seen[l.audio.Hash] = true
		picked = append(picked, l)
	}
	var man bytes.Buffer
	counts := map[string]int{}
	secs := 0.0
	out := []cas.File{}
	have := map[string]bool{}
	var hashes []string
	for _, l := range picked {
		man.Write(l.raw)
		man.WriteByte('\n')
		split := l.split
		if split == "" {
			split = "test"
		}
		counts[split]++
		secs += l.dur
		hashes = append(hashes, l.audio.Hash)
		if !have[l.audio.Path] {
			have[l.audio.Path] = true
			out = append(out, l.audio)
		}
	}
	hours := math.Round(secs/3600*1e6) / 1e6
	header["counts"], header["hours"] = counts, hours
	desc, _ := header["description"].(string)
	header["description"] = strings.TrimSpace(fmt.Sprintf("Parity sample: the first %d utterances by audio hash of %s. %s", len(picked), label, desc))
	hdr, err := json.MarshalIndent(header, "", "  ")
	if err != nil {
		return Sample{}, err
	}
	for _, f := range []struct {
		path string
		b    []byte
	}{{data.HeaderFile, hdr}, {data.ManifestFile, man.Bytes()}} {
		h, err := store.PutBytes(f.b)
		if err != nil {
			return Sample{}, fmt.Errorf("store the sample's %s: %w", f.path, err)
		}
		out = append(out, cas.File{Path: f.path, Hash: h, Size: int64(len(f.b))})
	}
	slices.SortFunc(out, func(a, b cas.File) int { return strings.Compare(a.Path, b.Path) })
	h, err := store.PutManifest(cas.Manifest{Files: out})
	if err != nil {
		return Sample{}, fmt.Errorf("store the parity sample: %w", err)
	}
	size := int64(0)
	for _, f := range out {
		size += f.Size
	}
	meta, _ := json.Marshal(map[string]any{"format": data.FormatV1, "layout": "dir", "sampleOf": datasetHash,
		"selection": SelectionFirstByHash, "utterances": len(picked)})
	return Sample{Ref: steps.ArtifactRef{Hash: h, Type: TypeDataset, Size: size, Meta: meta}, Utterances: len(picked), Hours: hours,
		Hashes: hashes}, nil
}

func readBlob(store *cas.Store, hash string, limit int64) ([]byte, error) {
	f, err := store.Open(hash)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", hash, err)
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", hash, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", hash, limit)
	}
	return b, nil
}
