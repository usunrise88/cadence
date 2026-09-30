package layout

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/policy"
	"github.com/usunrise88/cadence/control-plane/templates"
)

func renderer(t *testing.T) Renderer {
	t.Helper()
	presets, err := policy.EmbeddedPresets()
	if err != nil {
		t.Fatal(err)
	}
	return Renderer{Tree: templates.FS, Presets: presets, Tools: []policy.Operation{
		{Name: "projects.get", VerbClass: "read"}, {Name: "projects.note", VerbClass: "mutate"},
		{Name: "approvals.approve", VerbClass: "mutate"},
	}}
}

func facts() Facts {
	return Facts{
		Name: "Hebrew telephony", Slug: "hebrew", Locales: []string{"he-IL"}, Domain: "telephony",
		BaseModel: BaseModel{VersionID: "ver_1", Collection: "base-model/nemotron", Version: "2026-09-29.abcdefabcdef",
			Repo: "nvidia/nemotron-3.5-asr-streaming-0.6b", Revision: "ea30d66d", Licence: "openmdw-1.1"},
		Agent: Agent{Driver: "claude-code", Model: "sonnet", OpencodeModel: "minimax/MiniMax-M2",
			PermissionPreset: "guardrails-default", InstructionsTemplate: "default", AutoMerge: "when-clean",
			DraftPolicy: map[string]string{"mix": "draft", "gate": "draft", "note": "direct", "language_pack": "draft"}},
		Repo:      Repo{Kind: "internal", CloneURL: "/git/hebrew.git"},
		Budgets:   Budgets{GPUHoursPerDay: 8, AgentTokensPerDay: 20000000},
		Lock:      []Locked{{Kind: "base_model", Collection: "base-model/nemotron", Version: "2026-09-29.abcdefabcdef", ID: "ver_1"}},
		Templates: []Locked{{Kind: "template", Collection: "template/skill-cadence-data", Version: "2026-09-29.000000000000", ID: "ver_2"}},
	}
}

func TestBootstrapFiles(t *testing.T) {
	r := renderer(t)
	files, err := r.Bootstrap(facts())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{ProjectYAML, AgentsMD, ClaudeMD, NotesMD, DataLock, ClaudeSettings, OpencodeJSON,
		".claude/skills/cadence-data/SKILL.md", ".claude/skills/cadence-train/SKILL.md", "pipelines/train-stage.yaml"} {
		if _, ok := files[p]; !ok {
			t.Errorf("missing %s (have %v)", p, files.Paths())
		}
	}
	if string(files[ClaudeMD]) != "@AGENTS.md\n" {
		t.Errorf("CLAUDE.md = %q", files[ClaudeMD])
	}
	agents := string(files[AgentsMD])
	for _, want := range []string{"# Hebrew telephony", "`hebrew`", "he-IL", "ea30d66d", "NOTES.md", "8 GPU-hours"} {
		if !strings.Contains(agents, want) {
			t.Errorf("AGENTS.md lacks %q:\n%s", want, agents)
		}
	}

	var settings map[string]any
	if err := json.Unmarshal(files[ClaudeSettings], &settings); err != nil {
		t.Fatalf(".claude/settings.json is not JSON: %v\n%s", err, files[ClaudeSettings])
	}
	if _, ok := settings["mcpServers"]; ok {
		t.Error(".claude/settings.json carries an MCP section (R2)")
	}
	perm, _ := settings["permissions"].(map[string]any)
	allow, _ := perm["allow"].([]any)
	deny, _ := perm["deny"].([]any)
	if !contains(allow, "mcp__cadence__projects.note") || !contains(deny, "mcp__cadence__approvals.approve") {
		t.Errorf("permissions not rendered from the preset: %v", perm)
	}

	var oc map[string]any
	if err := json.Unmarshal(files[OpencodeJSON], &oc); err != nil {
		t.Fatalf("opencode.json is not JSON: %v\n%s", err, files[OpencodeJSON])
	}
	if oc["model"] != "minimax/MiniMax-M2" || oc["permission"] == nil || oc["mcp"] != nil {
		t.Errorf("opencode.json = %v", oc)
	}

	var py struct {
		Slug      string   `yaml:"slug"`
		Locales   []string `yaml:"locales"`
		BaseModel struct {
			Revision string `yaml:"revision"`
		} `yaml:"base_model"`
		Agent struct {
			DraftPolicy map[string]string `yaml:"draft_policy"`
		} `yaml:"agent"`
		Budgets struct {
			AgentTokensPerDay int64 `yaml:"agent_tokens_per_day"`
		} `yaml:"budgets"`
	}
	if err := yaml.Unmarshal(files[ProjectYAML], &py); err != nil {
		t.Fatal(err)
	}
	if py.Slug != "hebrew" || py.Locales[0] != "he-IL" || py.BaseModel.Revision != "ea30d66d" ||
		py.Agent.DraftPolicy["mix"] != "draft" || py.Budgets.AgentTokensPerDay != 20000000 {
		t.Errorf("project.yaml = %+v\n%s", py, files[ProjectYAML])
	}
	if !strings.Contains(string(files[DataLock]), "id: ver_1") || !strings.Contains(string(files[DataLock]), "template/skill-cadence-data") {
		t.Errorf("data.lock:\n%s", files[DataLock])
	}

	// Rendering is deterministic: the same facts give the same bytes.
	again, err := r.Render(facts())
	if err != nil {
		t.Fatal(err)
	}
	for p, b := range again {
		if string(files[p]) != string(b) {
			t.Errorf("%s renders differently the second time", p)
		}
	}
}

func TestCustomInstructionsAndPresets(t *testing.T) {
	r := renderer(t)
	f := facts()
	f.Agent.InstructionsTemplate = CustomInstructions
	files, err := r.Render(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files[AgentsMD]; ok {
		t.Error("custom instructions were re-rendered")
	}
	f.Agent.InstructionsTemplate = "minimal"
	f.Agent.PermissionPreset = "read-only"
	files, err = r.Render(f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(files[AgentsMD]), "Cadence project `hebrew` (he-IL)") {
		t.Errorf("minimal AGENTS.md:\n%s", files[AgentsMD])
	}
	if !strings.Contains(string(files[ClaudeSettings]), `"dontAsk"`) {
		t.Errorf("read-only preset not rendered:\n%s", files[ClaudeSettings])
	}
	f.Agent.PermissionPreset = "nope"
	if _, err := r.Render(f); err == nil {
		t.Error("rendered with an unknown preset")
	}
	f.Agent.PermissionPreset, f.Agent.InstructionsTemplate = "guardrails-default", "../x"
	if _, err := r.Render(f); err == nil {
		t.Error("rendered an instructions template outside the tree")
	}
	names, err := r.Instructions()
	if err != nil || strings.Join(names, ",") != "default,minimal" {
		t.Errorf("instructions = %v, %v", names, err)
	}
}

func TestAppendNote(t *testing.T) {
	d1 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	n := AppendNote(nil, d1, "Batch 32 OOMs at the 24 GB cap.")
	n = AppendNote(n, d1, "Use bucketing.\nIt halves padding.")
	n = AppendNote(n, d1.AddDate(0, 0, 1), "Second day.")
	got := string(n)
	if strings.Count(got, "## 2026-09-30") != 1 || !strings.Contains(got, "## 2026-10-01\n\n- Second day.\n") ||
		!strings.Contains(got, "- Use bucketing.\n  It halves padding.\n") || !strings.HasPrefix(got, "# Notes") {
		t.Errorf("NOTES.md:\n%s", got)
	}
}

func contains(list []any, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
