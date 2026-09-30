package search

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

var testKinds = Kinds(Sources())

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParse(t *testing.T) {
	tests := []struct {
		q    string
		want Query
	}{
		{"", Query{}},
		{"fleurs hebrew", Query{Text: "fleurs hebrew", Terms: []Term{{Text: "fleurs"}, {Text: "hebrew"}}}},
		{"Fleurs-HE-smoke", Query{Text: "Fleurs-HE-smoke", Terms: []Term{{Text: "fleurs"}, {Text: "he"}, {Text: "smoke"}}}},
		{`"call center" kind:job`, Query{Text: `"call center"`, Terms: []Term{{Text: "call center", Phrase: true}}, Kinds: []string{"job"}}},
		{"kind:dataset,model,job", Query{Kinds: []string{"dataset_version", "base_model", "job"}}},
		{"is:collection", Query{Kinds: []string{"registry_collection"}}},
		{"status:Running state:done", Query{Statuses: []string{"running", "done"}}},
		{"lang:he-IL locale:ru", Query{Langs: []string{"he-il", "ru"}}},
		{"actor:agent actor:usr_admin", Query{Actors: []string{"agent", "usr_admin"}}},
		{"updated:>2026-09-01", Query{Updated: []TimeBound{{From: day("2026-09-02")}}}},
		{"updated:>=2026-09-01", Query{Updated: []TimeBound{{From: day("2026-09-01")}}}},
		{"updated:<2026-09-01", Query{Updated: []TimeBound{{Before: day("2026-09-01")}}}},
		{"updated:<=2026-09-01", Query{Updated: []TimeBound{{Before: day("2026-09-02")}}}},
		{"updated:2026-09-01", Query{Updated: []TimeBound{{From: day("2026-09-01"), Before: day("2026-09-02")}}}},
		{"updated:2026-09-01..2026-09-03", Query{Updated: []TimeBound{{From: day("2026-09-01"), Before: day("2026-09-04")}}}},
		{"project:hebrew", Query{Projects: []string{"hebrew"}}},
		{"scope:all", Query{Scope: "all"}},
		{"scope:ALL", Query{Scope: "all"}},
		{"tag:telephony", Query{Tags: []string{"telephony"}}},
		{`tag:"call center"`, Query{Tags: []string{"call center"}}},
		{"alias:@production alias:baseline", Query{Aliases: []string{"production", "baseline"}}},
		{"wer<10", Query{Numbers: []NumBound{{Field: "wer", Op: "<", Value: 10}}}},
		{"wer<=9.5 cer>=1 hours>2 rev=3", Query{Numbers: []NumBound{
			{Field: "wer", Op: "<=", Value: 9.5}, {Field: "cer", Op: ">=", Value: 1}, {Field: "hours", Op: ">", Value: 2},
			{Field: "rev", Op: "=", Value: 3},
		}}},
		{"wer:<10 progress:0.5", Query{Numbers: []NumBound{{Field: "wer", Op: "<", Value: 10}, {Field: "progress", Op: "=", Value: 0.5}}}},
		{"dur:2..8", Query{Numbers: []NumBound{{Field: "dur", Op: "..", Value: 2, Value2: 8}}}},
		{"wer<12%", Query{Numbers: []NumBound{{Field: "wer", Op: "<", Value: 12}}}},
		{"12:30 meeting", Query{Text: "12:30 meeting", Terms: []Term{{Text: "12"}, {Text: "30"}, {Text: "meeting"}}}},
		{`"http://x.io"`, Query{Text: `"http://x.io"`, Terms: []Term{{Text: "http://x.io", Phrase: true}}}},
		{"שִׂיחָה", Query{Text: "שִׂיחָה", Terms: []Term{{Text: "שיחה"}}}},
		{"תוכנית", Query{Text: "תוכנית", Terms: []Term{{Text: "תכנית"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			got, err := Parse(tt.q, testKinds)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tt.q, err)
			}
			tt.want.Raw = tt.q
			got.Qualifiers = nil // checked in TestParseQualifiers
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%q)\n got %+v\nwant %+v", tt.q, got, tt.want)
			}
		})
	}
}

