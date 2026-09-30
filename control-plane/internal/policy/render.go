package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Operation is one MCP tool the renderer classifies: its name (<entity>.<verb>) and verb class (read | mutate),
// as in internal/mcp/tools.json (readOnlyHint) and api/vocabulary.yaml.
type Operation struct {
	Name      string
	VerbClass string
}

// MCPServer is the name the agents know the Cadence MCP server by.
const MCPServer = "cadence"

// agentSide is what the agent's own permission file says about a Cadence tool. Everything the server may run
// (read, draft, spend, gated) is allowed: the server answers spend over budget and gated commands with an approval
// id, so an agent-side prompt would ask the person twice. What the server denies is denied up front.
func agentSide(c Class) Access {
	switch c {
	case ClassRead, ClassDraft, ClassSpend, ClassGated:
		return AccessAllow
	default:
		return AccessDeny
	}
}

// AgentAllowed lists the tools of ops (sorted by name) the agent may call without a permission prompt under p: the
// same set the rendered files allow. The agent host pre-allows them in agents that do not apply a repository's own
// allow rules (Claude Code ignores permissions.allow in .claude/settings.json for its sessions, 2026-09-30).
func AgentAllowed(p *Preset, ops []Operation) []string {
	out := []string{}
	for _, op := range sortedOps(ops) {
		if c, _ := p.ClassOf(op.Name, op.VerbClass); agentSide(c) == AccessAllow {
			out = append(out, op.Name)
		}
	}
	return out
}

// ClaudeSettings is the rendered .claude/settings.json content (R7; code.claude.com/docs/en/permissions,
// …/sandboxing). The MCP server itself is not in the file: it arrives through ACP session/new (R2).
type ClaudeSettings struct {
	Permissions ClaudePermissions `json:"permissions"`
	Sandbox     *ClaudeSandbox    `json:"sandbox,omitempty"`
}

// ClaudePermissions are allow / ask / deny rules; deny beats ask beats allow.
type ClaudePermissions struct {
	DefaultMode string   `json:"defaultMode"`
	Allow       []string `json:"allow"`
	Ask         []string `json:"ask"`
	Deny        []string `json:"deny"`
}

// ClaudeSandbox is Claude Code's shell sandbox.
type ClaudeSandbox struct {
	Enabled                  bool                 `json:"enabled"`
	AllowUnsandboxedCommands bool                 `json:"allowUnsandboxedCommands"`
	Network                  ClaudeSandboxNetwork `json:"network"`
}

// ClaudeSandboxNetwork lists the domains the sandboxed shell may reach.
type ClaudeSandboxNetwork struct {
	AllowedDomains []string `json:"allowedDomains"`
}

// RenderClaude turns p into Claude Code settings for the tools ops.
func RenderClaude(p *Preset, ops []Operation) ClaudeSettings {
	perm := ClaudePermissions{DefaultMode: "default", Allow: []string{}, Ask: []string{}, Deny: []string{}}
	for _, op := range sortedOps(ops) {
		c, _ := p.ClassOf(op.Name, op.VerbClass)
		rule := ClaudeMCPTool(op.Name)
		if agentSide(c) == AccessAllow {
			perm.Allow = append(perm.Allow, rule)
		} else {
			perm.Deny = append(perm.Deny, rule)
		}
	}
	// Files: "/" anchors at the project root (the worktree) in a project settings file.
	put := func(a Access, rules ...string) {
		switch a {
		case AccessAllow:
			perm.Allow = append(perm.Allow, rules...)
		case AccessAsk:
			perm.Ask = append(perm.Ask, rules...)
		case AccessDeny:
			perm.Deny = append(perm.Deny, rules...)
		}
	}
	put(or(p.Files.Read, AccessAllow), "Read(/**)")
	put(or(p.Files.Edit, AccessAsk), "Edit(/**)")
	for _, f := range p.Files.Deny {
		put(AccessDeny, "Read(/"+strings.TrimPrefix(f, "/")+")", "Edit(/"+strings.TrimPrefix(f, "/")+")")
	}
	for _, c := range p.Shell.Allow {
		put(AccessAllow, "Bash("+c+")")
	}
	for _, c := range p.Shell.Ask {
		put(AccessAsk, "Bash("+c+")")
	}
	for _, c := range p.Shell.Deny {
		put(AccessDeny, "Bash("+c+")")
	}
	if p.Shell.Default == AccessDeny {
		// A deny-by-default shell: a bare Bash deny would beat the allow rules, so the mode denies whatever no
		// rule pre-approves instead of prompting.
		perm.DefaultMode = "dontAsk"
	}
	if w := or(p.Web, AccessAsk); w != AccessAllow {
		put(w, "WebFetch", "WebSearch")
	}
	out := ClaudeSettings{Permissions: perm}
	if p.Sandbox.Enabled {
		out.Sandbox = &ClaudeSandbox{Enabled: true, Network: ClaudeSandboxNetwork{AllowedDomains: nonNil(p.Sandbox.Network)}}
	}
	return out
}

