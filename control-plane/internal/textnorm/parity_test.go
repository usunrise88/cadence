package textnorm

import (
	"encoding/json"
	"os"
	"testing"
)

// TestParityWithTheWorker: the control plane interprets a payload as the worker's scorer does
// (worker/tests/fixtures/normalizer_parity.json, whose wants the Python interpreter wrote; its own test reads the same
// file).
func TestParityWithTheWorker(t *testing.T) {
	b, err := os.ReadFile("../../../worker/tests/fixtures/normalizer_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Normalizers map[string]json.RawMessage `json:"normalizers"`
		Cases       []struct {
			Normalizer string `json:"normalizer"`
			In         string `json:"in"`
			Want       string `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	ns := map[string]*Normalizer{}
	for name, raw := range doc.Normalizers {
		spec, err := Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if ns[name], err = New(spec); err != nil {
			t.Fatal(err)
		}
	}
	if len(doc.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range doc.Cases {
		if got := ns[c.Normalizer].Apply(c.In); got != c.Want {
			t.Errorf("%s(%+q) = %+q, want %+q", c.Normalizer, c.In, got, c.Want)
		}
	}
}
