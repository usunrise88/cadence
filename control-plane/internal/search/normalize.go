package search

import (
	"strings"
	"unicode"
)

// Text is normalised the same way when it is indexed and when it is searched, so a phrase is found however it was
// written (docs/spec/11-ui-panels.md "Query language"): lower case, then every locale folder whose script appears
// in the text. Folders are chosen by script, not by the document's locale, because a query rarely says its
// language and the folds are harmless to text in other scripts.

// Folder is the per-locale normalisation hook: a locale, a test for the script it handles, and the fold.
type Folder struct {
	Locale  string
	Applies func(r rune) bool
	Fold    func(s string) string
}

// folders is the table of locale folders. A new locale adds a row.
var folders = []Folder{
	{Locale: "he", Applies: isHebrewLetter, Fold: foldHebrew},
}

// Normalize lowercases s and applies the folders of the scripts it contains.
func Normalize(s string) string {
	s = strings.ToLower(s)
	for _, f := range folders {
		if strings.IndexFunc(s, f.Applies) >= 0 {
			s = f.Fold(s)
		}
	}
	return s
}

func isHebrewLetter(r rune) bool { return r >= 0x05D0 && r <= 0x05EA }

// hebrewMark reports the Hebrew points and cantillation marks that are stripped: U+0591–U+05BD (cantillation and
// vowel points), U+05BF (rafe), U+05C1–U+05C2 (shin and sin dots), U+05C4–U+05C5, U+05C7 (qamats qatan), and the
// geresh and gershayim of abbreviations (U+05F3, U+05F4).
func hebrewMark(r rune) bool {
	switch {
	case r >= 0x0591 && r <= 0x05BD, r == 0x05BF, r == 0x05C1, r == 0x05C2, r == 0x05C4, r == 0x05C5, r == 0x05C7:
		return true
	case r == 0x05F3, r == 0x05F4:
		return true
	}
	return false
}

// hebrewSpelling folds ktiv male (plene) and ktiv haser (defective) spellings of the same word to one form.
//
// Cadence recommendation, a stub: a short list of frequent words whose two spellings are both common and never
// mean anything else. The full treatment — the Academy's ktiv male rules applied to a lexicon, so any word folds —
// belongs to the Hebrew language pack (phase 3), which then replaces this list. Removing every medial vav and yod
// is not done: it merges unrelated words (חודש "month" and חדש "new").
var hebrewSpelling = map[string]string{
	"תוכנית":  "תכנית",
	"תוכניות": "תכניות",
	"מילה":    "מלה",
	"מילים":   "מלים",
	"סיפור":   "ספור",
	"דיבור":   "דבור",
	"חיבור":   "חבור",
	"צהריים":  "צהרים",
	"אמיתי":   "אמתי",
	"שיחזור":  "שחזור",
}

// foldHebrew strips points and cantillation, turns maqaf and paseq into spaces and folds spelling variants.
func foldHebrew(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case hebrewMark(r):
		case r == 0x05BE || r == 0x05C0 || r == 0x05C3 || r == 0x05C6: // maqaf, paseq, sof pasuq, nun hafukha
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return foldWords(b.String(), hebrewSpelling)
}

// foldWords replaces every whole word of s found in table, keeping the separators.
func foldWords(s string, table map[string]string) string {
	var b strings.Builder
	b.Grow(len(s))
	start := -1
	flush := func(end int) {
		if start < 0 {
			return
		}
		w := s[start:end]
		if f, ok := table[w]; ok {
			w = f
		}
		b.WriteString(w)
		start = -1
	}
	for i, r := range s {
		if isWordRune(r) {
			if start < 0 {
				start = i
			}
			continue
		}
		flush(i)
		b.WriteRune(r)
	}
	flush(len(s))
	return b.String()
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// Words splits normalised text into the words the full-text index sees: runs of letters and digits.
func Words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return !isWordRune(r) })
}
