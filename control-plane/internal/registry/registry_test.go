package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/api"
	"github.com/usunrise88/cadence/control-plane/templates"
)

func TestFingerprintAndVersionString(t *testing.T) {
	a, err := Canonical([]byte(`{"b": 1, "a": [1, 2.50, "x"]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Canonical([]byte("{\n  \"a\": [1, 2.50, \"x\"],\n  \"b\": 1\n}"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || string(a) != `{"a":[1,2.50,"x"],"b":1}` {
		t.Fatalf("canonical forms differ: %s vs %s", a, b)
	}
	fp := Fingerprint(a)
	if len(fp) != 64 {
		t.Fatalf("fingerprint %q", fp)
	}
	at := time.Date(2026, 9, 29, 23, 30, 0, 0, time.FixedZone("x", -2*3600)) // 2026-09-30 in UTC
	if got := VersionString(at, fp); got != "2026-09-30."+fp[:12] {
		t.Fatalf("VersionString = %s", got)
	}
	if _, err := Canonical([]byte(`{} {}`)); err == nil {
		t.Error("trailing data accepted")
	}
}

func TestSearchQualifiers(t *testing.T) {
	f := Search(Filter{Limit: 5, ProjectID: "prj_1"}, "fleurs kind:dataset_version tag:fixture locale:he-IL state:frozen hebrew tag:")
	want := Filter{
		Limit: 5, ProjectID: "prj_1", Kind: KindDataset, State: StateFrozen, Tags: []string{"fixture"},
		Locales: []string{"he-IL"}, Text: []string{"fleurs", "hebrew", "tag:"},
	}
	if !reflect.DeepEqual(f, want) {
		t.Fatalf("Search = %+v\nwant     %+v", f, want)
	}
}

func TestReservations(t *testing.T) {
	for name, want := range map[string]string{"production": AliasPromotion, "baseline": AliasGated, "train-current": AliasFree} {
		if got := Reservation(name); got != want {
			t.Errorf("Reservation(%s) = %s, want %s", name, got, want)
		}
	}
	if !Gated("baseline") || Gated("production") || Gated("train-current") {
		t.Error("only baseline is gated")
	}
}

// TestBundledInputsMatchContract checks every bundled version against the contract's payload schema for its kind,
// so a fixture or template the API could not serve never reaches the registry.
func TestBundledInputsMatchContract(t *testing.T) {
	spec, err := api.GetSpec()
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]string{
		KindBaseModel: "BaseModelPayload", KindDataset: "DatasetPayload", KindTemplate: "TemplatePayload",
		KindNormalizer: "NormalizerPayload",
	}
	inputs, err := BundledInputs(templates.FS)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, in := range inputs {
		if names[in.Name] {
			t.Errorf("collection %s registered twice", in.Name)
		}
		names[in.Name] = true
		if !collectionName.MatchString(in.Name) || !strings.HasPrefix(in.Name, kinds[in.Kind].prefix) {
			t.Errorf("collection name %q does not fit kind %s", in.Name, in.Kind)
		}
		if !in.Freeze || in.Licence == "" || in.Description == "" {
			t.Errorf("%s: bundled versions are frozen and carry a licence and a description", in.Name)
		}
		var doc any
		if err := json.Unmarshal(in.Payload, &doc); err != nil {
			t.Fatal(err)
		}
		if err := spec.Components.Schemas[schemas[in.Kind]].Value.VisitJSON(doc); err != nil {
			t.Errorf("%s does not match %s: %v", in.Name, schemas[in.Kind], err)
		}
	}
	for _, want := range []string{
		"base-model/nemotron-3.5-asr-streaming-0.6b", "dataset/fleurs-he-smoke", "template/instructions-default",
		"template/skill-cadence-train", "template/pipeline-train-stage", "template/agent-config-claude-settings",
		"normalizer/basic", "normalizer/he-il",
	} {
		if !names[want] {
			t.Errorf("bundled inputs lack %s", want)
		}
	}
}

func TestTemplateInputs(t *testing.T) {
	tree := fstest.MapFS{
		"README.md":                              {Data: []byte("ignored")},
		"embed.go":                               {Data: []byte("ignored")},
		"unknown/x.txt":                          {Data: []byte("ignored")},
		"presets/guardrails-default.yaml":        {Data: []byte("opaque: [yaml")},
		"skills/cadence-data/SKILL.md":           {Data: []byte("# data")},
		"skills/cadence-data/references/more.md": {Data: []byte("more")},
	}
	ins, err := TemplateInputs(tree)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]TemplatePayload{}
	for _, in := range ins {
		var p TemplatePayload
		if err := json.Unmarshal(in.Payload, &p); err != nil {
			t.Fatal(err)
		}
		got[in.Name] = p
	}
	if len(got) != 2 {
		t.Fatalf("got %d templates, want 2: %v", len(got), got)
	}
	preset := got["template/preset-guardrails-default"]
	if preset.TemplateKind != "preset" || preset.Path != "presets/guardrails-default.yaml" || len(preset.Files) != 1 {
		t.Errorf("preset = %+v", preset)
	}
	skill := got["template/skill-cadence-data"]
	if skill.TemplateKind != "skill" || len(skill.Files) != 2 || skill.Files[0].Path != "skills/cadence-data/SKILL.md" ||
		skill.Files[1].Bytes != 4 || len(skill.Files[1].SHA256) != 64 {
		t.Errorf("skill = %+v", skill)
	}

	// A changed file changes the fingerprint; an unchanged tree keeps it.
	again, _ := TemplateInputs(tree)
	tree["skills/cadence-data/SKILL.md"] = &fstest.MapFile{Data: []byte("# data, edited")}
	changed, _ := TemplateInputs(tree)
	fp := func(ins []RegisterInput, name string) string {
		for _, in := range ins {
			if in.Name == name {
				c, _ := Canonical(in.Payload)
				return Fingerprint(c)
			}
		}
		return ""
	}
	if fp(ins, "template/skill-cadence-data") != fp(again, "template/skill-cadence-data") ||
		fp(ins, "template/skill-cadence-data") == fp(changed, "template/skill-cadence-data") {
		t.Error("fingerprints do not follow content")
	}
}
