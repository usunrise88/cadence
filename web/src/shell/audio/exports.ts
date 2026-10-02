// Word exports (R51): Praat TextGrid (long text format), NIST CTM and WebVTT. Times are seconds on the audio's axis.

export type ExportWord = { word: string; start: number; end: number; confidence?: number };

const t3 = (s: number) => (Math.round(s * 1000) / 1000).toFixed(3);

/** Praat's quoted string: double quotes doubled. */
function praatString(s: string): string {
  return `"${s.replace(/"/g, '""')}"`;
}

/**
 * A TextGrid with one IntervalTier: the words, and empty intervals for the gaps, covering [0, duration] without
 * holes or overlaps (Praat refuses either). Overlapping words are clipped to the previous end.
 */
export function toTextGrid(words: ExportWord[], duration: number, tier = "words"): string {
  const sorted = [...words].sort((a, b) => a.start - b.start);
  const xmax = Math.max(duration, ...sorted.map((w) => w.end), 0);
  const intervals: { xmin: number; xmax: number; text: string }[] = [];
  let t = 0;
  for (const w of sorted) {
    const a = Math.max(t, Math.min(w.start, xmax));
    const b = Math.max(a, Math.min(w.end, xmax));
    if (b <= a) continue;
    if (a > t) intervals.push({ xmin: t, xmax: a, text: "" });
    intervals.push({ xmin: a, xmax: b, text: w.word });
    t = b;
  }
  if (t < xmax || intervals.length === 0) intervals.push({ xmin: t, xmax, text: "" });
  const lines = [
    'File type = "ooTextFile"',
    'Object class = "TextGrid"',
    "",
    "xmin = 0 ",
    `xmax = ${t3(xmax)} `,
    "tiers? <exists> ",
    "size = 1 ",
    "item []: ",
    "    item [1]:",
    '        class = "IntervalTier" ',
    `        name = ${praatString(tier)} `,
    "        xmin = 0 ",
    `        xmax = ${t3(xmax)} `,
    `        intervals: size = ${intervals.length} `,
  ];
  intervals.forEach((iv, i) => {
    lines.push(`        intervals [${i + 1}]:`, `            xmin = ${t3(iv.xmin)} `, `            xmax = ${t3(iv.xmax)} `, `            text = ${praatString(iv.text)} `);
  });
  return lines.join("\n") + "\n";
}

/**
 * NIST CTM: one line per word, `<file> <channel> <start> <duration> <word> [<confidence>]`. Whitespace inside a
 * word would split the field, so it becomes an underscore.
 */
export function toCtm(words: ExportWord[], file: string, channel = "1"): string {
  return words
    .map((w) => {
      const word = w.word.trim().replace(/\s+/g, "_") || "<unk>";
      const conf = w.confidence === undefined ? "" : ` ${(Math.round(w.confidence * 1000) / 1000).toFixed(3)}`;
      return `${file} ${channel} ${t3(w.start)} ${t3(Math.max(0, w.end - w.start))} ${word}${conf}`;
    })
    .join("\n")
    .concat(words.length ? "\n" : "");
}

function vttTime(s: number): string {
  const ms = Math.max(0, Math.round(s * 1000));
  const h = Math.floor(ms / 3_600_000);
  const m = Math.floor((ms % 3_600_000) / 60_000);
  const sec = Math.floor((ms % 60_000) / 1000);
  const r = ms % 1000;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")}.${String(r).padStart(3, "0")}`;
}

/** Escapes the three characters WebVTT cue text reserves. */
function vttText(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/**
 * WebVTT: words grouped into cues that break at a pause of at least `pause` seconds or after `maxWords` words; each
 * cue lists its words with inner timestamps (<00:00:01.200>) so a player can highlight them as they are spoken.
 */
export function toWebVtt(words: ExportWord[], pause = 0.5, maxWords = 10): string {
  const sorted = [...words].sort((a, b) => a.start - b.start);
  const cues: ExportWord[][] = [];
  for (const w of sorted) {
    const cur = cues[cues.length - 1];
    const last = cur?.[cur.length - 1];
    if (!cur || !last || w.start - last.end >= pause || cur.length >= maxWords) cues.push([w]);
    else cur.push(w);
  }
  const out = ["WEBVTT", ""];
  for (const c of cues) {
    const first = c[0]!;
    const end = Math.max(...c.map((w) => w.end));
    const text = c.map((w, i) => (i === 0 ? vttText(w.word) : `<${vttTime(w.start)}>${vttText(w.word)}`)).join(" ");
    out.push(`${vttTime(first.start)} --> ${vttTime(Math.max(end, first.start + 0.001))}`, text, "");
  }
  return out.join("\n");
}
