package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand/v2"
	"testing"
)

// randomPeaks are base-level peaks of frames pairs and channels with random values.
func randomPeaks(frames, channels int, seed uint64) Peaks {
	r := rand.New(rand.NewPCG(seed, 1))
	p := Peaks{Channels: channels, HopS: PeaksHop, Data: make([]int8, frames*2*channels)}
	for i := 0; i < len(p.Data); i += 2 {
		a, b := int8(r.IntN(255)-127), int8(r.IntN(255)-127)
		p.Data[i], p.Data[i+1] = min(a, b), max(a, b)
	}
	p.Clipped = []int{3, 4000, 40000}
	return p
}

func TestPeaksFileLevels(t *testing.T) {
	short := randomPeaks(900, 1, 1)
	if _, levels := EncodePeaksFile(short); len(levels) != 1 || levels[0].Frames != 900 {
		t.Fatalf("a short audio's peaks have one level: %+v", levels)
	}
	// An hour of stereo call: the base level, ×16, ×256 and ×4096.
	p := randomPeaks(360000, 2, 2)
	b, levels := EncodePeaksFile(p)
	want := []PeaksLevel{{1, 360000, 0}, {16, 22500, 1440000}, {256, 1407, 1530000}, {4096, 88, 1535628}}
	if fmt.Sprint(levels) != fmt.Sprint(want) {
		t.Fatalf("levels %+v, want %+v", levels, want)
	}
	if len(b) != 1440000+90000+5628+352 {
		t.Fatalf("file of %d bytes", len(b))
	}
	m := peaksMeta{Format: PeaksFormat, Channels: 2, HopS: PeaksHop, Frames: p.Frames(), Clipped: p.Clipped, Levels: levels}
	held := StoredPeaks{meta: m, data: b}
	read := StoredPeaks{meta: m, open: func() (io.ReaderAt, func(), error) { return bytes.NewReader(b), func() {}, nil }}
	cases := []struct {
		first, count, factor int
		wantFirst            int
		wantHop              float64
	}{
		{0, 360000, 1, 0, 0.01},        // the base level, whole
		{12345, 2000, 1, 12345, 0.01},  // a span of the base level
		{0, 360000, 256, 0, 2.56},      // the overview, from the ×256 level
		{1000, 64008, 32, 992, 0.32},   // ×16 level, first aligned down to 16
		{5120, 51200, 512, 5120, 5.12}, // ×256 level pooled by two
		{100, 1000, 3, 100, 0.03},      // a factor no level divides: the base level
	}
	for _, c := range cases {
		for name, sp := range map[string]StoredPeaks{"held": held, "read": read} {
			got, first, err := sp.Span(c.first, c.count, c.factor)
			if err != nil {
				t.Fatal(err)
			}
			if first != c.wantFirst || math.Abs(got.HopS-c.wantHop) > 1e-12 {
				t.Fatalf("%s %+v: first %d hop %v", name, c, first, got.HopS)
			}
			end := c.first + c.count
			ref := p.Span(first, end-first, c.factor) // pooled straight from the base level
			if !bytes.Equal(got.Bytes(), ref.Bytes()) {
				t.Fatalf("%s %+v: %d frames differ from the base level pooled (%d frames)", name, c, got.Frames(), ref.Frames())
			}
			if fmt.Sprint(got.Clipped) != fmt.Sprint(ref.Clipped) {
				t.Fatalf("%s %+v: clipped %v, want %v", name, c, got.Clipped, ref.Clipped)
			}
		}
	}
	// A phase-3 file (cadence.peaks/1) is its base level alone.
	raw, _ := json.Marshal(map[string]any{"format": PeaksFormatV1, "audio": "b3:x", "channels": 1, "hopS": 0.01, "frames": 900})
	if old, ok := decodeMeta(raw); !ok || len(old.levels()) != 1 || old.levels()[0].Factor != 1 {
		t.Fatalf("v1 meta %+v %v", old, ok)
	}
	if _, ok := decodeMeta([]byte(`{"format":"cadence.peaks/9","channels":1,"hopS":0.01}`)); ok {
		t.Fatal("an unknown format was read")
	}
}

