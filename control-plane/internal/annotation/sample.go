package annotation

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"math/rand/v2"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/mounts"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

// The frame of a batch is a segments artifact (cadence.segments/1, docs/help/steps/sdp-ingest.md): segments.json,
// segments.jsonl (one row per segment, every key kept) and files.jsonl (each source file's channels, roles and speech
// per channel). The sample is drawn from its rows of one role; the context (the other party's turns, speech for the
// end-of-utterance gap) comes from the same artifact.

// Files of a segments artifact.
const (
	SegmentsHeader = "segments.json"
	SegmentsRows   = "segments.jsonl"
	SegmentsFiles  = "files.jsonl"
	SegmentsFormat = "cadence.segments/1"
)

// maxRowBytes bounds one segments row.
const maxRowBytes = 4 << 20

// Row is one segments row with every key the producing steps wrote.
type Row map[string]any

func (r Row) str(k string) string {
	if v, ok := r[k].(string); ok {
		return v
	}
	return ""
}

func (r Row) num(k string) (float64, bool) {
	switch v := r[k].(type) {
	case float64:
		return v, true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	}
	return 0, false
}

func (r Row) channel() int {
	if v, ok := r.num("channel"); ok {
		return int(v)
	}
	if u, err := mounts.ParseURI(r.str("uri")); err == nil && u.Channel != nil {
		return *u.Channel
	}
	return -1
}

// file is the source file's mount URI (the row's file, else its URI without the fragment).
func (r Row) file() string {
	if f := r.str("file"); f != "" {
		return f
	}
	u := r.str("uri")
	if i := strings.IndexByte(u, '#'); i >= 0 {
		return u[:i]
	}
	return u
}

func (r Row) span() (float64, float64) {
	a, _ := r.num("start")
	b, _ := r.num("end")
	return a, b
}

// speech is the row's voice activity in file seconds (vad.speech is relative to the segment's start).
func (r Row) speech() [][2]float64 {
	start, _ := r.span()
	vad, _ := r["vad"].(map[string]any)
	list, _ := vad["speech"].([]any)
	var out [][2]float64
	for _, x := range list {
		p, ok := x.([]any)
		if !ok || len(p) != 2 {
			continue
		}
		a, ok1 := p[0].(float64)
		b, ok2 := p[1].(float64)
		if ok1 && ok2 && b > a {
			out = append(out, [2]float64{start + a, start + b})
		}
	}
	return out
}

// FileInfo is one line of files.jsonl.
type FileInfo struct {
	URI        string         `json:"uri"`
	Duration   float64        `json:"duration"`
	SampleRate int            `json:"sampleRate"`
	Channels   int            `json:"channels"`
	Roles      []string       `json:"roles"`
	Codec      string         `json:"codec"`
	Speech     [][][2]float64 `json:"speech"` // per track (one track per channel of a split file), file seconds
}

// Frame is a read segments artifact.
type Frame struct {
	Hash   string
	Header map[string]any
	Rows   []Row
	Files  map[string]FileInfo
}

// Source is the frame's source name (segments.json source.name).
func (f Frame) Source() string {
	if s, ok := f.Header["source"].(map[string]any); ok {
		if n, ok := s["name"].(string); ok {
			return n
		}
	}
	return ""
}

