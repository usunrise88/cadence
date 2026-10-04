package annotation

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/auth"
)

func TestWER(t *testing.T) {
	cases := []struct {
		ref, hyp string
		want     float64
	}{
		{"Dobar dan, kako ste?", "dobar dan kako ste", 0},
		{"Dobar dan kako ste", "dobar dan kako", 0.25},
		{"a b c d", "a x c d e", 0.5},
		{"", "", 0},
		{"", "nešto", 1},
		{"Hvala.", "", 1},
	}
	for _, c := range cases {
		if got := WER(c.ref, c.hyp); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("WER(%q, %q) = %v, want %v", c.ref, c.hyp, got, c.want)
		}
	}
}

func row(i int, file string, start, end float64, role string, conf *float64) Row {
	r := Row{"hash": fmt.Sprintf("b3:%064x", i), "uri": fmt.Sprintf("mount://corpora/calls/%s#t=%g,%g&ch=0", file, start, end),
		"file": "mount://corpora/calls/" + file, "start": start, "end": end, "duration": end - start, "channel": float64(0), "role": role}
	if role == "bot" {
		r["channel"] = float64(1)
		r["text"] = "Dobar dan, ovde je služba."
	}
	if conf != nil {
		r["confidence"] = *conf
	}
	return r
}

func TestDrawStratifiesAndIsReproducible(t *testing.T) {
	var rows []Row
	hi, lo := 0.9, 0.3
	for i := range 40 {
		dur := 1.0
		if i%4 == 0 {
			dur = 12
		}
		c := &hi
		if i%5 == 0 {
			c = &lo
		}
		rows = append(rows, row(i, fmt.Sprintf("c%02d.wav", i/10), float64(i), float64(i)+dur, "caller", c))
	}
	rows = append(rows, row(100, "c00.wav", 0, 1, "bot", nil))
	fr := Frame{Rows: rows}
	e := Edges{Duration: []float64{2, 5, 10, 20}, Confidence: []float64{0.6, 0.85}}
	a := Draw(fr, "caller", AllStrata, 10, 7, e)
	b := Draw(fr, "caller", AllStrata, 10, 7, e)
	if a.Candidates != 40 || len(a.Rows) != 10 {
		t.Fatalf("candidates %d, rows %d", a.Candidates, len(a.Rows))
	}
	for i := range a.Rows {
		if a.Rows[i].str("hash") != b.Rows[i].str("hash") {
			t.Fatalf("the same seed drew another sample")
		}
		if a.Rows[i].str("role") != "caller" {
			t.Fatalf("a %s row was sampled", a.Rows[i].str("role"))
		}
	}
	sum := 0
	for _, s := range a.Strata {
		sum += s.Sampled
		if s.Sampled < 1 || s.Sampled > s.Frame {
			t.Errorf("stratum %v: %d of %d", s.Key, s.Sampled, s.Frame)
		}
	}
	if sum != 10 {
		t.Errorf("strata add up to %d", sum)
	}
	c := Draw(fr, "caller", AllStrata, 10, 8, e)
	same := 0
	for i := range c.Rows {
		if c.Rows[i].str("hash") == a.Rows[i].str("hash") {
			same++
		}
	}
	if same == 10 {
		t.Error("another seed drew the same sample")
	}
	if all := Draw(fr, "caller", nil, 100, 1, e); len(all.Rows) != 40 {
		t.Errorf("a sample larger than the frame: %d rows", len(all.Rows))
	}
	if d := doubles(a.Rows, 0.2, 7); len(d) != 2 {
		t.Errorf("doubles: %d", len(d))
	}
}

func TestBuildItemContextAndEOU(t *testing.T) {
	caller := Row{"hash": "b3:" + fmt.Sprintf("%064x", 1), "uri": "mount://corpora/calls/c.wav#t=5,9&ch=0", "start": 5.0, "end": 9.0,
		"duration": 4.0, "channel": 0.0, "role": "caller", "text": "molim vas", "origin": "pseudo-label", "confidence": 0.7,
		"vad": map[string]any{"speech": []any{[]any{0.2, 3.5}}}}
	bot := Row{"hash": "b3:" + fmt.Sprintf("%064x", 2), "uri": "mount://corpora/calls/c.wav#t=9.3,12&ch=1", "start": 9.3, "end": 12.0,
		"channel": 1.0, "role": "bot", "text": "Hvala, recite broj ugovora.", "origin": "model:tts-script"}
	fr := Frame{Rows: []Row{caller, bot}, Files: map[string]FileInfo{"mount://corpora/calls/c.wav": {
		URI: "mount://corpora/calls/c.wav", Duration: 30, Channels: 2, Roles: []string{"caller", "bot"},
		Speech: [][][2]float64{{{5.2, 8.5}}, {{9.4, 11.9}}}}}}
	it := buildItem(caller, fr, fr.Rows, 2, Edges{Duration: []float64{5}, Confidence: []float64{0.6, 0.85}}, AllStrata)
	if it.Window.Start != 3 || it.Window.End != 11 || it.Window.Channels != 2 || len(it.Window.Roles) != 2 {
		t.Errorf("window %+v", it.Window)
	}
	if len(it.Context.Turns) != 1 || it.Context.Turns[0].Role != "bot" {
		t.Errorf("turns %+v", it.Context.Turns)
	}
	if it.Prefill.Text != "molim vas" || it.Prefill.Origin != "pseudo-label" || it.Prefill.Confidence == nil {
		t.Errorf("prefill %+v", it.Prefill)
	}
	if it.EOU == nil || it.EOU.SpeechEnd != 8.5 || it.EOU.GapS == nil || math.Abs(*it.EOU.GapS-0.9) > 1e-9 {
		t.Errorf("eou %+v", it.EOU)
	}
	if it.Strata[StratumCampaign] != "calls" || it.Strata[StratumMonth] != "unknown" || it.Strata[StratumConfidence] != "0.6–0.85" {
		t.Errorf("strata %v", it.Strata)
	}
}

