package experiments

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

func ptr(v float64) *float64 { return &v }

func TestPointsGrid(t *testing.T) {
	params := []Param{
		{Name: "peak_lr", Values: []any{0.0001, 0.0003}},
		{Name: "warmup_steps", Values: []any{100.0, 500.0, 1000.0}},
	}
	got, err := Points(ModeGrid, params, 0, 8, 16, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := []map[string]any{
		{"peak_lr": 0.0001, "warmup_steps": 100.0}, {"peak_lr": 0.0001, "warmup_steps": 500.0}, {"peak_lr": 0.0001, "warmup_steps": 1000.0},
		{"peak_lr": 0.0003, "warmup_steps": 100.0}, {"peak_lr": 0.0003, "warmup_steps": 500.0}, {"peak_lr": 0.0003, "warmup_steps": 1000.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grid %v", got)
	}
	// runs cuts the grid to its first combinations.
	if got, err := Points(ModeGrid, params, 4, 8, 16, 1); err != nil || len(got) != 4 || got[3]["peak_lr"] != 0.0003 {
		t.Fatalf("cut grid %v (%v)", got, err)
	}
	// More combinations than sweeps.max_runs is refused.
	var pe *problems.Error
	if _, err := Points(ModeGrid, params, 0, 8, 5, 1); !errors.As(err, &pe) || pe.Type != problems.ValidationFailed || !strings.Contains(pe.Errors[0].Message, "6 combinations") {
		t.Fatalf("over max runs: %v", err)
	}
	// Values can be anything JSON, an augmentation profile included.
	aug := []Param{{Name: "augmentation", Values: []any{map[string]any{"profile": "clean"}, map[string]any{"profile": "telephony"}}}}
	if got, err := Points(ModeGrid, aug, 0, 8, 16, 1); err != nil || len(got) != 2 {
		t.Fatalf("object values %v (%v)", got, err)
	}
}

func TestPointsRandom(t *testing.T) {
	params := []Param{
		{Name: "peak_lr", Min: ptr(1e-5), Max: ptr(1e-3), Scale: ScaleLog},
		{Name: "warmup_steps", Min: ptr(10), Max: ptr(1000), Integer: true},
		{Name: "replayShare", Values: []any{0.0, 0.15, 0.3}},
	}
	a, err := Points(ModeRandom, params, 0, 6, 16, 7)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Points(ModeRandom, params, 0, 6, 16, 7)
	if len(a) != 6 || !reflect.DeepEqual(a, b) {
		t.Fatalf("the same seed draws the same points: %v vs %v", a, b)
	}
	c, _ := Points(ModeRandom, params, 0, 6, 16, 8)
	if reflect.DeepEqual(a, c) {
		t.Fatal("another seed draws other points")
	}
	for _, p := range a {
		lr := p["peak_lr"].(float64)
		w := p["warmup_steps"].(float64)
		if lr < 1e-5 || lr > 1e-3 || w < 10 || w > 1000 || w != float64(int(w)) {
			t.Fatalf("out of range %v", p)
		}
		if s, _ := json.Marshal(lr); len(s) > 12 {
			t.Errorf("drawn values are rounded to 4 significant digits: %s", s)
		}
		switch p["replayShare"] {
		case 0.0, 0.15, 0.3:
		default:
			t.Fatalf("replayShare %v not one of the values", p["replayShare"])
		}
	}
	if got, err := Points(ModeRandom, params, 3, 6, 16, 7); err != nil || len(got) != 3 {
		t.Fatalf("runs %v (%v)", got, err)
	}
	if _, err := Points(ModeRandom, params, 20, 6, 16, 7); err == nil {
		t.Fatal("more runs than sweeps.max_runs")
	}
}

func TestCheckParams(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		params []Param
		path   string
	}{
		{"none", ModeGrid, nil, "/parameters"},
		{"no name", ModeGrid, []Param{{Values: []any{1.0}}}, "/parameters/0/name"},
		{"twice", ModeGrid, []Param{{Name: "a", Values: []any{1.0}}, {Name: "a", Values: []any{2.0}}}, "/parameters/1/name"},
		{"grid without values", ModeGrid, []Param{{Name: "a"}}, "/parameters/0/values"},
		{"grid with a range", ModeGrid, []Param{{Name: "a", Values: []any{1.0}, Min: ptr(0), Max: ptr(1)}}, "/parameters/0/min"},
		{"random without values or range", ModeRandom, []Param{{Name: "a", Min: ptr(0)}}, "/parameters/0/values"},
		{"values and range", ModeRandom, []Param{{Name: "a", Values: []any{1.0}, Min: ptr(0), Max: ptr(1)}}, "/parameters/0/min"},
		{"min above max", ModeRandom, []Param{{Name: "a", Min: ptr(2), Max: ptr(1)}}, "/parameters/0/max"},
		{"log from zero", ModeRandom, []Param{{Name: "a", Min: ptr(0), Max: ptr(1), Scale: ScaleLog}}, "/parameters/0/min"},
		{"unknown scale", ModeRandom, []Param{{Name: "a", Min: ptr(0), Max: ptr(1), Scale: "cubic"}}, "/parameters/0/scale"},
		{"replay share not a number", ModeGrid, []Param{{Name: ReplayShare, Values: []any{"lots"}}}, "/parameters/0/values/0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var pe *problems.Error
			if err := checkParams(tc.mode, tc.params); !errors.As(err, &pe) || len(pe.Errors) == 0 || pe.Errors[0].Path != tc.path {
				t.Fatalf("want a problem at %s, got %v", tc.path, err)
			}
		})
	}
	if err := checkParams(ModeRandom, []Param{{Name: "a", Min: ptr(1e-5), Max: ptr(1e-3), Scale: ScaleLog}, {Name: "b", Values: []any{1.0}}}); err != nil {
		t.Fatal(err)
	}
}

func TestTagOf(t *testing.T) {
	for in, want := range map[string]string{
		"LR × warm-up (he)":     "lr-warm-up-he",
		"  ":                    "experiment",
		"v1.2_test":             "v1.2_test",
		strings.Repeat("a", 80): strings.Repeat("a", 63),
	} {
		if got := tagOf(in); got != want {
			t.Errorf("tagOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAtPointNamesThePoint(t *testing.T) {
	err := atPoint(2, map[string]any{"peak_lr": 5.0}, problems.RecipeMismatch.New("too high"))
	var pe *problems.Error
	if !errors.As(err, &pe) || pe.Type != problems.RecipeMismatch || pe.Detail != `point 2 {"peak_lr":5}: too high` {
		t.Fatalf("%v", err)
	}
	plain := errors.New("db down")
	if !errors.Is(atPoint(0, nil, plain), plain) {
		t.Fatal("other errors pass through")
	}
}
