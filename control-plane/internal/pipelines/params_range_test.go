package pipelines

import (
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestCheckRangePatternAndLength(t *testing.T) {
	prop := func(r map[string]any) *jsonschema.Schema {
		return &jsonschema.Schema{Extra: map[string]any{"x-cadence": map[string]any{"range": r}}}
	}
	name := prop(map[string]any{"pattern": "^[a-z0-9][a-z0-9._-]{0,98}[a-z0-9]$"})
	licence := prop(map[string]any{"minLength": 1.0, "maxLength": 200.0})
	tests := []struct {
		name string
		prop *jsonschema.Schema
		v    any
		want string // a substring of the problem; "" = fine
	}{
		{"a source name", name, "fleurs", ""},
		{"an empty source name (R18)", name, "", "does not match"},
		{"an upper-case source name", name, "FLEURS", "does not match"},
		{"a licence", licence, "CC-BY-4.0", ""},
		{"an empty licence (R18)", licence, "", "shorter than"},
		{"a broken pattern", prop(map[string]any{"pattern": "("}), "x", "does not compile"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkRange(tt.prop, tt.v)
			if (tt.want == "") != (got == "") || !strings.Contains(got, tt.want) {
				t.Errorf("checkRange(%v) = %q, want %q", tt.v, got, tt.want)
			}
		})
	}
}