// ClaudeMCPTool is the name Claude Code gives an MCP tool in permission rules and tool calls:
// mcp__<server>__<tool> with characters outside [A-Za-z0-9_-] replaced by underscores (mixes.get → mixes_get; the
// live A2 run showed that rules with the dotted name never match).
func ClaudeMCPTool(op string) string {
	return "mcp__" + MCPServer + "__" + strings.TrimPrefix(OpencodeMCPTool(op), MCPServer+"_")
}

// OpencodeMCPTool is the name opencode gives an MCP tool: <server>_<tool> with characters outside
// [A-Za-z0-9_-] replaced by underscores.
func OpencodeMCPTool(op string) string {
	var b strings.Builder
	b.WriteString(MCPServer + "_")
	for _, r := range op {
		if r == '_' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

// RenderOpencode turns p into opencode's `permission` object (opencode.ai/docs/permissions). opencode applies the
// last matching pattern, so every object lists its catch-all first and the stricter patterns after it.
func RenderOpencode(p *Preset, ops []Operation) Ordered {
	out := Ordered{}
	read := Ordered{{"*", string(or(p.Files.Read, AccessAllow))}}
	edit := Ordered{{"*", string(or(p.Files.Edit, AccessAsk))}}
	for _, f := range p.Files.Deny {
		read = append(read, KV{f, string(AccessDeny)})
		edit = append(edit, KV{f, string(AccessDeny)})
	}
	out = append(out, KV{"read", read}, KV{"edit", edit})
	for _, k := range []string{"glob", "grep", "list"} {
		out = append(out, KV{k, string(or(p.Files.Read, AccessAllow))})
	}
	bash := Ordered{{"*", string(or(p.Shell.Default, AccessAsk))}}
	for _, c := range p.Shell.Allow {
		bash = append(bash, KV{c, string(AccessAllow)})
	}
	for _, c := range p.Shell.Ask {
		bash = append(bash, KV{c, string(AccessAsk)})
	}
	for _, c := range p.Shell.Deny {
		bash = append(bash, KV{c, string(AccessDeny)})
	}
	out = append(out, KV{"bash", bash})
	web := string(or(p.Web, AccessAsk))
	out = append(out, KV{"webfetch", web}, KV{"websearch", web})
	if p.Files.Worktree {
		out = append(out, KV{"external_directory", string(AccessDeny)})
	}
	out = append(out, KV{MCPServer + "_*", string(AccessDeny)})
	for _, op := range sortedOps(ops) {
		c, _ := p.ClassOf(op.Name, op.VerbClass)
		if a := agentSide(c); a == AccessAllow {
			out = append(out, KV{OpencodeMCPTool(op.Name), string(a)})
		}
	}
	return out
}

// KV is one member of an Ordered object.
type KV struct {
	Key   string
	Value any // string or Ordered
}

// Ordered is a JSON object that keeps its members in order (opencode's last-match-wins patterns need it).
type Ordered []KV

// MarshalJSON writes the members in order.
func (o Ordered) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, kv := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(kv.Key)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(kv.Value)
		if err != nil {
			return nil, fmt.Errorf("marshal %s: %w", kv.Key, err)
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// JSON renders v indented with two spaces and a trailing newline, the way the files are written.
func JSON(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, b, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func sortedOps(ops []Operation) []Operation {
	out := append([]Operation(nil), ops...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func or(a, def Access) Access {
	if a == "" {
		return def
	}
	return a
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