// ReadFrame reads a segments artifact from the content store.
func ReadFrame(store *cas.Store, hash string) (Frame, error) {
	if store == nil {
		return Frame{}, fmt.Errorf("this control plane has no content store")
	}
	m, err := store.ReadManifest(hash)
	if err != nil {
		return Frame{}, fmt.Errorf("segments artifact %s: %w", hash, err)
	}
	files := map[string]cas.File{}
	for _, f := range m.Files {
		files[f.Path] = f
	}
	fr := Frame{Hash: hash, Header: map[string]any{}, Files: map[string]FileInfo{}}
	if f, ok := files[SegmentsHeader]; ok {
		if err := readJSON(store, f.Hash, 16<<20, &fr.Header); err != nil {
			return Frame{}, fmt.Errorf("segments artifact %s: %s: %w", hash, SegmentsHeader, err)
		}
	}
	if fm, _ := fr.Header["format"].(string); fm != "" && fm != SegmentsFormat {
		return Frame{}, fmt.Errorf("segments artifact %s: format %q is not %s", hash, fm, SegmentsFormat)
	}
	rows, ok := files[SegmentsRows]
	if !ok {
		return Frame{}, fmt.Errorf("segments artifact %s has no %s", hash, SegmentsRows)
	}
	err = eachLine(store, rows.Hash, func(n int, line []byte) error {
		var r Row
		if err := json.Unmarshal(line, &r); err != nil {
			return fmt.Errorf("%s line %d: %w", SegmentsRows, n, err)
		}
		fr.Rows = append(fr.Rows, r)
		return nil
	})
	if err != nil {
		return Frame{}, fmt.Errorf("segments artifact %s: %w", hash, err)
	}
	if f, ok := files[SegmentsFiles]; ok {
		err := eachLine(store, f.Hash, func(n int, line []byte) error {
			var fi FileInfo
			if err := json.Unmarshal(line, &fi); err != nil {
				return fmt.Errorf("%s line %d: %w", SegmentsFiles, n, err)
			}
			if fi.URI != "" {
				fr.Files[fi.URI] = fi
			}
			return nil
		})
		if err != nil {
			return Frame{}, fmt.Errorf("segments artifact %s: %w", hash, err)
		}
	}
	return fr, nil
}

