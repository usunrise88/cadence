package exports

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
)

func TestStepKindOf(t *testing.T) {
	for format, want := range map[string]string{
		FormatShar: SharKind, FormatHub: HubKind, FormatBundle: GenericKind, "nemo-manifest": GenericKind,
	} {
		if got := StepKindOf(format); got != want {
			t.Errorf("StepKindOf(%q) = %q, want %q", format, got, want)
		}
	}
}

func TestReadManifestChecksTheExportArtifact(t *testing.T) {
	store, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(m any) string {
		t.Helper()
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		h, err := store.PutBytes(b)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := store.PutManifest(cas.Manifest{Files: []cas.File{{Path: ManifestFile, Hash: h, Size: int64(len(b))}}})
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	good := Manifest{Format: ManifestFormat, ExportFormat: FormatShar, Target: "mount://exports/a/b", Utterances: 2,
		Files: []File{{Path: "train/cuts.000000.jsonl.gz", Hash: cas.Hash([]byte("x")), Bytes: 1}}}
	m, err := ReadManifest(store, put(good))
	if err != nil || m.Target != good.Target || len(m.Files) != 1 {
		t.Fatalf("good manifest: %+v, %v", m, err)
	}
	tests := []struct {
		name string
		edit func(*Manifest)
		want string
	}{
		{"format", func(m *Manifest) { m.Format = "x" }, "is not cadence.export/1"},
		{"export format", func(m *Manifest) { m.ExportFormat = "tar" }, "not an export format"},
		{"target", func(m *Manifest) { m.Target = "/tmp/x" }, "is not cas"},
		{"mount target", func(m *Manifest) { m.Target = "mount://exports/../x" }, "mount"},
		{"path", func(m *Manifest) { m.Files = []File{{Path: "../x", Bytes: 1}} }, "relative"},
		{"hash", func(m *Manifest) { m.Files = []File{{Path: "a", Hash: "sha256:x", Bytes: 1}} }, "not a b3 hash"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bad := good
			bad.Files = append([]File{}, good.Files...)
			tc.edit(&bad)
			if _, err := ReadManifest(store, put(bad)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err %v, want %q", err, tc.want)
			}
		})
	}
	for _, target := range []string{TargetCAS, "hf://datasets/acme/x"} {
		ok := good
		ok.Target = target
		if _, err := ReadManifest(store, put(ok)); err != nil {
			t.Errorf("target %s: %v", target, err)
		}
	}
}
