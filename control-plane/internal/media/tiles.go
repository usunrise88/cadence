package media

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/defaults"
	"github.com/usunrise88/cadence/control-plane/internal/events"
	"github.com/usunrise88/cadence/control-plane/internal/jobs"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// The spectrogram tile pyramid built in the control plane (R52; phase 4 tail): for audio longer than the browser's
// limit, the audio view's first spectrogram.get starts media.spectrogram, which reads the audio as audio.get serves
// it (resampled to 16 kHz), computes the same STFT as spectrogram_tiles@1 and the browser (a periodic Hann window
// centred in the FFT frame, centred frames with zero padding, magnitude × 2 / Σwindow in dB re full scale) and
// writes the cadence.spectrogram-tiles/1 directory into the content store: uint8 dB (-120 + 0.5 × v) tiles of
// bins × tile frames, bin-major, level 0 on the hop grid and level L max-pooled over 2^L frames. Bins stop at the
// origin's Nyquist, and at 4 kHz when the estimated bandwidth says the audio is of 8 kHz origin (narrowband). The
// worker step remains for pipelines; the control plane builds here because the audio the view plays may be a window
// of a file on a mount with no artifact to hand a step, and an utterance has no project to run a pipeline in.

// TilesSchema is the manifest's schema.
const TilesSchema = "cadence.spectrogram-tiles/1"

const (
	tileFloorDB     = -120.0
	tileStepDB      = 0.5
	tileBlockFrames = 2048 // STFT frames computed at once
	bandStride      = 4    // the bandwidth pass reads every fourth frame
	narrowTopHz     = 4000.0
)

// TileSettings are the STFT and tile shape (defaults.yaml views.audio).
type TileSettings struct {
	WindowMs   float64 `json:"windowMs"`
	HopMs      float64 `json:"hopMs"`
	NFFT       int     `json:"nFft"`
	TileFrames int     `json:"tileFrames"`
}

// Key names the settings a pyramid was built at (meta.settings).
func (t TileSettings) Key() string {
	return "hann-" + strconv.FormatFloat(t.WindowMs, 'g', -1, 64) + "ms-" + strconv.FormatFloat(t.HopMs, 'g', -1, 64) +
		"ms-n" + strconv.Itoa(t.NFFT) + "-t" + strconv.Itoa(t.TileFrames)
}

func lookupNum(d *defaults.Defaults, ref string, def float64) float64 {
	v, ok := d.Lookup(ref)
	if !ok {
		return def
	}
	switch x := v.(type) {
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case float64:
		return x
	}
	return def
}

// TileSettingsOf reads the views.audio settings.
func TileSettingsOf(d *defaults.Defaults) TileSettings {
	return TileSettings{
		WindowMs:   lookupNum(d, "views.audio.window_ms", 25),
		HopMs:      lookupNum(d, "views.audio.hop_ms", 10),
		NFFT:       int(lookupNum(d, "views.audio.n_fft", 512)),
		TileFrames: int(lookupNum(d, "views.audio.tile_frames", 512)),
	}
}

// BandParams are the bandwidth estimate's settings (defaults.yaml annotation.*, as tracks.get).
type BandParams struct {
	MarginDB, FloorDB, BandwidthFloorDB float64
}

// PyramidManifest is manifest.json of a pyramid (cadence.spectrogram-tiles/1).
type PyramidManifest struct {
	Schema           string             `json:"schema"`
	Audio            string             `json:"audio"`
	SampleRate       int                `json:"sampleRate"`
	OriginSampleRate int                `json:"originSampleRate"`
	Channels         int                `json:"channels"`
	Window           string             `json:"window"`
	WindowSamples    int                `json:"windowSamples"`
	HopSamples       int                `json:"hopSamples"`
	NFFT             int                `json:"nFft"`
	Bins             int                `json:"bins"`
	BinHz            float64            `json:"binHz"`
	TileFrames       int                `json:"tileFrames"`
	Encoding         map[string]float64 `json:"encoding"`
	Levels           []TileLevel        `json:"levels"`
	PeakDB           []float64          `json:"peakDb"`
	DurationS        float64            `json:"durationS"`
	BandwidthHz      float64            `json:"bandwidthHz"`
	Narrowband       bool               `json:"narrowband"`
	Settings         string             `json:"settings"`
}

