package eviction

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func st(hash, prun, pstate string, ageHours int, referenced string) state {
	return state{Candidate: Candidate{Hash: hash, Type: TypeTrainingState, PipelineRunID: prun,
		CreatedAt: t0.Add(-time.Duration(ageHours) * time.Hour)}, pipelineState: pstate, referenced: referenced}
}

func hashes(cs []Candidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Hash)
	}
	return out
}

func keptHashes(ks []Kept) []string {
	out := []string{}
	for _, k := range ks {
		out = append(out, k.Hash)
	}
	return out
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		states     []state
		evict      []string
		kept       []string
		keptReason string // substring of the first kept reason
	}{
		{"a finished run: every state", []state{st("a1", "plr_a", runDone, 3, ""), st("a2", "plr_a", runDone, 1, "")},
			[]string{"a1", "a2"}, []string{}, ""},
		{"a failed run keeps its newest", []state{st("f1", "plr_f", runFailed, 5, ""), st("f2", "plr_f", runFailed, 1, ""),
			st("f3", "plr_f", runFailed, 3, "")}, []string{"f1", "f3"}, []string{"f2"}, "newest state of a failed run"},
		{"a cancelled run keeps its newest", []state{st("c1", "plr_c", runCancelled, 2, ""), st("c2", "plr_c", runCancelled, 1, "")},
			[]string{"c1"}, []string{"c2"}, "runs.resume"},
		{"a running run keeps everything", []state{st("r1", "plr_r", runRunning, 2, ""), st("r2", "plr_r", runRunning, 1, "")},
			[]string{}, []string{"r1", "r2"}, "still running"},
		{"referenced states stay", []state{st("x1", "plr_x", runDone, 2, "a registered checkpoint")},
			[]string{}, []string{"x1"}, "registered checkpoint"},
		{"a referenced newest still counts as the newest", []state{st("y1", "plr_y", runFailed, 3, ""),
			st("y2", "plr_y", runFailed, 1, "a waiting or running step job names it")},
			[]string{"y1"}, []string{"y2"}, "step job"},
		{"no producer: nothing says it is superseded", []state{st("n1", "", "", 9, "")},
			[]string{}, []string{"n1"}, "no pipeline run"},
		{"runs are independent", []state{st("a1", "plr_a", runFailed, 9, ""), st("b1", "plr_b", runFailed, 1, "")},
			[]string{}, []string{"a1", "b1"}, "newest"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evict, kept := classify(tt.states)
			if got := hashes(evict); !slices.Equal(got, tt.evict) {
				t.Errorf("evict %v, want %v", got, tt.evict)
			}
			if got := keptHashes(kept); !slices.Equal(got, tt.kept) {
				t.Errorf("kept %v, want %v", got, tt.kept)
			}
			if tt.keptReason != "" && (len(kept) == 0 || !strings.Contains(kept[0].Reason, tt.keptReason)) {
				t.Errorf("kept %+v, want a reason with %q", kept, tt.keptReason)
			}
			for _, c := range evict {
				if c.Reason == "" {
					t.Errorf("%s has no reason", c.Hash)
				}
			}
		})
	}
}

func TestNarrow(t *testing.T) {
	evict, kept := classify([]state{
		st("old", "plr_a", runDone, 24*10, ""), st("new", "plr_a", runDone, 2, ""),
		st("live", "plr_b", runRunning, 1, ""),
	})
	t.Run("older than", func(t *testing.T) {
		e, k, err := narrow(evict, kept, Filter{OlderThan: 7 * 24 * time.Hour, Now: t0})
		if err != nil || !slices.Equal(hashes(e), []string{"old"}) || !slices.Contains(keptHashes(k), "new") {
			t.Fatalf("evict %v kept %+v err %v", hashes(e), k, err)
		}
	})
	t.Run("named hashes", func(t *testing.T) {
		e, k, err := narrow(evict, kept, Filter{Hashes: []string{"new"}, Strict: true, Now: t0})
		if err != nil || !slices.Equal(hashes(e), []string{"new"}) || len(k) != 0 {
			t.Fatalf("evict %v kept %+v err %v", hashes(e), k, err)
		}
	})
	t.Run("a named hash that is needed is refused", func(t *testing.T) {
		_, _, err := narrow(evict, kept, Filter{Hashes: []string{"new", "live"}, Strict: true, Now: t0})
		pe, ok := problems.As(err)
		if !ok || pe.Type != problems.ArtifactNotEvictable || !strings.Contains(err.Error(), "live: its run is still running") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("an unknown hash is refused", func(t *testing.T) {
		_, _, err := narrow(evict, kept, Filter{Hashes: []string{"nope"}, Strict: true, Now: t0})
		if pe, ok := problems.As(err); !ok || pe.Type != problems.ArtifactNotEvictable || !strings.Contains(err.Error(), "not a live") {
			t.Fatalf("err %v", err)
		}
	})
	t.Run("lenient: the job skips what stopped being evictable", func(t *testing.T) {
		e, k, err := narrow(evict, kept, Filter{Hashes: []string{"new", "live"}, Now: t0})
		if err != nil || !slices.Equal(hashes(e), []string{"new"}) || !slices.Equal(keptHashes(k), []string{"live"}) {
			t.Fatalf("evict %v kept %+v err %v", hashes(e), k, err)
		}
	})
}
