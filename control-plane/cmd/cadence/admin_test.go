package main

import (
	"bytes"
	"strings"
	"testing"
)

// The paths that fail before touching a database; the reset itself is covered by the server integration tests
// (TestResetPassword).
func TestAdminArguments(t *testing.T) {
	env := func(string) string { return "" }
	tests := []struct {
		name  string
		args  []string
		stdin string
		want  string
	}{
		{"no subcommand", nil, "", "unknown admin command"},
		{"unknown subcommand", []string{"drop-everything"}, "", "unknown admin command"},
		{"unknown flag", []string{"reset-password", "--nope"}, "", "flag provided but not defined"},
		{"short password from stdin", []string{"reset-password"}, "short\n", "at least 12 characters"},
		{"short password flag", []string{"reset-password", "--password", "short"}, "", "at least 12 characters"},
		{"no database", []string{"reset-password"}, "a long enough password\n", "DATABASE_URL is not set"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := admin(t.Context(), tt.args, env, strings.NewReader(tt.stdin), &out)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("admin(%v) = %v, want %q", tt.args, err, tt.want)
			}
		})
	}
}
