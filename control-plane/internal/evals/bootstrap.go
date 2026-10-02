package evals

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
)

// Significance is how a delta's confidence interval is computed (R54): Bisani & Ney's bootstrap with whole calls,
// speakers or utterances as the resampling unit.
type Significance struct {
	Samples int     `json:"samples"`
	Level   float64 `json:"level"`
	Seed    int     `json:"seed"`
}

// Interval is a point estimate with its percentile interval.
type Interval struct {
	Value float64 `json:"value"`
	Low   float64 `json:"low"`
	High  float64 `json:"high"`
}

// Excludes reports whether the interval lies wholly above or below zero.
func (i Interval) Excludes() bool { return i.Low > 0 || i.High < 0 }

// Group is one resampling unit of a paired comparison: the errors both models made on its utterances and the
// reference words they share.
type Group struct {
	Key                       string
	RefWords                  int
	SubjectErrors, BaseErrors int // substitutions + deletions + insertions
	SubjectDel, BaseDel       int
	SubjectIns, BaseIns       int
}

// Comparison is a paired blockwise bootstrap of subject − baseline: WER, deletion rate and insertion rate deltas.
type Comparison struct {
	WER    Interval
	Del    Interval
	Ins    Interval
	Groups int
}

// ErrNoWords is Compare's error for groups without a single reference word.
var ErrNoWords = errors.New("the golden set has no reference words")

// Compare runs the paired blockwise bootstrap (Bisani & Ney, ICASSP 2004; resampling whole groups, Liu & Peng,
// arXiv:1912.09508): sig.Samples resamples of len(groups) groups drawn with replacement, each giving the deltas of the
// pooled rates (Σ subject errors − Σ baseline errors) / Σ reference words; the interval is the percentile interval at
// sig.Level (linear interpolation between order statistics). The same groups and significance give the same answer.
func Compare(groups []Group, sig Significance) (Comparison, error) {
	if len(groups) == 0 {
		return Comparison{}, ErrNoWords
	}
	if sig.Samples < 1 || sig.Level <= 0 || sig.Level >= 1 {
		return Comparison{}, fmt.Errorf("bootstrap: samples %d and level %v are out of range", sig.Samples, sig.Level)
	}
	var n, de, dd, di int
	for _, g := range groups {
		n += g.RefWords
		de += g.SubjectErrors - g.BaseErrors
		dd += g.SubjectDel - g.BaseDel
		di += g.SubjectIns - g.BaseIns
	}
	if n == 0 {
		return Comparison{}, ErrNoWords
	}
	rng := rand.New(rand.NewPCG(uint64(sig.Seed), 0x63616465_6e636521)) //nolint:gosec // reproducible resampling, not secrecy
	wer, del, ins := make([]float64, 0, sig.Samples), make([]float64, 0, sig.Samples), make([]float64, 0, sig.Samples)
	for range sig.Samples {
		var sn, se, sd, si int
		for range groups {
			g := groups[rng.IntN(len(groups))]
			sn += g.RefWords
			se += g.SubjectErrors - g.BaseErrors
			sd += g.SubjectDel - g.BaseDel
			si += g.SubjectIns - g.BaseIns
		}
		if sn == 0 { // every drawn group was empty: no rate, the sample counts as no change
			wer, del, ins = append(wer, 0), append(del, 0), append(ins, 0)
			continue
		}
		f := float64(sn)
		wer, del, ins = append(wer, float64(se)/f), append(del, float64(sd)/f), append(ins, float64(si)/f)
	}
	f := float64(n)
	return Comparison{
		WER:    interval(float64(de)/f, wer, sig.Level),
		Del:    interval(float64(dd)/f, del, sig.Level),
		Ins:    interval(float64(di)/f, ins, sig.Level),
		Groups: len(groups),
	}, nil
}

func interval(point float64, samples []float64, level float64) Interval {
	slices.Sort(samples)
	a := (1 - level) / 2
	return Interval{Value: round6(point), Low: round6(Quantile(samples, a)), High: round6(Quantile(samples, 1-a))}
}

// Quantile is the p-quantile of sorted values with linear interpolation between order statistics (type 7, the
// default of R and numpy).
func Quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	h := p * float64(len(sorted)-1)
	lo := int(math.Floor(h))
	if lo >= len(sorted)-1 {
		return sorted[len(sorted)-1]
	}
	return sorted[lo] + (h-float64(lo))*(sorted[lo+1]-sorted[lo])
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// Pair groups the utterances of two scored cells of one golden set for Compare: rows are paired by position when both
// list the same audio in the same order (the scores contract: dataset order), else by audio hash; a row of one side
// without its pair is an error. The group of a pair is its group field (call, else speaker, else audio), else its
// position.
func Pair(subject, baseline []Utterance) ([]Group, error) { return pair(subject, baseline, false) }

// PairChars is Pair at the character level, for golden sets of languages written without spaces between words
// (eval.character_error_languages): a group's units are reference characters and its errors character edits.
// Character-level deletions and insertions are not in the scores, so the Del and Ins deltas stay zero.
func PairChars(subject, baseline []Utterance) ([]Group, error) { return pair(subject, baseline, true) }

func pair(subject, baseline []Utterance, chars bool) ([]Group, error) {
	if len(subject) != len(baseline) {
		return nil, fmt.Errorf("the cells scored %d and %d utterances of one golden set", len(subject), len(baseline))
	}
	byAudio := map[string]int{}
	sameOrder := true
	for i := range subject {
		if subject[i].Audio != baseline[i].Audio {
			sameOrder = false
		}
		byAudio[baseline[i].Audio] = i
	}
	index := map[string]int{}
	var out []Group
	for i, s := range subject {
		j := i
		if !sameOrder {
			var ok bool
			if j, ok = byAudio[s.Audio]; !ok || s.Audio == "" {
				return nil, fmt.Errorf("utterance %d (%s) of the subject has no pair in the baseline's scores", i, s.Audio)
			}
		}
		b := baseline[j]
		key := s.Group
		if key == "" {
			key = s.Audio
		}
		if key == "" {
			key = "#" + strconv.Itoa(i)
		}
		k, seen := index[key]
		if !seen {
			k = len(out)
			index[key] = k
			out = append(out, Group{Key: key})
		}
		g := &out[k]
		if chars {
			g.RefWords += s.RefChars
			g.SubjectErrors += s.CharErrors
			g.BaseErrors += b.CharErrors
			continue
		}
		g.RefWords += s.RefWords
		g.SubjectErrors += s.Sub + s.Del + s.Ins
		g.BaseErrors += b.Sub + b.Del + b.Ins
		g.SubjectDel += s.Del
		g.BaseDel += b.Del
		g.SubjectIns += s.Ins
		g.BaseIns += b.Ins
	}
	return out, nil
}
