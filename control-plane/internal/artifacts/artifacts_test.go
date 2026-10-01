package artifacts

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
)

func TestVerifyAndReadContent(t *testing.T) {
	store, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	put := func(s string) string {
		h, err := store.PutBytes([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	a, b := put("alpha"), put("be")
	dir, err := store.PutManifest(cas.Manifest{Files: []cas.File{{Path: "x/a.txt", Hash: a, Size: 5}, {Path: "b.txt", Hash: b, Size: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		ref     steps.ArtifactRef
		wantDir bool
		wantErr string
	}{
		{"file", steps.ArtifactRef{Hash: a, Type: "text", Size: 5}, false, ""},
		{"file size differs", steps.ArtifactRef{Hash: a, Type: "text", Size: 6}, false, "has 5 bytes, declared 6"},
		{"missing", steps.ArtifactRef{Hash: cas.Hash([]byte("nope")), Type: "text", Size: 4}, false, "not in the content store"},
		{"bad hash", steps.ArtifactRef{Hash: "sha:1", Type: "text"}, false, "not an artifact hash"},
		{"bad type", steps.ArtifactRef{Hash: a, Type: "Text!", Size: 5}, false, "not an artifact type"},
		{"directory by total", steps.ArtifactRef{Hash: dir, Type: "shar", Size: 7}, true, ""},
		{"directory size differs", steps.ArtifactRef{Hash: dir, Type: "shar", Size: 8}, false, "holds 7 bytes, declared 8"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isDir, err := Verify(store, tt.ref)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || isDir != tt.wantDir {
				t.Fatalf("dir %v err %v", isDir, err)
			}
		})
	}
	if _, err := Verify(store, steps.ArtifactRef{Hash: cas.Hash([]byte("x")), Type: "text", Size: 1}); !errors.Is(err, ErrMissing) {
		t.Errorf("missing blob is not ErrMissing: %v", err)
	}

	d := Artifact{Hash: dir, Type: "shar", Size: 7, Directory: true}
	c, err := ReadContent(store, d, "x/a.txt")
	if err != nil || c.Content != "alpha" || c.Encoding != "utf8" {
		t.Errorf("directory file %+v %v", c, err)
	}
	if c, _ := ReadContent(store, d, ""); c.Omitted == "" {
		t.Error("a directory without path returned content")
	}
	if _, err := ReadContent(store, d, "nope"); err == nil {
		t.Error("an unknown file of a directory was read")
	}
	bin := put("\xff\xfe")
	if c, _ := ReadContent(store, Artifact{Hash: bin, Size: 2}, ""); c.Encoding != "base64" || c.Content != "//4=" {
		t.Errorf("binary content %+v", c)
	}
	if c, _ := ReadContent(store, Artifact{Hash: a, Size: ContentLimit + 1}, ""); c.Omitted == "" {
		t.Error("a large artifact was returned inline")
	}
	files, err := Files(context.Background(), nil, store, d)
	if err != nil || len(files) != 2 || files[0].Path != "b.txt" {
		t.Errorf("files %+v %v", files, err)
	}
	gone := Artifact{Hash: a, Size: 5, Evicted: &Eviction{At: time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)}}
	if c, _ := ReadContent(store, gone, ""); c.Content != "" || !strings.Contains(c.Omitted, "evicted") ||
		!strings.Contains(c.Omitted, "2026-10-01") {
		t.Errorf("an evicted artifact's content %+v", c)
	}
}
