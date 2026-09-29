package defaults

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/api"
)

func TestEmbeddedDefaultsParse(t *testing.T) {
	d := Get()
	if d.Version < 1 {
		t.Fatalf("version %d", d.Version)
	}
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"budgets.gpu_hours_per_project_per_day", d.Budgets.GPUHoursPerProjectPerDay.Value, 8.0},
		{"budgets.agent_turns_per_session", d.Budgets.AgentTurnsPerSession.Value, 200},
		{"timeouts.stuck_turn_minutes", d.Timeouts.StuckTurnMinutes.Value, 5},
		{"timeouts.idle_session_minutes", d.Timeouts.IdleSessionMinutes.Value, 30},
		{"timeouts.approval_expiry_hours", d.Timeouts.ApprovalExpiryHrs.Value, 24},
		{"training.steps", d.Training.Steps.Value, 3000},
		{"training.gpus", d.Training.GPUs.Value, 1},
		{"training.init", d.Training.Init.Value, "base"},
		{"wizard.base_model", d.Wizard.BaseModel.Value, "base-model/nemotron-3.5-asr-streaming-0.6b"},
		{"cache.high_water_mark", d.Cache.HighWaterMark.Value, 0.8},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if len(d.Compute.Hosts) != 1 || d.Compute.Hosts[0].Name != "staging" || len(d.Compute.Hosts[0].Cards) != 1 ||
		d.Compute.Hosts[0].Cards[0].MemoryCapGB != 24 {
		t.Errorf("compute hosts = %+v, want staging with one card capped at 24 GB", d.Compute.Hosts)
	}
	card := d.Compute.Hosts[0].Cards[0]
	if _, ok := d.TrainingEstimate(d.Wizard.BaseModel.Value, card.CardClass, card.MemoryCapGB, d.Training.Precision.Value); !ok {
		t.Error("the estimate table has no row for the default base model on the seeded card")
	}
}

// TestDocumentMatchesContract validates what defaults.get serves against the contract's Defaults schema.
func TestDocumentMatchesContract(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	schema := spec.Components.Schemas["Defaults"].Value
	raw, err := Get().JSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if err := schema.VisitJSON(doc); err != nil {
		t.Fatalf("defaults.yaml does not match #/components/schemas/Defaults: %v", err)
	}
}

func TestParseRejects(t *testing.T) {
	const good = `version: 1
wizard: {}
budgets:
  gpu_hours_per_project_per_day: {value: 8, description: d, source: s, range: {min: 0, max: 10}}
estimates:
  training:
    - {base_model: m, card_class: c, memory_cap_gb: 24, precision: bf16, seconds_per_step: 1, plus_minus: 0.5, description: d, source: s}
`
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("good file rejected: %v", err)
	}
	tests := map[string]struct{ from, to, want string }{
		"no source":          {"description: d, source: s, range", "description: d, range", "missing source"},
		"no description":     {"value: 8, description: d,", "value: 8,", "missing description"},
		"above range":        {"value: 8,", "value: 11,", "above the maximum"},
		"unknown key":        {"version: 1", "version: 1\nextra: 1", "field extra not found"},
		"unknown param key":  {"source: s, range", "source: s, note: x, range", "field note not found"},
		"row without source": {"description: d, source: s}", "description: d}", "missing source"},
		"bad row":            {"seconds_per_step: 1", "seconds_per_step: 0", "seconds_per_step"},
		"no version":         {"version: 1", "version: 0", "version"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			src := strings.Replace(good, tt.from, tt.to, 1)
			if src == good {
				t.Fatalf("replacement %q did not apply", tt.from)
			}
			_, err := Parse([]byte(src))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}

func TestRange(t *testing.T) {
	lo, hi := 1.0, 2.0
	r := &Range{Min: &lo, Max: &hi, Values: []string{"a"}}
	for v, ok := range map[float64]bool{0.5: false, 1: true, 2: true, 2.5: false} {
		if (r.Check(v) == nil) != ok {
			t.Errorf("Check(%v) ok = %v", v, !ok)
		}
	}
	if !r.Allows("a") || r.Allows("b") || !(*Range)(nil).Allows("x") || (*Range)(nil).Check(99) != nil {
		t.Error("Allows or nil range misbehaves")
	}
}
