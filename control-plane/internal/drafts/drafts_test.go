package drafts

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

func TestTransition(t *testing.T) {
	tests := []struct {
		from, to string
		ok       bool
	}{
		{StateOpen, StateOpen, true}, // the author edits again
		{StateOpen, StateAccepted, true},
		{StateOpen, StateReverted, true},
		{StateOpen, "merged", false},
		{StateAccepted, StateReverted, false},
		{StateAccepted, StateOpen, false},
		{StateAccepted, StateAccepted, false},
		{StateReverted, StateAccepted, false},
		{StateReverted, StateOpen, false},
	}
	for _, tt := range tests {
		t.Run(tt.from+"→"+tt.to, func(t *testing.T) {
			err := Transition(tt.from, tt.to)
			if (err == nil) != tt.ok {
				t.Fatalf("Transition(%s, %s) = %v, want ok=%v", tt.from, tt.to, err, tt.ok)
			}
			if err != nil {
				if pe, ok := problems.As(err); !ok || pe.Type != problems.Conflict {
					t.Errorf("error %v is not a conflict", err)
				}
			}
		})
	}
}

func TestPolicy(t *testing.T) {
	d := *defaults.Get()
	if got := Policy(&d, "prj_1", "mix"); got != PolicyDraft {
		t.Errorf("default mix policy = %s, want draft (defaults.yaml drafts.mix)", got)
	}
	d.Drafts.Mix.Value = PolicyDirect
	if got := Policy(&d, "prj_1", "mix"); got != PolicyDirect {
		t.Errorf("direct policy = %s", got)
	}
	if got := Policy(&d, "prj_1", "gate"); got != PolicyDraft {
		t.Errorf("unknown kinds default to draft, got %s", got)
	}
}

func TestDiff(t *testing.T) {
	base := `{"name":"he","temperature":1,"groups":[{"name":"target","weight":1,"datasets":["ver_a"]}],"gone":true}`
	tests := []struct {
		name  string
		after string
		want  []Change
	}{
		{"same", base, []Change{}},
		{"scalar", `{"name":"he","temperature":2,"groups":[{"name":"target","weight":1,"datasets":["ver_a"]}],"gone":true}`,
			[]Change{{Path: "/temperature", Before: json.Number("1"), After: json.Number("2")}}},
		{"nested weight and added group", `{"name":"he","temperature":1,"groups":[{"name":"target","weight":3,"datasets":["ver_a"]},{"name":"replay","weight":1,"datasets":["ver_b"]}],"gone":true}`,
			[]Change{
				{Path: "/groups/0/weight", Before: json.Number("1"), After: json.Number("3")},
				{Path: "/groups/1", After: map[string]any{"name": "replay", "weight": json.Number("1"), "datasets": []any{"ver_b"}}},
			}},
		{"scalar list is one change", `{"name":"he","temperature":1,"groups":[{"name":"target","weight":1,"datasets":["ver_a","ver_c"]}],"gone":true}`,
			[]Change{{Path: "/groups/0/datasets", Before: []any{"ver_a"}, After: []any{"ver_a", "ver_c"}}}},
		{"removed and added keys", `{"name":"he","temperature":1,"groups":[{"name":"target","weight":1,"datasets":["ver_a"]}],"new/key":"x"}`,
			[]Change{{Path: "/gone", Before: true}, {Path: "/new~1key", After: "x"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Diff(json.RawMessage(base), json.RawMessage(tt.after))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Diff =\n %#v\nwant\n %#v", got, tt.want)
			}
		})
	}
}

func TestRebase(t *testing.T) {
	base := `{"name":"he","temperature":1,"replayShare":0,"groups":[1]}`
	draft := `{"name":"he","temperature":2,"replayShare":0,"groups":[1]}`        // the agent changed temperature
	current := `{"name":"he v2","temperature":1,"replayShare":0.2,"groups":[1]}` // a person renamed and set replay
	got, err := Rebase(json.RawMessage(base), json.RawMessage(draft), json.RawMessage(current))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"name": "he v2", "temperature": 2.0, "replayShare": 0.2, "groups": []any{1.0}}
	if !reflect.DeepEqual(m, want) {
		t.Errorf("Rebase = %v, want %v (the draft's change on top of the person's)", m, want)
	}
	// A field the draft removed stays removed.
	got, err = Rebase(json.RawMessage(`{"a":1,"b":2}`), json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":5,"b":3}`))
	if err != nil || string(got) != `{"a":5}` {
		t.Errorf("removed field: %s %v", got, err)
	}
}