// TileLevel is one level of the pyramid.
type TileLevel struct {
	Level  int     `json:"level"`
	HopS   float64 `json:"hopS"`
	Frames int     `json:"frames"`
	Tiles  int     `json:"tiles"`
}

// levelTable is the pyramid's levels for frames base frames: halve until one tile holds a level.
func levelTable(frames, tile int, hopS float64) []TileLevel {
	var out []TileLevel
	n := frames
	for l := 0; ; l++ {
		out = append(out, TileLevel{Level: l, HopS: math.Round(hopS*math.Pow(2, float64(l))*1e6) / 1e6, Frames: n,
			Tiles: max(1, (n+tile-1)/tile)})
		if n <= tile {
			return out
		}
		n = (n + 1) / 2
	}
}

// fftPlan is an in-place radix-2 complex FFT of one size with its twiddles and bit reversal precomputed.
type fftPlan struct {
	n   int
	rev []int
	tw  []complex128
}

func newFFTPlan(n int) (*fftPlan, error) {
	if n < 2 || n&(n-1) != 0 {
		return nil, fmt.Errorf("an FFT of %d points: the size must be a power of two", n)
	}
	p := &fftPlan{n: n, rev: make([]int, n), tw: make([]complex128, n/2)}
	bits := 0
	for 1<<bits < n {
		bits++
	}
	for i := range n {
		r := 0
		for b := range bits {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		p.rev[i] = r
	}
	for k := range n / 2 {
		p.tw[k] = cmplx.Exp(complex(0, -2*math.Pi*float64(k)/float64(n)))
	}
	return p, nil
}

func (p *fftPlan) run(a []complex128) {
	for i, r := range p.rev {
		if i < r {
			a[i], a[r] = a[r], a[i]
		}
	}
	for size := 2; size <= p.n; size <<= 1 {
		half, step := size/2, p.n/size
		for start := 0; start < p.n; start += size {
			for k := range half {
				v := a[start+k+half] * p.tw[k*step]
				u := a[start+k]
				a[start+k], a[start+k+half] = u+v, u-v
			}
		}
	}
}

// stft computes frames of one channel's 16 kHz signal: frame f is centred at f × hop, samples outside the signal
// are zero. get returns the samples [lo, hi) of the signal (zero outside it).
type stft struct {
	plan   *fftPlan
	win    []float64 // the window placed in the FFT frame
	scale  float64
	hop    int
	buf    []complex128
	frames int
}

func newSTFT(st TileSettings, n16 int) (*stft, error) {
	win := int(math.Round(st.WindowMs * ServeRate / 1000))
	hop := int(math.Round(st.HopMs * ServeRate / 1000))
	if hop < 1 || win < 2 {
		return nil, fmt.Errorf("window %g ms and hop %g ms are too short", st.WindowMs, st.HopMs)
	}
	if win > st.NFFT {
		return nil, fmt.Errorf("a %d-sample window does not fit a %d-point FFT", win, st.NFFT)
	}
	plan, err := newFFTPlan(st.NFFT)
	if err != nil {
		return nil, err
	}
	s := &stft{plan: plan, win: make([]float64, st.NFFT), hop: hop, buf: make([]complex128, st.NFFT), frames: 1 + n16/hop}
	off := (st.NFFT - win) / 2
	sum := 0.0
	for i := range win {
		h := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(win)) // periodic Hann
		s.win[off+i] = h
		sum += h
	}
	s.scale = 2 / sum
	return s, nil
}

// frame computes frame f's magnitudes (× scale) into mag (n/2+1 values) from x, which holds the signal from lo.
func (s *stft) frame(f int, x []float32, lo int, mag []float64) {
	n := s.plan.n
	a := f*s.hop - n/2 - lo
	for i := range n {
		v := 0.0
		if j := a + i; j >= 0 && j < len(x) {
			v = float64(x[j])
		}
		s.buf[i] = complex(v*s.win[i], 0)
	}
	s.plan.run(s.buf)
	for k := range mag {
		mag[k] = cmplx.Abs(s.buf[k]) * s.scale
	}
}

func encodeDB(db float64) byte {
	v := math.RoundToEven((db - tileFloorDB) / tileStepDB)
	return byte(max(0, min(255, v)))
}

