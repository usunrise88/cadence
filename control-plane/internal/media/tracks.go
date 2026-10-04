package media

import (
	"math"
	"math/cmplx"
	"sort"

	"github.com/usunrise88/cadence/control-plane/internal/problems"
)

// The audio view's analysis tracks (R51 "Energy, VAD": level in dBFS, speech regions, endpoints, estimated
// bandwidth), computed from the audio itself on request: an energy detector over 10 ms frames per channel (the
// channel's noise floor, its 10th percentile, plus a margin), the bandwidth from the mean power spectrum of the speech
// frames, and the end-of-utterance gap between the target channel and the others.

// TrackParams are the detector's settings (defaults.yaml annotation.*).
type TrackParams struct {
	HopMs            int
	MarginDB         float64
	FloorDB          float64
	MinSilenceMs     int
	BandwidthFloorDB float64
}

// ChannelTrack is one channel's tracks.
type ChannelTrack struct {
	Channel      int          `json:"channel"`
	Role         string       `json:"role,omitempty"`
	LevelDB      []float64    `json:"levelDb"`
	Speech       [][2]float64 `json:"speech"`
	NoiseFloorDB float64      `json:"noiseFloorDb"`
	BandwidthHz  float64      `json:"bandwidthHz"`
	Narrowband   bool         `json:"narrowband"`
}

// TrackEOU is the end-of-utterance gap of a window (seconds from the window's start).
type TrackEOU struct {
	SpeechEnd  float64  `json:"speechEnd"`
	NextSpeech *float64 `json:"nextSpeech,omitempty"`
	GapS       *float64 `json:"gapS,omitempty"`
}

// Tracks are an audio's analysis tracks (the contract's AudioTracks).
type Tracks struct {
	UtteranceID string         `json:"utteranceId"`
	HopS        float64        `json:"hopS"`
	DurationS   float64        `json:"durationS"`
	SampleRate  int            `json:"sampleRate"`
	Start       *float64       `json:"start,omitempty"`
	Channels    []ChannelTrack `json:"channels"`
	BandwidthHz float64        `json:"bandwidthHz"`
	Narrowband  bool           `json:"narrowband"`
	EOU         *TrackEOU      `json:"eou,omitempty"`
}

// narrowEdge is the bandwidth at or under which audio counts as of 8 kHz origin (4 kHz plus the bin it falls in).
const narrowEdge = 4200.0

// fftSize is the bandwidth estimate's transform (32 ms at 16 kHz).
const fftSize = 512

// Tracks computes the tracks of the utterance's audio (up to MaxSpan seconds).
func (s *Service) Tracks(u Utterance, p TrackParams) (Tracks, error) {
	f, info, err := s.open(u)
	if err != nil {
		return Tracks{}, err
	}
	defer func() { _ = f.Close() }()
	if s.MaxSpan > 0 && info.Duration() > s.MaxSpan {
		return Tracks{}, problems.BadRequest.New("the audio is %.0f s long, past the %.0f s analysed at once (media.max_span_s)", info.Duration(), s.MaxSpan)
	}
	pcm, err := ReadFrames(f, info, 0, info.Frames())
	if err != nil {
		return Tracks{}, err
	}
	hop := max(10, p.HopMs) / 10 * 10
	t := Tracks{UtteranceID: u.ID, HopS: float64(hop) / 1000, DurationS: round3(info.Duration()), SampleRate: info.SampleRate}
	if u.Window != nil {
		st := u.Window.Start
		t.Start = &st
	}
	channels := make([]int, 0, info.Channels)
	if info.Only != nil {
		channels = append(channels, *info.Only)
	} else {
		for c := range info.Channels {
			channels = append(channels, c)
		}
	}
	for _, c := range channels {
		ct := analyse(pcm[c], info.SampleRate, p)
		ct.Channel = c
		if c < len(u.Roles) {
			ct.Role = u.Roles[c]
		}
		// The level track at the asked resolution (the detector always runs at 10 ms).
		ct.LevelDB = pool(ct.LevelDB, hop/10)
		t.Channels = append(t.Channels, ct)
		t.BandwidthHz = math.Max(t.BandwidthHz, ct.BandwidthHz)
	}
	t.Narrowband = t.BandwidthHz > 0 && t.BandwidthHz <= narrowEdge
	if u.Window != nil && u.Target >= 0 && len(t.Channels) > 1 {
		t.EOU = eou(t.Channels, u.Target)
	}
	return t, nil
}

