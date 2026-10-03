package textnorm

import (
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// The normalizers Cadence seeds (internal/registry/fixtures/normalizers.yaml) parse and behave as their
// descriptions say.
func TestSeededNormalizers(t *testing.T) {
	inputs, err := registry.BundledInputs(templates.FS)
	if err != nil {
		t.Fatal(err)
	}
	seeds := map[string]*Normalizer{}
	for _, in := range inputs {
		if in.Kind != registry.KindNormalizer {
			continue
		}
		spec, err := Parse(in.Payload)
		if err != nil {
			t.Fatalf("%s: %v", in.Name, err)
		}
		if seeds[in.Name], err = New(spec); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct{ normalizer, in, want string }{
		{"normalizer/basic", "Hello, World!", "hello world"},
		{"normalizer/basic", "Café", "café"},
		{"normalizer/he-il", "שָׁלוֹם, עוֹלָם!", "שלום עולם"},
		{"normalizer/he-il", "צה״ל וצה\"ל", "צהל וצהל"},
		{"normalizer/he-il", "ג׳ירפה", "גירפה"},
		{"normalizer/he-il", "צה”ל ג’ירפה “מרכאות” ‘ציטוט’", "צהל גירפה מרכאות ציטוט"},
		{"normalizer/he-il", "בית־ספר", "בית ספר"},
		{"normalizer/he-il", "\u200Fשלום\u200E", "שלום"},
		{"normalizer/he-il", "WhatsApp", "whatsapp"},
	}
	for _, tt := range tests {
		n, ok := seeds[tt.normalizer]
		if !ok {
			t.Fatalf("no seeded %s", tt.normalizer)
		}
		if got := n.Apply(tt.in); got != tt.want {
			t.Errorf("%s(%q) = %q, want %q", tt.normalizer, tt.in, got, tt.want)
		}
	}
}
