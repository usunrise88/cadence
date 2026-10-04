package media

import (
	"fmt"
	"io"
	"math"
)

// PeaksHop is the base resolution of peaks: 10 ms, the model's frame grid (S5: 720 KB per channel-hour).
const PeaksHop = 0.01

// clipLevel is the sample magnitude counted as clipping (full scale of 16-bit PCM, one step of slack).
const clipLevel = 32766.0 / 32768

// maxClipped bounds the clipped frames a response lists.
const maxClipped = 1000

// Peaks are int8 min/max pairs, frames × channels × [min, max], value / 127 = sample.
type Peaks struct {
	Channels int
	HopS     float64
	Data     []int8
	Clipped  []int // frames where any channel reached full scale (at most maxClipped)
}

// Frames is the number of min/max pairs per channel.
func (p Peaks) Frames() int {
	if p.Channels == 0 {
		return 0
	}
	return len(p.Data) / (2 * p.Channels)
}

// Bytes returns the data as raw bytes (the peaks artifact's content).
//
//nolint:gosec // int8 to byte is the artifact's two's complement encoding
func (p Peaks) Bytes() []byte {
	b := make([]byte, len(p.Data))
	for i, v := range p.Data {
		b[i] = byte(v)
	}
	return b
}

// PeaksFromBytes reads a peaks artifact's content.
//
//nolint:gosec // byte to int8, the inverse of Bytes
func PeaksFromBytes(b []byte, channels int, hop float64) (Peaks, error) {
	if channels < 1 || len(b)%(2*channels) != 0 {
		return Peaks{}, fmt.Errorf("peaks of %d bytes do not hold %d channels of min/max pairs", len(b), channels)
	}
	d := make([]int8, len(b))
	for i, v := range b {
		d[i] = int8(v)
	}
	return Peaks{Channels: channels, HopS: hop, Data: d}, nil
}

// ComputePeaks reads the whole file in one-second chunks and returns its 10 ms peaks at the stored rate.
func ComputePeaks(r io.ReaderAt, info Info) (Peaks, error) {
	rate := float64(info.SampleRate)
	total := info.Frames()
	frames := int(math.Floor(float64(total) / (rate * PeaksHop)))
	if frames == 0 && total > 0 {
		frames = 1
	}
	p := Peaks{Channels: info.Channels, HopS: PeaksHop, Data: make([]int8, frames*2*info.Channels)}
	bound := func(f int) int64 {
		if f >= frames {
			return total
		}
		return int64(math.Floor(float64(f) * rate * PeaksHop))
	}
	const chunk = 100 // peaks frames per read: one second
	for f0 := 0; f0 < frames; f0 += chunk {
		f1 := min(frames, f0+chunk)
		s0, s1 := bound(f0), bound(f1)
		pcm, err := ReadFrames(r, info, s0, s1-s0)
		if err != nil {
			return Peaks{}, err
		}
		for f := f0; f < f1; f++ {
			a, b := bound(f)-s0, bound(f+1)-s0
			clipped := false
			for c := range info.Channels {
				lo, hi := float32(1), float32(-1)
				for _, v := range pcm[c][a:b] {
					lo, hi = min(lo, v), max(hi, v)
				}
				if a == b {
					lo, hi = 0, 0
				}
				if float64(hi) >= clipLevel || float64(lo) <= -clipLevel {
					clipped = true
				}
				o := (f*info.Channels + c) * 2
				p.Data[o], p.Data[o+1] = quant(lo), quant(hi)
			}
			if clipped && len(p.Clipped) < maxClipped {
				p.Clipped = append(p.Clipped, f)
			}
		}
	}
	return p, nil
}

func quant(v float32) int8 {
	return int8(math.Round(float64(max(-1, min(1, v))) * 127))
}

// Channel returns the peaks of one channel (c in range; clipped frames kept as they are).
func (p Peaks) Channel(c int) Peaks {
	if c < 0 || c >= p.Channels {
		return p
	}
	n := p.Frames()
	out := Peaks{Channels: 1, HopS: p.HopS, Data: make([]int8, 2*n), Clipped: p.Clipped}
	for f := range n {
		o := (f*p.Channels + c) * 2
		out.Data[2*f], out.Data[2*f+1] = p.Data[o], p.Data[o+1]
	}
	return out
}

// Span returns the pairs from first to first+count (clamped), pooled by factor: each output pair is the min of the
// mins and the max of the maxes of factor input pairs. Clipped frames are re-indexed to the output.
func (p Peaks) Span(first, count, factor int) Peaks {
	n := p.Frames()
	first = max(0, min(first, n))
	count = max(0, min(count, n-first))
	factor = max(1, factor)
	out := Peaks{Channels: p.Channels, HopS: p.HopS * float64(factor)}
	m := (count + factor - 1) / factor
	out.Data = make([]int8, m*2*p.Channels)
	for i := range m {
		a, b := first+i*factor, min(first+count, first+(i+1)*factor)
		for c := range p.Channels {
			lo, hi := int8(127), int8(-127)
			for f := a; f < b; f++ {
				o := (f*p.Channels + c) * 2
				lo, hi = min(lo, p.Data[o]), max(hi, p.Data[o+1])
			}
			o := (i*p.Channels + c) * 2
			out.Data[o], out.Data[o+1] = lo, hi
		}
	}
	for _, f := range p.Clipped {
		if f >= first && f < first+count {
			i := (f - first) / factor
			if len(out.Clipped) == 0 || out.Clipped[len(out.Clipped)-1] != i {
				out.Clipped = append(out.Clipped, i)
			}
		}
	}
	return out
}
