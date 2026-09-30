package contract

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// frameworkNames are the model family and runtime names only a framework pack may know (R41): the Nemotron family,
// the NeMo runtime and its architecture names. Data (defaults.yaml, templates), docs, the worker and generated files
// (whose comments quote the contract's examples) may name them; control-plane and web source may not.
var frameworkNames = regexp.MustCompile(`(?i)nemotron|\bnemo\b|nemo[-_]speech|fastconformer|parakeet`)

// TestNoCodeNamesAFramework is the R41 seam check of the phase-2 gate: no control-plane (Go, non-test) or web source
// names the Nemotron family or the NeMo runtime, so nothing can branch on them.
func TestNoCodeNamesAFramework(t *testing.T) {
	roots := []struct {
		dir  string
		exts []string
		skip func(rel string) bool
	}{
		{"../..", []string{".go"}, func(rel string) bool {
			return strings.HasPrefix(rel, "templates/") || strings.HasSuffix(rel, "_test.go") ||
				strings.HasSuffix(rel, ".gen.go") || strings.Contains(rel, "/testdata/")
		}},
		{"../../../web/src", []string{".ts", ".tsx"}, func(rel string) bool {
			return strings.Contains(rel, ".test.") || strings.HasPrefix(rel, "api/gen/") ||
				strings.HasSuffix(rel, ".gen.ts") || strings.HasPrefix(rel, "spikes/") || strings.HasPrefix(rel, "test/")
		}},
	}
	scanned := 0
	for _, r := range roots {
		err := filepath.WalkDir(r.dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(r.dir, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == "dist" {
					return filepath.SkipDir
				}
				return nil
			}
			if !hasExt(rel, r.exts) || r.skip(rel) {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for i, line := range strings.Split(string(b), "\n") {
				if m := frameworkNames.FindString(line); m != "" {
					t.Errorf("%s:%d names %q; family and runtime names belong in worker packs, defaults and templates (R41)",
						path, i+1, m)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 100 {
		t.Fatalf("scanned only %d files; the roots moved?", scanned)
	}
}

func hasExt(name string, exts []string) bool {
	for _, e := range exts {
		if strings.HasSuffix(name, e) {
			return true
		}
	}
	return false
}