// tilesOf builds the pyramid of WAV bytes into a map of files.
func tilesOf(t *testing.T, wav []byte, st TileSettings) (PyramidManifest, map[string][]byte) {
	t.Helper()
	info, err := ReadInfo(bytes.NewReader(wav), int64(len(wav)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	m, err := BuildTiles(bytes.NewReader(wav), info, "b3:test", st, BandParams{MarginDB: 10, FloorDB: -60, BandwidthFloorDB: 50},
		func(path string, b []byte) error {
			files[path] = append([]byte(nil), b...)
			return nil
		}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m, files
}

var testTiles = TileSettings{WindowMs: 25, HopMs: 10, NFFT: 512, TileFrames: 512}

// speechLike is a sum of tones up to top Hz with a slow envelope, so a level detector sees speech and pauses.
func speechLike(n, rate int, top float64) []float32 {
	x := make([]float32, n)
	for i := range x {
		t := float64(i) / float64(rate)
		env := 0.5 + 0.5*math.Sin(2*math.Pi*0.5*t)
		v := 0.0
		for hz := 200.0; hz <= top; hz += 300 {
			v += math.Sin(2*math.Pi*hz*t + hz)
		}
		x[i] = float32(env * 0.05 * v)
	}
	return x
}

func TestBuildTiles(t *testing.T) {
	// 12 s of a 1 kHz full-scale-ish sine at 16 kHz, mono: 1201 frames → levels of 1201, 601, 301 frames.
	n := 12 * 16000
	wav := EncodeWAV([][]float32{sine(n, 16000, 1000, 0.5)}, 16000)
	m, files := tilesOf(t, wav, testTiles)
	// A pure tone has no energy above 4 kHz: by its estimated bandwidth it is narrowband, so bins stop at 4 kHz.
	if m.Schema != TilesSchema || m.Channels != 1 || m.Bins != 129 || !m.Narrowband || m.HopSamples != 160 || m.WindowSamples != 400 {
		t.Fatalf("manifest %+v", m)
	}
	wantLevels := []TileLevel{{0, 0.01, 1201, 3}, {1, 0.02, 601, 2}, {2, 0.04, 301, 1}}
	if fmt.Sprint(m.Levels) != fmt.Sprint(wantLevels) {
		t.Fatalf("levels %+v", m.Levels)
	}
	if len(files) != 3+2+1+1 {
		t.Fatalf("%d files", len(files))
	}
	for _, lv := range m.Levels {
		for i := range lv.Tiles {
			if b := files[fmt.Sprintf("c0/l%d/%d.u8", lv.Level, i)]; len(b) != 129*512 {
				t.Fatalf("tile l%d/%d has %d bytes", lv.Level, i, len(b))
			}
		}
	}
	// A sine of amplitude 0.5 reads -6 dB in its bin (1000 Hz / 31.25 Hz = bin 32).
	if math.Abs(m.PeakDB[0]-(-6.02)) > 0.2 {
		t.Fatalf("peak %v dB", m.PeakDB[0])
	}
	l0 := files["c0/l0/0.u8"]
	at := func(tile []byte, bin, frame int) float64 { return -120 + 0.5*float64(tile[bin*512+frame]) }
	if db := at(l0, 32, 200); math.Abs(db-(-6)) > 0.5 {
		t.Fatalf("bin 32 frame 200: %v dB", db)
	}
	if db := at(l0, 100, 200); db > -60 {
		t.Fatalf("bin 100 frame 200 should be quiet: %v dB", db)
	}
	// Level 1 is the max of pairs of level 0 (frames 0..1023 of level 0 are tiles 0 and 1).
	l0b, l1 := files["c0/l0/1.u8"], files["c0/l1/0.u8"]
	frame0 := func(f, bin int) byte {
		if f < 512 {
			return l0[bin*512+f]
		}
		return l0b[bin*512+f-512]
	}
	for _, f := range []int{0, 7, 300, 511} {
		for _, bin := range []int{0, 32, 120} {
			if got, want := l1[bin*512+f], max(frame0(2*f, bin), frame0(2*f+1, bin)); got != want {
				t.Fatalf("level 1 frame %d bin %d: %d, want %d", f, bin, got, want)
			}
		}
	}
	// The last level's odd frame pairs with itself; tiles are zero-padded past the level's frames.
	if l2 := files["c0/l2/0.u8"]; l2[32*512+301] != 0 || l2[32*512+300] == 0 {
		t.Fatal("level 2 padding")
	}
}

func TestBuildTilesNarrowband(t *testing.T) {
	// 16 kHz audio of 8 kHz origin (nothing above 3.4 kHz): bins stop at 4 kHz.
	wav := EncodeWAV([][]float32{speechLike(6*16000, 16000, 3400)}, 16000)
	m, files := tilesOf(t, wav, testTiles)
	if !m.Narrowband || m.Bins != 129 || m.BandwidthHz > narrowEdge || m.BandwidthHz < 3000 {
		t.Fatalf("narrowband: %v bins %d bandwidth %v", m.Narrowband, m.Bins, m.BandwidthHz)
	}
	if b := files["c0/l0/0.u8"]; len(b) != 129*512 {
		t.Fatalf("tile of %d bytes", len(b))
	}
	// Wideband speech keeps every bin; stereo at 8 kHz keeps bins to the origin's Nyquist on both channels.
	wide := EncodeWAV([][]float32{speechLike(6*16000, 16000, 7000)}, 16000)
	if m, _ := tilesOf(t, wide, testTiles); m.Narrowband || m.Bins != 257 {
		t.Fatalf("wideband: %v bins %d bandwidth %v", m.Narrowband, m.Bins, m.BandwidthHz)
	}
	call := EncodeWAV([][]float32{speechLike(4*8000, 8000, 3400), speechLike(4*8000, 8000, 2000)}, 8000)
	m, files = tilesOf(t, call, testTiles)
	if m.Channels != 2 || m.Bins != 129 || m.OriginSampleRate != 8000 || len(m.PeakDB) != 2 {
		t.Fatalf("call %+v", m)
	}
	if _, ok := files["c1/l0/0.u8"]; !ok {
		t.Fatal("no tiles of channel 1")
	}
}

func TestTileSettingsKey(t *testing.T) {
	if k := testTiles.Key(); k != "hann-25ms-10ms-n512-t512" {
		t.Fatal(k)
	}
	if _, err := newSTFT(TileSettings{WindowMs: 25, HopMs: 10, NFFT: 500, TileFrames: 512}, 16000); err == nil {
		t.Fatal("an FFT size that is not a power of two was accepted")
	}
	if _, err := newSTFT(TileSettings{WindowMs: 50, HopMs: 10, NFFT: 512, TileFrames: 512}, 16000); err == nil {
		t.Fatal("a window longer than the FFT was accepted")
	}
}
