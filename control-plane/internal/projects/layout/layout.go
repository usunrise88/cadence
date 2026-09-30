// Package layout renders the files of a project repository from the project's facts and the bundled templates
// (docs/spec/02-domain-projects-registry.md "Project wizard"; control-plane/templates/README.md): project.yaml,
// AGENTS.md from the instructions template, CLAUDE.md, NOTES.md, data.lock, the agent config files rendered from
// the permission preset (R7; permissions only, no MCP section — R2), the product skills and the starter pipelines.
// It is pure: no git, no database.
package layout

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/usunrise88/cadence/control-plane/internal/policy"
)

// Paths of the files Cadence writes.
const (
	ProjectYAML    = "project.yaml"
	AgentsMD       = "AGENTS.md"
	ClaudeMD       = "CLAUDE.md"
	NotesMD        = "NOTES.md"
	DataLock       = "data.lock"
	ClaudeSettings = ".claude/settings.json"
	OpencodeJSON   = "opencode.json"
	SkillsDir      = ".claude/skills"
	PipelinesDir   = "pipelines"
	PlaybooksDir   = "playbooks"
)

// CustomInstructions is the instructions template of a project whose AGENTS.md was edited by hand: it is never
// re-rendered.
const CustomInstructions = "custom"

// BaseModel is the project's default base model.
type BaseModel struct {
	VersionID  string
	Collection string
	Version    string
	Repo       string // Hugging Face repository
	Revision   string
	Licence    string
}

// Agent is the project's agent profile.
type Agent struct {
	Driver               string
	Model                string
	OpencodeModel        string // opencode.json's model: the profile's when the driver is opencode, else the default
	PermissionPreset     string
	InstructionsTemplate string
	AutoMerge            string
	DraftPolicy          map[string]string
}

// Repo is where the project repository lives.
type Repo struct {
	Kind     string
	CloneURL string
	Remote   string
}

// Budgets are the project's daily budgets.
type Budgets struct {
	GPUHoursPerDay    float64
	AgentTokensPerDay int64
}

// Locked is one registry version the project depends on (data.lock).
type Locked struct {
	Kind       string
	Collection string
	Version    string
	ID         string
}

// Facts is everything the templates and project.yaml are rendered from.
type Facts struct {
	Name        string
	Slug        string
	Description string
	Locales     []string
	Domain      string
	BaseModel   BaseModel
	Agent       Agent
	Repo        Repo
	Budgets     Budgets
	// Lock lists the adopted registry versions; Templates the template versions the files were rendered from.
	Lock      []Locked
	Templates []Locked
}

// Renderer renders repositories from the templates tree with one preset set and one tool list.
type Renderer struct {
	Tree    fs.FS                     // control-plane/templates
	Presets map[string]*policy.Preset // permission presets by name
	Tools   []policy.Operation        // the MCP tools, for the permission rules
}

// Files maps repository paths to content.
type Files map[string][]byte

