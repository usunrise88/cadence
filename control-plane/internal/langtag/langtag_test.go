package langtag

import "testing"

func TestMacro(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"nb-NO", "no"},
		{"nn_NO", "no"},
		{"no", "no"},
		{"NB", "no"},
		{"zsm-MY", "ms"},
		{"arb", "ar"},
		{"arz-EG", "arz"},
		{"he-IL", "he"},
		{"sr-Latn-RS", "sr"},
		{" hr ", "hr"},
		{"", ""},
	} {
		if got := Macro(tc.in); got != tc.want {
			t.Errorf("Macro(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSame(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"nb-NO", "no", true},
		{"nn-NO", "nb-NO", true},
		{"he", "he-IL", true},
		{"sr-RS", "hr-HR", false},
		{"ms", "id", false},
	} {
		if got := Same(tc.a, tc.b); got != tc.want {
			t.Errorf("Same(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
