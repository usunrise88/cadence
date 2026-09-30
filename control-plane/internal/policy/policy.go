// Package policy is the server-side policy engine (docs/spec/08-resolutions.md R7) and the permission presets it
// reads: on every command it answers allow, approval or deny with the rule that decided and a reason, and it
// renders a preset into the agents' own permission files (render.go).
//
// Rules are data (control-plane/templates/presets/<name>.yaml), evaluated in order, first match wins. Agents and
// automation keys get the preset of their scope and are denied what no rule allows; people are allowed everything
// except the rules marked `everyone` in the default preset (R8: the baseline alias).
package policy

import (
	"context"
	"fmt"
	"io/fs"
	"strings"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// DefaultPreset is the preset of an agent whose scope names none, and the source of the rules that apply to people.
const DefaultPreset = "guardrails-default"

// Outcome is the engine's answer.
type Outcome string

// Outcomes.
const (
	Allow    Outcome = "allow"
	Approval Outcome = "approval"
	Deny     Outcome = "deny"
)

// Rule names the engine uses for answers that do not come from a preset rule.
const (
	RulePeople     = "people"      // a person, no `everyone` rule matched
	RuleTokenScope = "token-scope" // the command touches a project outside the credential's scope
	RuleNoMatch    = "no-match"    // an agent command no rule allows (default deny)
	RuleNoPreset   = "no-preset"   // the scope names a preset that does not exist
	// RuleKeyAgentSessions: an API key allowed to run agent sessions in its project (auth.Scope.AgentSessions).
	RuleKeyAgentSessions = "key-agent-sessions"
)

// Scope is what a credential may reach. Stream A's auth.Scope carries the same facts; until it lands the pipeline
// reads this one from the context (WithScope).
type Scope struct {
	ProjectID    string `json:"projectId,omitempty"`    // empty: every project
	RegistryRead bool   `json:"registryRead,omitempty"` // may read the registry
	Preset       string `json:"preset,omitempty"`       // permission preset; empty: DefaultPreset
	// AgentSessions: an automation key the admin allowed to run agent sessions in its project.
	AgentSessions bool `json:"agentSessions,omitempty"`
}

type scopeKey struct{}

// WithScope returns ctx carrying s.
func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}

// ScopeFromContext returns the scope set by WithScope (the zero scope when none was).
func ScopeFromContext(ctx context.Context) Scope {
	s, _ := ctx.Value(scopeKey{}).(Scope)
	return s
}

// Estimate is what a command will spend, from its dry run (R12).
type Estimate struct {
	GPUHours float64 `json:"gpuHours"`
}

// Input is one question to the engine.
type Input struct {
	Actor      auth.Actor
	Scope      Scope
	Operation  string // <entity>.<verb>
	VerbClass  string // read | mutate, as in api/vocabulary.yaml
	ProjectID  string // the project the command touches; empty for registry and global commands
	Estimate   *Estimate
	PathParams map[string]string
}

// Decision is the engine's answer with the rule that decided and a reason fit for a person and an agent.
type Decision struct {
	Outcome           Outcome   `json:"outcome"`
	Rule              string    `json:"rule"`
	Class             Class     `json:"class,omitempty"`
	Preset            string    `json:"preset,omitempty"`
	Reason            string    `json:"reason"`
	Estimate          *Estimate `json:"estimate,omitempty"`
	RemainingGPUHours *float64  `json:"remainingGpuHours,omitempty"`
}

// Budget answers how many GPU-hours a project has left today.
type Budget interface {
	RemainingGPUHours(ctx context.Context, projectID string) (float64, error)
}

// SessionBudget is implemented by a Budget that also answers how many GPU-hours an agent session has left; a spend
// by a session's actor must fit both its project's and its session's remaining budget (phase 2, internal/runs.Meter).
type SessionBudget interface {
	RemainingSessionGPUHours(ctx context.Context, sessionID string) (float64, error)
}

// StubBudget is a fixed daily allowance and no usage: tests, and a control plane without a database. The real
// source is internal/runs.Meter (the project's budget and the day's metered lease time on GPU cards).
type StubBudget struct{ GPUHoursPerDay float64 }

// RemainingGPUHours returns the whole daily allowance.
func (b StubBudget) RemainingGPUHours(context.Context, string) (float64, error) {
	return b.GPUHoursPerDay, nil
}

// Engine decides commands against the presets.
type Engine struct {
	presets map[string]*Preset
	budget  Budget
}

// New returns an engine over presets; it needs the default preset.
func New(presets map[string]*Preset, budget Budget) (*Engine, error) {
	if _, ok := presets[DefaultPreset]; !ok {
		return nil, fmt.Errorf("policy: preset %q is missing", DefaultPreset)
	}
	return &Engine{presets: presets, budget: budget}, nil
}

// Embedded returns an engine over the presets embedded in the binary.
func Embedded(budget Budget) (*Engine, error) {
	presets, err := EmbeddedPresets()
	if err != nil {
		return nil, err
	}
	return New(presets, budget)
}

