package media

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// ConvertSpan converts block by block with the bytes a whole-span conversion gives: rates up and down, every channel
// or one, spans shorter and longer than a block, and edges inside the audio (the filter reads past them).
func TestConvertSpanEqualsWhole(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rate, ch int
		frames   int
		a, b     int64
		channel  int
	}{
		{"44.1 kHz stereo, both channels, past two blocks", 44100, 2, 44100 * 23, 0, 44100 * 23, -1},
		{"44.1 kHz stereo, right channel, inner span", 44100, 2, 44100 * 25, 44100*3 + 17, 44100*24 - 5, 1},
		{"8 kHz mono, inner span", 8000, 1, 8000 * 30, 8000 * 2, 8000 * 27, 0},
		{"16 kHz stereo, one channel (no resampling)", 16000, 2, 16000 * 12, 1000, 16000*12 - 1000, 0},
		{"48 kHz mono, short", 48000, 1, 48000, 100, 4900, -1},
		{"empty span", 22050, 1, 22050, 500, 500, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chans := make([][]float32, tc.ch)
			for c := range chans {
				chans[c] = sine(tc.frames, tc.rate, 300+float64(c)*700, 0.4)
			}
			src := EncodeWAV(chans, tc.rate)
			info, err := ReadInfo(bytes.NewReader(src), int64(len(src)))
			if err != nil {
				t.Fatal(err)
			}
			pcm, err := ReadFrames(bytes.NewReader(src), info, tc.a, tc.b-tc.a)
			if err != nil {
				t.Fatal(err)
			}
			if tc.channel >= 0 {
				pcm = pcm[tc.channel : tc.channel+1]
			}
			for c := range pcm {
				pcm[c] = Resample(pcm[c], tc.rate, ServeRate)
			}
			want := EncodeWAV(pcm, ServeRate)
			var got bytes.Buffer
			if err := ConvertSpan(&got, bytes.NewReader(src), info, tc.a, tc.b, tc.channel); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got.Bytes(), want) {
				t.Fatalf("block conversion differs from the whole span (%d vs %d bytes)", got.Len(), len(want))
			}
		})
	}
}

func TestSpanCache(t *testing.T) {
	dir := t.TempDir()
	limit := int64(3000)
	c := &SpanCache{Dir: dir, MaxBytes: func() int64 { return limit }}
	put := func(key string, n int) {
		t.Helper()
		f, size, err := c.Put(key, func(w io.Writer) error { _, err := w.Write(make([]byte, n)); return err })
		if err != nil || size != int64(n) {
			t.Fatalf("put %s: %d %v", key, size, err)
		}
		_ = f.Close()
	}
	put("a.wav", 1000)
	put("b.wav", 1000)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "a.wav"), old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, "b.wav"), old.Add(time.Minute), old.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Serving a refreshes it: b is now the least recently used and goes first.
	if f, _, ok := c.Open("a.wav"); !ok {
		t.Fatal("a.wav not cached")
	} else {
		_ = f.Close()
	}
	put("c.wav", 1500)
	if _, err := os.Stat(filepath.Join(dir, "b.wav")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("b.wav should have been pruned: %v", err)
	}
	for _, k := range []string{"a.wav", "c.wav"} {
		if f, _, ok := c.Open(k); !ok {
			t.Fatalf("%s pruned", k)
		} else {
			_ = f.Close()
		}
	}
	// A failed fill leaves nothing behind.
	if _, _, err := c.Put("d.wav", func(io.Writer) error { return errors.New("boom") }); err == nil {
		t.Fatal("a failed fill was cached")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("cache holds %d files after a failed fill", len(entries))
	}
	if spanKey("b3:x", 0, 10, -1) == spanKey("b3:x", 0, 10, 0) || spanKey("b3:x", 0, 10, 0) == spanKey("b3:x", 1, 10, 0) {
		t.Fatal("span keys collide")
	}
}

func TestConversionsBound(t *testing.T) {
	s := &Service{Conversions: make(chan struct{}, 1)}
	release, err := s.acquire()
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.acquire()
	var pe *problems.Error
	if !errors.As(err, &pe) || pe.Type != problems.MediaBusy || pe.RetryAfter <= 0 {
		t.Fatalf("second conversion: %v", err)
	}
	release()
	if release, err = s.acquire(); err != nil {
		t.Fatalf("after release: %v", err)
	}
	release()
}

func TestPlays(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	p := &Plays{Now: func() time.Time { return now }}
	k := PlayKey("user", "usr_a", "utt_1", 0, 2, -1)
	if !p.First(k, time.Minute) {
		t.Fatal("the first request of a play")
	}
	if p.First(k, time.Minute) {
		t.Fatal("a further range of the same play")
	}
	if !p.First(PlayKey("user", "usr_b", "utt_1", 0, 2, -1), time.Minute) || !p.First(PlayKey("user", "usr_a", "utt_1", 0, 2, 0), time.Minute) {
		t.Fatal("another viewer or channel is another play")
	}
	now = now.Add(2 * time.Minute)
	if !p.First(k, time.Minute) {
		t.Fatal("a play after the window")
	}
	for i := range maxPlays + 10 {
		p.First(PlayKey("user", "usr_c", "utt_x", float64(i), float64(i+1), -1), time.Minute)
	}
	if len(p.seen) > maxPlays {
		t.Fatalf("%d plays remembered", len(p.seen))
	}
}
