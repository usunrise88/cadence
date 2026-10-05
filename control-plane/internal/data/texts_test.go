package data

import "testing"

func TestTruncateText(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		limit int
		want  string
		cut   bool
	}{
		{"short", "# Card", 10, "# Card", false},
		{"exact", "abcd", 4, "abcd", false},
		{"ascii cut", "abcdef", 4, "abcd", true},
		{"rune split by the cut", "abžc", 3, "ab", true},
		{"three-byte rune split", "a€b", 3, "a", true},
		{"rune at the boundary", "abžc", 4, "abž", true},
		{"invalid bytes replaced", "a\xffb", 10, "a�b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, cut := TruncateText([]byte(tt.in), tt.limit)
			if got != tt.want || cut != tt.cut {
				t.Errorf("TruncateText(%q, %d) = %q, %v; want %q, %v", tt.in, tt.limit, got, cut, tt.want, tt.cut)
			}
		})
	}
}