// analyse runs the detector over one channel at 10 ms.
func analyse(x []float32, rate int, p TrackParams) ChannelTrack {
	n := max(1, rate/100)
	frames := len(x) / n
	level := make([]float64, frames)
	for i := range frames {
		var e float64
		for _, v := range x[i*n : (i+1)*n] {
			e += float64(v) * float64(v)
		}
		level[i] = math.Max(-100, 10*math.Log10(e/float64(n)+1e-12))
		level[i] = math.Round(level[i]*10) / 10
	}
	ct := ChannelTrack{LevelDB: level, Speech: [][2]float64{}}
	if frames == 0 {
		return ct
	}
	sorted := append([]float64(nil), level...)
	sort.Float64s(sorted)
	floor := sorted[len(sorted)/10]
	ct.NoiseFloorDB = floor
	thr := math.Max(floor+p.MarginDB, p.FloorDB)
	gap := p.MinSilenceMs / 10
	start, last := -1, -1
	for i, l := range level {
		if l < thr {
			continue
		}
		if start >= 0 && i-last-1 > gap {
			ct.Speech = append(ct.Speech, [2]float64{float64(start) / 100, float64(last+1) / 100})
			start = -1
		}
		if start < 0 {
			start = i
		}
		last = i
	}
	if start >= 0 {
		ct.Speech = append(ct.Speech, [2]float64{float64(start) / 100, float64(last+1) / 100})
	}
	ct.BandwidthHz = bandwidth(x, rate, level, thr, p.BandwidthFloorDB)
	ct.Narrowband = ct.BandwidthHz > 0 && ct.BandwidthHz <= narrowEdge
	return ct
}

// bandwidth estimates the highest frequency whose band keeps mean power within floorDB of the loudest band, over the
// frames above the speech threshold (all frames when none is).
func bandwidth(x []float32, rate int, level []float64, thr, floorDB float64) float64 {
	if len(x) < fftSize {
		return float64(rate) / 2
	}
	win := make([]float64, fftSize)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(fftSize-1))
	}
	power := make([]float64, fftSize/2+1)
	buf := make([]complex128, fftSize)
	used := 0
	step := fftSize / 2
	for pass := 0; pass < 2 && used == 0; pass++ {
		for o := 0; o+fftSize <= len(x); o += step {
			fi := min(len(level)-1, (o+fftSize/2)/max(1, rate/100))
			if pass == 0 && (fi < 0 || level[fi] < thr) {
				continue
			}
			for i := range fftSize {
				buf[i] = complex(float64(x[o+i])*win[i], 0)
			}
			fft(buf)
			for k := range power {
				a := cmplx.Abs(buf[k])
				power[k] += a * a
			}
			used++
		}
	}
	peak := 0.0
	for k := 1; k < len(power); k++ {
		peak = math.Max(peak, power[k])
	}
	if peak <= 0 {
		return 0
	}
	cut := peak * math.Pow(10, -floorDB/10)
	top := 0
	for k := 1; k < len(power); k++ {
		if power[k] >= cut {
			top = k
		}
	}
	return math.Round(float64(top) * float64(rate) / fftSize)
}

// fft is an in-place iterative radix-2 FFT (len(a) a power of two).
func fft(a []complex128) {
	n := len(a)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			a[i], a[j] = a[j], a[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		w := cmplx.Exp(complex(0, -2*math.Pi/float64(size)))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := range size / 2 {
				u, v := a[start+k], a[start+k+size/2]*wk
				a[start+k], a[start+k+size/2] = u+v, u-v
				wk *= w
			}
		}
	}
}

// pool takes the maximum of every factor values (the loudest frame of each hop).
func pool(x []float64, factor int) []float64 {
	if factor <= 1 {
		return x
	}
	out := make([]float64, 0, (len(x)+factor-1)/factor)
	for i := 0; i < len(x); i += factor {
		m := -100.0
		for _, v := range x[i:min(len(x), i+factor)] {
			m = math.Max(m, v)
		}
		out = append(out, m)
	}
	return out
}

// eou is the gap between the target channel's last speech end and the other channels' next speech start.
func eou(chs []ChannelTrack, target int) *TrackEOU {
	var tgt *ChannelTrack
	for i := range chs {
		if chs[i].Channel == target {
			tgt = &chs[i]
		}
	}
	if tgt == nil || len(tgt.Speech) == 0 {
		return nil
	}
	end := tgt.Speech[len(tgt.Speech)-1][1]
	out := &TrackEOU{SpeechEnd: end}
	best := math.Inf(1)
	for _, c := range chs {
		if c.Channel == target {
			continue
		}
		for _, iv := range c.Speech {
			if iv[1] > end && iv[0] < best {
				best = iv[0]
			}
		}
	}
	if !math.IsInf(best, 1) {
		gap := round3(best - end)
		out.NextSpeech, out.GapS = &best, &gap
	}
	return out
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
