package media

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// spanBlockFrames is how many output frames a span conversion computes at once (10 s at 16 kHz): memory stays a few
// MB per conversion whatever the span's length, rate or channel count.
const spanBlockFrames = 10 * ServeRate

// ConvertSpan writes frames [a, b) of the audio (r, info) as a canonical 16 kHz 16-bit PCM WAV — channel ch, or every
// channel when ch < 0 — resampled block by block; the bytes equal EncodeWAV of Resample of the whole span.
func ConvertSpan(w io.Writer, r io.ReaderAt, info Info, a, b int64, ch int) error {
	n := int(max(0, b-a))
	rs := newResampler(info.SampleRate, ServeRate)
	outN := rs.outLen(n)
	nc := info.Channels
	if ch >= 0 {
		nc = 1
	}
	head := make([]byte, WAVHeaderSize)
	putWAVHeader(head, outN, nc, ServeRate)
	if _, err := w.Write(head); err != nil {
		return fmt.Errorf("write span: %w", err)
	}
	out := make([][]float32, nc)
	for c := range out {
		out[c] = make([]float32, min(spanBlockFrames, outN))
	}
	buf := make([]byte, min(spanBlockFrames, outN)*nc*2)
	for i0 := 0; i0 < outN; i0 += spanBlockFrames {
		i1 := min(i0+spanBlockFrames, outN)
		lo, hi := rs.inputSpan(i0, i1)
		lo, hi = max(lo, 0), max(min(hi, n), max(lo, 0))
		pcm, err := ReadFrames(r, info, a+int64(lo), int64(hi-lo))
		if err != nil {
			return err
		}
		block := make([][]float32, nc)
		for c := range nc {
			src := pcm[c]
			if ch >= 0 {
				src = pcm[ch]
			}
			block[c] = out[c][:i1-i0]
			rs.run(block[c], i0, src, lo, n)
		}
		m := (i1 - i0) * nc * 2
		putPCM16(buf[:m], block, i1-i0)
		if _, err := w.Write(buf[:m]); err != nil {
			return fmt.Errorf("write span: %w", err)
		}
	}
	return nil
}

// SpanCache keeps converted spans as files in Dir (the content store's cache/media-spans: derived, never backed up),
// named by audio hash, frame span and channel, so the ranges a media element fetches while it plays and seeks read a
// file instead of converting again. Beyond MaxBytes the least recently served spans are removed.
type SpanCache struct {
	Dir      string
	MaxBytes func() int64
	mu       sync.Mutex // one Put at a time prunes
}

// spanKey names a span's file.
func spanKey(audio string, a, b int64, ch int) string {
	h := sha256.Sum256([]byte(audio + "|" + strconv.FormatInt(a, 10) + "|" + strconv.FormatInt(b, 10) + "|" + strconv.Itoa(ch)))
	return hex.EncodeToString(h[:]) + ".wav"
}

// Open returns the cached span key and marks it used; ok is false when it is not cached.
func (c *SpanCache) Open(key string) (*os.File, int64, bool) {
	p := filepath.Join(c.Dir, key)
	f, err := os.Open(p) //nolint:gosec // the name is a hex digest under the cache directory
	if err != nil {
		return nil, 0, false
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, 0, false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return f, st.Size(), true
}

// Put writes a span with fill into the cache under key and opens it. A failed fill leaves nothing behind.
func (c *SpanCache) Put(key string, fill func(io.Writer) error) (*os.File, int64, error) {
	if err := os.MkdirAll(c.Dir, 0o750); err != nil {
		return nil, 0, fmt.Errorf("span cache: %w", err)
	}
	tmp, err := os.CreateTemp(c.Dir, "put-*")
	if err != nil {
		return nil, 0, fmt.Errorf("span cache: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	err = fill(tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, 0, err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(c.Dir, key)); err != nil {
		return nil, 0, fmt.Errorf("span cache: %w", err)
	}
	f, size, ok := c.Open(key)
	if !ok {
		return nil, 0, problems.Internal.New("the converted span vanished from the cache")
	}
	c.prune(key)
	return f, size, nil
}

// prune removes the least recently used spans (by modification time, which Open refreshes) until the cache fits;
// keep is never removed. An open file stays readable after its name is removed.
func (c *SpanCache) prune(keep string) {
	if c.MaxBytes == nil {
		return
	}
	limit := c.MaxBytes()
	c.mu.Lock()
	defer c.mu.Unlock()
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	type file struct {
		name string
		size int64
		at   time.Time
	}
	var (
		files []file
		total int64
	)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".wav" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, file{e.Name(), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for _, f := range files {
		if total <= limit {
			return
		}
		if f.name == keep {
			continue
		}
		if os.Remove(filepath.Join(c.Dir, f.name)) == nil {
			total -= f.size
		}
	}
}
