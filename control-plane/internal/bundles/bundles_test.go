package bundles

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/exports"
)

const (
	verA = "ver_01a10000-0000-7000-8000-00000000000a"
	verB = "ver_01a10000-0000-7000-8000-00000000000b"
	verC = "ver_01a10000-0000-7000-8000-00000000000c"
)

func entry(id, payload string) PlanVersion {
	return PlanVersion{Entry: exports.BundleEntry{Record: exports.Record{VersionID: id, Payload: json.RawMessage(payload)}}}
}

func TestOrderPutsReferencedVersionsFirst(t *testing.T) {
	// C (a golden set) names A and B; B (a model) names A.
	list := []PlanVersion{
		entry(verC, `{"datasetVersionId":"`+verA+`","normalizerVersionId":"`+verB+`"}`),
		entry(verB, `{"base":{"id":"`+verA+`"}}`),
		entry(verA, `{}`),
	}
	var got []string
	for _, pv := range order(list) {
		got = append(got, pv.Entry.Record.VersionID)
	}
	if strings.Join(got, ",") != strings.Join([]string{verA, verB, verC}, ",") {
		t.Fatalf("order %v", got)
	}
	// A cycle does not loop.
	cyc := []PlanVersion{entry(verA, `{"x":"`+verB+`"}`), entry(verB, `{"x":"`+verA+`"}`)}
	if n := len(order(cyc)); n != 2 {
		t.Fatalf("cycle: %d versions", n)
	}
}

func TestRemap(t *testing.T) {
	out, err := remap(json.RawMessage(`{"a":"`+verA+`","list":["`+verB+`","keep"],"n":3,"nested":{"x":"`+verA+`"}}`),
		map[string]string{verA: verC})
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v["a"] != verC || v["list"].([]any)[0] != verB || v["list"].([]any)[1] != "keep" || v["nested"].(map[string]any)["x"] != verC ||
		v["n"].(float64) != 3 {
		t.Fatalf("remapped %s", out)
	}
}

func TestVersionDate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"2026-10-02.4f1c2a9e7b30", time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)},
		{"2026-10-02", time.Time{}},
		{"2026-13-02.4f1c2a9e7b30", time.Time{}},
		{"x", time.Time{}},
	} {
		if got := versionDate(tc.in); !got.Equal(tc.want) {
			t.Errorf("%s: %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestBundleCheck(t *testing.T) {
	good := exports.Record{Kind: "normalizer", Collection: "normalizer/he-il", Version: "2026-10-02.4f1c2a9e7b30", VersionID: verA,
		Fingerprint: strings.Repeat("a", 64)}
	for name, tc := range map[string]struct {
		doc  exports.BundleDoc
		want string
	}{
		"ok":            {exports.BundleDoc{Format: exports.ProjectBundleFormat, Versions: []exports.BundleEntry{{Record: good}}}, ""},
		"dataset":       {exports.BundleDoc{Format: exports.DatasetBundleFormat}, "not a project bundle"},
		"kind":          {exports.BundleDoc{Format: exports.ProjectBundleFormat, Versions: []exports.BundleEntry{{Record: with(good, func(r *exports.Record) { r.Kind = "nope" })}}}, "unknown registry kind"},
		"collection":    {exports.BundleDoc{Format: exports.ProjectBundleFormat, Versions: []exports.BundleEntry{{Record: with(good, func(r *exports.Record) { r.Collection = "dataset/x" })}}}, "collection name"},
		"version":       {exports.BundleDoc{Format: exports.ProjectBundleFormat, Versions: []exports.BundleEntry{{Record: with(good, func(r *exports.Record) { r.Version = "v1" })}}}, "version string"},
		"repeated":      {exports.BundleDoc{Format: exports.ProjectBundleFormat, Versions: []exports.BundleEntry{{Record: good}, {Record: good}}}, "repeated"},
		"dataset path":  {exports.BundleDoc{Format: exports.ProjectBundleFormat, Versions: []exports.BundleEntry{{Record: good, Dataset: "../x"}}}, "leaves the bundle"},
		"artifact hash": {exports.BundleDoc{Format: exports.ProjectBundleFormat, Artifacts: []exports.BundleArtifact{{Hash: "b3:x", Type: "checkpoint"}}}, "not a content-store artifact"},
	} {
		err := (&Bundle{URI: "mount://exports/b", Doc: tc.doc}).check()
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%s: %v, want %q", name, err, tc.want)
		}
	}
}

func with(r exports.Record, f func(*exports.Record)) exports.Record {
	f(&r)
	return r
}
