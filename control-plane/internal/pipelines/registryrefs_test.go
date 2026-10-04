package pipelines

import (
	"encoding/json"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

func TestRegistryRefs(t *testing.T) {
	k := Kind{Name: "member", Version: "1", Params: json.RawMessage(`{"type":"object","properties":{
		"auxiliary":{"type":"string","x-cadence":{"default":"auxiliary/w","registryRef":{"kind":"auxiliary","role":"pseudolabel"}}},
		"batch":{"type":"integer","x-cadence":{"default":8}}}}`)}
	refs := RegistryRefs(k)
	if len(refs) != 1 || refs["auxiliary"] != (RefSpec{Kind: "auxiliary", Role: "pseudolabel"}) {
		t.Fatalf("refs %+v", refs)
	}
	if got := RegistryRefs(Kind{}); len(got) != 0 {
		t.Fatalf("no params: %+v", got)
	}
}

func TestHashParamsCoverTheResolvedVersion(t *testing.T) {
	params := map[string]any{"auxiliary": "auxiliary/w", "batch": 8.0}
	plain := StepRow{Params: params}
	if h := hashParams(plain); len(h) != 2 {
		t.Fatalf("a step without references hashes its params as before: %+v", h)
	}
	in := map[string]steps.ArtifactRef{"data": {Hash: "b3:data", Type: "dataset"}}
	a := StepRow{Params: params, Auxiliaries: map[string]steps.RegistryRef{"auxiliary": {VersionID: "ver_1"}}}
	b := StepRow{Params: params, Auxiliaries: map[string]steps.RegistryRef{"auxiliary": {VersionID: "ver_2"}}}
	ha, _ := InputHash("member", "1", "ver_rt", hashParams(a), in)
	hb, _ := InputHash("member", "1", "ver_rt", hashParams(b), in)
	hp, _ := InputHash("member", "1", "ver_rt", hashParams(plain), in)
	if ha == hb || ha == hp {
		t.Fatalf("hashes %s %s %s", ha, hb, hp)
	}
	if _, ok := params["$registryRefs"]; ok {
		t.Fatal("hashParams changed the step's params")
	}
}
