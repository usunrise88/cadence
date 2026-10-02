package textnorm

import (
	"strings"
	"testing"
)

func basic() Spec {
	return Spec{Locale: "*", Unicode: FormNFC, Casefold: true, Punctuation: PunctStrip, RemoveMarks: false,
		Mappings: []Mapping{}, Numbers: NumbersKeep}
}

func TestApply(t *testing.T) {
	hebrew := basic()
	hebrew.Locale, hebrew.RemoveMarks = "he-IL", true
	hebrew.Mappings = []Mapping{{From: "׳", To: ""}, {From: "״", To: ""}, {From: "'", To: ""}, {From: "\"", To: ""}}

	keep := basic()
	keep.Punctuation, keep.Casefold = PunctKeep, false

	nfkc := basic()
	nfkc.Unicode = FormNFKC

	ordered := basic()
	ordered.Mappings = []Mapping{{From: "a", To: "b"}, {From: "b", To: "c"}}

	marks := basic()
	marks.RemoveMarks = true

	tests := []struct {
		name string
		spec Spec
		in   string
		want string
	}{
		{"lower case and punctuation", basic(), "Hello, World!", "hello world"},
		{"whitespace collapses", basic(), "  a \t b\n\nc  ", "a b c"},
		{"full case folding", basic(), "Straße", "strasse"},
		{"accents stay without removeMarks", basic(), "Café", "café"},
		{"accents go with removeMarks", marks, "Café naïve", "cafe naive"},
		{"decomposed input composes", basic(), "Café", "café"},
		{"NFKC folds compatibility forms", nfkc, "ﬁve", "five"},
		{"NFC keeps compatibility forms", keep, "ﬁve", "ﬁve"},
		{"case folding expands the ligature", basic(), "ﬁve", "five"},
		{"mappings apply in order", ordered, "a", "c"},
		{"keep punctuation and case", keep, "Hello,  World!", "Hello, World!"},
		{"Hebrew niqqud and shin dot", hebrew, "שָׁלוֹם", "שלום"},
		{"Hebrew cantillation", hebrew, "בְּרֵאשִׁ֖ית", "בראשית"},
		{"Hebrew maqaf splits", hebrew, "בית־ספר", "בית ספר"},
		{"Hebrew geresh mapped away", hebrew, "צ׳יפס", "ציפס"},
		{"Hebrew gershayim in an acronym", hebrew, "צה״ל", "צהל"},
		{"ASCII stand-ins for geresh", hebrew, "צ'יפס צה\"ל", "ציפס צהל"},
		{"Hebrew sof pasuq", hebrew, "סוף׃", "סוף"},
		{"Serbian Cyrillic folds case", basic(), "Ђорђе ЉУБА", "ђорђе љуба"},
		{"digits stay", basic(), "Room 101.", "room 101"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := New(tt.spec)
			if err != nil {
				t.Fatal(err)
			}
			if got := n.Apply(tt.in); got != tt.want {
				t.Fatalf("Apply(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFoldKeepsPunctuation(t *testing.T) {
	s := basic()
	s.RemoveMarks = true
	n, err := New(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Fold("Ver_01 he-IL שָׁלוֹם"); got != "ver_01 he-il שלום" {
		t.Fatalf("Fold = %q", got)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantErr string
	}{
		{"valid", `{"locale":"he-IL","unicode":"NFC","casefold":true,"punctuation":"strip","removeMarks":true,"mappings":[{"from":"׳","to":""}],"numbers":"keep"}`, ""},
		{"bad form", `{"locale":"*","unicode":"NFD","casefold":true,"punctuation":"strip","removeMarks":true,"mappings":[],"numbers":"keep"}`, "unicode"},
		{"bad punctuation", `{"locale":"*","unicode":"NFC","casefold":true,"punctuation":"drop","removeMarks":true,"mappings":[],"numbers":"keep"}`, "punctuation"},
		{"numbers", `{"locale":"*","unicode":"NFC","casefold":true,"punctuation":"strip","removeMarks":true,"mappings":[],"numbers":"spoken"}`, "numbers"},
		{"empty from", `{"locale":"*","unicode":"NFC","casefold":true,"punctuation":"strip","removeMarks":true,"mappings":[{"from":"","to":"x"}],"numbers":"keep"}`, "mappings[0]"},
		{"not JSON", `nope`, "normalizer payload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.payload))
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("unexpected error %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("error %v, want one about %q", err, tt.wantErr)
			}
		})
	}
}
