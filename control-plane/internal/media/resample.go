package media

import "math"

// halfTaps is the windowed-sinc filter's half length in zero crossings of the output's band edge: 16 keeps the
// passband within ±0.01 dB up to 0.9 × the lower Nyquist and the stopband below -60 dB, enough for listening and
// for the browser's spectrogram.
const halfTaps = 16

// resampler is a polyphase Hann-windowed sinc low-pass (cutoff 0.95 × the lower Nyquist) from one rate to another.
// The filter table is built once: gcd(from, to) makes the phases repeat, so a 44.1 kHz → 16 kHz conversion needs 160
// phases and an 8 → 16 kHz one 2. Output sample i reads the input around i × down / up; run computes any block of
// outputs from the block of input around it, so a long span converts in pieces with the same result as at once.
type resampler struct {
	from, to    int
	up, down    int
	ratio       float64
	width, taps int
	table       []float64
	identity    bool
}

func newResampler(from, to int) *resampler {
	r := &resampler{from: from, to: to, identity: from == to}
	if r.identity {
		r.up, r.down, r.ratio = 1, 1, 1
		return r
	}
	g := gcd(from, to)
	r.up, r.down = to/g, from/g
	r.ratio = float64(to) / float64(from)
	cutoff := 0.95 * math.Min(1, r.ratio) // in units of the input's Nyquist
	r.width = int(math.Ceil(halfTaps / cutoff))
	r.taps = 2 * r.width
	r.table = make([]float64, r.up*r.taps)
	for p := range r.up {
		frac := float64(p) / float64(r.up)
		for j := range r.taps {
			k := j - r.width + 1 // input offset from floor(t)
			d := frac - float64(k)
			u := d / float64(r.width)
			if math.Abs(u) >= 1 {
				continue
			}
			w := 0.5 * (1 + math.Cos(math.Pi*u))
			r.table[p*r.taps+j] = cutoff * sinc(cutoff*d) * w
		}
	}
	return r
}

// outLen is the number of output samples of n input samples.
func (r *resampler) outLen(n int) int {
	if r.identity {
		return n
	}
	return int(math.Round(float64(n) * r.ratio))
}

// inputSpan is the input range [lo, hi) outputs [i0, i1) read (before clipping to the input).
func (r *resampler) inputSpan(i0, i1 int) (lo, hi int) {
	if r.identity {
		return i0, i1
	}
	c0 := int(int64(i0) * int64(r.down) / int64(r.up))
	c1 := int(int64(i1-1) * int64(r.down) / int64(r.up))
	return c0 - r.width + 1, c1 - r.width + 1 + r.taps
}

// run writes outputs [i0, i0+len(out)) of an input of n samples, of which x holds [x0, x0+len(x)); x must cover the
// outputs' inputSpan clipped to [0, n). Samples outside [0, n) count as silence.
func (r *resampler) run(out []float32, i0 int, x []float32, x0, n int) {
	if r.identity {
		copy(out, x[i0-x0:])
		return
	}
	for o := range out {
		i := i0 + o
		num := int64(i) * int64(r.down)
		c := int(num / int64(r.up))
		p := int(num % int64(r.up))
		row := r.table[p*r.taps : (p+1)*r.taps]
		var sum float64
		lo := c - r.width + 1
		if lo >= 0 && lo+r.taps <= n {
			seg := x[lo-x0 : lo-x0+r.taps]
			for j, h := range row {
				sum += h * float64(seg[j])
			}
		} else {
			for j, h := range row {
				if k := lo + j; k >= 0 && k < n {
					sum += h * float64(x[k-x0])
				}
			}
		}
		out[o] = float32(sum)
	}
}

// Resample converts x from rate from to rate to (see resampler).
func Resample(x []float32, from, to int) []float32 {
	if from == to || len(x) == 0 {
		return append([]float32(nil), x...)
	}
	r := newResampler(from, to)
	out := make([]float32, r.outLen(len(x)))
	r.run(out, 0, x, 0, len(x))
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