// EmbeddedPresets parses control-plane/templates/presets.
func EmbeddedPresets() (map[string]*Preset, error) {
	sub, err := subFS()
	if err != nil {
		return nil, err
	}
	return LoadPresets(sub)
}

// Preset returns the named preset.
func (e *Engine) Preset(name string) (*Preset, bool) {
	p, ok := e.presets[name]
	return p, ok
}

// Decide answers one command. Errors come only from the budget source.
func (e *Engine) Decide(ctx context.Context, in Input) (Decision, error) {
	if in.Scope.ProjectID != "" && in.ProjectID != "" && in.ProjectID != in.Scope.ProjectID {
		return Decision{Outcome: Deny, Rule: RuleTokenScope,
			Reason: "the credential is scoped to another project"}, nil
	}
	if in.Actor.Kind == auth.KindUser {
		return e.decidePerson(in), nil
	}
	if in.Actor.Kind == auth.KindAutomation && in.Scope.AgentSessions && in.Scope.ProjectID != "" && sessionOperation(in.Operation) {
		return Decision{Outcome: Allow, Rule: RuleKeyAgentSessions,
			Reason: "the API key may run agent sessions in its project"}, nil
	}
	name := in.Scope.Preset
	if name == "" {
		name = DefaultPreset
	}
	p, ok := e.presets[name]
	if !ok {
		return Decision{Outcome: Deny, Rule: RuleNoPreset, Preset: name,
			Reason: fmt.Sprintf("the permission preset %q does not exist", name)}, nil
	}
	for _, r := range p.Tools {
		if !r.match(in.Operation, in.VerbClass, in.PathParams) {
			continue
		}
		d := decision(p.Name, r)
		if r.Class == ClassSpend && in.Estimate != nil && e.budget != nil {
			left, err := e.budget.RemainingGPUHours(ctx, in.ProjectID)
			if err != nil {
				return Decision{}, fmt.Errorf("read GPU-hours budget: %w", err)
			}
			d.Estimate, d.RemainingGPUHours = in.Estimate, &left
			if in.Estimate.GPUHours > left {
				d.Outcome = Approval
				d.Reason = fmt.Sprintf("the estimate of %.2f GPU-hours exceeds the %.2f left in today's budget",
					in.Estimate.GPUHours, left)
			}
			if sb, ok := e.budget.(SessionBudget); ok && in.Actor.SessionID != "" && d.Outcome == Allow {
				sleft, err := sb.RemainingSessionGPUHours(ctx, in.Actor.SessionID)
				if err != nil {
					return Decision{}, fmt.Errorf("read the session's GPU-hours budget: %w", err)
				}
				if in.Estimate.GPUHours > sleft {
					d.Outcome, d.RemainingGPUHours = Approval, &sleft
					d.Reason = fmt.Sprintf("the estimate of %.2f GPU-hours exceeds the %.2f left in this agent session's budget",
						in.Estimate.GPUHours, sleft)
				}
			}
		}
		return d, nil
	}
	return Decision{Outcome: Deny, Rule: RuleNoMatch, Preset: p.Name,
		Reason: fmt.Sprintf("no rule of the %q preset allows %s", p.Name, in.Operation)}, nil
}

// sessionOperation: the operations that start, steer and end agent sessions (the preset's sessions-are-for-people).
func sessionOperation(op string) bool {
	return strings.HasPrefix(op, "agentSessions.") || strings.HasPrefix(op, "agentMessages.")
}

// decidePerson applies only the default preset's `everyone` rules.
func (e *Engine) decidePerson(in Input) Decision {
	p := e.presets[DefaultPreset]
	for _, r := range p.Tools {
		if r.Everyone && r.match(in.Operation, in.VerbClass, in.PathParams) {
			return decision(p.Name, r)
		}
	}
	return Decision{Outcome: Allow, Rule: RulePeople, Reason: "people may run any command not gated for everyone"}
}

func decision(preset string, r ToolRule) Decision {
	d := Decision{Rule: r.ID, Class: r.Class, Preset: preset, Reason: r.Reason}
	switch r.Class {
	case ClassGated:
		d.Outcome = Approval
	case ClassForbidden:
		d.Outcome = Deny
	default:
		d.Outcome = Allow
	}
	if d.Reason == "" {
		d.Reason = fmt.Sprintf("rule %q (%s)", r.ID, r.Class)
	}
	return d
}

// ClassOf is the class an agent's call of op falls in when nothing is known about its parameters or estimate,
// or "" when no rule matches (denied). The renderer uses it.
func (p *Preset) ClassOf(op, verbClass string) (Class, string) {
	for _, r := range p.Tools {
		if r.match(op, verbClass, nil) {
			return r.Class, r.ID
		}
	}
	return "", RuleNoMatch
}

func subFS() (fs.FS, error) {
	sub, err := fs.Sub(templates.Presets, "presets")
	if err != nil {
		return nil, fmt.Errorf("open embedded presets: %w", err)
	}
	return sub, nil
}