// signal16 reads one channel of the audio at 16 kHz in ranges, as audio.get converts it.
type signal16 struct {
	r    io.ReaderAt
	info Info
	rs   *resampler
	n    int // input frames
	n16  int
}

func newSignal16(r io.ReaderAt, info Info) *signal16 {
	rs := newResampler(info.SampleRate, ServeRate)
	n := int(info.Frames())
	return &signal16{r: r, info: info, rs: rs, n: n, n16: rs.outLen(n)}
}

// read returns samples [lo, hi) of channel c (zero outside the signal).
func (g *signal16) read(c, lo, hi int) ([]float32, error) {
	out := make([]float32, max(0, hi-lo))
	a, b := max(lo, 0), min(hi, g.n16)
	if b <= a {
		return out, nil
	}
	ilo, ihi := g.rs.inputSpan(a, b)
	ilo, ihi = max(ilo, 0), max(min(ihi, g.n), max(ilo, 0))
	pcm, err := ReadFrames(g.r, g.info, int64(ilo), int64(ihi-ilo))
	if err != nil {
		return nil, err
	}
	g.rs.run(out[a-lo:b-lo], a, pcm[c], ilo, g.n)
	return out, nil
}

// pyramid writes one channel's tiles as frames arrive, pooling pairs of frames into the next level on the way.
type pyramid struct {
	ch, bins, tile int
	levels         []*pyrLevel
	put            func(path string, b []byte) error
}

type pyrLevel struct {
	buf      []byte
	n, index int
	carry    []byte
	hasCarry bool
}

func newPyramid(ch, bins, tile, nLevels int, put func(string, []byte) error) *pyramid {
	p := &pyramid{ch: ch, bins: bins, tile: tile, put: put}
	for range nLevels {
		p.levels = append(p.levels, &pyrLevel{buf: make([]byte, bins*tile), carry: make([]byte, bins)})
	}
	return p
}

func (p *pyramid) push(l int, frame []byte) error {
	lv := p.levels[l]
	for k := range p.bins {
		lv.buf[k*p.tile+lv.n] = frame[k]
	}
	lv.n++
	if lv.n == p.tile {
		if err := p.flush(l); err != nil {
			return err
		}
	}
	if l+1 == len(p.levels) {
		return nil
	}
	if !lv.hasCarry {
		copy(lv.carry, frame)
		lv.hasCarry = true
		return nil
	}
	pooled := make([]byte, p.bins)
	for k := range pooled {
		pooled[k] = max(lv.carry[k], frame[k])
	}
	lv.hasCarry = false
	return p.push(l+1, pooled)
}

func (p *pyramid) flush(l int) error {
	lv := p.levels[l]
	if err := p.put(fmt.Sprintf("c%d/l%d/%d.u8", p.ch, l, lv.index), lv.buf); err != nil {
		return err
	}
	lv.index++
	lv.n = 0
	lv.buf = make([]byte, p.bins*p.tile)
	return nil
}

// finish pairs every level's odd last frame with itself and writes the partial tiles (zero-padded).
func (p *pyramid) finish() error {
	for l, lv := range p.levels {
		if lv.hasCarry && l+1 < len(p.levels) {
			lv.hasCarry = false
			if err := p.push(l+1, append([]byte(nil), lv.carry...)); err != nil {
				return err
			}
		}
		if lv.n > 0 || lv.index == 0 {
			if err := p.flush(l); err != nil {
				return err
			}
		}
	}
	return nil
}

