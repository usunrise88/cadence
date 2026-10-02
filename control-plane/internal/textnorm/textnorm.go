// Package textnorm interprets a scoring normalizer (the contract's NormalizerPayload, docs/spec/08-resolutions.md
// R21): the text both sides of a WER are compared after. The worker's scorer (wer_score, Python) implements the same
// steps from the same payload; the control plane uses this one where it compares text itself (the search index).
//
// The steps, in order:
//
//  1. Unicode normalisation to the payload's form (NFC or NFKC).
//  2. mappings: literal replacements, applied one after another in the order given (each sees the result of the
//     previous one).
//  3. removeMarks: canonical decomposition (NFD), every nonspacing combining mark (category Mn: Hebrew niqqud and
//     cantillation, Latin accents) removed, then canonical composition (NFC).
//  4. casefold: Unicode full case folding (ß → ss), as Python's str.casefold.
//  5. punctuation strip: every Unicode punctuation character (categories P*) becomes a space.
//  6. Whitespace collapses to single spaces, trimmed at both ends.
//
// numbers is "keep" only (phase 3): numbers compare as written.
package textnorm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// Values of the payload's enums.
const (
	FormNFC  = "NFC"
	FormNFKC = "NFKC"

	PunctKeep  = "keep"
	PunctStrip = "strip"

	NumbersKeep = "keep"
)

// Mapping is one literal replacement.
type Mapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Spec is a NormalizerPayload.
type Spec struct {
	Locale      string    `json:"locale"`
	Unicode     string    `json:"unicode"`
	Casefold    bool      `json:"casefold"`
	Punctuation string    `json:"punctuation"`
	RemoveMarks bool      `json:"removeMarks"`
	Mappings    []Mapping `json:"mappings"`
	Numbers     string    `json:"numbers"`
	Description string    `json:"description,omitempty"`
}

// Parse decodes and checks a NormalizerPayload.
func Parse(payload []byte) (Spec, error) {
	var s Spec
	if err := json.Unmarshal(payload, &s); err != nil {
		return Spec{}, fmt.Errorf("normalizer payload: %w", err)
	}
	return s, s.Check()
}

// Check reports what is wrong with s.
func (s Spec) Check() error {
	var errs []error
	switch s.Unicode {
	case FormNFC, FormNFKC:
	default:
		errs = append(errs, fmt.Errorf("unicode must be NFC or NFKC, not %q", s.Unicode))
	}
	switch s.Punctuation {
	case PunctKeep, PunctStrip:
	default:
		errs = append(errs, fmt.Errorf("punctuation must be keep or strip, not %q", s.Punctuation))
	}
	if s.Numbers != NumbersKeep {
		errs = append(errs, fmt.Errorf("numbers must be keep, not %q", s.Numbers))
	}
	for i, m := range s.Mappings {
		if m.From == "" {
			errs = append(errs, fmt.Errorf("mappings[%d].from is empty", i))
		}
	}
	return errors.Join(errs...)
}

// Normalizer applies one Spec.
type Normalizer struct {
	spec Spec
	form norm.Form
	fold cases.Caser
}

// New returns the normalizer of s; s must check out.
func New(s Spec) (*Normalizer, error) {
	if err := s.Check(); err != nil {
		return nil, err
	}
	n := &Normalizer{spec: s, form: norm.NFC, fold: cases.Fold()}
	if s.Unicode == FormNFKC {
		n.form = norm.NFKC
	}
	return n, nil
}

// Spec returns the normalizer's spec.
func (n *Normalizer) Spec() Spec { return n.spec }

// Apply runs every step on s.
func (n *Normalizer) Apply(s string) string {
	s = n.Fold(s)
	if n.spec.Punctuation == PunctStrip {
		s = StripPunctuation(s)
	}
	return strings.Join(strings.Fields(s), " ")
}

// Fold runs the character-level steps only (Unicode form, mappings, marks, case) and keeps punctuation and spacing.
// The search index uses it: identifiers and quoted phrases stay searchable while the letters fold as scoring
// folds them.
func (n *Normalizer) Fold(s string) string {
	s = n.form.String(s)
	for _, m := range n.spec.Mappings {
		s = strings.ReplaceAll(s, m.From, m.To)
	}
	if n.spec.RemoveMarks {
		s = RemoveMarks(s)
	}
	if n.spec.Casefold {
		s = n.fold.String(s)
	}
	return s
}

// RemoveMarks decomposes s canonically, drops every nonspacing mark (Mn) and recomposes.
func RemoveMarks(s string) string {
	d := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(d))
	for _, r := range d {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return norm.NFC.String(b.String())
}

// StripPunctuation turns every Unicode punctuation character (P*) into a space.
func StripPunctuation(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) {
			return ' '
		}
		return r
	}, s)
}
