package evals

import (
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// exactBootstrap enumerates every ordered resample of groups (len^len of them, equally likely) and returns the sorted
// WER deltas: the bootstrap distribution Compare samples from.
func exactBootstrap(groups []Group) []float64 {
	n := len(groups)
	idx := make([]int, n)
	var out []float64
	for {
		var sn, se int
		for _, i := range idx {
			sn += groups[i].RefWords
			se += groups[i].SubjectErrors - groups[i].BaseErrors
		}
		out = append(out, float64(se)/float64(sn))
		k := 0
		for k < n {
			idx[k]++
			if idx[k] < n {
				break
			}
			idx[k] = 0
			k++
		}
		if k == n {
			break
		}
	}
	sort.Float64s(out)
	return out
}

// TestCompareAgainstExactBootstrap checks the Monte Carlo interval against the exact bootstrap distribution of a small
// golden set: each bound lies between the exact quantiles a little below and above its level.
func TestCompareAgainstExactBootstrap(t *testing.T) {
	groups := []Group{
		{Key: "call-1", RefWords: 40, SubjectErrors: 3, BaseErrors: 6},
		{Key: "call-2", RefWords: 25, SubjectErrors: 4, BaseErrors: 3},
		{Key: "call-3", RefWords: 60, SubjectErrors: 5, BaseErrors: 9},
		{Key: "call-4", RefWords: 10, SubjectErrors: 2, BaseErrors: 2},
		{Key: "call-5", RefWords: 33, SubjectErrors: 1, BaseErrors: 4},
	}
	exact := exactBootstrap(groups)
	for _, level := range []float64{0.9, 0.95} {
		cmp, err := Compare(groups, Significance{Samples: 200000, Level: level, Seed: 7})
		if err != nil {
			t.Fatal(err)
		}
		// The point estimate is the pooled delta: (15 − 24) / 168.
		if want := round6(-9.0 / 168); cmp.WER.Value != want || cmp.Groups != 5 {
			t.Fatalf("point %v (groups %d), want %v", cmp.WER.Value, cmp.Groups, want)
		}
		a := (1 - level) / 2
		for _, b := range []struct {
			got float64
			p   float64
		}{{cmp.WER.Low, a}, {cmp.WER.High, 1 - a}} {
			lo, hi := Quantile(exact, math.Max(0, b.p-0.01)), Quantile(exact, math.Min(1, b.p+0.01))
			if b.got < lo-1e-6 || b.got > hi+1e-6 {
				t.Errorf("level %v: bound %v at %v outside the exact quantiles [%v, %v]", level, b.got, b.p, lo, hi)
			}
		}
	}
}

func TestCompareIsDeterministicAndPaired(t *testing.T) {
	groups := []Group{
		{Key: "a", RefWords: 10, SubjectErrors: 1, BaseErrors: 3, SubjectDel: 1, BaseDel: 3},
		{Key: "b", RefWords: 12, SubjectErrors: 2, BaseErrors: 2, SubjectIns: 2},
		{Key: "c", RefWords: 8, SubjectErrors: 0, BaseErrors: 1, BaseDel: 1},
	}
	sig := Significance{Samples: 1000, Level: 0.95, Seed: 1}
	a, err := Compare(groups, sig)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Compare(groups, sig)
	if a != b {
		t.Fatalf("same input, same seed: %+v vs %+v", a, b)
	}
	if a.Del.Value != round6(-3.0/30) || a.Ins.Value != round6(2.0/30) {
		t.Fatalf("deletion and insertion deltas %+v %+v", a.Del, a.Ins)
	}
	if a.WER.Low > a.WER.Value || a.WER.High < a.WER.Value {
		t.Fatalf("interval %+v does not hold its point", a.WER)
	}
	// Two identical models: every resample gives zero, so the interval is [0, 0] and not significant.
	same := []Group{{Key: "a", RefWords: 5, SubjectErrors: 2, BaseErrors: 2}, {Key: "b", RefWords: 7, SubjectErrors: 1, BaseErrors: 1}}
	z, _ := Compare(same, sig)
	if z.WER != (Interval{}) || z.WER.Excludes() {
		t.Fatalf("identical models: %+v", z.WER)
	}
	if _, err := Compare(nil, sig); !errors.Is(err, ErrNoWords) {
		t.Fatalf("no groups: %v", err)
	}
	if _, err := Compare([]Group{{Key: "x"}}, sig); !errors.Is(err, ErrNoWords) {
		t.Fatalf("no words: %v", err)
	}
}

func TestPairGroupsByCallAndAudio(t *testing.T) {
	sub := []Utterance{
		{Audio: "a1", Group: "c1", RefWords: 4, Sub: 1},
		{Audio: "a2", Group: "c1", RefWords: 3, Del: 1},
		{Audio: "a3", RefWords: 5, Ins: 2},
	}
	base := []Utterance{
		{Audio: "a3", RefWords: 5},
		{Audio: "a1", Group: "c1", RefWords: 4, Sub: 2},
		{Audio: "a2", Group: "c1", RefWords: 3, Del: 2},
	}
	groups, err := Pair(sub, base)
	if err != nil {
		t.Fatal(err)
	}
	want := []Group{
		{Key: "c1", RefWords: 7, SubjectErrors: 2, BaseErrors: 4, SubjectDel: 1, BaseDel: 2},
		{Key: "a3", RefWords: 5, SubjectErrors: 2, SubjectIns: 2},
	}
	if !slices.Equal(groups, want) {
		t.Fatalf("groups %+v, want %+v", groups, want)
	}
	if _, err := Pair(sub, base[:2]); err == nil {
		t.Fatal("cells of different sizes paired")
	}
	if _, err := Pair(sub, []Utterance{{Audio: "x"}, {Audio: "y"}, {Audio: "z"}}); err == nil {
		t.Fatal("utterances without a pair were paired")
	}
}

// A language written without spaces is compared on characters: a group's units are reference characters and its
// errors character edits; deletions and insertions are not counted.
func TestPairCharsCountsCharacters(t *testing.T) {
	sub := []Utterance{{Audio: "a1", Group: "s1", RefWords: 1, Sub: 1, RefChars: 12, CharErrors: 2}, {Audio: "a2", Group: "s1", RefWords: 1, Del: 1, RefChars: 8, CharErrors: 1}}
	base := []Utterance{{Audio: "a1", Group: "s1", RefWords: 1, Sub: 1, RefChars: 12, CharErrors: 5}, {Audio: "a2", Group: "s1", RefWords: 1, Sub: 1, RefChars: 8, CharErrors: 4}}
	groups, err := PairChars(sub, base)
	if err != nil {
		t.Fatal(err)
	}
	if want := []Group{{Key: "s1", RefWords: 20, SubjectErrors: 3, BaseErrors: 9}}; !slices.Equal(groups, want) {
		t.Fatalf("groups %+v, want %+v", groups, want)
	}
	for _, tt := range []struct {
		locale string
		want   bool
	}{{"zh-CN", true}, {"ja-JP", true}, {"th-TH", true}, {"he-IL", false}, {"sr-RS", false}, {"", false}} {
		if got := CharScored(tt.locale, []string{"zh", "ja", "th"}); got != tt.want {
			t.Errorf("CharScored(%q) = %v", tt.locale, got)
		}
	}
}

func TestQuantile(t *testing.T) {
	s := []float64{1, 2, 3, 4}
	for _, c := range []struct{ p, want float64 }{{0, 1}, {1, 4}, {0.5, 2.5}, {0.25, 1.75}} {
		if got := Quantile(s, c.p); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("Quantile(%v) = %v, want %v", c.p, got, c.want)
		}
	}
}