// estimateBandwidth is the highest frequency whose band keeps mean power within BandwidthFloorDB of the loudest band,
// over the frames above the speech threshold (the 10th-percentile level + MarginDB, at least FloorDB; every frame
// when none is), from every bandStride-th STFT frame of every channel.
func estimateBandwidth(g *signal16, chans []int, s *stft, nBins int, binHz float64, bp BandParams) (float64, error) {
	const buckets = 121 // frame levels -120..0 dB in 1 dB steps
	// Per 1 dB bucket of frame level: the summed power spectrum and the frames counted.
	type bucket struct {
		sum []float64
		n   int
	}
	bs := make([]bucket, buckets)
	mag := make([]float64, nBins)
	for _, c := range chans {
		for f0 := 0; f0 < s.frames; f0 += tileBlockFrames * bandStride {
			f1 := min(s.frames, f0+tileBlockFrames*bandStride)
			lo, hi := f0*s.hop-s.plan.n/2, (f1-1)*s.hop+s.plan.n/2
			x, err := g.read(c, lo, hi)
			if err != nil {
				return 0, err
			}
			for f := f0; f < f1; f += bandStride {
				s.frame(f, x, lo, mag)
				e := 0.0
				for _, m := range mag {
					e += m * m
				}
				level := 10 * math.Log10(e/float64(nBins)+1e-12)
				bk := &bs[int(max(0, min(buckets-1, math.Round(level)+120)))]
				if bk.sum == nil {
					bk.sum = make([]float64, nBins)
				}
				for k, m := range mag {
					bk.sum[k] += m * m
				}
				bk.n++
			}
		}
	}
	total := 0
	for _, bk := range bs {
		total += bk.n
	}
	if total == 0 {
		return 0, nil
	}
	p10, seen := 0, 0
	for b, bk := range bs {
		seen += bk.n
		if seen*10 >= total {
			p10 = b
			break
		}
	}
	thr := int(math.Ceil(math.Max(float64(p10-120)+bp.MarginDB, bp.FloorDB))) + 120
	power := make([]float64, nBins)
	used := 0
	for pass := 0; pass < 2 && used == 0; pass++ {
		for b, bk := range bs {
			if bk.sum == nil || (pass == 0 && b < thr) {
				continue
			}
			for k, v := range bk.sum {
				power[k] += v
			}
			used += bk.n
		}
	}
	peak := 0.0
	for k := 1; k < nBins; k++ {
		peak = math.Max(peak, power[k])
	}
	if peak <= 0 {
		return 0, nil
	}
	cut := peak * math.Pow(10, -bp.BandwidthFloorDB/10)
	top := 0
	for k := 1; k < nBins; k++ {
		if power[k] >= cut {
			top = k
		}
	}
	return math.Round(float64(top) * binHz), nil
}

// BuildTiles computes the pyramid of the audio (every channel, or only the audio's own) and hands each file to put:
// the tiles c<ch>/l<L>/<i>.u8, then manifest.json. progress gets the fraction done.
func BuildTiles(r io.ReaderAt, info Info, key string, st TileSettings, bp BandParams, put func(path string, b []byte) error, progress func(float64)) (PyramidManifest, error) {
	if st.TileFrames < 1 {
		return PyramidManifest{}, errors.New("tile frames must be positive")
	}
	chans := make([]int, 0, info.Channels)
	if info.Only != nil {
		chans = append(chans, *info.Only)
	} else {
		for c := range info.Channels {
			chans = append(chans, c)
		}
	}
	g := newSignal16(r, info)
	s, err := newSTFT(st, g.n16)
	if err != nil {
		return PyramidManifest{}, err
	}
	full := st.NFFT/2 + 1
	binHz := float64(ServeRate) / float64(st.NFFT)
	bw, err := estimateBandwidth(g, chans, s, full, binHz, bp)
	if err != nil {
		return PyramidManifest{}, err
	}
	narrow := bw > 0 && bw <= narrowEdge
	nyquist := math.Min(float64(info.SampleRate)/2, ServeRate/2)
	if narrow {
		nyquist = math.Min(nyquist, narrowTopHz)
	}
	bins := min(full, int(math.Floor(nyquist/binHz))+1)
	levels := levelTable(s.frames, st.TileFrames, float64(s.hop)/ServeRate)
	m := PyramidManifest{Schema: TilesSchema, Audio: key, SampleRate: ServeRate, OriginSampleRate: info.SampleRate,
		Channels: len(chans), Window: "hann", WindowSamples: int(math.Round(st.WindowMs * ServeRate / 1000)),
		HopSamples: s.hop, NFFT: st.NFFT, Bins: bins, BinHz: binHz, TileFrames: st.TileFrames,
		Encoding: map[string]float64{"floorDb": tileFloorDB, "stepDb": tileStepDB}, Levels: levels,
		DurationS: math.Round(info.Duration()*1e6) / 1e6, BandwidthHz: bw, Narrowband: narrow, Settings: st.Key()}
	mag := make([]float64, full)
	frame := make([]byte, bins)
	for i, c := range chans {
		pyr := newPyramid(i, bins, st.TileFrames, len(levels), put)
		peak := math.Inf(-1)
		for f0 := 0; f0 < s.frames; f0 += tileBlockFrames {
			f1 := min(s.frames, f0+tileBlockFrames)
			lo, hi := f0*s.hop-st.NFFT/2, (f1-1)*s.hop+st.NFFT/2
			x, err := g.read(c, lo, hi)
			if err != nil {
				return PyramidManifest{}, err
			}
			for f := f0; f < f1; f++ {
				s.frame(f, x, lo, mag)
				for k := range bins {
					db := 20 * math.Log10(math.Max(mag[k], 1e-7))
					peak = math.Max(peak, db)
					frame[k] = encodeDB(db)
				}
				if err := pyr.push(0, frame); err != nil {
					return PyramidManifest{}, err
				}
			}
			if progress != nil {
				progress((float64(i) + float64(f1)/float64(s.frames)) / float64(len(chans)))
			}
		}
		if err := pyr.finish(); err != nil {
			return PyramidManifest{}, err
		}
		if math.IsInf(peak, -1) {
			peak = tileFloorDB
		}
		m.PeakDB = append(m.PeakDB, math.Round(peak*100)/100)
	}
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return PyramidManifest{}, fmt.Errorf("encode the tiles manifest: %w", err)
	}
	if err := put(TilesManifest, append(b, '\n')); err != nil {
		return PyramidManifest{}, err
	}
	return m, nil
}