// Paths returns the paths in order.
func (f Files) Paths() []string {
	out := make([]string, 0, len(f))
	for p := range f {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Pick returns the files at paths (and under directory prefixes ending in /).
func (f Files) Pick(paths ...string) Files {
	out := Files{}
	for p, b := range f {
		for _, want := range paths {
			if p == want || (strings.HasSuffix(want, "/") && strings.HasPrefix(p, want)) {
				out[p] = b
			}
		}
	}
	return out
}

// Instructions lists the instruction templates in the tree (default, minimal, …).
func (r Renderer) Instructions() ([]string, error) { return r.dirs("instructions") }

// Skills lists the product skills in the tree.
func (r Renderer) Skills() ([]string, error) { return r.dirs("skills") }

func (r Renderer) dirs(root string) ([]string, error) {
	entries, err := fs.ReadDir(r.Tree, root)
	if err != nil {
		return nil, fmt.Errorf("read templates/%s: %w", root, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Render returns every file the templates own (all but NOTES.md). AGENTS.md is left out when the instructions are
// custom: it belongs to the person who edited it.
func (r Renderer) Render(f Facts) (Files, error) {
	out := Files{}
	py, err := ProjectFile(f)
	if err != nil {
		return nil, err
	}
	out[ProjectYAML] = py
	out[DataLock] = LockFile(f)
	out[ClaudeMD] = []byte("@AGENTS.md\n")
	if f.Agent.InstructionsTemplate != CustomInstructions {
		b, err := r.AgentsMD(f)
		if err != nil {
			return nil, err
		}
		out[AgentsMD] = b
	}
	cfg, err := r.AgentConfig(f)
	if err != nil {
		return nil, err
	}
	for p, b := range cfg {
		out[p] = b
	}
	if err := r.copyDir("skills", SkillsDir, out); err != nil {
		return nil, err
	}
	if err := r.copyDir("pipelines", PipelinesDir, out); err != nil {
		return nil, err
	}
	if _, err := fs.Stat(r.Tree, "playbooks"); err == nil {
		if err := r.copyDir("playbooks", PlaybooksDir, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Bootstrap is Render plus the first NOTES.md.
func (r Renderer) Bootstrap(f Facts) (Files, error) {
	out, err := r.Render(f)
	if err != nil {
		return nil, err
	}
	out[NotesMD] = []byte(NotesHeader)
	return out, nil
}

// NotesHeader starts NOTES.md.
const NotesHeader = "# Notes\n\n" +
	"<!-- written by Cadence: dated learnings recorded with projects.note, oldest first. Sessions read this file. -->\n"

// data is what the text templates see (documented in control-plane/templates/README.md).
type data struct {
	Name, Slug, Description, Locales, Domain string
	BaseModel                                BaseModel
	Agent                                    agentData
	Repo                                     struct{ URL, Kind string }
	Budgets                                  Budgets
}

type agentData struct {
	Agent
	PermissionPresetClaudeJSON        string
	PermissionPresetClaudeSandboxJSON string
	PermissionPresetOpencodeJSON      string
}

func (r Renderer) data(f Facts) (data, error) {
	d := data{Name: f.Name, Slug: f.Slug, Description: f.Description, Locales: strings.Join(f.Locales, ", "),
		Domain: f.Domain, BaseModel: f.BaseModel, Budgets: f.Budgets}
	d.Repo.Kind = f.Repo.Kind
	d.Repo.URL = f.Repo.Remote
	if d.Repo.URL == "" {
		d.Repo.URL = f.Repo.CloneURL
	}
	preset, ok := r.Presets[f.Agent.PermissionPreset]
	if !ok {
		return data{}, fmt.Errorf("no permission preset %q", f.Agent.PermissionPreset)
	}
	claude := policy.RenderClaude(preset, r.Tools)
	perm, err := indentJSON(claude.Permissions, "  ")
	if err != nil {
		return data{}, err
	}
	var sandbox any = map[string]any{"enabled": false}
	if claude.Sandbox != nil {
		sandbox = claude.Sandbox
	}
	sb, err := indentJSON(sandbox, "  ")
	if err != nil {
		return data{}, err
	}
	oc, err := indentJSON(policy.RenderOpencode(preset, r.Tools), "  ")
	if err != nil {
		return data{}, err
	}
	d.Agent = agentData{Agent: f.Agent, PermissionPresetClaudeJSON: perm, PermissionPresetClaudeSandboxJSON: sb,
		PermissionPresetOpencodeJSON: oc}
	return d, nil
}

func indentJSON(v any, prefix string) (string, error) {
	b, err := policy.JSON(v)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(strings.TrimRight(string(b), "\n"), "\n", "\n"+prefix), nil
}

// AgentsMD renders AGENTS.md from the project's instructions template.
func (r Renderer) AgentsMD(f Facts) ([]byte, error) {
	name := f.Agent.InstructionsTemplate
	if name == "" || name == CustomInstructions || strings.ContainsAny(name, "/.") {
		return nil, fmt.Errorf("no instructions template %q", name)
	}
	return r.render(path.Join("instructions", name, "AGENTS.md.tmpl"), f)
}

// AgentConfig renders .claude/settings.json and opencode.json from the permission preset.
func (r Renderer) AgentConfig(f Facts) (Files, error) {
	out := Files{}
	for dst, src := range map[string]string{
		ClaudeSettings: "agent-config/claude-settings.json.tmpl",
		OpencodeJSON:   "agent-config/opencode.json.tmpl",
	} {
		b, err := r.render(src, f)
		if err != nil {
			return nil, err
		}
		out[dst] = b
	}
	return out, nil
}

func (r Renderer) render(name string, f Facts) ([]byte, error) {
	src, err := fs.ReadFile(r.Tree, name)
	if err != nil {
		return nil, fmt.Errorf("read template %s: %w", name, err)
	}
	t, err := template.New(name).Option("missingkey=error").Parse(string(src))
	if err != nil {
		return nil, fmt.Errorf("parse template %s: %w", name, err)
	}
	d, err := r.data(f)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, d); err != nil {
		return nil, fmt.Errorf("render template %s: %w", name, err)
	}
	return b.Bytes(), nil
}

func (r Renderer) copyDir(src, dst string, out Files) error {
	return fs.WalkDir(r.Tree, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(r.Tree, p)
		if err != nil {
			return err
		}
		out[path.Join(dst, strings.TrimPrefix(p, src+"/"))] = b
		return nil
	})
}

// ---------------------------------------------------------------- project.yaml and data.lock

type projectDoc struct {
	Version     int           `yaml:"version"`
	Name        string        `yaml:"name"`
	Slug        string        `yaml:"slug"`
	Description string        `yaml:"description"`
	Locales     []string      `yaml:"locales,flow"`
	Domain      string        `yaml:"domain"`
	BaseModel   baseModelDoc  `yaml:"base_model"`
	Agent       agentDoc      `yaml:"agent"`
	Repository  repositoryDoc `yaml:"repository"`
	Storage     storageDoc    `yaml:"storage"`
	Budgets     budgetsDoc    `yaml:"budgets"`
}

type baseModelDoc struct {
	VersionID  string `yaml:"version_id"`
	Collection string `yaml:"collection"`
	Version    string `yaml:"version"`
	Repo       string `yaml:"repo"`
	Revision   string `yaml:"revision"`
	Licence    string `yaml:"licence"`
}

type agentDoc struct {
	Driver               string      `yaml:"driver"`
	Model                string      `yaml:"model"`
	PermissionPreset     string      `yaml:"permission_preset"`
	InstructionsTemplate string      `yaml:"instructions_template"`
	AutoMerge            string      `yaml:"auto_merge"`
	DraftPolicy          draftPolicy `yaml:"draft_policy,flow"`
}

type draftPolicy struct {
	Mix          string `yaml:"mix"`
	Gate         string `yaml:"gate"`
	Note         string `yaml:"note"`
	LanguagePack string `yaml:"language_pack"`
}

type repositoryDoc struct {
	Kind     string `yaml:"kind"`
	CloneURL string `yaml:"clone_url"`
	Remote   string `yaml:"remote"`
	Branch   string `yaml:"branch"`
}

type storageDoc struct {
	Mounts []string `yaml:"mounts,flow"`
}

type budgetsDoc struct {
	GPUHoursPerDay    float64 `yaml:"gpu_hours_per_day"`
	AgentTokensPerDay int64   `yaml:"agent_tokens_per_day"`
}

const projectHeader = "# written by Cadence — the project wizard's choices; change them in the Project document or Agent\n" +
	"# settings, which commit this file again. Keys are stable; keep prose out of this file.\n"

// ProjectFile renders project.yaml.
func ProjectFile(f Facts) ([]byte, error) {
	dp := f.Agent.DraftPolicy
	doc := projectDoc{
		Version: 1, Name: f.Name, Slug: f.Slug, Description: f.Description, Locales: f.Locales, Domain: f.Domain,
		BaseModel: baseModelDoc(f.BaseModel),
		Agent: agentDoc{Driver: f.Agent.Driver, Model: f.Agent.Model, PermissionPreset: f.Agent.PermissionPreset,
			InstructionsTemplate: f.Agent.InstructionsTemplate, AutoMerge: f.Agent.AutoMerge,
			DraftPolicy: draftPolicy{Mix: dp["mix"], Gate: dp["gate"], Note: dp["note"], LanguagePack: dp["language_pack"]}},
		Repository: repositoryDoc{Kind: f.Repo.Kind, CloneURL: f.Repo.CloneURL, Remote: f.Repo.Remote, Branch: "main"},
		Storage:    storageDoc{Mounts: []string{}},
		Budgets:    budgetsDoc{GPUHoursPerDay: f.Budgets.GPUHoursPerDay, AgentTokensPerDay: f.Budgets.AgentTokensPerDay},
	}
	if doc.Locales == nil {
		doc.Locales = []string{}
	}
	var b bytes.Buffer
	b.WriteString(projectHeader)
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("render project.yaml: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("render project.yaml: %w", err)
	}
	return b.Bytes(), nil
}

// LockFile renders data.lock: the adopted registry versions and the template versions the files came from, in a
// stable order so an unchanged project renders byte for byte the same.
func LockFile(f Facts) []byte {
	var b strings.Builder
	b.WriteString("# written by Cadence — every registry version this project depends on (docs/spec/02-domain-projects-registry.md\n")
	b.WriteString("# \"Registry\", lockfile). Never edited by hand.\nversion: 1\n")
	write := func(key string, list []Locked) {
		sorted := append([]Locked(nil), list...)
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].Kind != sorted[j].Kind {
				return sorted[i].Kind < sorted[j].Kind
			}
			return sorted[i].Collection < sorted[j].Collection
		})
		if len(sorted) == 0 {
			b.WriteString(key + ": []\n")
			return
		}
		b.WriteString(key + ":\n")
		for _, l := range sorted {
			b.WriteString("  - kind: " + l.Kind + "\n")
			b.WriteString("    collection: " + l.Collection + "\n")
			b.WriteString("    version: " + quote(l.Version) + "\n")
			b.WriteString("    id: " + l.ID + "\n")
		}
	}
	write("resolved", f.Lock)
	write("templates", f.Templates)
	return []byte(b.String())
}

func quote(s string) string { return strconv.Quote(s) }

// ---------------------------------------------------------------- NOTES.md

// AppendNote appends a dated learning to NOTES.md (created with its header when empty). Notes of the same day go
// under one heading.
func AppendNote(notes []byte, day time.Time, text string) []byte {
	var b bytes.Buffer
	if len(bytes.TrimSpace(notes)) == 0 {
		b.WriteString(NotesHeader)
	} else {
		b.Write(bytes.TrimRight(notes, "\n"))
		b.WriteString("\n")
	}
	heading := "## " + day.Format(time.DateOnly)
	if !bytes.Contains(b.Bytes(), []byte("\n"+heading+"\n")) {
		b.WriteString("\n" + heading + "\n")
	}
	b.WriteString("\n")
	for i, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if i == 0 {
			b.WriteString("- " + strings.TrimRight(line, " ") + "\n")
		} else {
			b.WriteString("  " + strings.TrimRight(line, " ") + "\n")
		}
	}
	return b.Bytes()
}
