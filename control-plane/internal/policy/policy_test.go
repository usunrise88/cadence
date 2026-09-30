package policy

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
)

func engine(t *testing.T, budget Budget) *Engine {
	t.Helper()
	e, err := Embedded(budget)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

var (
	agent      = auth.Actor{Kind: auth.KindAgent, ID: "ses_1", SessionID: "ses_1"}
	automation = auth.Actor{Kind: auth.KindAutomation, ID: "crd_1"}
	person     = auth.DevActor()
)

func TestDecide(t *testing.T) {
	e := engine(t, StubBudget{GPUHoursPerDay: 4})
	readOnly := Scope{ProjectID: "prj_a", Preset: "read-only"}
	scoped := Scope{ProjectID: "prj_a"}
	tests := []struct {
		name    string
		in      Input
		outcome Outcome
		rule    string
	}{
		{"agent reads", Input{Actor: agent, Operation: "projects.get", VerbClass: "read"}, Allow, "read"},
		{"agent waits on a job (read verb)", Input{Actor: agent, Operation: "jobs.wait", VerbClass: "read"}, Allow, "read"},
		{"agent edits a project", Input{Actor: agent, Operation: "projects.edit", VerbClass: "mutate"}, Allow, "draft"},
		{"agent creates a mix", Input{Actor: agent, Operation: "mixes.new", VerbClass: "mutate"}, Allow, "draft"},
		{"agent archives a project: gated fixture", Input{Actor: agent, Operation: "projects.archive", VerbClass: "mutate"}, Approval, "archive-project"},
		{"agent archives a source: no deletes", Input{Actor: agent, Operation: "sources.archive", VerbClass: "mutate"}, Deny, "no-deletes"},
		{"agent revokes a credential", Input{Actor: agent, Operation: "credentials.revoke", VerbClass: "mutate"}, Deny, "admin-only"},
		{"agent approves", Input{Actor: agent, Operation: "approvals.approve", VerbClass: "mutate"}, Deny, "approvals-are-for-people"},
		{"agent writes a secret", Input{Actor: agent, Operation: "secrets.new", VerbClass: "mutate"}, Deny, "admin-only"},
		{"agent freezes a golden set", Input{Actor: agent, Operation: "goldenSets.freeze", VerbClass: "mutate"}, Approval, "evaluation-gates"},
		{"agent freezes a dataset", Input{Actor: agent, Operation: "datasets.freeze", VerbClass: "mutate"}, Allow, "draft"},
		{"agent promotes", Input{Actor: agent, Operation: "deployments.promote", VerbClass: "mutate"}, Approval, "deployments"},
		{"agent registers a mount", Input{Actor: agent, Operation: "mounts.new", VerbClass: "mutate"}, Approval, "registry-changes"},
		{"agent sets a free alias", Input{Actor: agent, Operation: "aliases.set", VerbClass: "mutate",
			PathParams: map[string]string{"p": "demo", "name": "candidate"}}, Allow, "draft"},
		{"agent sets baseline", Input{Actor: agent, Operation: "aliases.set", VerbClass: "mutate",
			PathParams: map[string]string{"p": "demo", "name": "baseline"}}, Approval, "baseline-alias"},
		{"agent runs without an estimate", Input{Actor: agent, Operation: "runs.new", VerbClass: "mutate"}, Allow, "gpu-spend"},
		{"agent runs within budget", Input{Actor: agent, Operation: "runs.new", VerbClass: "mutate",
			Estimate: &Estimate{GPUHours: 3.5}}, Allow, "gpu-spend"},
		{"agent runs over budget", Input{Actor: agent, Operation: "runs.new", VerbClass: "mutate",
			Estimate: &Estimate{GPUHours: 4.5}}, Approval, "gpu-spend"},
		{"agent calls an unknown verb", Input{Actor: agent, Operation: "things.frobnicate", VerbClass: "mutate"}, Deny, RuleNoMatch},
		{"automation follows the agent rules", Input{Actor: automation, Operation: "projects.archive", VerbClass: "mutate"}, Approval, "archive-project"},
		{"automation starts no agent session", Input{Actor: automation, Scope: scoped, Operation: "agentSessions.new", VerbClass: "mutate",
			ProjectID: "prj_a"}, Deny, "sessions-are-for-people"},
		{"a key allowed agent sessions starts one in its project", Input{Actor: automation, Scope: Scope{ProjectID: "prj_a", AgentSessions: true},
			Operation: "agentSessions.new", VerbClass: "mutate", ProjectID: "prj_a"}, Allow, RuleKeyAgentSessions},
		{"a key allowed agent sessions messages them", Input{Actor: automation, Scope: Scope{ProjectID: "prj_a", AgentSessions: true},
			Operation: "agentMessages.new", VerbClass: "mutate", ProjectID: "prj_a"}, Allow, RuleKeyAgentSessions},
		{"a key allowed agent sessions stays out of other projects", Input{Actor: automation, Scope: Scope{ProjectID: "prj_a", AgentSessions: true},
			Operation: "agentSessions.new", VerbClass: "mutate", ProjectID: "prj_b"}, Deny, RuleTokenScope},
		{"a key allowed agent sessions keeps the other rules", Input{Actor: automation, Scope: Scope{ProjectID: "prj_a", AgentSessions: true},
			Operation: "projects.archive", VerbClass: "mutate", ProjectID: "prj_a"}, Approval, "archive-project"},
		{"an agent never gets it", Input{Actor: agent, Scope: Scope{ProjectID: "prj_a", AgentSessions: true},
			Operation: "agentSessions.new", VerbClass: "mutate", ProjectID: "prj_a"}, Deny, "sessions-are-for-people"},
		{"agent outside its project", Input{Actor: agent, Scope: scoped, Operation: "projects.edit", VerbClass: "mutate",
			ProjectID: "prj_b"}, Deny, RuleTokenScope},
		{"agent inside its project", Input{Actor: agent, Scope: scoped, Operation: "projects.edit", VerbClass: "mutate",
			ProjectID: "prj_a"}, Allow, "draft"},
		{"read-only session reads", Input{Actor: agent, Scope: readOnly, Operation: "projects.get", VerbClass: "read"}, Allow, "read"},
		{"read-only session edits", Input{Actor: agent, Scope: readOnly, Operation: "projects.edit", VerbClass: "mutate"}, Deny, "no-changes"},
		{"unknown preset", Input{Actor: agent, Scope: Scope{Preset: "nope"}, Operation: "projects.get", VerbClass: "read"}, Deny, RuleNoPreset},
		{"person archives", Input{Actor: person, Operation: "projects.archive", VerbClass: "mutate"}, Allow, RulePeople},
		{"person runs over budget", Input{Actor: person, Operation: "runs.new", VerbClass: "mutate",
			Estimate: &Estimate{GPUHours: 100}}, Allow, RulePeople},
		{"person sets baseline: gated for everyone", Input{Actor: person, Operation: "aliases.set", VerbClass: "mutate",
			PathParams: map[string]string{"name": "baseline"}}, Approval, "baseline-alias"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := e.Decide(context.Background(), tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if d.Outcome != tt.outcome || d.Rule != tt.rule {
				t.Fatalf("Decide = %s by %q (%s), want %s by %q", d.Outcome, d.Rule, d.Reason, tt.outcome, tt.rule)
			}
			if d.Reason == "" {
				t.Error("every decision carries a reason")
			}
		})
	}
}

func TestDecideOverBudgetExplains(t *testing.T) {
	d, err := engine(t, StubBudget{GPUHoursPerDay: 2}).Decide(context.Background(),
		Input{Actor: agent, Operation: "runs.new", VerbClass: "mutate", Estimate: &Estimate{GPUHours: 3}})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome != Approval || d.RemainingGPUHours == nil || *d.RemainingGPUHours != 2 || !strings.Contains(d.Reason, "3.00") {
		t.Fatalf("decision %+v", d)
	}
}

// sessionBudget has a project allowance and a smaller session allowance.
type sessionBudget struct{ project, session float64 }

func (b sessionBudget) RemainingGPUHours(context.Context, string) (float64, error) {
	return b.project, nil
}
func (b sessionBudget) RemainingSessionGPUHours(context.Context, string) (float64, error) {
	return b.session, nil
}

func TestDecideSessionBudget(t *testing.T) {
	b := sessionBudget{project: 8, session: 1}
	inSession := agent // ses_1
	automation := auth.Actor{Kind: auth.KindAutomation, ID: "key_1"}
	tests := []struct {
		name     string
		actor    auth.Actor
		estimate float64
		want     Outcome
		reason   string
	}{
		{"within both", inSession, 0.5, Allow, ""},
		{"over the session's", inSession, 2, Approval, "agent session's budget"},
		{"over the project's first", inSession, 9, Approval, "today's budget"},
		{"no session: only the project's", automation, 2, Allow, ""},
		{"a person is not budget-gated", person, 20, Allow, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := engine(t, b).Decide(context.Background(),
				Input{Actor: tt.actor, Operation: "runs.calibrate", VerbClass: "mutate", Estimate: &Estimate{GPUHours: tt.estimate}})
			if err != nil {
				t.Fatal(err)
			}
			if d.Outcome != tt.want || !strings.Contains(d.Reason, tt.reason) {
				t.Fatalf("decision %+v", d)
			}
		})
	}
}

