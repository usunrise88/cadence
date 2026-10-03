// Package media serves audio to people (docs/spec/06-platform.md "Media", R25, R51, R52): an utterance's audio as
// 16 kHz PCM WAV spans with byte ranges, signed short-lived links, waveform peaks cached in the content store, the
// spectrogram tile pyramid of spectrogram_tiles@1, and the timed words of a hypotheses artifact marked against a
// scores artifact. The server's handlers (internal/server/handlers_media.go) add identity, audit and the refusal of
// agent tokens; this package knows only utterances, the content store and the artifact index.
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usunrise88/cadence/control-plane/internal/artifacts"
	"github.com/usunrise88/cadence/control-plane/internal/cas"
	"github.com/usunrise88/cadence/control-plane/internal/problems"
	"github.com/usunrise88/cadence/control-plane/internal/steps"
	"github.com/usunrise88/cadence/control-plane/internal/storage"
)

// ServeRate is the sample rate audio.get answers (R25: 16 kHz, the models' rate).
const ServeRate = 16000

// Artifact types this package reads and writes.
const (
	TypePeaks            = "peaks"
	TypeSpectrogramTiles = "spectrogram_tiles"
	PeaksFormat          = "cadence.peaks/1"
	TilesManifest        = "manifest.json"
)

// Utterance is what serving needs of an utterance.
type Utterance struct {
	ID         string
	Hash       string // the audio blob in the content store (b3:…)
	Duration   float64
	SampleRate int
	Channels   int
}

// Lookup finds an utterance by id (utt_…) or by its audio's content hash (b3:…).
func Lookup(ctx context.Context, q storage.Querier, ref string) (Utterance, error) {
	var u Utterance
	err := q.QueryRow(ctx, `SELECT id, content_hash, duration_s, sample_rate, channels FROM utterances
		WHERE id = $1 OR content_hash = $1 LIMIT 1`, ref).Scan(&u.ID, &u.Hash, &u.Duration, &u.SampleRate, &u.Channels)
	if errors.Is(err, pgx.ErrNoRows) {
		return Utterance{}, problems.NotFound.New("no utterance %q in the registry", ref)
	}
	if err != nil {
		return Utterance{}, fmt.Errorf("look up utterance %s: %w", ref, err)
	}
	return u, nil
}

// Service serves media from the content store.
type Service struct {
	Pool *pgxpool.Pool
	CAS  *cas.Store
	// MaxSpan is the longest span Audio converts (seconds); a canonical file asked whole is served at any length.
	MaxSpan float64
	// Cache keeps converted spans; nil converts every request into memory.
	Cache *SpanCache
	// Conversions bounds the conversions running at once (a buffered channel used as a semaphore; nil: no bound).
	// One more answers 429 media-busy.
	Conversions chan struct{}
}

// busyRetryAfter is the Retry-After (seconds) of media-busy: a 10-minute span converts in well under that.
const busyRetryAfter = 2

// acquire takes a conversion slot, or answers media-busy.
func (s *Service) acquire() (func(), error) {
	if s.Conversions == nil {
		return func() {}, nil
	}
	select {
	case s.Conversions <- struct{}{}:
		return func() { <-s.Conversions }, nil
	default:
		pe := problems.MediaBusy.New("%d audio conversions are running (media.max_conversions); try again in a moment", cap(s.Conversions))
		pe.RetryAfter = busyRetryAfter
		return nil, pe
	}
}

// Served is a WAV response body.
type Served struct {
	Content io.ReadSeeker
	Size    int64
	// Start, End and Channel are the span served (Channel -1: every channel).
	Start, End float64
	Channel    int
	Channels   int
	// Direct is true when the stored file itself is served.
	Direct bool
	close  func() error
}

// Close releases the body.
func (s *Served) Close() error {
	if s.close != nil {
		return s.close()
	}
	return nil
}

