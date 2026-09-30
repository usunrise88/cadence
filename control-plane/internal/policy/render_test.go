package policy

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

// The tools the host pre-allows (HostStart.allowedTools) are exactly the MCP allow rules of the rendered
// .claude/settings.json, and never one the server denies.
func TestAgentAllowedMatchesClaudeAllow(t *testing.T) {
	presets, err := EmbeddedPresets()
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range presets {
		var fromFile []string
		for _, r := range RenderClaude(p, fixtureOps).Permissions.Allow {
			if strings.HasPrefix(r, "mcp__") {
				fromFile = append(fromFile, r)
			}
		}
		allowed := AgentAllowed(p, fixtureOps)
		var named []string
		for _, op := range allowed {
			named = append(named, ClaudeMCPTool(op))
			if c, _ := p.ClassOf(op, verbClassOf(op)); c == "" || c == ClassForbidden {
				t.Errorf("%s pre-allows %s, which the server denies", name, op)
			}
		}
		if !slices.Equal(named, fromFile) {
			t.Errorf("%s: pre-allowed %v, settings.json allows %v", name, named, fromFile)
		}
	}
	if got := AgentAllowed(presets["guardrails-default"], fixtureOps); !slices.Contains(got, "mixes.edit") || slices.Contains(got, "secrets.new") {
		t.Errorf("guardrails-default pre-allows %v", got)
	}
}

func verbClassOf(op string) string {
	for _, o := range fixtureOps {
		if o.Name == op {
			return o.VerbClass
		}
	}
	return ""
}
