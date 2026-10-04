package media

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// The stored peaks of an audio (R51; phase 4 tail): one blob per audio, recorded as a registry artifact of type peaks
// whose meta says how to read it. Format cadence.peaks/2 holds the 10 ms base level followed by coarser levels, each
// max-pooled over PeaksLevelFactor frames of the one before, until a level fits in peaksTopFrames: a long call's
// overview reads a few kilobytes instead of the whole base level. cadence.peaks/1 (phase 3) is the base level alone.

// Who stored a peaks file (meta.source).
const (
	PeaksSourceView = "view"   // the first peaks.get of the audio (the fallback)
	PeaksSourceJob  = JobPeaks // the media.peaks job of a dataset version
)

// PeaksFormatV1 is the single-level format phase 3 wrote; it is still read.
const PeaksFormatV1 = "cadence.peaks/1"

// PeaksLevelFactor is how many frames of a level one frame of the next coarser level pools.
const PeaksLevelFactor = 16

// peaksTopFrames: a coarser level is added while the current one has more frames than this.
const peaksTopFrames = 1024

// PeaksLevel is one level of a peaks file: Factor base frames per pair, Frames pairs per channel, starting Offset
// bytes into the blob.
type PeaksLevel struct {
	Factor int   `json:"factor"`
	Frames int   `json:"frames"`
	Offset int64 `json:"offset"`
}

// peaksMeta is a peaks artifact's meta.
type peaksMeta struct {
	Format     string       `json:"format"`
	Audio      string       `json:"audio"`
	Channels   int          `json:"channels"`
	HopS       float64      `json:"hopS"`
	Frames     int          `json:"frames"`
	SampleRate int          `json:"sampleRate"`
	Clipped    []int        `json:"clipped,omitempty"`
	Levels     []PeaksLevel `json:"levels,omitempty"`
	Source     string       `json:"source,omitempty"` // what stored them: view (first view) or the job (media.peaks)
}

// levels returns the meta's levels; a cadence.peaks/1 file is its base level alone.
func (m peaksMeta) levels() []PeaksLevel {
	if len(m.Levels) > 0 {
		return m.Levels
	}
	return []PeaksLevel{{Factor: 1, Frames: m.Frames}}
}

// EncodePeaksFile returns the cadence.peaks/2 bytes of base-level peaks and the level table.
func EncodePeaksFile(p Peaks) ([]byte, []PeaksLevel) {
	var buf bytes.Buffer
	levels := []PeaksLevel{}
	cur, factor := p, 1
	for {
		levels = append(levels, PeaksLevel{Factor: factor, Frames: cur.Frames(), Offset: int64(buf.Len())})
		buf.Write(cur.Bytes())
		if cur.Frames() <= peaksTopFrames {
			return buf.Bytes(), levels
		}
		cur = cur.Span(0, cur.Frames(), PeaksLevelFactor)
		factor *= PeaksLevelFactor
	}
}

// StoredPeaks are an audio's peaks: a stored artifact read in ranges, or, when the store could not index them, the
// bytes just computed.
type StoredPeaks struct {
	Artifact string // the peaks artifact (b3:…); "" when not recorded
	Computed bool   // this request computed them
	meta     peaksMeta
	data     []byte // the file's bytes when held in memory
	open     func() (io.ReaderAt, func(), error)
}

// Channels is the number of channels.
func (s StoredPeaks) Channels() int { return s.meta.Channels }

// HopS is the base level's hop (seconds).
func (s StoredPeaks) HopS() float64 { return s.meta.HopS }

// Frames is the base level's pairs per channel.
func (s StoredPeaks) Frames() int { return s.meta.Frames }

// SampleRate is the stored audio's rate.
func (s StoredPeaks) SampleRate() int { return s.meta.SampleRate }

// Levels is the stored level table.
func (s StoredPeaks) Levels() []PeaksLevel { return s.meta.levels() }

// Span returns base frames [first, first+count) pooled by factor, read from the coarsest stored level whose factor
// divides factor: only that level's bytes for the span are read. With a coarser level, first moves down to a multiple
// of its factor; the returned first is where the answer starts.
func (s StoredPeaks) Span(first, count, factor int) (Peaks, int, error) {
	n := s.meta.Frames
	first = max(0, min(first, n))
	count = max(0, min(count, n-first))
	factor = max(1, factor)
	lv := s.meta.levels()[0]
	for _, l := range s.meta.levels() {
		if l.Factor <= factor && factor%l.Factor == 0 && l.Factor > lv.Factor {
			lv = l
		}
	}
	end := first + count
	first -= first % lv.Factor
	a, b := first/lv.Factor, min(lv.Frames, (end+lv.Factor-1)/lv.Factor)
	b = max(a, b)
	nc := max(1, s.meta.Channels)
	raw := make([]byte, (b-a)*2*nc)
	if len(raw) > 0 {
		off := lv.Offset + int64(a*2*nc)
		if s.data != nil {
			if off+int64(len(raw)) > int64(len(s.data)) {
				return Peaks{}, 0, fmt.Errorf("peaks: level %d is shorter than its table says", lv.Factor)
			}
			copy(raw, s.data[off:])
		} else {
			r, closeFn, err := s.open()
			if err != nil {
				return Peaks{}, 0, err
			}
			_, err = r.ReadAt(raw, off)
			closeFn()
			if err != nil {
				return Peaks{}, 0, fmt.Errorf("read peaks %s: %w", s.Artifact, err)
			}
		}
	}
	lp, err := PeaksFromBytes(raw, nc, s.meta.HopS*float64(lv.Factor))
	if err != nil {
		return Peaks{}, 0, err
	}
	out := lp.Span(0, lp.Frames(), factor/lv.Factor)
	out.HopS = s.meta.HopS * float64(factor)
	out.Clipped = nil
	for _, f := range s.meta.Clipped {
		if f >= first && f < end {
			i := (f - first) / factor
			if len(out.Clipped) == 0 || out.Clipped[len(out.Clipped)-1] != i {
				out.Clipped = append(out.Clipped, i)
			}
		}
	}
	return out, first, nil
}

// decodeMeta reads a peaks artifact's meta; ok is false for a meta this package cannot read.
func decodeMeta(raw json.RawMessage) (peaksMeta, bool) {
	var m peaksMeta
	if err := json.Unmarshal(raw, &m); err != nil || m.HopS <= 0 || m.Channels < 1 {
		return peaksMeta{}, false
	}
	if m.Format != PeaksFormat && m.Format != PeaksFormatV1 {
		return peaksMeta{}, false
	}
	return m, true
}
