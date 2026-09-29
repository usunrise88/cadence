package policy

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Class is the guardrail class a tool rule puts an operation in (R7).
type Class string

// The five classes and what the server answers for an agent.
const (
	ClassRead      Class = "read"      // allow
	ClassDraft     Class = "draft"     // allow (reversible)
	ClassSpend     Class = "spend"     // allow within the GPU-hours budget, approval over it
	ClassGated     Class = "gated"     // approval
	ClassForbidden Class = "forbidden" // deny
)

// Access is an allow / ask / deny answer for files, shell commands and web access.
type Access string

// Access values; ask means the agent's own permission prompt asks the person.
const (
	AccessAllow Access = "allow"
	AccessAsk   Access = "ask"
	AccessDeny  Access = "deny"
)

// Preset is one templates/presets/<name>.yaml.
type Preset struct {
	Name        string     `yaml:"name"`
	Description string     `yaml:"description"`
	Tools       []ToolRule `yaml:"tools"`
	Files       FileRules  `yaml:"files"`
	Shell       ShellRules `yaml:"shell"`
	Web         Access     `yaml:"web"`
	Sandbox     Sandbox    `yaml:"sandbox"`
}

// ToolRule classifies the operations it matches. Every non-empty condition must hold: an operation pattern
// (exact, `<entity>.*`, `*.<verb>` or `*`), a verb, the vocabulary's verb class, and path parameters.
type ToolRule struct {
	ID         string            `yaml:"id"`
	Class      Class             `yaml:"class"`
	Everyone   bool              `yaml:"everyone"` // applies to people too, not only agents and automation
	Operations []string          `yaml:"operations"`
	Verbs      []string          `yaml:"verbs"`
	VerbClass  string            `yaml:"verbClass"` // read | mutate (api/vocabulary.yaml)
	Params     map[string]string `yaml:"params"`
	Reason     string            `yaml:"reason"`
}

// FileRules limit what the agent reads and edits in its worktree.
type FileRules struct {
	Worktree bool     `yaml:"worktree"` // nothing outside the worktree
	Read     Access   `yaml:"read"`
	Edit     Access   `yaml:"edit"`
	Deny     []string `yaml:"deny"` // gitignore-style patterns relative to the worktree, never read or edited
}

// ShellRules are command patterns (`*` matches any text); deny beats ask beats allow, anything else is Default.
type ShellRules struct {
	Default Access   `yaml:"default"`
	Allow   []string `yaml:"allow"`
	Ask     []string `yaml:"ask"`
	Deny    []string `yaml:"deny"`
}

// Sandbox configures the agent's own shell sandbox (Claude Code); the egress proxy (R4) enforces the same list.
type Sandbox struct {
	Enabled bool     `yaml:"enabled"`
	Network []string `yaml:"network"` // domains the sandboxed shell may reach
}

// ParsePreset reads and validates one preset.
func ParsePreset(b []byte) (*Preset, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var p Preset
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("parse preset: %w", err)
	}
	if err := p.validate(); err != nil {
		return nil, fmt.Errorf("preset %q: %w", p.Name, err)
	}
	return &p, nil
}

func (p *Preset) validate() error {
	if p.Name == "" {
		return fmt.Errorf("needs a name")
	}
	seen := map[string]bool{}
	for i, r := range p.Tools {
		if r.ID == "" {
			return fmt.Errorf("tools[%d] needs an id", i)
		}
		if seen[r.ID] {
			return fmt.Errorf("duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		switch r.Class {
		case ClassRead, ClassDraft, ClassSpend, ClassGated, ClassForbidden:
		default:
			return fmt.Errorf("rule %q: unknown class %q", r.ID, r.Class)
		}
		if len(r.Operations) == 0 && len(r.Verbs) == 0 && r.VerbClass == "" {
			return fmt.Errorf("rule %q matches nothing: give operations, verbs or verbClass", r.ID)
		}
		switch r.VerbClass {
		case "", "read", "mutate":
		default:
			return fmt.Errorf("rule %q: verbClass must be read or mutate", r.ID)
		}
		for _, op := range r.Operations {
			if _, err := path.Match(op, "x.y"); err != nil || !strings.Contains(op, ".") && op != "*" {
				return fmt.Errorf("rule %q: bad operation pattern %q", r.ID, op)
			}
		}
	}
	for _, a := range []Access{p.Files.Read, p.Files.Edit, p.Shell.Default, p.Web} {
		switch a {
		case "", AccessAllow, AccessAsk, AccessDeny:
		default:
			return fmt.Errorf("unknown access %q (allow, ask or deny)", a)
		}
	}
	return nil
}

// LoadPresets reads every *.yaml in fsys; the file name must equal the preset's name.
func LoadPresets(fsys fs.FS) (map[string]*Preset, error) {
	names, err := fs.Glob(fsys, "*.yaml")
	if err != nil {
		return nil, fmt.Errorf("list presets: %w", err)
	}
	out := make(map[string]*Preset, len(names))
	for _, n := range names {
		b, err := fs.ReadFile(fsys, n)
		if err != nil {
			return nil, fmt.Errorf("read preset %s: %w", n, err)
		}
		p, err := ParsePreset(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		if want := strings.TrimSuffix(n, ".yaml"); p.Name != want {
			return nil, fmt.Errorf("%s: name %q must equal the file name", n, p.Name)
		}
		out[p.Name] = p
	}
	return out, nil
}

// match reports whether r applies to the operation.
func (r ToolRule) match(op, verbClass string, params map[string]string) bool {
	if len(r.Operations) > 0 && !anyMatch(r.Operations, op) {
		return false
	}
	if len(r.Verbs) > 0 {
		_, verb, _ := strings.Cut(op, ".")
		if !contains(r.Verbs, verb) {
			return false
		}
	}
	if r.VerbClass != "" && r.VerbClass != verbClass {
		return false
	}
	for k, v := range r.Params {
		if params[k] != v {
			return false
		}
	}
	return true
}

func anyMatch(patterns []string, op string) bool {
	for _, p := range patterns {
		if p == op || p == "*" {
			return true
		}
		if ok, _ := path.Match(p, op); ok {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
