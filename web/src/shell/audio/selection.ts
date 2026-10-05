// Spans as selections (R51): `utt:<id>#t=1.20,2.35`, the W3C Media Fragments temporal syntax. The selection item
// may also carry the eval context the Audio panel uses to show hypothesis words: `&cell=evc_…` (the followed eval
// document's cell) or `&hyp=b3:…&scores=b3:…` (artifacts directly), and `&gs=ver_…` the golden set whose reference
// alignment gives the reference word track. Unknown fragment dimensions are ignored, as the
// Media Fragments rules ask.

export type AudioTarget = {
  /** Utterance id (utt_…) or the audio's content hash (b3:…). */
  utterance: string;
  start?: number;
  end?: number;
  channel?: number;
  /** A golden set version (ver_…): its reference alignment gives the reference word track. */
  goldenSet?: string;
  /** An eval cell (evc_…) of the followed eval document: its hypotheses and scores give the word track. */
  cell?: string;
  hypotheses?: string;
  scores?: string;
};

const num = (s: number) => (Math.round(s * 100) / 100).toFixed(2);

/** The temporal fragment of a span: `t=1.20,2.35` (`t=1.20` without an end, `t=,2.35` without a start). */
export function spanFragment(start?: number, end?: number): string {
  if (start === undefined && end === undefined) return "";
  return `t=${start === undefined ? "" : num(start)}${end === undefined ? "" : `,${num(end)}`}`;
}

/** A Media Fragments clock value: seconds (npt), mm:ss or hh:mm:ss, each with an optional fraction. */
export function parseClock(s: string): number | undefined {
  const v = s.trim().replace(/^npt:/, "");
  if (v === "") return undefined;
  if (/^\d+(\.\d+)?$/.test(v)) return Number(v);
  const m = /^(?:(\d+):)?(\d{1,2}):(\d{2}(?:\.\d+)?)$/.exec(v);
  if (!m) return undefined;
  return Number(m[1] ?? 0) * 3600 + Number(m[2]) * 60 + Number(m[3]);
}

/** The selection item for a target: `utt:<id>[#t=a,b][&ch=n][&cell=…|&hyp=…&scores=…][&gs=ver_…]`. */
export function audioItem(t: AudioTarget): string {
  const parts: string[] = [];
  const tf = spanFragment(t.start, t.end);
  if (tf) parts.push(tf);
  if (t.channel !== undefined) parts.push(`ch=${t.channel}`);
  if (t.cell) parts.push(`cell=${t.cell}`);
  if (t.hypotheses) parts.push(`hyp=${t.hypotheses}`);
  if (t.scores) parts.push(`scores=${t.scores}`);
  if (t.goldenSet) parts.push(`gs=${t.goldenSet}`);
  return `utt:${t.utterance}${parts.length ? `#${parts.join("&")}` : ""}`;
}

/** Parses `utt:<id>#…` (or a bare id or hash with a fragment); undefined for anything else. */
export function parseAudioItem(item: string | undefined): AudioTarget | undefined {
  if (!item) return undefined;
  const m = /^(?:@?(?:utt|utterance):)?((?:utt_[A-Za-z0-9-]+)|(?:b3:[0-9a-f]{64}))(?:#(.*))?$/.exec(item.trim());
  if (!m) return undefined;
  const out: AudioTarget = { utterance: m[1]! };
  for (const part of (m[2] ?? "").split("&")) {
    const eq = part.indexOf("=");
    if (eq < 0) continue;
    const k = part.slice(0, eq);
    const v = decodeURIComponent(part.slice(eq + 1));
    if (k === "t") {
      const [a, b] = v.split(",");
      const s = a !== undefined ? parseClock(a) : undefined;
      const e = b !== undefined ? parseClock(b) : undefined;
      if (s !== undefined) out.start = s;
      if (e !== undefined && (s === undefined || e > s)) out.end = e;
    } else if (k === "ch" && /^\d+$/.test(v)) out.channel = Number(v);
    else if (k === "cell" && /^evc_[A-Za-z0-9-]+$/.test(v)) out.cell = v;
    else if (k === "hyp" && /^b3:[0-9a-f]{64}$/.test(v)) out.hypotheses = v;
    else if (k === "scores" && /^b3:[0-9a-f]{64}$/.test(v)) out.scores = v;
    else if (k === "gs" && /^ver_[A-Za-z0-9_-]+$/.test(v)) out.goldenSet = v;
  }
  return out;
}

/**
 * What a span attaches to chat (`@utt:<id>#t=a,b`): the utterance id and the temporal fragment only — the eval
 * context stays in the UI. Content hashes are not reference ids, so a target still addressed by hash gives none.
 */
export function spanReference(t: AudioTarget): { ref: string; label: string } | undefined {
  if (!t.utterance.startsWith("utt_")) return undefined;
  const tf = spanFragment(t.start, t.end);
  const label = t.start !== undefined && t.end !== undefined ? `${t.utterance} ${num(t.start)}–${num(t.end)} s` : t.utterance;
  return { ref: `@utt:${t.utterance}${tf ? `#${tf}` : ""}`, label };
}
