package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

func sine(n, rate int, hz, amp float64) []float32 {
	x := make([]float32, n)
	for i := range x {
		x[i] = float32(amp * math.Sin(2*math.Pi*hz*float64(i)/float64(rate)))
	}
	return x
}

func TestWAVRoundTrip(t *testing.T) {
	left, right := sine(8000, 8000, 440, 0.5), sine(8000, 8000, 1000, 0.25)
	b := EncodeWAV([][]float32{left, right}, 8000)
	info, err := ReadInfo(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRate != 8000 || info.Channels != 2 || info.Bits != 16 || info.Frames() != 8000 || info.DataOffset != WAVHeaderSize {
		t.Fatalf("info %+v", info)
	}
	if info.Canonical() || math.Abs(info.Duration()-1) > 1e-9 {
		t.Fatalf("canonical %v duration %v", info.Canonical(), info.Duration())
	}
	pcm, err := ReadFrames(bytes.NewReader(b), info, 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		if math.Abs(float64(pcm[0][i]-left[100+i])) > 1.0/32768 || math.Abs(float64(pcm[1][i]-right[100+i])) > 1.0/32768 {
			t.Fatalf("sample %d: %v %v", i, pcm[0][i], left[100+i])
		}
	}
	// Reads past the end are clamped.
	if pcm, _ := ReadFrames(bytes.NewReader(b), info, 7990, 50); len(pcm[0]) != 10 {
		t.Fatalf("clamped read of %d frames", len(pcm[0]))
	}
}

func TestReadInfoFormats(t *testing.T) {
	float := func() []byte {
		b := EncodeWAV([][]float32{{0.5, -0.25}}, 16000)
		// Rewrite as 32-bit float: tag 3, 4 bytes per sample.
		out := append([]byte(nil), b[:WAVHeaderSize]...)
		binary.LittleEndian.PutUint16(out[20:], formatFloat)
		binary.LittleEndian.PutUint16(out[34:], 32)
		binary.LittleEndian.PutUint16(out[32:], 4)
		binary.LittleEndian.PutUint32(out[40:], 8)
		for _, v := range []float32{0.5, -0.25} {
			out = binary.LittleEndian.AppendUint32(out, math.Float32bits(v))
		}
		return out
	}()
	cases := []struct {
		name string
		body []byte
		ok   bool
	}{
		{"canonical", EncodeWAV([][]float32{{0, 0.5}}, 16000), true},
		{"float32", float, true},
		{"not riff", []byte("OggS0000000000000000000000000000000000000000"), false},
		{"no data", EncodeWAV(nil, 16000)[:36], false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			info, err := ReadInfo(bytes.NewReader(c.body), int64(len(c.body)))
			if (err == nil) != c.ok {
				t.Fatalf("err %v", err)
			}
			if !c.ok && !errors.Is(err, ErrNotWAV) {
				t.Fatalf("want ErrNotWAV, got %v", err)
			}
			if c.name == "float32" {
				pcm, _ := ReadFrames(bytes.NewReader(c.body), info, 0, 2)
				if !info.Float || pcm[0][0] != 0.5 || pcm[0][1] != -0.25 {
					t.Fatalf("float decode %+v %v", info, pcm)
				}
			}
		})
	}
}

// rms of the difference between x and a reference sine, skipping the edges where the filter sees zeros.
func errRMS(x []float32, rate int, hz, amp float64, skip int) float64 {
	var s float64
	n := 0
	for i := skip; i < len(x)-skip; i++ {
		d := float64(x[i]) - amp*math.Sin(2*math.Pi*hz*float64(i)/float64(rate))
		s += d * d
		n++
	}
	return math.Sqrt(s / float64(n))
}

func TestResample(t *testing.T) {
	cases := []struct {
		from, to int
		hz       float64
	}{
		{8000, 16000, 1000},
		{44100, 16000, 1000},
		{48000, 16000, 3000},
		{22050, 16000, 440},
	}
	for _, c := range cases {
		x := sine(c.from, c.from, c.hz, 0.5) // one second
		y := Resample(x, c.from, c.to)
		if len(y) != c.to {
			t.Fatalf("%d → %d: %d samples", c.from, c.to, len(y))
		}
		if e := errRMS(y, c.to, c.hz, 0.5, 200); e > 0.005 {
			t.Errorf("%d → %d at %.0f Hz: rms error %.4f", c.from, c.to, c.hz, e)
		}
	}
	// Above the new Nyquist is filtered out: a 7 kHz tone at 44.1 kHz all but vanishes at 8 kHz.
	y := Resample(sine(44100, 44100, 7000, 0.5), 44100, 8000)
	var peak float64
	for _, v := range y[200 : len(y)-200] {
		peak = max(peak, math.Abs(float64(v)))
	}
	if peak > 0.01 {
		t.Errorf("7 kHz leaks through a 4 kHz Nyquist: peak %.4f", peak)
	}
	if got := Resample([]float32{1, 2}, 16000, 16000); len(got) != 2 || got[1] != 2 {
		t.Errorf("identity %v", got)
	}
}