type failingBudget struct{}

func (failingBudget) RemainingGPUHours(context.Context, string) (float64, error) {
	return 0, errors.New("down")
}

func TestDecideBudgetError(t *testing.T) {
	_, err := engine(t, failingBudget{}).Decide(context.Background(),
		Input{Actor: agent, Operation: "runs.new", VerbClass: "mutate", Estimate: &Estimate{GPUHours: 1}})
	if err == nil {
		t.Fatal("a failing budget source is an error, not an allow")
	}
}

func TestParsePresetRejects(t *testing.T) {
	tests := map[string]string{
		"no name":        "tools: []",
		"unknown class":  "name: x\ntools: [{id: a, class: maybe, verbs: [get]}]",
		"matches none":   "name: x\ntools: [{id: a, class: read}]",
		"duplicate id":   "name: x\ntools: [{id: a, class: read, verbs: [get]}, {id: a, class: read, verbs: [list]}]",
		"bad verb class": "name: x\ntools: [{id: a, class: read, verbClass: write}]",
		"bad pattern":    "name: x\ntools: [{id: a, class: read, operations: [projects]}]",
		"unknown key":    "name: x\ntool: []",
		"bad access":     "name: x\nweb: sometimes",
	}
	for name, y := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePreset([]byte(y)); err == nil {
				t.Fatalf("ParsePreset(%q) succeeded", y)
			}
		})
	}
}

func TestScopeContext(t *testing.T) {
	s := Scope{ProjectID: "prj_a", Preset: "read-only"}
	if got := ScopeFromContext(WithScope(context.Background(), s)); got != s {
		t.Fatalf("got %+v", got)
	}
	if got := ScopeFromContext(context.Background()); got != (Scope{}) {
		t.Fatalf("empty context gives %+v", got)
	}
}
