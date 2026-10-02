package evals

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/pipelines"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
)

func fam(ps ...Profile) family { return family{Profiles: ps} }

func names(ps []Profile) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// TestChooseProfiles: families line up by milliseconds (R43), the columns carry the subject family's names, and the
// primary profile resolves by name, else by the latency its name states.
func TestChooseProfiles(t *testing.T) {
	d := defaults.Get() // matrix_profiles [80ms, 160ms, 1120ms], primary_profile 160ms
	byMs := fam(Profile{Name: "80ms", LatencyMs: 80}, Profile{Name: "160ms", LatencyMs: 160}, Profile{Name: "560ms", LatencyMs: 560},
		Profile{Name: "1120ms", LatencyMs: 1120})
	named := fam(Profile{Name: "fast", LatencyMs: 80}, Profile{Name: "mid", LatencyMs: 160}, Profile{Name: "slow", LatencyMs: 1120})
	tests := []struct {
		name      string
		primary   string
		requested []string
		fams      []family
		want      []string
		prim      string
		problem   problems.Type
	}{
		{name: "same names", primary: "160ms", fams: []family{byMs, byMs}, want: []string{"80ms", "160ms", "1120ms"}, prim: "160ms"},
		{name: "a subject family naming its profiles differently", primary: "160ms", fams: []family{named, byMs},
			want: []string{"fast", "mid", "slow"}, prim: "mid"},
		{name: "latency fallback for the primary profile", primary: "160ms", requested: []string{"slow"}, fams: []family{named},
			want: []string{"slow", "mid"}, prim: "mid"},
		{name: "a requested profile named by the baseline's family", primary: "1120ms", requested: []string{"160ms"},
			fams: []family{named, byMs}, want: []string{"mid", "slow"}, prim: "slow"},
		{name: "a primary profile no family has", primary: "320ms", fams: []family{byMs}, want: []string{"80ms", "160ms", "1120ms"}, prim: ""},
		{name: "an unknown requested profile", primary: "160ms", requested: []string{"2000ms"}, fams: []family{byMs, named},
			problem: problems.ValidationFailed},
		{name: "no shared profile", primary: "160ms", fams: []family{fam(Profile{Name: "80ms", LatencyMs: 80}), fam(Profile{Name: "320ms", LatencyMs: 320})},
			problem: problems.FamilyUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, prim, err := chooseProfiles(d, tt.primary, tt.requested, tt.fams...)
			if tt.problem != (problems.Type{}) {
				pe, ok := problems.As(err)
				if !ok || pe.Type != tt.problem {
					t.Fatalf("err %v, want %v", err, tt.problem)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(names(got), tt.want) || prim != tt.prim {
				t.Fatalf("profiles %v primary %q, want %v %q", names(got), prim, tt.want, tt.prim)
			}
		})
	}
	// A transcribe step decodes at its own family's name for the column.
	if own := (family{Profiles: byMs.Profiles}).own(Profile{Name: "mid", LatencyMs: 160}); own != "160ms" {
		t.Fatalf("own %q", own)
	}
}

// TestVerdictResolvesThePrimaryProfileByLatency: a gate naming 160ms reads the cells of a family that calls that
// profile otherwise.
func TestVerdictResolvesThePrimaryProfileByLatency(t *testing.T) {
	e := Eval{
		Profiles:     []Profile{{Name: "mid", LatencyMs: 160}, {Name: "slow", LatencyMs: 1120}},
		Significance: Significance{Samples: 1000, Level: 0.95, Seed: 1},
		GoldenSets:   []GoldenSet{{VersionID: "ver_t", Name: "golden-set/t", Locale: "he-IL"}},
	}
	good := mustJSON(Delta{WER: Interval{Value: -0.02, Low: -0.03, High: -0.01}, Samples: 1000, Level: 0.95})
	bad := mustJSON(Delta{WER: Interval{Value: 0.05, Low: 0.04, High: 0.06}, Samples: 1000, Level: 0.95})
	cells := []Cell{
		{ID: "s-mid", Role: RoleSubject, GoldenSetVersionID: "ver_t", Profile: "mid", RecordID: "r1", Delta: good},
		{ID: "b-mid", Role: RoleBaseline, GoldenSetVersionID: "ver_t", Profile: "mid", RecordID: "r2"},
		{ID: "s-slow", Role: RoleSubject, GoldenSetVersionID: "ver_t", Profile: "slow", RecordID: "r3", Delta: bad},
		{ID: "b-slow", Role: RoleBaseline, GoldenSetVersionID: "ver_t", Profile: "slow", RecordID: "r4"},
	}
	g := DefaultGate(defaults.Get())
	g.PrimaryProfile, g.DeletionsInsertions = "160ms", false
	v := (&Service{}).verdict(e, []string{"he-IL"}, GateFile{Gate: g}, cells, map[string]Record{})
	if v.Verdict != VerdictPassed || len(v.Checks) != 1 || v.Checks[0].Profile != "mid" {
		t.Fatalf("verdict %s %+v", v.Verdict, v.Checks)
	}
}

// TestVerdictOnCERReadsOnlyThePrimaryCell: a set of a language without spaces is gated on CER, and the cells of other
// decoding variants and augmentations never enter the verdict.
func TestVerdictOnCERReadsOnlyThePrimaryCell(t *testing.T) {
	e := Eval{
		Profiles:     []Profile{{Name: "160ms", LatencyMs: 160}},
		Significance: Significance{Samples: 1000, Level: 0.95, Seed: 1},
		GoldenSets:   []GoldenSet{{VersionID: "ver_zh", Name: "golden-set/fleurs-zh", Locale: "zh-CN"}},
	}
	char := func(v, lo, hi float64) json.RawMessage {
		return mustJSON(Delta{Unit: UnitChar, WER: Interval{Value: v, Low: lo, High: hi}, Samples: 1000, Level: 0.95})
	}
	cells := []Cell{
		{ID: "s", Role: RoleSubject, GoldenSetVersionID: "ver_zh", Profile: "160ms", RecordID: "rs", Delta: char(-0.05, -0.07, -0.03)},
		{ID: "b", Role: RoleBaseline, GoldenSetVersionID: "ver_zh", Profile: "160ms", RecordID: "rb"},
		// A boosted variant and an augmented cell that regress: not the deployed decoding, not the gate's business.
		{ID: "s-boost", Role: RoleSubject, GoldenSetVersionID: "ver_zh", Profile: "160ms", DecodingIndex: 1, RecordID: "rs", Delta: char(0.2, 0.1, 0.3)},
		{ID: "b-boost", Role: RoleBaseline, GoldenSetVersionID: "ver_zh", Profile: "160ms", DecodingIndex: 1, RecordID: "rb"},
		{ID: "s-aug", Role: RoleSubject, GoldenSetVersionID: "ver_zh", Profile: "160ms", AugmentationIndex: 1, RecordID: "rs", Delta: char(0.2, 0.1, 0.3)},
		{ID: "b-aug", Role: RoleBaseline, GoldenSetVersionID: "ver_zh", Profile: "160ms", AugmentationIndex: 1, RecordID: "rb"},
	}
	recs := map[string]Record{"rs": {Summary: json.RawMessage(`{"wer":0.95,"cer":0.20}`)}, "rb": {Summary: json.RawMessage(`{"wer":0.97,"cer":0.25}`)}}
	g := DefaultGate(defaults.Get())
	g.DeletionsInsertions = true
	v := (&Service{}).verdict(e, []string{"zh-CN"}, GateFile{Gate: g}, cells, recs)
	if v.Verdict != VerdictPassed || len(v.Checks) != 2 {
		t.Fatalf("verdict %s %+v", v.Verdict, v.Checks)
	}
	c := v.Checks[0]
	if c.Kind != CheckTarget || c.Unit != UnitChar || *c.CandidateWER != 0.20 || *c.BaselineWER != 0.25 || !strings.Contains(c.Message, "CER") {
		t.Fatalf("target check %+v", c)
	}
	if di := v.Checks[1]; di.Kind != CheckDelIns || di.State != CheckPassed || !strings.Contains(di.Message, "not applicable") {
		t.Fatalf("deletions/insertions check %+v", di)
	}
}

// putScores stores a scores directory artifact with these utterance rows.
func putScores(t *testing.T, store *cas.Store, rows []Utterance) string {
	t.Helper()
	var b strings.Builder
	for _, u := range rows {
		b.Write(mustJSON(u))
		b.WriteByte('\n')
	}
	h, err := store.PutBytes([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	dir, err := store.PutManifest(cas.Manifest{Files: []cas.File{{Path: UtterancesFile, Hash: h, Size: int64(b.Len())}}})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDeltaAtRecomputes: a stored delta is used only at the significance and unit it was computed at; otherwise the
// gate recomputes it from the two cells' scores.
func TestDeltaAtRecomputes(t *testing.T) {
	store, err := cas.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var subj, base []Utterance
	for i := range 6 {
		a := string(rune('a' + i))
		subj = append(subj, Utterance{Audio: a, RefWords: 10, Sub: 1, RefChars: 40, CharErrors: 2})
		base = append(base, Utterance{Audio: a, RefWords: 10, Sub: 3, RefChars: 40, CharErrors: 6})
	}
	s := &Service{CAS: store}
	recs := map[string]Record{"rs": {ID: "rs", Scores: putScores(t, store, subj)}, "rb": {ID: "rb", Scores: putScores(t, store, base)}}
	stored := Delta{BaselineCellID: "b", Groups: 99, Samples: 1000, Level: 0.95} // a marker no recomputation gives
	cells := []Cell{
		{ID: "s", Role: RoleSubject, GoldenSetVersionID: "g", Profile: "160ms", RecordID: "rs", Delta: mustJSON(stored)},
		{ID: "b", Role: RoleBaseline, GoldenSetVersionID: "g", Profile: "160ms", RecordID: "rb"},
	}
	have := Significance{Samples: 1000, Level: 0.95, Seed: 1}
	if d := s.deltaAt(cells[0], cells, recs, have, have, false); d == nil || d.Groups != 99 {
		t.Fatalf("the stored delta was not used: %+v", d)
	}
	want := Significance{Samples: 2000, Level: 0.9, Seed: 1}
	d := s.deltaAt(cells[0], cells, recs, have, want, false)
	if d == nil || d.Error != "" || d.Groups != 6 || d.Samples != 2000 || d.Level != 0.9 || d.WER.Value != -0.2 || !d.Significant {
		t.Fatalf("recomputed at another significance: %+v", d)
	}
	// The same significance but the other unit (the set became char-scored): recomputed on characters.
	d = s.deltaAt(cells[0], cells, recs, have, have, true)
	if d == nil || d.Unit != UnitChar || d.WER.Value != -0.1 {
		t.Fatalf("recomputed on characters: %+v", d)
	}
}

// TestDecodingHashCoversResolvedParams: the record key changes with a transcribe parameter's default (a defaults.yaml
// change) but not with one that only changes how the decode runs; the family's own profile name and a base model's
// materialize kind enter it.
func TestDecodingHashCoversResolvedParams(t *testing.T) {
	kind := func(eou, reserve int) pipelines.Kind {
		prop := func(def int) map[string]any {
			return map[string]any{"type": "integer", "x-cadence": map[string]any{"default": def, "description": "d", "source": "s", "range": "any"}}
		}
		params := map[string]any{"type": "object", "properties": map[string]any{
			"profile":             map[string]any{"type": "string", "x-cadence": map[string]any{"default": "160ms", "description": "d", "source": "s", "range": "any"}},
			"target_lang":         map[string]any{"type": "string", "x-cadence": map[string]any{"default": "", "description": "d", "source": "s", "range": "any"}},
			"stop_history_eou_ms": prop(eou), "cuda_context_reserve_mb": prop(reserve),
		}}
		return pipelines.Kind{Name: "fx_transcribe", Version: "3", Params: mustJSON(params)}
	}
	b := &builder{s: &Service{Defaults: defaults.Get}}
	gs := GoldenSet{Locale: "he-IL"}
	model := &Model{family: fam(Profile{Name: "fast", LatencyMs: 160})}
	hash := func(k pipelines.Kind, materialize *pipelines.Kind) (map[string]any, string) {
		return b.decoding(&modelSteps{model: model, transcribe: k, localeParam: "target_lang", materialize: materialize},
			Profile{Name: "160ms", LatencyMs: 160}, gs, Decoding{Boost: "none"}, Augmentation{Profile: AugmentNone})
	}
	dec, h0 := hash(kind(800, 8), nil)
	if dec["profile"] != "fast" || dec["target_lang"] != "he-IL" || dec["params"].(map[string]any)["stop_history_eou_ms"] != float64(800) {
		t.Fatalf("decoding %v", dec)
	}
	if _, h := hash(kind(800, 1024), nil); h != h0 {
		t.Fatal("the CUDA context reserve changed the decoding hash")
	}
	if _, h := hash(kind(600, 8), nil); h == h0 {
		t.Fatal("a changed end-of-utterance default kept the decoding hash")
	}
	m := pipelines.Kind{Name: "fx_materialize", Version: "1"}
	if dec, h := hash(kind(800, 8), &m); h == h0 || dec["materialize"] != "fx_materialize@1" {
		t.Fatalf("the materialize kind is not in the decoding: %v", dec)
	}
}

// TestCheckLanguages refuses a golden set a base model cannot decode, naming languages as the fix.
func TestCheckLanguages(t *testing.T) {
	m := Model{Label: "subject", base: registry.Version{ID: "ver_b", Name: "base-model/x", Tags: []string{"family:x", "locale:hr-HR", "locale:ru"}}}
	untagged := Model{Label: "baseline", base: registry.Version{ID: "ver_u", Name: "base-model/u"}}
	sr := GoldenSet{VersionID: "ver_sr", Name: "golden-set/sr", Locale: "sr-RS"}
	if err := checkLanguages([]GoldenSet{sr}, untagged); err != nil {
		t.Fatalf("a base model without locale tags is not checked: %v", err)
	}
	err := checkLanguages([]GoldenSet{{VersionID: "ver_ru", Locale: "ru-RU"}, sr}, m, m)
	pe, ok := problems.As(err)
	if !ok || pe.Type != problems.ValidationFailed || len(pe.Errors) != 1 || pe.Errors[0].Path != "/languages/sr-RS" ||
		!strings.Contains(pe.Errors[0].Message, "languages") || !strings.Contains(pe.Errors[0].Message, "hr, ru") {
		t.Fatalf("refusal %v", err)
	}
	sr.DecodeAs = "hr-HR"
	if err := checkLanguages([]GoldenSet{sr}, m); err != nil {
		t.Fatalf("a set decoded in a language the model knows: %v", err)
	}
}