func TestWorstOf(t *testing.T) {
	s := func(v string) *string { return &v }
	rows := []Utterance{
		{Audio: "a", Ref: "one two", Hyp: "one two", RefWords: 2},
		{Audio: "b", Ref: "one two three", Hyp: "one", RefWords: 3, Del: 2, Ops: [][]*string{{s("="), s("one"), s("one")}, {s("D"), s("two"), nil}}},
		{Audio: "c", Ref: "x", Hyp: "y z", RefWords: 1, Sub: 1, Ins: 1},
	}
	w := WorstOf(rows, 2)
	if len(w) != 2 || w[0].Audio != "c" || w[0].WER != 2 || w[1].Audio != "b" || w[1].Index != 1 || w[1].Ops[1][2] != "" {
		t.Fatalf("worst %+v", w)
	}
}

func TestParseGate(t *testing.T) {
	d := defaults.Get()
	_, g, err := ParseGate([]byte("# only a comment\n"), d)
	if err != nil {
		t.Fatal(err)
	}
	if g.PrimaryProfile != d.Eval.PrimaryProfile.Value || g.MaxRegression != 0.005 || !g.DeletionsInsertions ||
		g.Significance != (Significance{Samples: 1000, Level: 0.95, Seed: 1}) || len(Departures(g, d)) != 0 {
		t.Fatalf("defaults %+v", g)
	}
	doc := `primaryProfile: 320ms
target: { goldenSets: [golden-set/fleurs-he], rule: beat-baseline }
replay: { goldenSets: ["golden-set/replay-golden-*"], maxRegression: 0.01 }
deletionsInsertions: false
significance: { samples: 2000, level: 0.9, seed: 3 }
`
	c, g, err := ParseGate([]byte(doc), d)
	if err != nil {
		t.Fatal(err)
	}
	if g.PrimaryProfile != "320ms" || g.MaxRegression != 0.01 || g.DeletionsInsertions || g.Significance.Samples != 2000 ||
		!slices.Equal(g.TargetGoldenSets, []string{"golden-set/fleurs-he"}) || len(Departures(g, d)) != 8 {
		t.Fatalf("parsed %+v, departures %+v", g, Departures(g, d))
	}
	out, err := RenderGate(c)
	if err != nil {
		t.Fatal(err)
	}
	if _, g2, err := ParseGate(out, d); err != nil || !slices.Equal(g2.ReplayGoldenSets, g.ReplayGoldenSets) || g2.Significance != g.Significance {
		t.Fatalf("round trip %s: %+v %v", out, g2, err)
	}
	for _, bad := range []struct{ doc, path string }{
		{"primaryProfile: 160ms\nthreshold: 3\n", "/"},
		{"replay: { maxRegression: 0.5 }\n", "/replay/maxRegression"},
		{"significance: { samples: 5 }\n", "/significance/samples"},
		{"target: { rule: be-better }\n", "/target/rule"},
		{"target: { goldenSets: [fleurs] }\n", "/target/goldenSets/0"},
		{"target: { goldenSets: [golden-set/a] }\nreplay: { goldenSets: [golden-set/a] }\n", "/replay/goldenSets"},
	} {
		_, _, err := ParseGate([]byte(bad.doc), d)
		pe, ok := problems.As(err)
		if !ok || pe.Type != problems.GateConfigInvalid || pe.Errors[0].Path != bad.path {
			t.Errorf("%q: %v (%+v)", bad.doc, err, pe)
		}
	}
}

