package audit

import "testing"

func TestRecorded(t *testing.T) {
	cases := []struct {
		operation, outcome string
		want               bool
	}{
		{"workspaces.set", OutcomeOK, false},            // the layout autosave: a preference, not a command
		{"workspaces.set", "precondition-failed", true}, // a failed attempt is still recorded
		{"workspaces.set", OutcomeDenied, true},         // so is a denial
		{"projects.new", OutcomeOK, true},
		{"views.set", OutcomeOK, true}, // a saved search is named by the person on purpose
		{"projects.archive", OutcomeApproval, true},
	}
	for _, c := range cases {
		if got := Recorded(c.operation, c.outcome); got != c.want {
			t.Errorf("Recorded(%q, %q) = %v, want %v", c.operation, c.outcome, got, c.want)
		}
	}
}
