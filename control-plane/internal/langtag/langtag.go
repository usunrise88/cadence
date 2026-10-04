// Package langtag compares the language of a locale with the languages a base model knows (its locale:<code> tags).
//
// A model card names a macrolanguage where data names one of its members: Nemotron's tag is `no` (Norwegian) while
// FLEURS and most corpora write `nb-NO` (Bokmål) or `nn-NO` (Nynorsk). Model checks fold each primary subtag to its
// macrolanguage (BCP 47 / ISO 639-3 macrolanguage mappings) before comparing, so `nb-NO` data meets a `no` model
// without an explicit languages map (owner decision of 2026-10-04 on the phase-4 gate). The table is deliberately
// small: the members Cadence has met or that corpora commonly tag apart from their macrolanguage.
package langtag

import "strings"

// macro maps an individual language to its macrolanguage (ISO 639-3 macrolanguage mappings, as BCP 47's registry
// records them in its Macrolanguage fields).
var macro = map[string]string{
	"nb": "no", "nn": "no", // Norwegian: Bokmål, Nynorsk
	"zsm": "ms", "zlm": "ms", // Malay: Standard Malay, Malay (individual language)
	"arb": "ar", // Standard Arabic (spoken varieties such as arz or ary are not folded: a model may not know them)
	"cmn": "zh", // Mandarin Chinese (Cantonese, yue, is not folded for the same reason)
	"pes": "fa", // Iranian Persian
	"swh": "sw", // Swahili (individual language)
	"ekk": "et", // Standard Estonian
	"lvs": "lv", // Standard Latvian
}

// Primary is a locale's primary language subtag in lower case (he-IL → he, sr_Latn_RS → sr).
func Primary(locale string) string {
	l, _, _ := strings.Cut(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"), "-")
	return strings.ToLower(l)
}

// Macro is a locale's primary language subtag folded to its macrolanguage (nb-NO → no, arb → ar); a language that is
// no member of one is its own.
func Macro(locale string) string {
	l := Primary(locale)
	if m, ok := macro[l]; ok {
		return m
	}
	return l
}

// Same reports whether two locales are in the same language once folded to macrolanguages (nb-NO ~ no, he ~ he-IL).
func Same(a, b string) bool { return Macro(a) == Macro(b) }
