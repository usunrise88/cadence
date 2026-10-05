package deployments

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/usunrise88/cadence/control-plane/internal/evals"
	"github.com/usunrise88/cadence/control-plane/internal/modelexports"
	"github.com/usunrise88/cadence/control-plane/internal/registry"
	"github.com/usunrise88/cadence/control-plane/internal/runs"
	"github.com/usunrise88/cadence/control-plane/internal/targets"
)

func TestModelName(t *testing.T) {
	for _, c := range []struct{ slot, version, want string }{
		{"asr-he-il", "2026-11-02.ab12cd", "asr-he-il-2026-11-02-ab12cd"},
		{"asr.sr", "2026-11-02.ab12cd", "asr.sr-2026-11-02-ab12cd"},
		{"x", "v 1/2", "x-v-1-2"},
	} {
		if got := ModelName(c.slot, c.version); got != c.want {
			t.Errorf("ModelName(%q, %q) = %q, want %q", c.slot, c.version, got, c.want)
		}
	}
	long := ModelName("slot", string(bytes.Repeat([]byte("a"), 200)))
	if len(long) > 100 {
		t.Errorf("model name of %d bytes", len(long))
	}
}

func wav(rate uint32, dataBytes uint32, extra bool) []byte {
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))
	b.WriteString("WAVE")
	if extra {
		b.WriteString("LIST")
		_ = binary.Write(&b, binary.LittleEndian, uint32(3))
		b.Write([]byte{1, 2, 3, 0}) // odd size, padded
	}
	b.WriteString("fmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(7))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint32(8000))
	_ = binary.Write(&b, binary.LittleEndian, rate)
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(8))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, dataBytes)
	return b.Bytes()
}

func TestWavSeconds(t *testing.T) {
	for _, c := range []struct {
		name string
		b    []byte
		want float64
		ok   bool
	}{
		{"mu-law stereo", wav(16000, 16000*90, false), 90, true},
		{"a chunk before fmt", wav(16000, 8000, true), 0.5, true},
		{"no byte rate", wav(0, 8000, false), 0, false},
		{"not a wav", []byte("ID3 an mp3"), 0, false},
	} {
		got, ok := wavSeconds(bytes.NewReader(c.b))
		if ok != c.ok || got != c.want {
			t.Errorf("%s: %v %v, want %v %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestIsAudio(t *testing.T) {
	for name, want := range map[string]bool{"calls/a.WAV": true, "a.opus": true, "a.cadence.json": false, "a.txt": false} {
		if isAudio(name) != want {
			t.Errorf("isAudio(%q) != %v", name, want)
		}
	}
}

func f(v float64) *float64 { return &v }

func TestBenchmarkAt(t *testing.T) {
	list := []modelexports.BenchmarkView{
		{State: "running", Streams: 32},
		{State: "done", Streams: 16, Verdict: "passed"},
		{State: "done", Streams: 32, Verdict: "failed", P95ChunkLatencyMs: f(140)},
		{State: "done", Streams: 32, Verdict: "passed"},
	}
	b, running := benchmarkAt(list, 32)
	if b == nil || b.Verdict != "failed" || !running {
		t.Fatalf("newest at 32: %+v %v", b, running)
	}
	if b, _ := benchmarkAt(list, 64); b != nil {
		t.Fatalf("none at 64: %+v", b)
	}
}

func TestDecodingSame(t *testing.T) {
	a := Decoding{BoostLists: []BoostList{{Locale: "he-IL", Domain: "names", SHA256: "x", Weight: 2}}}
	b := a
	b.BoostLists = append([]BoostList(nil), a.BoostLists...)
	if !a.same(b) {
		t.Fatal("same lists differ")
	}
	b.BoostLists[0].Weight = 3
	if a.same(b) || a.same(Decoding{}) {
		t.Fatal("a changed weight or a missing list is the same")
	}
	if n := (Decoding{}).norm(); len(n.BoostLists) != 0 || n.BoostLists == nil {
		t.Fatal("norm keeps an empty list, not nil")
	}
}

func TestCheckTarget(t *testing.T) {
	dep := func(server, card string) deployed {
		b, _ := json.Marshal(map[string]any{"server": map[string]any{"kind": "srv", "version": server}, "engine": map[string]any{"cardClass": card}})
		return deployed{model: evals.DeployModel{Family: runs.Family{Name: "fam"}, Version: registry.Version{ID: "ver_1"}},
			export: modelexports.Export{Format: "fmt", Profile: "80ms", Deployable: b}}
	}
	target := targets.Target{Name: "prod", Config: targets.Config{Serves: []targets.Serves{{Family: "fam", Formats: []string{"fmt"}, Profiles: []string{"80ms"}}},
		Server: targets.Server{Kind: "srv", Version: "26.08"}, CardClass: "blackwell-96gb"}}
	s := &Service{}
	verdicts := func(dp deployed, t targets.Target) map[string]string {
		var pl Plan
		s.checkTarget(&pl, t, dp)
		out := map[string]string{}
		for _, c := range pl.Checks {
			out[c.Name] = c.State + ":" + c.ProblemType
		}
		return out
	}
	for _, c := range []struct {
		name         string
		dp           deployed
		t            targets.Target
		serve, engin string
	}{
		{"the engine's release and card", dep("26.08", "blackwell-96gb"), target, "passed:", "passed:"},
		{"another server release", dep("26.07", "blackwell-96gb"), target, "passed:", "failed:target-does-not-serve"},
		{"another card class", dep("26.08", "ada-48gb"), target, "passed:", "failed:target-does-not-serve"},
		{"card class unknown", dep("26.08", ""), target, "passed:", "warning:"},
		{"another profile", func() deployed { d := dep("26.08", "blackwell-96gb"); d.export.Profile = "160ms"; return d }(), target,
			"failed:target-does-not-serve", "passed:"},
	} {
		v := verdicts(c.dp, c.t)
		if v[CheckTargetServes] != c.serve || v[CheckEngine] != c.engin {
			t.Errorf("%s: %v", c.name, v)
		}
	}
}