// ---------------------------------------------------------------- finding, starting and running a build

// tilesTouchEvery is how stale a pyramid's last view may be before a view records a new one (one write per view
// session, not per tile).
const tilesTouchEvery = time.Hour

// TouchTiles records a view of a pyramid (its manifest was read): the retention (internal/eviction,
// media.tiles_retention_days) counts from the last view. Best effort: a failed write only shortens the retention.
func (s *Service) TouchTiles(ctx context.Context, artifact string) {
	if s.Pool == nil || artifact == "" {
		return
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE artifacts SET last_used_at = now()
		WHERE hash = $1 AND evicted_at IS NULL AND (last_used_at IS NULL OR last_used_at < now() - make_interval(secs => $2))`,
		artifact, tilesTouchEvery.Seconds())
}

// tilesMeta is a spectrogram_tiles artifact's meta (the worker step's, plus settings and source when the control
// plane built it).
type tilesMeta struct {
	Audio    string `json:"audio"`
	Settings string `json:"settings,omitempty"`
	Levels   int    `json:"levels"`
	Channels int    `json:"channels"`
	Bins     int    `json:"bins"`
	Bytes    int64  `json:"bytes"`
	Source   string `json:"source,omitempty"`
}

// newestTiles is the newest live pyramid of audio at settings (or one with no settings recorded: the worker step's).
func newestTiles(ctx context.Context, q storage.Querier, audio, settings string) (string, bool, error) {
	rows, err := q.Query(ctx, `SELECT hash FROM artifacts WHERE type = $1 AND meta->>'audio' = $2 AND evicted_at IS NULL
			AND directory AND coalesce(meta->>'settings', $3) = $3
		ORDER BY created_at DESC LIMIT 1`, TypeSpectrogramTiles, audio, settings)
	if err != nil {
		return "", false, fmt.Errorf("find the tiles of %s: %w", audio, err)
	}
	hash, err := pgx.CollectExactlyOneRow(rows, pgx.RowTo[string])
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find the tiles of %s: %w", audio, err)
	}
	return hash, true, nil
}

type tilesArgs struct {
	Ref      string       `json:"ref"` // what Lookup reads the audio by (utt_…, b3:…, bit_…, tri_…)
	Key      string       `json:"key"` // the audio's key (meta.audio)
	Settings TileSettings `json:"settings"`
}

// TilesBuild is the state of a pyramid that is not there yet.
type TilesBuild struct {
	Job     jobs.Job
	Started bool
	Drafts  []events.Draft
}

// StartTiles returns the job building the pyramid of the utterance's audio at the current settings, starting one when
// none is queued or running: a failed build is answered (media-tiles-failed) until media.tiles_retry_s has passed.
// Audio longer than media.tiles_max_s is refused.
func (s *Service) StartTiles(ctx context.Context, tx pgx.Tx, ref string, u Utterance) (TilesBuild, error) {
	d := s.defaults()
	info, err := s.Info(u)
	if err != nil {
		return TilesBuild{}, err
	}
	if lim := float64(d.Media.TilesMaxSeconds.Value); lim > 0 && info.Duration() > lim {
		return TilesBuild{}, problems.BadRequest.New("the audio is %.0f s long: the control plane builds spectrogram tiles up to %.0f s (media.tiles_max_s)", info.Duration(), lim)
	}
	st := TileSettingsOf(d)
	retry := time.Duration(d.Media.TilesRetrySeconds.Value) * time.Second
	j, started, drafts, err := s.ensureJob(ctx, tx, JobSpectrogram, u.Hash+"|"+st.Key(), tilesArgs{Ref: ref, Key: u.Hash, Settings: st}, retry, true)
	if err != nil {
		return TilesBuild{}, err
	}
	if jobs.Terminal(j.State) && j.State != jobs.StateDone {
		return TilesBuild{}, problems.MediaTilesFailed.New("building the spectrogram tiles of %s failed (job %s): %s; a request after %s builds them again (media.tiles_retry_s)",
			u.ID, j.ID, j.Error, retry)
	}
	return TilesBuild{Job: j, Started: started, Drafts: drafts}, nil
}

// runSpectrogram builds and records one pyramid.
func (s *Service) runSpectrogram(ctx context.Context, run *jobs.Run) (any, error) {
	var a tilesArgs
	if err := json.Unmarshal(run.Args, &a); err != nil || a.Ref == "" || a.Key == "" {
		return nil, fmt.Errorf("media.spectrogram: bad args %s", run.Args)
	}
	if hash, ok, err := newestTiles(ctx, s.Pool, a.Key, a.Settings.Key()); err != nil {
		return nil, err
	} else if ok {
		return map[string]any{"artifact": hash, "reused": true}, nil
	}
	u, err := Lookup(ctx, s.Pool, a.Ref)
	if err != nil {
		return nil, err
	}
	release, err := s.heavy(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	f, info, err := s.open(u)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	d := s.defaults()
	bp := BandParams{MarginDB: d.Annotation.VADMarginDB.Value, FloorDB: d.Annotation.VADFloorDB.Value,
		BandwidthFloorDB: d.Annotation.BandwidthFloorDB.Value}
	var files []cas.File
	var size int64
	put := func(path string, b []byte) error {
		h, err := s.CAS.PutBytes(b)
		if err != nil {
			return fmt.Errorf("store %s: %w", path, err)
		}
		files = append(files, cas.File{Path: path, Hash: h, Size: int64(len(b))})
		size += int64(len(b))
		return ctx.Err()
	}
	last := time.Now()
	m, err := BuildTiles(f, info, a.Key, a.Settings, bp, put, func(frac float64) {
		if time.Since(last) > 2*time.Second {
			last = time.Now()
			_ = run.Progress(ctx, frac*0.98, fmt.Sprintf("%.0f %%", frac*100))
		}
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	dir, err := s.CAS.PutManifest(cas.Manifest{Files: files})
	if err != nil {
		return nil, fmt.Errorf("store the tiles manifest: %w", err)
	}
	meta, err := json.Marshal(tilesMeta{Audio: a.Key, Settings: a.Settings.Key(), Levels: len(m.Levels), Channels: m.Channels,
		Bins: m.Bins, Bytes: size, Source: JobSpectrogram})
	if err != nil {
		return nil, err
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := artifacts.Record(ctx, tx, s.CAS, steps.ArtifactRef{Hash: dir, Type: TypeSpectrogramTiles, Size: size, Meta: meta}, "", nil); err != nil {
			return err
		}
		// A build is a view: a pyramid rebuilt after the retention evicted it keeps its row (and creation) and starts
		// its retention again.
		_, err := tx.Exec(ctx, `UPDATE artifacts SET last_used_at = now() WHERE hash = $1`, dir)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("record the tiles of %s: %w", a.Key, err)
	}
	return map[string]any{"artifact": dir, "levels": len(m.Levels), "channels": m.Channels, "bins": m.Bins, "bytes": size,
		"narrowband": m.Narrowband, "bandwidthHz": m.BandwidthHz}, nil
}
