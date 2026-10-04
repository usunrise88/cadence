package sessions

import (
	"slices"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/repos"
)

// GuardedPaths are the project-repository paths an agent session's branch never auto-merges into main: the
// promotion gate (gates.yaml), the language packs (lang/), the project settings (project.yaml), the lockfile
// (data.lock: written by projects.adopt, which runs the adoption checks; an edited copy adopts nothing) and the
// agents' own configuration and permission rules (.claude/, opencode.json). A branch touching one waits for a person to accept
// it (agentSessions.accept), whatever the profile's autoMerge says — the guardrail in docs/spec/05-agents.md
// ("Freeze a golden set, change a baseline or gate": an agent's edit of gates.yaml reaches main only when a person
// accepts the session changes). An entry ending in "/" guards the directory and everything under it.
var GuardedPaths = []string{"gates.yaml", "lang/", "project.yaml", "data.lock", ".claude/", "opencode.json"}

// GuardedFiles returns the paths among files that GuardedPaths names, sorted.
func GuardedFiles(files []repos.FileDiff) []string {
	var out []string
	for _, f := range files {
		if guarded(f.Path) && !slices.Contains(out, f.Path) {
			out = append(out, f.Path)
		}
	}
	slices.Sort(out)
	return out
}

func guarded(path string) bool {
	path = strings.TrimPrefix(path, "./")
	for _, g := range GuardedPaths {
		if dir, ok := strings.CutSuffix(g, "/"); ok {
			if path == dir || strings.HasPrefix(path, g) {
				return true
			}
		} else if path == g {
			return true
		}
	}
	return false
}
