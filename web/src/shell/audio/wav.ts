// Decodes what audio.get serves: RIFF/WAVE, 16-bit PCM (any rate and channel count), into one Float32Array per
// channel. 8/24/32-bit PCM and 32-bit float are read too, for completeness.

export type DecodedAudio = { sampleRate: number; channels: Float32Array[] };

export function decodeWav(buf: ArrayBuffer): DecodedAudio {
  const v = new DataView(buf);
  const tag = (o: number) => String.fromCharCode(v.getUint8(o), v.getUint8(o + 1), v.getUint8(o + 2), v.getUint8(o + 3));
  if (buf.byteLength < 12 || tag(0) !== "RIFF" || tag(8) !== "WAVE") throw new Error("not a WAV file");
  let fmt: { format: number; channels: number; rate: number; bits: number } | null = null;
  let pos = 12;
  while (pos + 8 <= buf.byteLength) {
    const id = tag(pos);
    const size = v.getUint32(pos + 4, true);
    const body = pos + 8;
    if (id === "fmt ") {
      let format = v.getUint16(body, true);
      if (format === 0xfffe && size >= 26) format = v.getUint16(body + 24, true);
      fmt = { format, channels: v.getUint16(body + 2, true), rate: v.getUint32(body + 4, true), bits: v.getUint16(body + 14, true) };
    } else if (id === "data") {
      if (!fmt) throw new Error("WAV data before fmt");
      const { format, channels, rate, bits } = fmt;
      const width = bits / 8;
      const frames = Math.floor(Math.min(size, buf.byteLength - body) / (width * channels));
      const out = Array.from({ length: channels }, () => new Float32Array(frames));
      for (let f = 0; f < frames; f++) {
        for (let c = 0; c < channels; c++) {
          const o = body + (f * channels + c) * width;
          let s: number;
          if (format === 3 && bits === 32) s = v.getFloat32(o, true);
          else if (format !== 1) throw new Error(`WAV format ${format} is not PCM`);
          else if (bits === 16) s = v.getInt16(o, true) / 32768;
          else if (bits === 8) s = (v.getUint8(o) - 128) / 128;
          else if (bits === 24) s = ((v.getUint8(o) | (v.getUint8(o + 1) << 8) | (v.getInt8(o + 2) << 16)) >> 0) / 8388608;
          else if (bits === 32) s = v.getInt32(o, true) / 2147483648;
          else throw new Error(`${bits}-bit WAV is not supported`);
          out[c]![f] = s;
        }
      }
      return { sampleRate: rate, channels: out };
    }
    pos = body + size + (size & 1);
  }
  throw new Error("WAV file without a data chunk");
}

/** Mono float samples as a 16-bit PCM WAV file (the page's own audio, played by the view from a blob URL). */
export function encodeWav16(samples: Float32Array, sampleRate: number): ArrayBuffer {
  const buf = new ArrayBuffer(44 + samples.length * 2);
  const v = new DataView(buf);
  const put = (o: number, s: string) => {
    for (let i = 0; i < s.length; i++) v.setUint8(o + i, s.charCodeAt(i));
  };
  put(0, "RIFF");
  v.setUint32(4, 36 + samples.length * 2, true);
  put(8, "WAVE");
  put(12, "fmt ");
  v.setUint32(16, 16, true);
  v.setUint16(20, 1, true);
  v.setUint16(22, 1, true);
  v.setUint32(24, sampleRate, true);
  v.setUint32(28, sampleRate * 2, true);
  v.setUint16(32, 2, true);
  v.setUint16(34, 16, true);
  put(36, "data");
  v.setUint32(40, samples.length * 2, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i]!));
    v.setInt16(44 + i * 2, s < 0 ? Math.round(s * 0x8000) : Math.round(s * 0x7fff), true);
  }
  return buf;
}