func TestPeaks(t *testing.T) {
	// 2.5 s at 8 kHz: channel 0 a quiet sine, channel 1 silent then clipped for 10 ms at 1.0 s.
	n := 20000
	a := sine(n, 8000, 100, 0.5)
	b := make([]float32, n)
	for i := 8000; i < 8080; i++ {
		b[i] = 1
	}
	body := EncodeWAV([][]float32{a, b}, 8000)
	info, _ := ReadInfo(bytes.NewReader(body), int64(len(body)))
	p, err := ComputePeaks(bytes.NewReader(body), info)
	if err != nil {
		t.Fatal(err)
	}
	if p.Frames() != 250 || p.Channels != 2 || p.HopS != PeaksHop {
		t.Fatalf("frames %d channels %d", p.Frames(), p.Channels)
	}
	if len(p.Clipped) != 1 || p.Clipped[0] != 100 {
		t.Fatalf("clipped %v", p.Clipped)
	}
	// A 100 Hz sine fills each 10 ms frame with one period: min ≈ -0.5, max ≈ 0.5 (×127 ≈ 64).
	if lo, hi := p.Data[0], p.Data[1]; lo > -60 || hi < 60 {
		t.Fatalf("frame 0 channel 0: %d %d", lo, hi)
	}
	if lo, hi := p.Data[2], p.Data[3]; lo != 0 || hi != 0 {
		t.Fatalf("frame 0 channel 1 should be silent: %d %d", lo, hi)
	}
	pooled := p.Span(90, 40, 4) // frames 90..129 in 10 pairs of 40 ms
	if pooled.Frames() != 10 || math.Abs(pooled.HopS-0.04) > 1e-12 {
		t.Fatalf("pooled %d frames hop %v", pooled.Frames(), pooled.HopS)
	}
	if hi := pooled.Data[(2*2+1)*2+1]; hi != 127 { // pooled pair 2 (frames 98..101), channel 1, max
		t.Fatalf("pooled clip max %d", hi)
	}
	if len(pooled.Clipped) != 1 || pooled.Clipped[0] != 2 {
		t.Fatalf("pooled clipped %v", pooled.Clipped)
	}
	back, err := PeaksFromBytes(p.Bytes(), 2, PeaksHop)
	if err != nil || !bytes.Equal(back.Bytes(), p.Bytes()) {
		t.Fatalf("round trip %v", err)
	}
	if _, err := PeaksFromBytes([]byte{1, 2, 3}, 2, PeaksHop); err == nil {
		t.Fatal("odd bytes accepted")
	}
}

func TestLinks(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	s := NewSigner([]byte("k"))
	s.Now = func() time.Time { return now }
	ch, a, b := 0, 1.2, 3.40
	l := Link{Utterance: "utt_1", Span: Span{Channel: &ch, Start: &a, End: &b}, Viewer: "usr_admin", Expires: now.Unix() + 60}
	sig := s.Sign(l)
	if err := s.Verify(l, sig); err != nil {
		t.Fatal(err)
	}
	q := s.Query(l)
	if q.Get("start") != "1.2" || q.Get("end") != "3.4" || q.Get("channel") != "0" || q.Get("sig") != sig {
		t.Fatalf("query %v", q)
	}
	other := 3.5
	for name, bad := range map[string]Link{
		"utterance": {Utterance: "utt_2", Span: l.Span, Viewer: l.Viewer, Expires: l.Expires},
		"span":      {Utterance: l.Utterance, Span: Span{Channel: &ch, Start: &a, End: &other}, Viewer: l.Viewer, Expires: l.Expires},
		"channel":   {Utterance: l.Utterance, Span: Span{Start: &a, End: &b}, Viewer: l.Viewer, Expires: l.Expires},
		"viewer":    {Utterance: l.Utterance, Span: l.Span, Viewer: "usr_other", Expires: l.Expires},
		"expiry":    {Utterance: l.Utterance, Span: l.Span, Viewer: l.Viewer, Expires: l.Expires + 1},
	} {
		if err := s.Verify(bad, sig); !isProblem(err, problems.MediaLinkInvalid) {
			t.Errorf("%s altered: %v", name, err)
		}
	}
	s.Now = func() time.Time { return now.Add(61 * time.Second) }
	if err := s.Verify(l, sig); !isProblem(err, problems.MediaLinkInvalid) || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired link: %v", err)
	}
	if a, b := NewSigner(nil), NewSigner(nil); a.Sign(l) == b.Sign(l) {
		t.Error("random keys sign alike")
	}
}

func isProblem(err error, ty problems.Type) bool {
	var pe *problems.Error
	return errors.As(err, &pe) && pe.Type.Slug == ty.Slug
}

