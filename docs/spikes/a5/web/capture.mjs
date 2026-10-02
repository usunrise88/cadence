// A5: drive the capture page in headless Chromium with a fake microphone that plays a WAV file.
//   docker run --rm --network host -v $PWD/docs/spikes/a5:/a5:ro -v ~/cadence-spikes/a5:/work \
//     -v <repo>/web/node_modules:/nm/node_modules:ro mcr.microsoft.com/playwright:v1.63.0-noble \
//     node /a5/web/capture.mjs /work/clips/ru/ru03.wav ru-RU
import { createRequire } from 'node:module';
import { writeFileSync } from 'node:fs';

const require = createRequire(import.meta.url);
const { chromium } = require('/nm/node_modules/playwright');

const wav = process.argv[2];
const language = process.argv[3] || 'ru-RU';
const base = process.env.A5_URL || 'http://127.0.0.1:18480/';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const browser = await chromium.launch({
  args: [
    '--use-fake-device-for-media-stream',
    '--use-fake-ui-for-media-stream',
    `--use-file-for-fake-audio-capture=${wav}`,
    '--autoplay-policy=no-user-gesture-required',
  ],
});
const ctx = await browser.newContext();
await ctx.grantPermissions(['microphone'], { origin: base });
const page = await ctx.newPage();
const consoleLines = [];
page.on('console', (m) => consoleLines.push(m.text()));
await page.goto(base);
const result = { browser: browser.version(), language };

// 1. processed capture (EC/NS/AGC on) just to read what the browser reports, then the raw capture we stream
result.processed = await page.evaluate(async () => {
  const s = await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true } });
  const t = s.getAudioTracks()[0];
  const r = { settings: t.getSettings(), capabilities: t.getCapabilities ? t.getCapabilities() : null, label: t.label };
  s.getTracks().forEach((x) => x.stop());
  return r;
});
const t0 = Date.now();
result.start = await page.evaluate((language) => window.a5start({ raw: true, language }), language);
await sleep(9000);
await page.evaluate(() => window.a5finalize());
await sleep(1500);
const s1 = await page.evaluate(() => {
  const st = window.__a5;
  return { frames: st.frames, bytes: st.bytes, peak: st.peak, clipped: st.clipped, events: st.events.slice(), closes: st.closes.slice(), tFinalize: st.tFinalize };
});
result.firstSession = {
  frames: s1.frames,
  bytesPerFrame: s1.bytes / s1.frames,
  peak: s1.peak,
  started: s1.events.find((e) => e.type === 'started'),
  partials: s1.events.filter((e) => e.type === 'partial').length,
  finals: s1.events.filter((e) => e.type === 'final').map((e) => ({ text: e.text, endpoint: e.endpoint, audioEnd: e.audioEnd, words: e.words.length })),
  finalizeToFinalMs: (() => {
    const f = s1.events.find((e) => e.type === 'final' && e.endpoint === 'finalize');
    return f ? Math.round(f._t - s1.tFinalize) : null;
  })(),
};
// 2. the ticket is single-use
result.ticketReuse = await page.evaluate(() => window.a5reuseTicket());
// 3. a dropped socket: the page needs a new session (new ticket) and a new start
result.reconnectSession = await page.evaluate((language) => window.a5dropAndReconnect(language), language);
await sleep(5000);
await page.evaluate(() => window.a5finalize());
await sleep(1500);
await page.evaluate(() => window.a5end());
await sleep(1500);
const s2 = await page.evaluate(() => window.__a5);
const after = s2.events.filter((e) => e._afterReconnect);
result.afterReconnect = {
  started: !!after.find((e) => e.type === 'started'),
  partials: after.filter((e) => e.type === 'partial').length,
  finals: after.filter((e) => e.type === 'final' && e.text).map((e) => e.text),
  summary: after.find((e) => e.type === 'summary') || null,
  relayStats: after.find((e) => e.type === 'stats' && e.source === 'relay') || null,
};
result.closes = s2.closes;
result.wallS = (Date.now() - t0) / 1000;
result.console = consoleLines.slice(-10);
await page.screenshot({ path: '/work/out/capture.png' });
await browser.close();
writeFileSync('/work/out/capture.json', JSON.stringify(result, null, 1));
console.log(JSON.stringify(result, null, 1));
