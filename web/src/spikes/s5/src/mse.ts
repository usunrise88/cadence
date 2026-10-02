// Playback through an HTMLMediaElement fed by Media Source Extensions (R51/R25): the file is fetched in Range
// segments, as signed short-lived segment URLs would be, and appended in order. Created in the view's own document
// and window (MediaSource and URL of that realm) so it works in a popout.
export type MseStats = { segments: number; bytes: number; firstAppendMs: number; mime: string; supported: boolean };

export async function mseAudio(doc: Document, url: string, mime = 'audio/webm; codecs="opus"', segment = 1 << 20): Promise<{ audio: HTMLAudioElement; stats: MseStats; done: Promise<void> }> {
  const win = doc.defaultView as Window & typeof globalThis;
  const audio = doc.createElement("audio");
  audio.preload = "auto";
  const stats: MseStats = { segments: 0, bytes: 0, firstAppendMs: 0, mime, supported: win.MediaSource?.isTypeSupported(mime) ?? false };
  if (!stats.supported) return { audio, stats, done: Promise.resolve() };
  const ms = new win.MediaSource();
  audio.src = win.URL.createObjectURL(ms);
  await new Promise<void>((r) => ms.addEventListener("sourceopen", () => r(), { once: true }));
  const sb = ms.addSourceBuffer(mime);
  const t0 = performance.now();
  const total = Number((await fetch(url, { method: "HEAD" })).headers.get("content-length"));
  const done = (async () => {
    for (let off = 0; off < total; off += segment) {
      const end = Math.min(total, off + segment) - 1;
      const buf = await (await fetch(url, { headers: { Range: `bytes=${off}-${end}` } })).arrayBuffer();
      if (ms.readyState !== "open") return; // the element was dropped (panel moved to a popout)
      await new Promise<void>((r) => {
        sb.addEventListener("updateend", () => r(), { once: true });
        sb.appendBuffer(buf);
      });
      if (stats.segments === 0) stats.firstAppendMs = performance.now() - t0;
      stats.segments++;
      stats.bytes += buf.byteLength;
    }
    if (ms.readyState === "open") ms.endOfStream();
  })();
  return { audio, stats, done };
}
