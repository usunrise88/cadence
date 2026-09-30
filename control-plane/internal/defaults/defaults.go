// Package defaults parses the embedded defaults.yaml (docs/spec/08-resolutions.md R11) into typed values for Go
// consumers (Get) and into the generic document the defaults.get operation and the MCP defaults:// resource serve
// (Document). Parse refuses a file where a value lacks its description or source or leaves its range.
package defaults

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	bundled "github.com/usunrise88/cadence/control-plane/defaults"
)

// Range is a value's safe range: numeric bounds or the allowed values.
type Range struct {
	Min    *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max    *float64 `yaml:"max,omitempty" json:"max,omitempty"`
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`
}

// Check reports whether v lies inside the range.
func (r *Range) Check(v float64) error {
	if r == nil {
		return nil
	}
	if r.Min != nil && v < *r.Min {
		return fmt.Errorf("%v is below the minimum %v", v, *r.Min)
	}
	if r.Max != nil && v > *r.Max {
		return fmt.Errorf("%v is above the maximum %v", v, *r.Max)
	}
	return nil
}

// Allows reports whether s is one of the allowed values (any value when the range lists none).
func (r *Range) Allows(s string) bool {
	return r == nil || len(r.Values) == 0 || slices.Contains(r.Values, s)
}

// Param is one default with its provenance.
type Param[T any] struct {
	Value       T      `yaml:"value"`
	Unit        string `yaml:"unit,omitempty"`
	Description string `yaml:"description"`
	Source      string `yaml:"source"`
	Range       *Range `yaml:"range,omitempty"`
}

// Defaults is the typed form of defaults.yaml.
type Defaults struct {
	Version   int       `yaml:"version"`
	Wizard    Wizard    `yaml:"wizard"`
	Budgets   Budgets   `yaml:"budgets"`
	Timeouts  Timeouts  `yaml:"timeouts"`
	Training  Training  `yaml:"training"`
	Estimates Estimates `yaml:"estimates"`
	Mix       Mix       `yaml:"mix"`
	Drafts    Drafts    `yaml:"drafts"`
	Cache     Cache     `yaml:"cache"`
	Compute   Compute   `yaml:"compute"`

	document map[string]any
}

// ModelFor returns the default model of an agent driver.
func (w Wizard) ModelFor(driver string) Param[string] {
	if driver == "opencode" {
		return w.OpencodeModel
	}
	return w.ClaudeCodeModel
}

// Wizard holds the project wizard's Recommended values.
type Wizard struct {
	Locale               Param[string]            `yaml:"locale"`
	Domain               Param[string]            `yaml:"domain"`
	BaseModel            Param[string]            `yaml:"base_model"`
	Driver               Param[string]            `yaml:"driver"`
	ClaudeCodeModel      Param[string]            `yaml:"claude_code_model"`
	OpencodeModel        Param[string]            `yaml:"opencode_model"`
	AutoMerge            Param[string]            `yaml:"auto_merge"`
	DraftPolicy          Param[map[string]string] `yaml:"draft_policy"`
	PermissionPreset     Param[string]            `yaml:"permission_preset"`
	InstructionsTemplate Param[string]            `yaml:"instructions_template"`
	Repository           Param[string]            `yaml:"repository"`
}

// Budgets holds per-project and per-session budgets.
type Budgets struct {
	GPUHoursPerProjectPerDay           Param[float64] `yaml:"gpu_hours_per_project_per_day"`
	AgentTokensPerProjectPerDay        Param[int64]   `yaml:"agent_tokens_per_project_per_day"`
	AgentTurnsPerSession               Param[int]     `yaml:"agent_turns_per_session"`
	ManualTestGPUHoursPerProjectPerDay Param[float64] `yaml:"manual_test_gpu_hours_per_project_per_day"`
}

// Timeouts holds the session clocks of R5.
type Timeouts struct {
	StuckTurnMinutes   Param[int] `yaml:"stuck_turn_minutes"`
	IdleSessionMinutes Param[int] `yaml:"idle_session_minutes"`
	ApprovalExpiryHrs  Param[int] `yaml:"approval_expiry_hours"`
}

// Training holds the training-run defaults the estimate needs.
type Training struct {
	Init      Param[string] `yaml:"init"`
	Steps     Param[int]    `yaml:"steps"`
	Precision Param[string] `yaml:"precision"`
	GPUs      Param[int]    `yaml:"gpus"`
}

// Estimates holds the R12 estimate table.
type Estimates struct {
	BytesPerAudioHour Param[int64]  `yaml:"bytes_per_audio_hour"`
	Training          []EstimateRow `yaml:"training"`
}

// EstimateRow is the measured-or-assumed speed of one base model on one card class under one memory cap.
type EstimateRow struct {
	BaseModel      string  `yaml:"base_model"`
	CardClass      string  `yaml:"card_class"`
	MemoryCapGB    float64 `yaml:"memory_cap_gb"`
	Precision      string  `yaml:"precision"`
	SecondsPerStep float64 `yaml:"seconds_per_step"`
	PlusMinus      float64 `yaml:"plus_minus"`
	Description    string  `yaml:"description"`
	Source         string  `yaml:"source"`
}

// Mix holds the values a new mix starts from (R13).
type Mix struct {
	Temperature Param[float64] `yaml:"temperature"`
	ReplayShare Param[float64] `yaml:"replay_share"`
	GroupWeight Param[float64] `yaml:"group_weight"`
}

// Drafts holds the draft policy per draftable kind and the presence window of direct agent edits.
type Drafts struct {
	Mix             Param[string] `yaml:"mix"`
	PresenceSeconds Param[int]    `yaml:"presence_seconds"`
}

// Cache holds the local cache limits.
type Cache struct {
	HighWaterMark Param[float64] `yaml:"high_water_mark"`
	ProjectQuota  Param[float64] `yaml:"project_quota"`
}

// Compute holds the hosts seeded at first start.
type Compute struct {
	Hosts []Host `yaml:"hosts"`
}

// Host is a compute host seeded at first start.
type Host struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Source      string `yaml:"source"`
	Cards       []Card `yaml:"cards"`
}

// Card is one card of a seeded host.
type Card struct {
	Index           int      `yaml:"index"`
	Name            string   `yaml:"name"`
	CardClass       string   `yaml:"card_class"`
	MemoryGB        float64  `yaml:"memory_gb"`
	MemoryCapGB     float64  `yaml:"memory_cap_gb"`
	AllowedJobKinds []string `yaml:"allowed_job_kinds"`
}

var get = sync.OnceValue(func() *Defaults {
	d, err := Parse(bundled.YAML)
	if err != nil {
		panic("embedded defaults.yaml is invalid (a unit test guards this): " + err.Error())
	}
	return d
})

// Get returns the parsed embedded defaults.yaml. The value is shared and must not be modified.
func Get() *Defaults { return get() }

// Parse decodes a defaults file strictly (unknown keys fail) and validates every value.
func Parse(b []byte) (*Defaults, error) {
	var d Defaults
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("decode defaults.yaml: %w", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("decode defaults.yaml: %w", err)
	}
	if d.Version < 1 {
		return nil, errors.New("defaults.yaml: version must be ≥ 1")
	}
	var problems []string
	validate(doc, "", &problems)
	for i, row := range d.Estimates.Training {
		if row.SecondsPerStep <= 0 || row.PlusMinus < 0 || row.MemoryCapGB <= 0 {
			problems = append(problems, fmt.Sprintf("estimates.training[%d]: seconds_per_step and memory_cap_gb must be > 0, plus_minus ≥ 0", i))
		}
	}
	for _, h := range d.Compute.Hosts {
		for _, c := range h.Cards {
			if c.MemoryCapGB <= 0 || c.MemoryCapGB > c.MemoryGB {
				problems = append(problems, fmt.Sprintf("compute.hosts[%s].cards[%d]: memory_cap_gb must be in (0, memory_gb]", h.Name, c.Index))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("defaults.yaml: %s", strings.Join(problems, "; "))
	}
	d.document = doc
	return &d, nil
}

// validate walks the generic document: a mapping with a `value` key is a default and needs a description, a
// source and a value inside its range; a list item that is a mapping needs a description and a source unless it
// sits inside another item (a host's cards).
func validate(node any, path string, problems *[]string) {
	switch n := node.(type) {
	case map[string]any:
		if v, ok := n["value"]; ok {
			checkParam(n, v, path, problems)
			return
		}
		for k, child := range n {
			validate(child, join(path, k), problems)
		}
	case []any:
		for i, item := range n {
			p := fmt.Sprintf("%s[%d]", path, i)
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if !strings.Contains(path, "[") {
				for _, key := range []string{"description", "source"} {
					if s, _ := m[key].(string); strings.TrimSpace(s) == "" {
						*problems = append(*problems, p+": missing "+key)
					}
				}
			}
			for k, child := range m {
				validate(child, join(p, k), problems)
			}
		}
	}
}

func checkParam(n map[string]any, v any, path string, problems *[]string) {
	for _, key := range []string{"description", "source"} {
		if s, _ := n[key].(string); strings.TrimSpace(s) == "" {
			*problems = append(*problems, path+": missing "+key)
		}
	}
	for k := range n {
		switch k {
		case "value", "unit", "description", "source", "range":
		default:
			*problems = append(*problems, path+": unknown key "+k)
		}
	}
	raw, ok := n["range"]
	if !ok {
		return
	}
	b, err := json.Marshal(raw)
	if err != nil {
		*problems = append(*problems, path+".range: "+err.Error())
		return
	}
	var r Range
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		*problems = append(*problems, path+".range: "+err.Error())
		return
	}
	for _, item := range asList(v) {
		switch x := item.(type) {
		case int:
			if err := r.Check(float64(x)); err != nil {
				*problems = append(*problems, path+": "+err.Error())
			}
		case float64:
			if err := r.Check(x); err != nil {
				*problems = append(*problems, path+": "+err.Error())
			}
		case string:
			if !r.Allows(x) {
				*problems = append(*problems, fmt.Sprintf("%s: %q is not one of %v", path, x, r.Values))
			}
		}
	}
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return []any{v}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// Document returns the defaults as a generic JSON-ready document, keys exactly as in defaults.yaml.
func (d *Defaults) Document() map[string]any { return d.document }

// JSON renders Document as JSON.
func (d *Defaults) JSON() ([]byte, error) {
	b, err := json.Marshal(d.document)
	if err != nil {
		return nil, fmt.Errorf("marshal defaults: %w", err)
	}
	return b, nil
}

// TrainingEstimate returns the table row for a base model collection, card class, memory cap and precision.
func (d *Defaults) TrainingEstimate(baseModel, cardClass string, memoryCapGB float64, precision string) (EstimateRow, bool) {
	for _, r := range d.Estimates.Training {
		if r.BaseModel == baseModel && r.CardClass == cardClass && r.MemoryCapGB == memoryCapGB && r.Precision == precision {
			return r, true
		}
	}
	return EstimateRow{}, false
}