func TestTargetAndReplay(t *testing.T) {
	he := GoldenSet{VersionID: "ver_1", Name: "golden-set/fleurs-he", Locale: "he-IL"}
	ru := GoldenSet{VersionID: "ver_2", Name: "golden-set/replay-golden-ru", Locale: "ru-RU"}
	locales := []string{"he-IL"}
	var none Gate
	if !Target(he, none, locales) || Replay(he, none, locales) || !Replay(ru, none, locales) || Target(ru, none, locales) {
		t.Fatal("without lists the project's locales are the target")
	}
	g := Gate{TargetGoldenSets: []string{"golden-set/fleurs-he"}, ReplayGoldenSets: []string{"golden-set/replay-golden-*"}}
	if !Target(he, g, nil) || !Replay(ru, g, nil) || Target(ru, g, nil) {
		t.Fatal("named lists decide")
	}
	if !Matches([]string{"ver_2"}, ru.VersionID, ru.Name) || Matches([]string{"golden-set/replay-*-he"}, ru.VersionID, ru.Name) {
		t.Fatal("matching by id and pattern")
	}
}

// TestVerdict runs the gate's checks over stored deltas: a target that beats the baseline, a replay within the
// allowed regression, a replay that regresses, and deletions traded for insertions.
func TestVerdict(t *testing.T) {
	sig := Significance{Samples: 1000, Level: 0.95, Seed: 1}
	delta := func(v, lo, hi float64, del, ins Interval) json.RawMessage {
		return mustJSON(Delta{WER: Interval{Value: v, Low: lo, High: hi}, Del: del, Ins: ins, Samples: 1000, Level: 0.95})
	}
	e := Eval{
		Profiles:     []Profile{{Name: "160ms"}, {Name: "1120ms"}},
		Significance: sig,
		GoldenSets: []GoldenSet{
			{VersionID: "ver_t", Name: "golden-set/fleurs-he", Locale: "he-IL"},
			{VersionID: "ver_r1", Name: "golden-set/replay-golden-ru", Locale: "ru-RU"},
			{VersionID: "ver_r2", Name: "golden-set/replay-golden-de", Locale: "de-DE"},
		},
	}
	cell := func(gs, role string, d json.RawMessage) Cell {
		return Cell{ID: gs + role, Role: role, GoldenSetVersionID: gs, Profile: "160ms", RecordID: "r" + gs + role, Delta: d}
	}
	zero := Interval{}
	cells := []Cell{
		cell("ver_t", RoleSubject, delta(-0.02, -0.03, -0.01, Interval{-0.01, -0.02, -0.005}, Interval{0.004, 0.001, 0.008})),
		cell("ver_t", RoleBaseline, nil),
		cell("ver_r1", RoleSubject, delta(0.002, -0.001, 0.004, zero, zero)),
		cell("ver_r1", RoleBaseline, nil),
		cell("ver_r2", RoleSubject, delta(0.01, 0.004, 0.02, zero, zero)),
		cell("ver_r2", RoleBaseline, nil),
	}
	recs := map[string]Record{"rver_tsubject": {Summary: json.RawMessage(`{"wer":0.1}`)}, "rver_tbaseline": {Summary: json.RawMessage(`{"wer":0.12}`)}}
	s := &Service{}
	gf := GateFile{Exists: true, Commit: "abc123", Gate: DefaultGate(defaults.Get())}
	v := s.verdict(e, []string{"he-IL"}, gf, cells, recs)
	states := map[string]string{}
	for _, c := range v.Checks {
		states[c.Kind+":"+c.GoldenSetVersionID] = c.State
	}
	want := map[string]string{"target:ver_t": CheckPassed, "deletionsInsertions:ver_t": CheckFailed, "replay:ver_r1": CheckPassed,
		"replay:ver_r2": CheckFailed}
	for k, st := range want {
		if states[k] != st {
			t.Errorf("%s is %s, want %s (%+v)", k, states[k], st, v.Checks)
		}
	}
	if v.Verdict != VerdictFailed || v.GatesSHA != "abc123" {
		t.Fatalf("verdict %s sha %s", v.Verdict, v.GatesSHA)
	}
	if c := v.Checks[0]; c.CandidateWER == nil || *c.CandidateWER != 0.1 || *c.BaselineWER != 0.12 {
		t.Fatalf("numbers of the target check %+v", c)
	}
	// Without the D/I trade and the regressing replay set, the gate passes.
	gf.Gate.DeletionsInsertions = false
	e.GoldenSets = e.GoldenSets[:2]
	if v := s.verdict(e, []string{"he-IL"}, gf, cells, recs); v.Verdict != VerdictPassed {
		t.Fatalf("verdict %s: %+v", v.Verdict, v.Checks)
	}
	// An interval spanning zero on the target is inconclusive, which fails the verdict.
	cells[0].Delta = delta(-0.01, -0.02, 0.001, zero, zero)
	if v := s.verdict(e, []string{"he-IL"}, gf, cells, recs); v.Verdict != VerdictFailed || v.Checks[0].State != CheckInconclusive {
		t.Fatalf("inconclusive target: %s %+v", v.Verdict, v.Checks)
	}
	// A primary profile the eval did not run fails at once.
	gf.Gate.PrimaryProfile = "80ms"
	if v := s.verdict(e, nil, gf, cells, recs); v.Verdict != VerdictFailed || v.Checks[0].Kind != CheckPrimaryProfile ||
		!strings.Contains(v.Checks[0].Message, "80ms") {
		t.Fatalf("missing primary: %+v", v.Checks)
	}
}
