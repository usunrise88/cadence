package evals

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckValueAgainstDefaultsRanges(t *testing.T) {
	num := map[string]any{"min": 0, "max": 1}
	pair := map[string]any{"min": -30, "max": 20}
	codecs := map[string]any{"values": []any{"g711-ulaw", "g711-alaw", "gsm-fr"}}
	cases := []struct {
		name string
		v    any
		rng  map[string]any
		want string // a substring of the message; "" passes
	}{
		{"probability in range", 0.5, num, ""},
		{"probability above", 1.5, num, "outside"},
		{"integer", 1, num, ""},
		{"not a number", "x", num, "not a number"},
		{"pair", []any{-10, 6}, pair, ""},
		{"inverted pair", []any{6, -10}, pair, "above the high"},
		{"pair out of range", []any{-40, 6}, pair, "outside"},
		{"not a pair", []any{1}, pair, "two numbers"},
		{"codecs", []any{"g711-ulaw", "gsm-fr"}, codecs, ""},
		{"unknown codec", []any{"g729"}, codecs, "not one of"},
		{"no codecs", []any{}, codecs, "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := checkValue(c.v, c.rng)
			if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
				t.Fatalf("checkValue(%v) = %q, want %q", c.v, got, c.want)
			}
		})
	}
}

func TestRobustnessComparesEachAugmentedCellWithItsUnaugmentedOne(t *testing.T) {
	sum := func(wer float64) json.RawMessage { return mustJSON(map[string]any{"wer": wer}) }
	recs := map[string]Record{"r0": {ID: "r0", Summary: sum(0.10)}, "r1": {ID: "r1", Summary: sum(0.25)}, "r2": {ID: "r2", Summary: sum(0.2)}}
	cells := []Cell{
		{ID: "c0", Role: RoleSubject, GoldenSetVersionID: "g", Profile: "160ms", RecordID: "r0"},
		{ID: "c1", Role: RoleSubject, GoldenSetVersionID: "g", Profile: "160ms", AugmentationIndex: 1, RecordID: "r1"},
		{ID: "c2", Role: RoleBaseline, GoldenSetVersionID: "g", Profile: "160ms", AugmentationIndex: 1, RecordID: "r2"},
		{ID: "c3", Role: RoleSubject, GoldenSetVersionID: "g", Profile: "80ms", AugmentationIndex: 1},
	}
	rows := robustness(cells, recs)
	if len(rows) != 3 {
		t.Fatalf("rows %+v", rows)
	}
	if r := rows[0]; r.CellID != "c1" || *r.WER != 0.25 || *r.WERNone != 0.10 || *r.Degradation != 0.15 {
		t.Fatalf("subject row %+v", r)
	}
	if r := rows[1]; r.WERNone != nil || r.Degradation != nil { // no unaugmented baseline cell
		t.Fatalf("baseline row %+v", r)
	}
	if r := rows[2]; r.WER != nil || r.Degradation != nil { // not scored yet
		t.Fatalf("pending row %+v", r)
	}
	if robustness(cells[:1], recs) != nil {
		t.Fatal("an eval without augmentation has a robustness matrix")
	}
}

func TestSameCellKeepsAugmentationsApart(t *testing.T) {
	a := Cell{GoldenSetVersionID: "g", Profile: "160ms"}
	b := a
	b.AugmentationIndex = 1
	if a.sameCell(b) || !a.sameCell(a) {
		t.Fatal("sameCell must compare the augmentation")
	}
}
