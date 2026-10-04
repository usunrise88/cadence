package annotation

import (
	"strings"
	"unicode"

	"github.com/usunrise88/cadence/control-plane/internal/textnorm"
)

// Words is a transcript as the agreement compares it: lower case, punctuation and marks gone, split at white space —
// the neutral fold every scoring normalizer starts from (internal/textnorm), so two annotators who differ only in
// casing or punctuation agree.
func Words(s string) []string {
	s = strings.ToLower(textnorm.StripPunctuation(s))
	return strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) })
}

// Edits is the word-level Levenshtein distance between ref and hyp (substitutions, deletions and insertions).
func Edits(ref, hyp []string) int {
	if len(ref) == 0 {
		return len(hyp)
	}
	prev := make([]int, len(hyp)+1)
	cur := make([]int, len(hyp)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ref); i++ {
		cur[0] = i
		for j := 1; j <= len(hyp); j++ {
			sub := prev[j-1]
			if ref[i-1] != hyp[j-1] {
				sub++
			}
			cur[j] = min(sub, prev[j]+1, cur[j-1]+1)
		}
		prev, cur = cur, prev
	}
	return prev[len(hyp)]
}

// WER is the word error rate of hyp against ref after Words; an empty reference scores 0 against an empty
// hypothesis and 1 against anything else.
func WER(ref, hyp string) float64 {
	r, h := Words(ref), Words(hyp)
	if len(r) == 0 {
		if len(h) == 0 {
			return 0
		}
		return 1
	}
	return float64(Edits(r, h)) / float64(len(r))
}
