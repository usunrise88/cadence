// A5: AudioWorklet capture (R50). Channel 0 only, at the context's (device) rate, PCM16 little-endian, 80 ms frames
// posted to the main thread as transferable ArrayBuffers. Also reports peak level for the meter and clipping.
class Capture extends AudioWorkletProcessor {
  constructor() {
    super();
    this.frame = Math.round(sampleRate * 0.08);
    this.buf = new Int16Array(this.frame);
    this.n = 0;
    this.peak = 0;
    this.clipped = 0;
  }

  process(inputs) {
    const ch = inputs[0] && inputs[0][0]; // channel 0: Safari gives stereo with audio on the left when EC is off
    if (!ch) return true;
    for (let i = 0; i < ch.length; i++) {
      let s = ch[i];
      const a = Math.abs(s);
      if (a > this.peak) this.peak = a;
      if (a >= 0.999) this.clipped++;
      s = s < -1 ? -1 : s > 1 ? 1 : s;
      this.buf[this.n++] = s < 0 ? s * 0x8000 : s * 0x7fff;
      if (this.n === this.frame) {
        const out = this.buf.buffer;
        this.port.postMessage({ pcm: out, peak: this.peak, clipped: this.clipped, t: currentTime }, [out]);
        this.buf = new Int16Array(this.frame);
        this.n = 0;
        this.peak = 0;
      }
    }
    return true;
  }
}

registerProcessor('a5-capture', Capture);
