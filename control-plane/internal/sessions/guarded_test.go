package sessions

import (
	"slices"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

func TestGuardedFiles(t *testing.T) {
	for _, c := range []struct {
		name  string
		files []string
		want  []string
	}{
		{"recipes only", []string{"pipelines/train.yaml", "NOTES.md", "AGENTS.md"}, nil},
		{"the lockfile", []string{"pipelines/train.yaml", "data.lock"}, []string{"data.lock"}},
		{"the gate", []string{"pipelines/a.yaml", "gates.yaml"}, []string{"gates.yaml"}},
		{"a language pack", []string{"lang/he/itn.yaml", "lang/he/normalizer.yaml"}, []string{"lang/he/itn.yaml", "lang/he/normalizer.yaml"}},
		{"project settings", []string{"project.yaml"}, []string{"project.yaml"}},
		{"agent settings", []string{".claude/settings.json", ".claude/skills/x/SKILL.md", "opencode.json"},
			[]string{".claude/settings.json", ".claude/skills/x/SKILL.md", "opencode.json"}},
		{"look-alikes", []string{"pipelines/gates.yaml", "language/x", "lang.md", "docs/project.yaml", ".claudeignore"}, nil},
	} {
		var files []repos.FileDiff
		for _, f := range c.files {
			files = append(files, repos.FileDiff{Path: f, Status: "M"})
		}
		if got := GuardedFiles(files); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