func TestSettle(t *testing.T) {
	now := time.Now()
	ann := func(id, user, status, text string, tags ...string) Annotation {
		return Annotation{ID: id, AnnotatorID: user, Annotator: auth.Actor{Kind: auth.KindUser, ID: user}, Status: status, Text: text, Tags: tags}
	}
	single := Item{Required: 1, State: ItemPending}
	double := Item{Required: 2, Double: true, State: ItemPending}
	cases := []struct {
		name  string
		it    Item
		list  []Annotation
		state string
	}{
		{"nothing yet", single, nil, ItemPending},
		{"one transcript", single, []Annotation{ann("a", "u1", StatusDone, "dobar dan")}, ItemAgreed},
		{"flagged needs a second", single, []Annotation{ann("a", "u1", StatusFlagged, "dobar dan")}, ItemPending},
		{"double, one in", double, []Annotation{ann("a", "u1", StatusDone, "dobar dan")}, ItemPending},
		{"double agree", double, []Annotation{ann("a", "u1", StatusDone, "Dobar dan!"), ann("b", "u2", StatusDone, "dobar dan")}, ItemAgreed},
		{"double disagree", double, []Annotation{ann("a", "u1", StatusDone, "dobar dan"), ann("b", "u2", StatusDone, "dobro dan")}, ItemDisputed},
		{"skips exclude", single, []Annotation{ann("a", "u1", StatusSkipped, ""), ann("b", "u2", StatusSkipped, "")}, ItemExcluded},
		{"one skip waits", single, []Annotation{ann("a", "u1", StatusSkipped, "")}, ItemPending},
		{"foreign excluded", single, []Annotation{ann("a", "u1", StatusDone, "hello", "foreign")}, ItemExcluded},
	}
	for _, c := range cases {
		got := settle(c.it, c.list, 0, 2, now)
		if got.State != c.state {
			t.Errorf("%s: %s, want %s", c.name, got.State, c.state)
		}
		if got.State == ItemAgreed && (got.Final == nil || got.Final.From != "a") {
			t.Errorf("%s: final %+v", c.name, got.Final)
		}
	}
}

func TestSummariseAgreement(t *testing.T) {
	items := []Item{{ID: "i1", Double: true, State: ItemAgreed}, {ID: "i2", Double: true, State: ItemDisputed}, {ID: "i3", State: ItemAgreed}}
	anns := map[string][]Annotation{
		"i1": {{Status: StatusDone, Text: "jedan dva tri četiri"}, {Status: StatusDone, Text: "jedan dva tri četiri"}},
		"i2": {{Status: StatusDone, Text: "pet šest sedam osam"}, {Status: StatusDone, Text: "pet šest sedam"}},
		"i3": {{Status: StatusDone, Text: "devet"}},
	}
	p, a, _ := summarise(items, anns, 0.05)
	if p.Items != 3 || p.Agreed != 2 || p.Disputed != 1 || p.DoubleItems != 2 || p.DoubleDone != 2 || p.Annotations != 5 {
		t.Errorf("progress %+v", p)
	}
	if a.Pairs != 2 || a.RefWords != 8 || a.Edits != 1 || a.IAAWER == nil || *a.IAAWER != 0.125 || a.Meets {
		t.Errorf("agreement %+v", a)
	}
}

func TestCheckEntities(t *testing.T) {
	got, err := checkEntities("Zovem se Ana Petrović.", []Entity{{Start: 9, End: 21, Class: "name"}})
	if err != nil || len(got) != 1 || got[0].Text != "Ana Petrović" {
		t.Errorf("entities %+v, %v", got, err)
	}
	if _, err := checkEntities("kratko", []Entity{{Start: 2, End: 40, Class: "name"}}); err == nil {
		t.Error("a span past the text was accepted")
	}
}
