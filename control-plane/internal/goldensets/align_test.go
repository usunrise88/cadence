package goldensets

import (
	"encoding/json"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

func TestCovers(t *testing.T) {
	for _, tc := range []struct {
		languages []string
		locale    string
		want      bool
	}{
		{[]string{"he", "sr", "hr", "bs"}, "sr-RS", true},
		{[]string{"he", "sr", "hr", "bs"}, "he-IL", true},
		{[]string{"he", "sr", "hr", "bs"}, "nb-NO", false},
		{[]string{"no", "zh"}, "nb-NO", true}, // macrolanguages fold
		{[]string{"zh"}, "cmn-Hans-CN", true},
		{[]string{"he", "sr"}, "sr_Latn_RS", true},
		{[]string{"*"}, "th-TH", true},
		{[]string{"he"}, "", false},
		{nil, "he-IL", false},
	} {
		if got := covers(tc.languages, tc.locale); got != tc.want {
			t.Errorf("covers(%v, %q) = %v, want %v", tc.languages, tc.locale, got, tc.want)
		}
	}
}

func TestMatches(t *testing.T) {
	for _, tc := range []struct {
		refs []string
		want bool
	}{
		{[]string{"golden-set/fleurs-he"}, true},
		{[]string{"ver_1"}, true},
		{[]string{"golden-set/fleurs-*"}, true},
		{[]string{"golden-set/replay-*"}, false},
		{[]string{"golden-set/fleurs"}, false},
	} {
		if got := matches(tc.refs, "ver_1", "golden-set/fleurs-he"); got != tc.want {
			t.Errorf("matches(%v) = %v, want %v", tc.refs, got, tc.want)
		}
	}
}

func TestAlignerParam(t *testing.T) {
	withRef := pipelines.Kind{Name: "fx", Version: "1", Params: json.RawMessage(`{"type":"object","properties":{
		"max_duration_s":{"type":"number"},
		"aligner":{"type":"string","x-cadence":{"registryRef":{"kind":"auxiliary","role":"align"}}}}}`)}
	if p, err := alignerParam(withRef, ""); err != nil || p != "aligner" {
		t.Fatalf("alignerParam = %q, %v", p, err)
	}
	plain := pipelines.Kind{Name: "fx", Version: "1", Params: json.RawMessage(`{"type":"object","properties":{}}`)}
	if p, err := alignerParam(plain, ""); err != nil || p != "" {
		t.Fatalf("no reference: %q, %v", p, err)
	}
	if _, err := alignerParam(plain, "auxiliary/x"); err == nil {
		t.Fatal("an aligner for a kind that takes none was accepted")
	}
}

func TestStepsOf(t *testing.T) {
	p := pipelines.Pipeline{Name: AlignPipeline, Steps: []pipelines.Step{
		{ID: "align-1", In: map[string]string{"data": "$inputs.data1"}},
		{ID: "align-2", In: map[string]string{"data": "$inputs.data2"}},
	}}
	start := &pipelines.StartInput{Pipeline: &p, Inputs: map[string]steps.ArtifactRef{
		"data1": {Hash: "b3:a"}, "data2": {Hash: "b3:b"}}, Estimates: map[string]float64{"align-1": 60, "align-2": 45}}
	got := stepsOf([]AlignSet{{Name: "x", datasetHash: "b3:a"}, {Name: "x-copy", datasetHash: "b3:a"}, {Name: "y", datasetHash: "b3:b"}}, start)
	if got[0].Step != "align-1" || got[1].Step != "align-1" || got[2].Step != "align-2" || got[2].EstimateSeconds != 45 {
		t.Fatalf("stepsOf %+v", got)
	}
}
