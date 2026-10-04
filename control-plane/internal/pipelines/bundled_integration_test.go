//go:build integration

package pipelines_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/pipelines/pipelinestest"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/templates"
)

// TestBundledPipelinesPlan plans every bundled pipeline (the templates and the example project's copies) against the
// step kinds the worker packs publish (testdata/bundled-kinds.json, kept current by the worker's
// tests/test_bundled_pins.py) and the seeded auxiliaries, as a pipeline file is checked when it is saved: kinds and
// versions exist, every input is wired to the type its kind consumes, every parameter resolves and fits its range,
// and an optional step is read only where the engine allows it.
func TestBundledPipelinesPlan(t *testing.T) {
	r := newRig(t, nil)
	ctx := context.Background()
	raw, err := os.ReadFile("testdata/bundled-kinds.json")
	if err != nil {
		t.Fatal(err)
	}
	var kinds []map[string]any
	if err := json.Unmarshal(raw, &kinds); err != nil {
		t.Fatal(err)
	}
	if err := pipelinestest.Register(ctx, r.pool, kinds...); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Seed(ctx, r.pool, templates.FS, time.Now()); err != nil {
		t.Fatal(err)
	}
	dirs := []string{"../../templates/pipelines", "../../../recipes/projects/hebrew/pipelines"}
	seen := map[string]bool{}
	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: no pipelines (%v)", dir, err)
		}
		for _, f := range files {
			content, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			name := strings.TrimSuffix(filepath.Base(f), ".yaml")
			seen[name] = true
			if err := r.eng.Validate(ctx, r.pool, content, name, nil); err != nil {
				t.Errorf("%s: %v", f, err)
			}
		}
	}
	for _, want := range []string{"data-ingest", "pseudo-label", "import", "train-stage", "eval-matrix"} {
		if !seen[want] {
			t.Errorf("no bundled pipeline %s", want)
		}
	}
}
