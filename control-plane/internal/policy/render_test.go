package policy

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

// fixtureOps is a fixed tool list, so the golden files change only when a preset or the renderer does.
var fixtureOps = []Operation{
	{"projects.get", "read"}, {"projects.list", "read"}, {"projects.edit", "mutate"}, {"projects.archive", "mutate"},
	{"jobs.wait", "read"}, {"jobs.cancel", "mutate"}, {"runs.new", "mutate"}, {"mixes.edit", "mutate"},
	{"aliases.set", "mutate"}, {"goldenSets.freeze", "mutate"}, {"approvals.list", "read"},
	{"approvals.approve", "mutate"}, {"secrets.new", "mutate"}, {"sources.archive", "mutate"},
	{"credentials.revoke", "mutate"}, {"help.get", "read"},
}

func TestRenderGolden(t *testing.T) {
	presets, err := EmbeddedPresets()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"guardrails-default", "read-only"} {
		p := presets[name]
		if p == nil {
			t.Fatalf("preset %s missing", name)
		}
		for suffix, v := range map[string]any{
			".claude.json":   RenderClaude(p, fixtureOps),
			".opencode.json": RenderOpencode(p, fixtureOps),
		} {
			t.Run(name+suffix, func(t *testing.T) {
				got, err := JSON(v)
				if err != nil {
					t.Fatal(err)
				}
				golden := filepath.Join("testdata", name+suffix)
				if *update {
					if err := os.WriteFile(golden, got, 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(golden)
				if err != nil {
					t.Fatalf("%v (run go test ./internal/policy -update)", err)
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s differs from the rendering; run go test ./internal/policy -update and review the diff\n%s", golden, got)
				}
			})
		}
	}
}

func TestOpencodeMCPTool(t *testing.T) {
	if got := OpencodeMCPTool("goldenSets.freeze"); got != "cadence_goldenSets_freeze" {
		t.Fatalf("got %q", got)
	}
}

// The agent-side files never allow what the server denies.
func TestRenderNeverWidens(t *testing.T) {
	presets, err := EmbeddedPresets()
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range presets {
		s := RenderClaude(p, fixtureOps)
		allowed := map[string]bool{}
		for _, a := range s.Permissions.Allow {
			allowed[a] = true
		}
		for _, op := range fixtureOps {
			c, _ := p.ClassOf(op.Name, op.VerbClass)
			if (c == "" || c == ClassForbidden) && allowed[ClaudeMCPTool(op.Name)] {
				t.Errorf("%s: %s is denied by the server but allowed in settings.json", name, op.Name)
			}
		}
	}
}
