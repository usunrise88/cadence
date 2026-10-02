package search

import (
	"context"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/storage"
	"github.com/usunrise88/cadence/control-plane/internal/textnorm"
)

// folder answers one normalizer for one locale, as a language pack would.
type folder struct {
	locale string
	n      *textnorm.Normalizer
	asked  []string
}

func (f *folder) For(_ context.Context, _ storage.Querier, projectID, locale string) (*textnorm.Normalizer, error) {
	f.asked = append(f.asked, projectID+"|"+locale)
	if strings.EqualFold(locale, f.locale) || strings.HasPrefix(strings.ToLower(f.locale), strings.ToLower(locale)+"-") {
		return f.n, nil
	}
	return nil, nil
}

func TestFoldFor(t *testing.T) {
	n, err := textnorm.New(textnorm.Spec{Locale: "he-IL", Unicode: textnorm.FormNFC, Casefold: true,
		Punctuation: textnorm.PunctStrip, RemoveMarks: true, Numbers: textnorm.NumbersKeep,
		Mappings: []textnorm.Mapping{{From: "״", To: ""}, {From: "צהרים", To: "צהריים"}}})
	if err != nil {
		t.Fatal(err)
	}
	f := &folder{locale: "he-IL", n: n}
	ctx := context.Background()
	tests := []struct {
		name, lang, text, want string
	}{
		{"script picks the Hebrew normalizer", "", "שִׂיחַת צה״ל בצהרים", "שיחת צהל בצהריים"},
		{"lang picks it too", "he-IL", "צה״ל", "צהל"},
		{"punctuation stays for search", "he-IL", "he-IL ver_01", "he-il ver_01"},
		{"no normalizer for English: Normalize alone", "", "Fleurs-HE", "fleurs-he"},
		{"unknown locale: Normalize alone", "ru-RU", "Привет", "привет"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fold, err := FoldFor(ctx, nil, f, "prj_x", tt.lang, tt.text)
			if err != nil {
				t.Fatal(err)
			}
			if got := fold(tt.text); got != tt.want {
				t.Fatalf("fold(%q) = %q, want %q", tt.text, got, tt.want)
			}
		})
	}
	if fold, _ := FoldFor(ctx, nil, nil, "", "he-IL", "צה״ל"); fold("שָׁלוֹם") != "שלום" {
		t.Fatal("without a folder FoldFor is Normalize")
	}

	q, err := Parse(`צה״ל "בצהרים היום" kind:job`, map[string]string{"job": "job"})
	if err != nil {
		t.Fatal(err)
	}
	fold, err := FoldFor(ctx, nil, f, "prj_x", "", q.Text)
	if err != nil {
		t.Fatal(err)
	}
	q.Refold(fold)
	if len(q.Terms) != 2 || q.Terms[0].Text != "צהל" || q.Terms[1].Text != "בצהריים היום" || !q.Terms[1].Phrase {
		t.Fatalf("refolded terms %+v", q.Terms)
	}
}