func TestParseQualifiers(t *testing.T) {
	got, err := Parse(`fleurs kind:job updated:>2026-09-01 wer<10 tag:"a b" state:done`, testKinds)
	if err != nil {
		t.Fatal(err)
	}
	want := []Qualifier{
		{Field: "kind", Op: ":", Value: "job", Raw: "kind:job"},
		{Field: "updated", Op: ">", Value: "2026-09-01", Raw: "updated:>2026-09-01"},
		{Field: "wer", Op: "<", Value: "10", Raw: "wer<10"},
		{Field: "tag", Op: ":", Value: "a b", Raw: `tag:"a b"`},
		{Field: "status", Op: ":", Value: "done", Raw: "state:done"},
	}
	if !reflect.DeepEqual(got.Qualifiers, want) {
		t.Errorf("qualifiers\n got %+v\nwant %+v", got.Qualifiers, want)
	}
	if got.Text != "fleurs" {
		t.Errorf("text = %q", got.Text)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct{ q, detail string }{
		{"knd:job", `unknown qualifier "knd:"`},
		{"foo<3", `unknown field "foo"`},
		{"http://x.io", `unknown qualifier "http:"`},
		{"kind:runz", "kind:runz is not a searchable kind"},
		{"status:>3", "status: takes a value, not a comparison"},
		{"tag:", "tag: needs a value"},
		{"updated:>last-week", "dates are YYYY-MM-DD"},
		{"updated:2026-09-05..2026-09-01", "the range ends before it starts"},
		{"wer<ten", `"ten" is not a number`},
		{"dur:8..2", "the range ends before it starts"},
		{"status<3", "status is not numeric"},
		{"scope:everything", "scope is all, project or registry"},
		{"scope:all scope:project", "contradicts"},
		{"lang:hebrew!", "is not a locale"},
		{"project:Bad_Slug", "is not a project slug"},
		{`"call center`, "a quote is not closed"},
	}
	for _, tt := range tests {
		t.Run(tt.q, func(t *testing.T) {
			_, err := Parse(tt.q, testKinds)
			var pe *problems.Error
			if !errors.As(err, &pe) || pe.Type != problems.InvalidQuery {
				t.Fatalf("Parse(%q) = %v, want invalid-query", tt.q, err)
			}
			if !strings.Contains(pe.Detail, tt.detail) {
				t.Errorf("detail %q does not say %q", pe.Detail, tt.detail)
			}
			if !strings.Contains(pe.Detail, "Qualifiers: ") || !strings.Contains(pe.Detail, "kind:") || !strings.Contains(pe.Detail, "wer") {
				t.Errorf("detail %q does not list the qualifiers", pe.Detail)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Fleurs HE", "fleurs he"},
		{"שָׁלוֹם", "שלום"},                 // niqqud stripped
		{"בְּרֵאשִׁ֖ית", "בראשית"},          // cantillation too
		{"תוכנית חדשה", "תכנית חדשה"},       // ktiv male folded to haser
		{"תכנית", "תכנית"},                  // the haser form stays
		{"חודש חדש", "חודש חדש"},            // no blanket vav/yod removal
		{"צה״ל", "צהל"},                     // gershayim
		{"בית־ספר", "בית ספר"},              // maqaf splits words
		{"program תוכנית", "program תכנית"}, // mixed scripts
		{"Тест", "тест"},                    // other scripts: lower case only
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSnippet(t *testing.T) {
	long := strings.Repeat("lorem ipsum ", 30) + "the fleurs subset " + strings.Repeat("dolor sit ", 30)
	s := snippet(long, []Term{{Text: "fleurs"}})
	if !strings.Contains(s, "fleurs") || !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") {
		t.Errorf("snippet = %q", s)
	}
	if got := snippet("short text", nil); got != "short text" {
		t.Errorf("snippet = %q", got)
	}
}

func TestKindsCoverSources(t *testing.T) {
	for _, s := range Sources() {
		if testKinds[s.Kind] != s.Kind {
			t.Errorf("kind %s is not accepted by kind:", s.Kind)
		}
	}
	if testKinds["dataset"] != "dataset_version" || testKinds["help"] != KindHelp {
		t.Errorf("aliases missing: %v", testKinds)
	}
}
