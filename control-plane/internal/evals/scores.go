package evals

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
)

// The scores artifact (docs/review/2026-10-02-phase-3-plan.md "The scores artifact"): a directory with summary.json
// and utterances.jsonl, written by the scorer step kind (wer_score@1) and read here only.
const (
	ScoresFormat   = "cadence.scores/1"
	SummaryFile    = "summary.json"
	UtterancesFile = "utterances.jsonl"
)

// Summary is the part of summary.json the control plane reads; the whole document is kept as the record's summary.
type Summary struct {
	Schema     string  `json:"schema"`
	Scorer     string  `json:"scorer"`
	Utterances int     `json:"utterances"`
	RefWords   int     `json:"refWords"`
	WER        float64 `json:"wer"`
	CER        float64 `json:"cer"`
	Sub        int     `json:"sub"`
	Del        int     `json:"del"`
	Ins        int     `json:"ins"`
}

// Utterance is one row of utterances.jsonl.
type Utterance struct {
	Audio      string      `json:"audio"`
	Speaker    string      `json:"speaker,omitempty"`
	Group      string      `json:"group,omitempty"`
	DurationS  float64     `json:"durationS"`
	Ref        string      `json:"ref"`
	Hyp        string      `json:"hyp"`
	RefWords   int         `json:"refWords"`
	Sub        int         `json:"sub"`
	Del        int         `json:"del"`
	Ins        int         `json:"ins"`
	RefChars   int         `json:"refChars,omitempty"`
	CharErrors int         `json:"charErrors,omitempty"`
	Ops        [][]*string `json:"ops"`
}

// maxSummary bounds summary.json (buckets and stability only; the rows are in utterances.jsonl).
const maxSummary = 1 << 20

// openFile opens one file of a directory artifact.
func openFile(store *cas.Store, dir, name string) (io.ReadCloser, error) {
	if store == nil {
		return nil, errors.New("no content store is configured")
	}
	m, err := store.ReadManifest(dir)
	if err != nil {
		return nil, fmt.Errorf("scores %s: %w", dir, err)
	}
	for _, f := range m.Files {
		if f.Path == name {
			return store.Open(f.Hash)
		}
	}
	return nil, fmt.Errorf("scores %s has no %s", dir, name)
}

// ReadSummary reads and checks summary.json of a scores artifact: the raw document and its parsed core.
func ReadSummary(store *cas.Store, hash string) (json.RawMessage, Summary, error) {
	f, err := openFile(store, hash, SummaryFile)
	if err != nil {
		return nil, Summary{}, err
	}
	defer func() { _ = f.Close() }()
	b, err := io.ReadAll(io.LimitReader(f, maxSummary+1))
	if err != nil {
		return nil, Summary{}, fmt.Errorf("read %s: %w", SummaryFile, err)
	}
	if len(b) > maxSummary {
		return nil, Summary{}, fmt.Errorf("%s of %s is larger than %d bytes", SummaryFile, hash, maxSummary)
	}
	var s Summary
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, Summary{}, fmt.Errorf("%s of %s: %w", SummaryFile, hash, err)
	}
	if s.Schema != ScoresFormat {
		return nil, Summary{}, fmt.Errorf("%s of %s has schema %q, not %s", SummaryFile, hash, s.Schema, ScoresFormat)
	}
	return b, s, nil
}

// ReadUtterances reads utterances.jsonl of a scores artifact in dataset order.
func ReadUtterances(store *cas.Store, hash string) ([]Utterance, error) {
	f, err := openFile(store, hash, UtterancesFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var out []Utterance
	for line := 1; sc.Scan(); line++ {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var u Utterance
		if err := json.Unmarshal(sc.Bytes(), &u); err != nil {
			return nil, fmt.Errorf("%s of %s line %d: %w", UtterancesFile, hash, line, err)
		}
		out = append(out, u)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s of %s: %w", UtterancesFile, hash, err)
	}
	return out, nil
}

// Worst is one of a cell's worst utterances (the contract's EvalUtterance).
type Worst struct {
	Index     int        `json:"index"`
	Audio     string     `json:"audio"`
	Speaker   string     `json:"speaker,omitempty"`
	Group     string     `json:"group,omitempty"`
	DurationS float64    `json:"durationS,omitempty"`
	Ref       string     `json:"ref"`
	Hyp       string     `json:"hyp"`
	RefWords  int        `json:"refWords"`
	Sub       int        `json:"sub"`
	Del       int        `json:"del"`
	Ins       int        `json:"ins"`
	Errors    int        `json:"errors"`
	WER       float64    `json:"wer"`
	Ops       [][]string `json:"ops"`
}

// WorstOf picks the n utterances with the most word errors (then the highest WER, then dataset order).
func WorstOf(rows []Utterance, n int) []Worst {
	out := make([]Worst, 0, len(rows))
	for i, u := range rows {
		e := u.Sub + u.Del + u.Ins
		w := Worst{Index: i, Audio: u.Audio, Speaker: u.Speaker, Group: u.Group, DurationS: u.DurationS, Ref: u.Ref, Hyp: u.Hyp,
			RefWords: u.RefWords, Sub: u.Sub, Del: u.Del, Ins: u.Ins, Errors: e, Ops: make([][]string, 0, len(u.Ops))}
		if u.RefWords > 0 {
			w.WER = round6(float64(e) / float64(u.RefWords))
		} else if e > 0 {
			w.WER = 1
		}
		for _, op := range u.Ops {
			row := make([]string, len(op))
			for k, s := range op {
				if s != nil {
					row[k] = *s
				}
			}
			w.Ops = append(w.Ops, row)
		}
		out = append(out, w)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Errors != out[j].Errors {
			return out[i].Errors > out[j].Errors
		}
		return out[i].WER > out[j].WER
	})
	if n < len(out) {
		out = out[:n]
	}
	return out
}
