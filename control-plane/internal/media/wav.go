package media

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// WAV format tags.
const (
	formatPCM        = 1
	formatFloat      = 3
	formatExtensible = 0xFFFE
)

// ErrNotWAV is returned for audio that is not a RIFF/WAVE file Cadence can decode (PCM 8/16/24/32-bit or IEEE float
// 32-bit). Imports store canonical 16-bit PCM WAV (worker/cadence_worker/audio.py), so this means foreign bytes.
var ErrNotWAV = errors.New("not a PCM or float WAV file")

// Info describes a WAV file's audio.
type Info struct {
	SampleRate int
	Channels   int
	Bits       int
	Float      bool
	DataOffset int64 // byte offset of the first sample
	DataBytes  int64
}

// BlockAlign is the bytes per frame (all channels).
func (i Info) BlockAlign() int { return i.Channels * i.Bits / 8 }

// Frames is the number of frames in the data chunk.
func (i Info) Frames() int64 { return i.DataBytes / int64(i.BlockAlign()) }

// Duration is the audio's length in seconds.
func (i Info) Duration() float64 { return float64(i.Frames()) / float64(i.SampleRate) }

// Canonical reports whether the file is what audio.get serves as is: 16 kHz 16-bit PCM.
func (i Info) Canonical() bool { return i.SampleRate == ServeRate && i.Bits == 16 && !i.Float }

// ReadInfo parses the RIFF chunks of a WAV file of size bytes.
func ReadInfo(r io.ReaderAt, size int64) (Info, error) {
	var hdr [12]byte
	if _, err := r.ReadAt(hdr[:], 0); err != nil {
		return Info{}, fmt.Errorf("%w: %w", ErrNotWAV, err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return Info{}, ErrNotWAV
	}
	var (
		info    Info
		haveFmt bool
		tag     uint16
	)
	pos := int64(12)
	for pos+8 <= size {
		var ch [8]byte
		if _, err := r.ReadAt(ch[:], pos); err != nil {
			return Info{}, fmt.Errorf("%w: chunk at %d: %w", ErrNotWAV, pos, err)
		}
		id, n := string(ch[0:4]), int64(binary.LittleEndian.Uint32(ch[4:8]))
		body := pos + 8
		switch id {
		case "fmt ":
			if n < 16 {
				return Info{}, fmt.Errorf("%w: fmt chunk of %d bytes", ErrNotWAV, n)
			}
			b := make([]byte, min(n, 40))
			if _, err := r.ReadAt(b, body); err != nil {
				return Info{}, fmt.Errorf("%w: fmt chunk: %w", ErrNotWAV, err)
			}
			tag = binary.LittleEndian.Uint16(b[0:2])
			info.Channels = int(binary.LittleEndian.Uint16(b[2:4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(b[4:8]))
			info.Bits = int(binary.LittleEndian.Uint16(b[14:16]))
			if tag == formatExtensible && len(b) >= 26 {
				tag = binary.LittleEndian.Uint16(b[24:26])
			}
			haveFmt = true
		case "data":
			if !haveFmt {
				return Info{}, fmt.Errorf("%w: data before fmt", ErrNotWAV)
			}
			info.DataOffset = body
			info.DataBytes = min(n, size-body)
			switch {
			case tag == formatPCM && (info.Bits == 8 || info.Bits == 16 || info.Bits == 24 || info.Bits == 32):
			case tag == formatFloat && info.Bits == 32:
				info.Float = true
			default:
				return Info{}, fmt.Errorf("%w: format %d with %d bits", ErrNotWAV, tag, info.Bits)
			}
			if info.Channels < 1 || info.Channels > 16 || info.SampleRate < 1000 || info.SampleRate > 384000 {
				return Info{}, fmt.Errorf("%w: %d channels at %d Hz", ErrNotWAV, info.Channels, info.SampleRate)
			}
			info.DataBytes -= info.DataBytes % int64(info.BlockAlign())
			return info, nil
		}
		pos = body + n + n&1
	}
	return Info{}, fmt.Errorf("%w: no data chunk", ErrNotWAV)
}

// ReadFrames decodes count frames from frame start into one float32 slice per channel, samples in [-1, 1].
func ReadFrames(r io.ReaderAt, info Info, start, count int64) ([][]float32, error) {
	start = max(0, min(start, info.Frames()))
	count = max(0, min(count, info.Frames()-start))
	ba := int64(info.BlockAlign())
	buf := make([]byte, count*ba)
	if _, err := r.ReadAt(buf, info.DataOffset+start*ba); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read audio: %w", err)
	}
	out := make([][]float32, info.Channels)
	for c := range out {
		out[c] = make([]float32, count)
	}
	width := info.Bits / 8
	for f := int64(0); f < count; f++ {
		for c := range info.Channels {
			b := buf[f*ba+int64(c*width):]
			out[c][f] = sample(b, info.Bits, info.Float)
		}
	}
	return out, nil
}

//nolint:gosec // two's complement reading of sample bits
func sample(b []byte, bits int, float bool) float32 {
	switch {
	case float:
		v := math.Float32frombits(binary.LittleEndian.Uint32(b))
		if v != v { // NaN
			return 0
		}
		return max(-1, min(1, v))
	case bits == 8:
		return (float32(b[0]) - 128) / 128
	case bits == 16:
		return float32(int16(binary.LittleEndian.Uint16(b))) / 32768
	case bits == 24:
		v := int32(uint32(b[0])<<8|uint32(b[1])<<16|uint32(b[2])<<24) >> 8
		return float32(v) / 8388608
	default:
		return float32(float64(int32(binary.LittleEndian.Uint32(b))) / 2147483648)
	}
}

// WAVHeaderSize is the size of the canonical header EncodeWAV writes.
const WAVHeaderSize = 44

// EncodeWAV writes channels (equal lengths) as a canonical 16-bit PCM WAV: a 44-byte header and little-endian
// interleaved samples, the form worker/cadence_worker/audio.py stores.
//
//nolint:gosec // header fields are bounded (16 channels, 384 kHz at most); samples are clamped to int16
func EncodeWAV(channels [][]float32, rate int) []byte {
	n := 0
	if len(channels) > 0 {
		n = len(channels[0])
	}
	nc := len(channels)
	b := make([]byte, WAVHeaderSize+n*nc*2)
	putWAVHeader(b, n, nc, rate)
	putPCM16(b[WAVHeaderSize:], channels, n)
	return b
}

// putWAVHeader writes the canonical 44-byte header of n frames of nc 16-bit channels at rate into b.
//
//nolint:gosec // header fields are bounded (16 channels, 384 kHz at most)
func putWAVHeader(b []byte, n, nc, rate int) {
	data := n * nc * 2
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+data))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], formatPCM)
	binary.LittleEndian.PutUint16(b[22:], uint16(nc))
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*nc*2))
	binary.LittleEndian.PutUint16(b[32:], uint16(nc*2))
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
}

// putPCM16 writes the first n frames of channels interleaved as little-endian 16-bit samples into b.
//
//nolint:gosec // samples are clamped to int16
func putPCM16(b []byte, channels [][]float32, n int) {
	o := 0
	for i := range n {
		for c := range channels {
			v := float64(channels[c][i]) * 32768
			v = math.Round(max(-32768, min(32767, v)))
			binary.LittleEndian.PutUint16(b[o:], uint16(int16(v)))
			o += 2
		}
	}
}
