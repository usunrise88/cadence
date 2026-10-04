package media

import (
	"bytes"
	"encoding/binary"
	"math"
	"math/cmplx"
	"math/rand/v2"
	"testing"
)

func TestG711(t *testing.T) {
	if muLaw[0xFF] != 0 || muLaw[0x7F] != 0 {
		t.Errorf("μ-law zero: %v %v", muLaw[0xFF], muLaw[0x7F])
	}
	if got := muLaw[0x00] * 32768; got != -32124 {
		t.Errorf("μ-law 0x00 = %v, want -32124", got)
	}
	if got := muLaw[0x80] * 32768; got != 32124 {
		t.Errorf("μ-law 0x80 = %v, want 32124", got)
	}
	if got := aLaw[0xD5] * 32768; got != 8 {
		t.Errorf("A-law 0xD5 = %v, want 8", got)
	}
	if got := aLaw[0x2A] * 32768; got != -32256 {
		t.Errorf("A-law 0x2A = %v, want -32256", got)
	}
}

// mulawWAV writes a stereo 8 kHz μ-law WAV of frames samples per channel from fn(channel, i) in [-1, 1].
func mulawWAV(frames int, fn func(c, i int) float64) []byte {
	enc := func(v float64) byte { // the nearest table entry
		best, bd := byte(0), math.Inf(1)
		for b := range 256 {
			if d := math.Abs(float64(muLaw[b]) - v); d < bd {
				best, bd = byte(b), d
			}
		}
		return best
	}
	var buf bytes.Buffer
	data := frames * 2
	buf.WriteString("RIFF")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+data))
	buf.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(7), uint16(2), uint32(8000), uint32(16000), uint16(2), uint16(8)} {
		_ = binary.Write(&buf, binary.LittleEndian, v)
	}
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(data))
	for i := range frames {
		for c := range 2 {
			buf.WriteByte(enc(fn(c, i)))
		}
	}
	return buf.Bytes()
}

func TestMuLawWindow(t *testing.T) {
	b := mulawWAV(8000*4, func(c, i int) float64 {
		if c == 0 {
			return 0.5 * math.Sin(2*math.Pi*440*float64(i)/8000)
		}
		return 0
	})
	info, err := ReadInfo(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if info.Law != LawMuLaw || info.Channels != 2 || info.SampleRate != 8000 || info.Duration() != 4 || info.Canonical() {
		t.Fatalf("info %+v", info)
	}
	w := info.Window(1, 2.5)
	if w.Frames() != 12000 || !w.Windowed {
		t.Fatalf("window %+v", w)
	}
	pcm, err := ReadFrames(bytes.NewReader(b), w, 0, w.Frames())
	if err != nil {
		t.Fatal(err)
	}
	peak := float32(0)
	for _, v := range pcm[0] {
		peak = max(peak, v)
	}
	if peak < 0.45 || peak > 0.55 {
		t.Errorf("channel 0 peak %v", peak)
	}
	var out bytes.Buffer
	if err := ConvertSpan(&out, bytes.NewReader(b), w, 0, w.Frames(), 1); err != nil {
		t.Fatal(err)
	}
	if got := out.Len(); got != WAVHeaderSize+24000*2 {
		t.Errorf("1.5 s of one channel at 16 kHz: %d bytes", got)
	}
}

func TestFFT(t *testing.T) {
	n := 64
	a := make([]complex128, n)
	for i := range a {
		a[i] = complex(math.Cos(2*math.Pi*5*float64(i)/float64(n)), 0)
	}
	fft(a)
	for k := range n {
		want := 0.0
		if k == 5 || k == n-5 {
			want = float64(n) / 2
		}
		if math.Abs(cmplx.Abs(a[k])-want) > 1e-9 {
			t.Fatalf("bin %d = %v, want %v", k, cmplx.Abs(a[k]), want)
		}
	}
}

func TestAnalyseSpeechBandwidthAndEOU(t *testing.T) {
	const rate = 16000
	rng := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // test noise
	// Channel 0: quiet noise, speech-like wideband bursts from 1.0 to 2.0 s; channel 1: a 1 kHz tone from 2.4 to 3.0 s.
	x0, x1 := make([]float32, rate*4), make([]float32, rate*4)
	for i := range x0 {
		t := float64(i) / rate
		x0[i] = float32(rng.NormFloat64() * 0.0005)
		x1[i] = float32(rng.NormFloat64() * 0.0005)
		if t >= 1 && t < 2 {
			x0[i] += float32(rng.NormFloat64() * 0.2)
		}
		if t >= 2.4 && t < 3 {
			x1[i] += float32(0.3 * math.Sin(2*math.Pi*1000*t))
		}
	}
	p := TrackParams{HopMs: 10, MarginDB: 12, FloorDB: -55, MinSilenceMs: 300, BandwidthFloorDB: 50}
	c0, c1 := analyse(x0, rate, p), analyse(x1, rate, p)
	if len(c0.Speech) != 1 || math.Abs(c0.Speech[0][0]-1) > 0.02 || math.Abs(c0.Speech[0][1]-2) > 0.02 {
		t.Errorf("channel 0 speech %v", c0.Speech)
	}
	if c0.Narrowband || c0.BandwidthHz < 7000 {
		t.Errorf("white noise bandwidth %v", c0.BandwidthHz)
	}
	if !c1.Narrowband || c1.BandwidthHz > 4200 {
		t.Errorf("a 1 kHz tone: bandwidth %v", c1.BandwidthHz)
	}
	c0.Channel, c1.Channel = 0, 1
	e := eou([]ChannelTrack{c0, c1}, 0)
	if e == nil || e.GapS == nil || math.Abs(*e.GapS-0.4) > 0.03 {
		t.Errorf("eou %+v", e)
	}
	if got := pool([]float64{-50, -20, -60, -30, -10}, 2); len(got) != 3 || got[0] != -20 || got[2] != -10 {
		t.Errorf("pool %v", got)
	}
}
