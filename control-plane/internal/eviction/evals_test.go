package eviction

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func evalArt(hash string, uses ...evalUse) evalArtifact {
	return evalArtifact{Candidate: Candidate{Hash: hash, Type: "scores"}, uses: uses}
}

func used(rec string, ageDays int) evalUse {
	return evalUse{Record: rec, LastUse: t0.Add(-time.Duration(ageDays) * 24 * time.Hour)}
}

func TestClassifyEval(t *testing.T) {
	const retention = 30 * 24 * time.Hour
	modelEval := used("erc_model", 90)
	modelEval.Model = "ver_model"
	running := used("erc_running", 60)
	running.Active = "evl_running"
	tests := []struct {
		name   string
		arts   []evalArtifact
		evict  []string
		kept   []string
		reason string // a substring of the first kept reason
	}{
		{"an old record's artifact goes", []evalArtifact{evalArt("b3:old", used("erc_old", 31))}, []string{"b3:old"}, []string{}, ""},
		{"a record within the retention keeps it", []evalArtifact{evalArt("b3:new", used("erc_new", 29))}, []string{}, []string{"b3:new"},
			"less than 30 days ago"},
		{"a newer record sharing the artifact protects it",
			[]evalArtifact{evalArt("b3:shared", used("erc_old", 200), used("erc_newer", 3))}, []string{}, []string{"b3:shared"},
			"erc_newer was last used"},
		{"a registered model's eval protects it", []evalArtifact{evalArt("b3:model", modelEval)}, []string{}, []string{"b3:model"},
			"registered model version ver_model"},
		{"a model's eval protects an artifact an old record shares with it",
			[]evalArtifact{evalArt("b3:both", used("erc_old", 400), modelEval)}, []string{}, []string{"b3:both"}, "ver_model"},
		{"an unfinished eval protects it", []evalArtifact{evalArt("b3:running", running)}, []string{}, []string{"b3:running"},
			"evl_running has not finished"},
		{"another reference protects it", []evalArtifact{{Candidate: Candidate{Hash: "b3:ref"}, uses: []evalUse{used("erc_old", 99)},
			referenced: "an input of a running pipeline run"}}, []string{}, []string{"b3:ref"}, "running pipeline run"},
		{"every old use lets it go", []evalArtifact{evalArt("b3:all", used("erc_a", 45), used("erc_b", 31))}, []string{"b3:all"}, []string{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			evict, kept := classifyEval(tt.arts, retention, t0)
			if got := hashes(evict); !slices.Equal(got, tt.evict) {
				t.Errorf("evict = %v, want %v", got, tt.evict)
			}
			if got := keptHashes(kept); !slices.Equal(got, tt.kept) {
				t.Errorf("kept = %v, want %v", got, tt.kept)
			}
			if tt.reason != "" && (len(kept) == 0 || !strings.Contains(kept[0].Reason, tt.reason)) {
				t.Errorf("kept = %+v, want a reason with %q", kept, tt.reason)
			}
			for _, c := range evict {
				if !strings.Contains(c.Reason, "more than 30 days ago") {
					t.Errorf("reason %q", c.Reason)
				}
			}
		})
	}
}