func TestParseRange(t *testing.T) {
	cases := []struct {
		h          string
		start, len int64
		none, bad  bool
	}{
		{h: "", none: true},
		{h: "bytes=0-", start: 0, len: 1000},
		{h: "bytes=100-199", start: 100, len: 100},
		{h: "bytes=900-5000", start: 900, len: 100},
		{h: "bytes=-100", start: 900, len: 100},
		{h: "bytes=-5000", start: 0, len: 1000},
		{h: "bytes=1000-", bad: true},
		{h: "bytes=-0", bad: true},
		{h: "bytes=0-1,5-6", none: true},
		{h: "items=0-1", none: true},
		{h: "bytes=5-1", none: true},
		{h: "bytes=x-1", none: true},
	}
	for _, c := range cases {
		r, err := ParseRange(c.h, 1000)
		switch {
		case c.bad:
			if !isProblem(err, problems.RangeNotSatisfiable) {
				t.Errorf("%q: want 416, got %v %v", c.h, r, err)
			}
		case c.none:
			if r != nil || err != nil {
				t.Errorf("%q: want no range, got %v %v", c.h, r, err)
			}
		default:
			if err != nil || r == nil || r.Start != c.start || r.Length != c.len {
				t.Errorf("%q: got %+v %v", c.h, r, err)
			}
		}
	}
}

func TestServe(t *testing.T) {
	body := bytes.NewReader([]byte("0123456789"))
	r := httptest.NewRequest("GET", "/x", nil)
	r.Header.Set("Range", "bytes=2-4")
	w := httptest.NewRecorder()
	status, err := Serve(w, r, body, 10, "audio/wav")
	if err != nil || status != 206 || w.Body.String() != "234" || w.Header().Get("Content-Range") != "bytes 2-4/10" ||
		w.Header().Get("Accept-Ranges") != "bytes" {
		t.Fatalf("206: %d %v %q %v", status, err, w.Body.String(), w.Header())
	}
	if FirstPlay(r) {
		t.Error("a range from byte 2 is not a play")
	}
	r = httptest.NewRequest("GET", "/x", nil)
	w = httptest.NewRecorder()
	if status, _ := Serve(w, r, body, 10, "audio/wav"); status != 200 || w.Body.String() != "0123456789" || !FirstPlay(r) {
		t.Fatalf("200: %d %q", status, w.Body.String())
	}
	r.Header.Set("Range", "bytes=0-")
	if !FirstPlay(r) {
		t.Error("bytes=0- starts a play")
	}
	r.Header.Set("Range", "bytes=10-")
	w = httptest.NewRecorder()
	if status, err := Serve(w, r, body, 10, "audio/wav"); status != 416 || err == nil || w.Header().Get("Content-Range") != "bytes */10" {
		t.Fatalf("416: %d %v", status, err)
	}
}

func TestAlign(t *testing.T) {
	words := []Word{{Word: "Shalom,", Start: 0, End: 0.4}, {Word: "olam", Start: 0.5, End: 0.9}, {Word: "uh", Start: 1, End: 1.1}}
	ops := [][]string{{"=", "shalom", "shalom"}, {"D", "lekulam", ""}, {"S", "olamot", "olam"}, {"I", "", "uh"}, {"D", "tov", ""}}
	out, dels, ok := Align(words, ops)
	if !ok || out[0].Op != "=" || out[1].Op != "S" || out[1].Ref != "olamot" || out[2].Op != "I" {
		t.Fatalf("aligned %v %+v", ok, out)
	}
	if len(dels) != 2 || dels[0] != (Deletion{Before: 1, Ref: "lekulam"}) || dels[1] != (Deletion{Before: 3, Ref: "tov"}) {
		t.Fatalf("deletions %+v", dels)
	}
	if words[1].Op != "" {
		t.Fatal("Align changed its input")
	}
	// The normaliser merged words: the ops consume two hypothesis words for three timed ones.
	out, dels, ok = Align(words, [][]string{{"=", "a", "a"}, {"S", "b", "c"}, {"D", "d", ""}})
	if ok || out[0].Op != "" || len(dels) != 1 || dels[0].Before != 3 {
		t.Fatalf("unaligned %v %+v %+v", ok, out, dels)
	}
}

func TestFindRow(t *testing.T) {
	jsonl := `{"audio":"b3:aa","text":"one","words":[]}
{"audio":"b3:bb","text":"two","words":[{"word":"two","start":0.1,"end":0.3,"confidence":0.9}],"partials":[{"audioOffsetMs":160,"emitMs":12.5,"text":"tw"}]}
{"note":"mentions \"b3:cc\" but is not its row","audio":"b3:dd","text":"x","words":[]}
`
	var row HypothesisRow
	ok, err := FindRow(strings.NewReader(jsonl), "b3:bb", &row)
	if err != nil || !ok || row.Text != "two" || len(row.Words) != 1 || *row.Words[0].Confidence != 0.9 || len(row.Partials) != 1 {
		t.Fatalf("%v %v %+v", ok, err, row)
	}
	if ok, err := FindRow(strings.NewReader(jsonl), "b3:cc", &row); ok || err != nil {
		t.Fatalf("b3:cc: %v %v", ok, err)
	}
}