func (s *Service) open(u Utterance) (*os.File, Info, error) {
	if s.CAS == nil {
		return nil, Info{}, problems.NotImplemented.New("this control plane has no content store")
	}
	f, err := s.CAS.Open(u.Hash)
	if errors.Is(err, cas.ErrNotFound) {
		return nil, Info{}, problems.NotFound.New("the audio of %s (%s) is not in the content store", u.ID, u.Hash)
	}
	if err != nil {
		return nil, Info{}, fmt.Errorf("open audio of %s: %w", u.ID, err)
	}
	st, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, Info{}, fmt.Errorf("stat audio of %s: %w", u.ID, err)
	}
	info, err := ReadInfo(f, st.Size())
	if err != nil {
		_ = f.Close()
		return nil, Info{}, problems.ValidationFailed.New("the audio of %s cannot be served: %v", u.ID, err)
	}
	return f, info, nil
}

// resolve checks a span against the audio and fills its defaults.
func resolve(sp Span, info Info) (start, end float64, channel int, err error) {
	dur := info.Duration()
	start, end, channel = 0, dur, -1
	if sp.Start != nil {
		start = *sp.Start
	}
	if sp.End != nil {
		end = min(*sp.End, dur)
	}
	if sp.Channel != nil {
		channel = *sp.Channel
		if channel >= info.Channels {
			return 0, 0, 0, problems.BadRequest.New("channel %d: the audio has %d channel(s)", channel, info.Channels)
		}
	}
	if start >= dur && dur > 0 {
		return 0, 0, 0, problems.BadRequest.New("start %.3f s is past the end of the audio (%.3f s)", start, dur)
	}
	if end <= start {
		return 0, 0, 0, problems.BadRequest.New("end (%.3f s) must be after start (%.3f s)", end, start)
	}
	return start, end, channel, nil
}

// Audio returns the WAV of a span: the stored file itself when it is 16 kHz 16-bit PCM and asked whole (every
// channel, no span), otherwise the span decoded, the channel picked, resampled to 16 kHz and encoded as 16-bit PCM —
// converted in blocks into the span cache once, and read from there by the requests that follow.
func (s *Service) Audio(u Utterance, sp Span) (*Served, error) {
	f, info, err := s.open(u)
	if err != nil {
		return nil, err
	}
	start, end, channel, err := resolve(sp, info)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	whole := sp.Start == nil && sp.End == nil && (sp.Channel == nil || info.Channels == 1)
	if whole && info.Canonical() && info.DataOffset == WAVHeaderSize {
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("stat audio of %s: %w", u.ID, err)
		}
		return &Served{Content: f, Size: st.Size(), Start: 0, End: info.Duration(), Channel: channel, Channels: info.Channels,
			Direct: true, close: f.Close}, nil
	}
	defer func() { _ = f.Close() }()
	if s.MaxSpan > 0 && end-start > s.MaxSpan {
		return nil, problems.BadRequest.New("a span of %.0f s is longer than the %.0f s converted at once (media.max_span_s); ask for a shorter span",
			end-start, s.MaxSpan)
	}
	rate := float64(info.SampleRate)
	a, b := int64(math.Round(start*rate)), int64(math.Round(end*rate))
	a, b = max(0, min(a, info.Frames())), max(0, min(b, info.Frames()))
	nc := info.Channels
	if channel >= 0 {
		nc = 1
	}
	served := func(body io.ReadSeeker, size int64, closeFn func() error) *Served {
		return &Served{Content: body, Size: size, Start: start, End: end, Channel: channel, Channels: nc, close: closeFn}
	}
	key := spanKey(u.Hash, a, b, channel)
	if s.Cache != nil {
		if cf, size, ok := s.Cache.Open(key); ok {
			return served(cf, size, cf.Close), nil
		}
	}
	release, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	if s.Cache == nil {
		var buf bytes.Buffer
		if err := ConvertSpan(&buf, f, info, a, b, channel); err != nil {
			return nil, err
		}
		return served(bytes.NewReader(buf.Bytes()), int64(buf.Len()), nil), nil
	}
	cf, size, err := s.Cache.Put(key, func(w io.Writer) error { return ConvertSpan(w, f, info, a, b, channel) })
	if err != nil {
		return nil, fmt.Errorf("convert a span of %s: %w", u.ID, err)
	}
	return served(cf, size, cf.Close), nil
}