func readJSON(store *cas.Store, hash string, limit int64, v any) error {
	f, err := store.Open(hash)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func eachLine(store *cas.Store, hash string, fn func(n int, line []byte) error) error {
	f, err := store.Open(hash)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxRowBytes)
	for n := 1; sc.Scan(); n++ {
		line := sc.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		if err := fn(n, line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// Strata dimensions (the contract's BatchStratum).
const (
	StratumCampaign   = "campaign"
	StratumMonth      = "month"
	StratumDuration   = "duration"
	StratumConfidence = "confidence"
)

// AllStrata is the default stratification.
var AllStrata = []string{StratumCampaign, StratumMonth, StratumDuration, StratumConfidence}

// Edges are the bucket edges of the duration and confidence strata.
type Edges struct {
	Duration   []float64
	Confidence []float64
}

func bucket(v float64, edges []float64, unit string) string {
	lo := 0.0
	for _, e := range edges {
		if v < e {
			return fmtNum(lo) + "–" + fmtNum(e) + unit
		}
		lo = e
	}
	return "≥" + fmtNum(lo) + unit
}

func fmtNum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

// strataOf is a row's value on each dimension of dims.
func strataOf(r Row, dims []string, e Edges) map[string]string {
	out := make(map[string]string, len(dims))
	for _, d := range dims {
		switch d {
		case StratumCampaign:
			v := r.str("campaign")
			if v == "" {
				if u, err := mounts.ParseURI(r.file()); err == nil {
					v = path.Base(path.Dir(u.Path))
				}
			}
			if v == "" || v == "." {
				v = "unknown"
			}
			out[d] = v
		case StratumMonth:
			v := r.str("month")
			if v == "" {
				if rec := r.str("recorded"); len(rec) >= 7 {
					v = rec[:7]
				}
			}
			if v == "" {
				v = "unknown"
			}
			out[d] = v
		case StratumDuration:
			dur, ok := r.num("duration")
			if !ok {
				a, b := r.span()
				dur = b - a
			}
			out[d] = bucket(dur, e.Duration, " s")
		case StratumConfidence:
			c, ok := r.num("confidence")
			if !ok {
				out[d] = "none"
			} else {
				out[d] = bucket(c, e.Confidence, "")
			}
		}
	}
	return out
}

func keyOf(m map[string]string, dims []string) string {
	parts := make([]string, len(dims))
	for i, d := range dims {
		parts[i] = d + "=" + m[d]
	}
	return strings.Join(parts, "|")
}

// Stratum is one stratum of a sample.
type Stratum struct {
	Key     map[string]string `json:"key"`
	Frame   int               `json:"frame"`
	Sampled int               `json:"sampled"`
}

// Sample is a drawn sample: the rows (in the frame's file and time order) and the strata.
type Sample struct {
	Rows   []Row
	Strata []Stratum
	// Candidates is the number of the frame's rows of the role.
	Candidates int
}

func seededRand(seed uint64, key string) *rand.Rand {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return rand.New(rand.NewPCG(seed, h.Sum64())) //nolint:gosec // a reproducible sample, not a secret
}

// Draw samples n rows of role from the frame, stratified over dims: each stratum gets its share of n (largest
// remainder), at least one when n reaches the number of strata, drawn reproducibly from seed. Rows without a hash or a
// mount URI are not candidates.
func Draw(fr Frame, role string, dims []string, n int, seed uint64, e Edges) Sample {
	var cand []Row
	for _, r := range fr.Rows {
		if r.str("role") != role && !(role == "mono" && r.str("role") == "") {
			continue
		}
		if !steps.ValidHash(r.str("hash")) || !strings.HasPrefix(r.str("uri"), mounts.Scheme) {
			continue
		}
		cand = append(cand, r)
	}
	s := Sample{Candidates: len(cand)}
	if len(cand) == 0 || n <= 0 {
		return s
	}
	groups := map[string][]Row{}
	keys := map[string]map[string]string{}
	for _, r := range cand {
		m := strataOf(r, dims, e)
		k := keyOf(m, dims)
		groups[k] = append(groups[k], r)
		keys[k] = m
	}
	order := make([]string, 0, len(groups))
	for k := range groups {
		order = append(order, k)
	}
	sort.Strings(order)
	n = min(n, len(cand))
	quota := allocate(order, groups, n)
	for _, k := range order {
		rows := groups[k]
		rows = slices.Clone(rows)
		rng := seededRand(seed, k)
		rng.Shuffle(len(rows), func(i, j int) { rows[i], rows[j] = rows[j], rows[i] })
		take := rows[:quota[k]]
		s.Rows = append(s.Rows, take...)
		s.Strata = append(s.Strata, Stratum{Key: keys[k], Frame: len(groups[k]), Sampled: len(take)})
	}
	sort.SliceStable(s.Rows, func(i, j int) bool {
		fi, fj := s.Rows[i].file(), s.Rows[j].file()
		if fi != fj {
			return fi < fj
		}
		ai, _ := s.Rows[i].span()
		aj, _ := s.Rows[j].span()
		return ai < aj
	})
	return s
}

// allocate gives each stratum its proportional share of n by largest remainder, with at least one per stratum when n
// covers every stratum, never more than a stratum holds.
func allocate(order []string, groups map[string][]Row, n int) map[string]int {
	total := 0
	for _, k := range order {
		total += len(groups[k])
	}
	quota := make(map[string]int, len(order))
	type rem struct {
		k string
		r float64
	}
	var rems []rem
	used := 0
	for _, k := range order {
		exact := float64(n) * float64(len(groups[k])) / float64(total)
		q := int(math.Floor(exact))
		if q == 0 && n >= len(order) {
			q = 1
		}
		q = min(q, len(groups[k]))
		quota[k] = q
		used += q
		rems = append(rems, rem{k, exact - math.Floor(exact)})
	}
	sort.SliceStable(rems, func(i, j int) bool { return rems[i].r > rems[j].r })
	for used < n {
		moved := false
		for _, r := range rems {
			if used >= n {
				break
			}
			if quota[r.k] < len(groups[r.k]) {
				quota[r.k]++
				used++
				moved = true
			}
		}
		if !moved {
			break
		}
	}
	for used > n { // the minimum of one per stratum overshot: take back from the largest quotas
		big := ""
		for _, k := range order {
			if quota[k] > 1 && (big == "" || quota[k] > quota[big]) {
				big = k
			}
		}
		if big == "" {
			break
		}
		quota[big]--
		used--
	}
	return quota
}

// doubles picks round(share × n) of the sample's hashes for double annotation, reproducibly from seed.
func doubles(rows []Row, share float64, seed uint64) map[string]bool {
	k := int(math.Round(share * float64(len(rows))))
	if k <= 0 {
		return map[string]bool{}
	}
	type scored struct {
		h string
		s uint64
	}
	list := make([]scored, 0, len(rows))
	for _, r := range rows {
		h := fnv.New64a()
		_, _ = h.Write([]byte(strconv.FormatUint(seed, 10) + "/" + r.str("hash")))
		list = append(list, scored{r.str("hash"), h.Sum64()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].s < list[j].s })
	out := make(map[string]bool, k)
	for _, x := range list[:min(k, len(list))] {
		out[x.h] = true
	}
	return out
}
