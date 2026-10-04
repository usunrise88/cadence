package pipelines

import (
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

func TestDeprecationClosed(t *testing.T) {
	day := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		after  string
		closed bool
	}{
		{"2026-10-04", false},
		{"2026-10-03", true},
		{"2026-01-01", true},
		{"soon", true}, // a date that does not parse closes: the pack meant to retire it
	} {
		if got := (Deprecation{After: tc.after}).Closed(day); got != tc.closed {
			t.Errorf("Closed(%s) = %v, want %v", tc.after, got, tc.closed)
		}
	}
}

func TestNewDeprecatedPins(t *testing.T) {
	now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	old := &Deprecation{After: "2026-09-01", ReplacedBy: "echo@2"}
	future := &Deprecation{After: "2027-01-01"}
	pipe := Pipeline{Name: "p", Steps: []Step{{ID: "a", Kind: "echo@1"}, {ID: "b", Kind: "tally@1"}}}
	plan := func(da, db *Deprecation) Plan {
		return Plan{Pipeline: pipe, Steps: []PlanStep{
			{Step: "a", Kind: Kind{Name: "echo", Version: "1", Deprecation: da}},
			{Step: "b", Kind: Kind{Name: "tally", Version: "1", Deprecation: db}},
		}}
	}
	for _, tc := range []struct {
		name     string
		plan     Plan
		previous map[string]bool
		refused  int
	}{
		{"nothing deprecated", plan(nil, nil), nil, 0},
		{"closed, new file", plan(old, nil), nil, 1},
		{"closed, pinned on main already", plan(old, nil), map[string]bool{"echo@1": true}, 0},
		{"not closed yet", plan(future, nil), nil, 0},
		{"two closed", plan(old, old), map[string]bool{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := newDeprecatedPins(tc.plan, tc.previous, now)
			if tc.refused == 0 {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			pe, ok := problems.As(err)
			if !ok || pe.Type != problems.StepKindDeprecated || len(pe.Errors) != tc.refused {
				t.Fatalf("err = %v, want step-kind-deprecated with %d field errors", err, tc.refused)
			}
			if pe.Errors[0].Path != "steps[0].kind" {
				t.Errorf("path = %s", pe.Errors[0].Path)
			}
		})
	}
}

func TestPins(t *testing.T) {
	got := pins([]byte("name: p\nsteps:\n  - id: a\n    kind: echo@1\n"), "p")
	if !got["echo@1"] || len(got) != 1 {
		t.Fatalf("pins = %v", got)
	}
	if pins([]byte("not: [valid"), "p") != nil || pins(nil, "p") != nil {
		t.Fatal("a broken or missing file pins nothing")
	}
}

func TestResolveLockedCollections(t *testing.T) {
	entries := []lockEntry{
		{Kind: "dataset_version", Collection: "dataset/fleurs-he", Version: "2026-09-01.aaaaaaaaaaaa", ID: "ver_1"},
		{Kind: "dataset_version", Collection: "dataset/fleurs-he", Version: "2026-10-01.bbbbbbbbbbbb", ID: "ver_2"},
		{Kind: "golden_set", Collection: "golden-set/fleurs-he", Version: "2026-10-01.cccccccccccc", ID: "ver_3"},
	}
	for _, tc := range []struct {
		kind, ref, want, problem string
	}{
		{"dataset_version", "fleurs-he", "ver_2", ""},
		{"dataset_version", "dataset/fleurs-he", "ver_2", ""},
		{"dataset_version", "ver_1", "ver_1", ""},
		{"golden_set", "fleurs-he", "ver_3", ""},
		{"golden_set", "ver_1", "", "not-adopted"}, // listed, but as a dataset version
		{"dataset_version", "dataset/other", "", "not-adopted"},
	} {
		l, err := resolveLocked(t.Context(), nil, "prj_x", tc.kind, tc.ref, entries)
		if got := problemSlug(err); got != tc.problem {
			t.Errorf("%s %s: problem %q, want %q (%v)", tc.kind, tc.ref, got, tc.problem, err)
			continue
		}
		if l.VersionID != tc.want {
			t.Errorf("%s %s: resolved %s, want %s", tc.kind, tc.ref, l.VersionID, tc.want)
		}
	}
}

func problemSlug(err error) string {
	if err == nil {
		return ""
	}
	if pe, ok := problems.As(err); ok {
		return pe.Type.Slug
	}
	return err.Error()
}
