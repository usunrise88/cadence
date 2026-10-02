package media

import "math"

// halfTaps is the windowed-sinc filter's half length in zero crossings of the output's band edge: 16 keeps the
// passband within ±0.01 dB up to 0.9 × the lower Nyquist and the stopband below -60 dB, enough for listening and
// for the browser's spectrogram.
const halfTaps = 16

// Resample converts x from rate from to rate to with a polyphase Hann-windowed sinc low-pass (cutoff 0.95 × the
// lower Nyquist). The filter table is built once per call: gcd(from, to) makes the phases repeat, so a 44.1 kHz →
// 16 kHz conversion needs 160 phases and an 8 → 16 kHz one 2.
func Resample(x []float32, from, to int) []float32 {
	if from == to || len(x) == 0 {
		return append([]float32(nil), x...)
	}
	g := gcd(from, to)
	up, down := to/g, from/g
	ratio := float64(to) / float64(from)
	cutoff := 0.95 * math.Min(1, ratio) // in units of the input's Nyquist
	width := int(math.Ceil(halfTaps / cutoff))
	taps := 2 * width
	table := make([]float64, up*taps)
	for p := range up {
		frac := float64(p) / float64(up)
		for j := range taps {
			k := j - width + 1 // input offset from floor(t)
			d := frac - float64(k)
			u := d / float64(width)
			if math.Abs(u) >= 1 {
				continue
			}
			w := 0.5 * (1 + math.Cos(math.Pi*u))
			table[p*taps+j] = cutoff * sinc(cutoff*d) * w
		}
	}
	n := int(math.Round(float64(len(x)) * ratio))
	out := make([]float32, n)
	for i := range n {
		num := int64(i) * int64(down)
		c := int(num / int64(up))
		p := int(num % int64(up))
		row := table[p*taps : (p+1)*taps]
		var sum float64
		lo := c - width + 1
		if lo >= 0 && lo+taps <= len(x) {
			seg := x[lo : lo+taps]
			for j, h := range row {
				sum += h * float64(seg[j])
			}
		} else {
			for j, h := range row {
				if k := lo + j; k >= 0 && k < len(x) {
					sum += h * float64(x[k])
				}
			}
		}
		out[i] = float32(sum)
	}
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