// Info returns the stored audio's format.
func (s *Service) Info(u Utterance) (Info, error) {
	f, info, err := s.open(u)
	if err != nil {
		return Info{}, err
	}
	_ = f.Close()
	return info, nil
}

type peaksMeta struct {
	Format     string  `json:"format"`
	Audio      string  `json:"audio"`
	Channels   int     `json:"channels"`
	HopS       float64 `json:"hopS"`
	Frames     int     `json:"frames"`
	SampleRate int     `json:"sampleRate"`
	Clipped    []int   `json:"clipped,omitempty"`
}

// cached returns the newest live artifact of type typ computed for audio (meta.audio), "" when none.
func cached(ctx context.Context, q storage.Querier, typ, audio string) (artifacts.Artifact, bool, error) {
	rows, err := q.Query(ctx, `SELECT hash FROM artifacts WHERE type = $1 AND meta->>'audio' = $2 AND evicted_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, typ, audio)
	if err != nil {
		return artifacts.Artifact{}, false, fmt.Errorf("find %s of %s: %w", typ, audio, err)
	}
	hash, err := pgx.CollectExactlyOneRow(rows, pgx.RowTo[string])
	if errors.Is(err, pgx.ErrNoRows) {
		return artifacts.Artifact{}, false, nil
	}
	if err != nil {
		return artifacts.Artifact{}, false, fmt.Errorf("find %s of %s: %w", typ, audio, err)
	}
	a, err := artifacts.Get(ctx, q, hash)
	if err != nil {
		return artifacts.Artifact{}, false, err
	}
	return a, true, nil
}

// Peaks returns the 10 ms peaks of the utterance's audio: from the newest peaks artifact of the audio when the
// store holds it, otherwise computed from the audio and recorded as one (a registry artifact, meta.audio = the
// audio's hash), so the next view reads them.
func (s *Service) Peaks(ctx context.Context, u Utterance) (Peaks, string, int, error) {
	if a, ok, err := cached(ctx, s.Pool, TypePeaks, u.Hash); err != nil {
		return Peaks{}, "", 0, err
	} else if ok {
		var m peaksMeta
		if err := json.Unmarshal(a.Meta, &m); err == nil && m.Format == PeaksFormat && m.HopS > 0 {
			if b, err := s.readBlob(a.Hash); err == nil {
				if p, err := PeaksFromBytes(b, m.Channels, m.HopS); err == nil {
					p.Clipped = m.Clipped
					return p, a.Hash, m.SampleRate, nil
				}
			}
		}
	}
	f, info, err := s.open(u)
	if err != nil {
		return Peaks{}, "", 0, err
	}
	defer func() { _ = f.Close() }()
	p, err := ComputePeaks(f, info)
	if err != nil {
		return Peaks{}, "", 0, err
	}
	b := p.Bytes()
	hash, err := s.CAS.PutBytes(b)
	if err != nil {
		return Peaks{}, "", 0, fmt.Errorf("store peaks of %s: %w", u.ID, err)
	}
	meta, err := json.Marshal(peaksMeta{Format: PeaksFormat, Audio: u.Hash, Channels: p.Channels, HopS: p.HopS,
		Frames: p.Frames(), SampleRate: info.SampleRate, Clipped: p.Clipped})
	if err != nil {
		return Peaks{}, "", 0, fmt.Errorf("encode peaks meta: %w", err)
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		_, err := artifacts.Record(ctx, tx, s.CAS, steps.ArtifactRef{Hash: hash, Type: TypePeaks, Size: int64(len(b)), Meta: meta}, "", nil)
		return err
	})
	if err != nil {
		// The peaks are right either way; a failed index only means the next view computes them again.
		return p, "", info.SampleRate, nil
	}
	return p, hash, info.SampleRate, nil
}

func (s *Service) readBlob(hash string) ([]byte, error) {
	f, err := s.CAS.Open(hash)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(f)
}

// Tiles is the newest spectrogram_tiles artifact of the utterance's audio.
type Tiles struct {
	Artifact string
	Manifest cas.Manifest
}

// FindTiles returns the tile pyramid computed for the utterance's audio; not-found when none was.
func (s *Service) FindTiles(ctx context.Context, u Utterance) (Tiles, error) {
	if s.CAS == nil {
		return Tiles{}, problems.NotImplemented.New("this control plane has no content store")
	}
	a, ok, err := cached(ctx, s.Pool, TypeSpectrogramTiles, u.Hash)
	if err != nil {
		return Tiles{}, err
	}
	if !ok || !a.Directory {
		return Tiles{}, problems.NotFound.New("no spectrogram tiles were computed for the audio of %s; the audio view computes the spectrogram in the browser up to views.audio.browser_stft_max_s", u.ID)
	}
	m, err := s.CAS.ReadManifest(a.Hash)
	if err != nil {
		return Tiles{}, problems.NotFound.New("the spectrogram tiles of %s (%s) are not in the content store", u.ID, a.Hash)
	}
	return Tiles{Artifact: a.Hash, Manifest: m}, nil
}

// File returns the bytes of one file of the pyramid (manifest.json or c<ch>/l<L>/<i>.u8).
func (s *Service) File(t Tiles, path string) ([]byte, error) {
	for _, f := range t.Manifest.Files {
		if f.Path == path {
			b, err := s.readBlob(f.Hash)
			if err != nil {
				return nil, problems.NotFound.New("%s of %s is not in the content store", path, t.Artifact)
			}
			return b, nil
		}
	}
	return nil, problems.NotFound.New("the spectrogram tiles %s have no %s", t.Artifact, path)
}

// Words returns the utterance's row of a hypotheses artifact and, when scores is set, its row of the scores
// artifact.
func (s *Service) Words(u Utterance, hypotheses artifacts.Artifact, scores *artifacts.Artifact) (HypothesisRow, *ScoreRow, error) {
	if s.CAS == nil {
		return HypothesisRow{}, nil, problems.NotImplemented.New("this control plane has no content store")
	}
	var hyp HypothesisRow
	if err := s.scan(hypotheses, "hypotheses.jsonl", u, &hyp); err != nil {
		return HypothesisRow{}, nil, err
	}
	if scores == nil {
		return hyp, nil, nil
	}
	var sc ScoreRow
	if err := s.scan(*scores, "utterances.jsonl", u, &sc); err != nil {
		return HypothesisRow{}, nil, err
	}
	return hyp, &sc, nil
}

// scan finds u's row in a JSON-lines artifact: a file artifact, or the file named name of a directory artifact.
func (s *Service) scan(a artifacts.Artifact, name string, u Utterance, v any) error {
	hash := a.Hash
	if a.Directory {
		m, err := s.CAS.ReadManifest(a.Hash)
		if err != nil {
			return problems.NotFound.New("the %s artifact %s is not in the content store", a.Type, a.Hash)
		}
		hash = ""
		for _, f := range m.Files {
			if f.Path == name || strings.HasSuffix(f.Path, "/"+name) {
				hash = f.Hash
				break
			}
		}
		if hash == "" {
			return problems.BadRequest.New("the directory artifact %s has no %s", a.Hash, name)
		}
	}
	f, err := s.CAS.Open(hash)
	if err != nil {
		return problems.NotFound.New("the %s artifact %s is not in the content store", a.Type, a.Hash)
	}
	defer func() { _ = f.Close() }()
	ok, err := FindRow(f, u.Hash, v)
	if err != nil {
		return problems.BadRequest.New("the %s artifact %s: %v", a.Type, a.Hash, err)
	}
	if !ok {
		return problems.NotFound.New("the %s artifact %s has no row for the audio of %s", a.Type, a.Hash, u.ID)
	}
	return nil
}
