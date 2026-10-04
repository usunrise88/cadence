import type { UtteranceWords } from "@/api/gen/types.gen";

// Word tracks are DOM, not canvas (R51): each word is its own bidi-isolated <bdi dir="auto"> run on the shared time
// axis, which runs left to right in every locale; Hebrew reads right to left inside its box, digits and Latin text
// stay isolated. Only words in the visible range exist as elements (a pool); above views.audio.words_max_visible
// the track shows density blocks. S, D and I against the reference show as a glyph and a colour, never colour alone.

export type WordOp = "=" | "S" | "I";

export type TrackWord = { word: string; start: number; end: number; confidence?: number; op?: WordOp; ref?: string };

export type WordTrackData = {
  id: string;
  label: string;
  /** BCP 47 language of the words (the track's lang attribute). */
  lang?: string;
  words: TrackWord[];
  /** Reference words the hypothesis dropped, drawn as a marker at the time the next word starts. */
  deletions?: { at: number; ref: string }[];
  /** Text shown across the track when it has no timed words (an unaligned reference, R51). */
  note?: string;
};

/**
 * The reference word track of a words.get answer (R51): the golden set's reference at its aligned times, outlined;
 * an unaligned reference shows as text with the reason. Undefined when the answer has no reference.
 */
export function referenceTrack(w: UtteranceWords, lang?: string): WordTrackData | undefined {
  const r = w.reference;
  if (!r) return undefined;
  const words: TrackWord[] = r.words.map((x) => ({ word: x.word, start: x.start, end: x.end }));
  const note = r.aligned ? undefined : `${r.text}${r.reason ? ` (unaligned: ${r.reason})` : " (unaligned)"}`;
  return { id: "ref", label: "Reference", lang: lang ?? r.language, words, note };
}

const TEXT_MIN_PX = 14;

/** First word whose end is after t (binary search; words are in time order). */
export function wordIndexAt(words: TrackWord[], t: number): number {
  let lo = 0;
  let hi = words.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (words[mid]!.end < t) lo = mid + 1;
    else hi = mid;
  }
  return lo;
}

/** The word the playhead should move to from t: the next one starting after t, or the previous one. */
export function stepWord(words: TrackWord[], t: number, dir: 1 | -1): number {
  if (words.length === 0) return -1;
  if (dir > 0) {
    const i = words.findIndex((w) => w.start > t + 0.001);
    return i < 0 ? words.length - 1 : i;
  }
  let i = -1;
  for (let k = 0; k < words.length; k++) if (words[k]!.start < t - 0.001) i = k;
  return Math.max(0, i);
}

/** The glyph and accessible name of an op. */
export function opLabel(op: WordOp | undefined, ref?: string): { glyph: string; text: string } {
  if (op === "S") return { glyph: "≠", text: ref ? `substituted for “${ref}”` : "substituted" };
  if (op === "I") return { glyph: "+", text: "inserted" };
  if (op === "=") return { glyph: "", text: "correct" };
  return { glyph: "", text: "" };
}

export class WordTrack {
  readonly el: HTMLDivElement;
  readonly data: WordTrackData;
  private doc: Document;
  private pool: HTMLElement[] = [];
  private blocks: HTMLElement[] = [];
  private dels: HTMLElement[] = [];
  private maxVisible: number;
  hovered = -1;
  visibleCount = 0;
  mode: "words" | "density" = "words";

  constructor(doc: Document, data: WordTrackData, maxVisible: number, onHover: (track: WordTrack, index: number) => void) {
    this.doc = doc;
    this.data = data;
    this.maxVisible = maxVisible;
    this.el = doc.createElement("div");
    this.el.className = "cadence-audio-words";
    this.el.dataset.track = data.id;
    if (data.lang) this.el.lang = data.lang;
    // The summary and the transcript beside the view carry the words for assistive technology.
    this.el.setAttribute("aria-hidden", "true");
    const label = doc.createElement("span");
    label.className = "cadence-audio-track-label";
    label.textContent = data.label;
    this.el.append(label);
    if (data.note && data.words.length === 0) {
      const note = doc.createElement("bdi");
      note.className = "cadence-audio-track-note";
      note.dir = "auto";
      note.textContent = data.note;
      this.el.append(note);
    }
    this.el.addEventListener("pointerover", (e) => {
      const t = (e.target as HTMLElement).closest<HTMLElement>("[data-i]");
      if (t) onHover(this, Number(t.dataset.i));
    });
  }

  word(i: number): TrackWord | undefined {
    return this.data.words[i];
  }

  render(start: number, span: number, width: number): void {
    const words = this.data.words;
    const pxPerS = width / span;
    const end = start + span;
    const first = wordIndexAt(words, start);
    let last = first;
    while (last < words.length && words[last]!.start <= end) last++;
    const n = last - first;
    this.visibleCount = n;
    if (n > this.maxVisible) {
      this.mode = "density";
      this.hide(this.pool, 0);
      this.hide(this.dels, 0);
      const bucketPx = 4;
      const buckets = Math.ceil(width / bucketPx);
      const counts = new Uint16Array(buckets);
      for (let i = first; i < last; i++) {
        const b = Math.floor(((words[i]!.start - start) * pxPerS) / bucketPx);
        if (b >= 0 && b < buckets) counts[b]!++;
      }
      let used = 0;
      for (let b = 0; b < buckets; b++) {
        if (!counts[b]) continue;
        const el = this.get(this.blocks, used++, "i", "cadence-audio-density");
        el.style.transform = `translateX(${b * bucketPx}px)`;
        el.style.opacity = String(Math.min(1, 0.25 + counts[b]! / 6));
      }
      this.hide(this.blocks, used);
      return;
    }
    this.mode = "words";
    this.hide(this.blocks, 0);
    for (let k = 0; k < n; k++) {
      const i = first + k;
      const w = words[i]!;
      const el = this.get(this.pool, k, "bdi", "cadence-audio-word");
      el.dir = "auto";
      const x = (w.start - start) * pxPerS;
      const px = Math.max(1, (w.end - w.start) * pxPerS);
      el.style.transform = `translateX(${x.toFixed(1)}px)`;
      el.style.width = `${px.toFixed(1)}px`;
      if (el.dataset.i !== String(i)) {
        el.dataset.i = String(i);
        if (w.op) el.dataset.op = w.op;
        else delete el.dataset.op;
        el.style.setProperty("--conf", String(w.confidence ?? 1));
        const op = opLabel(w.op, w.ref);
        const conf = w.confidence === undefined ? "" : ` · confidence ${w.confidence.toFixed(2)}`;
        el.title = `${w.word} · ${w.start.toFixed(2)}–${w.end.toFixed(2)} s${conf}${op.text && w.op !== "=" ? ` · ${op.text}` : ""}`;
      }
      const text = px >= TEXT_MIN_PX ? w.word : "";
      if (el.textContent !== text) el.textContent = text;
      el.classList.toggle("cadence-audio-hover", i === this.hovered);
    }
    this.hide(this.pool, n);
    let d = 0;
    for (const del of this.data.deletions ?? []) {
      if (del.at < start || del.at > end) continue;
      const el = this.get(this.dels, d++, "span", "cadence-audio-deletion");
      el.style.transform = `translateX(${((del.at - start) * pxPerS).toFixed(1)}px)`;
      el.title = `deleted: “${del.ref}”`;
      el.textContent = "−";
    }
    this.hide(this.dels, d);
  }

  private get(list: HTMLElement[], i: number, tag: string, cls: string): HTMLElement {
    let el = list[i];
    if (!el) {
      el = this.doc.createElement(tag);
      el.className = cls;
      list.push(el);
      this.el.append(el);
    }
    if (el.style.display === "none") el.style.display = "";
    return el;
  }

  private hide(list: HTMLElement[], from: number): void {
    for (let i = from; i < list.length; i++) if (list[i]!.style.display !== "none") list[i]!.style.display = "none";
  }

  domCount(): number {
    return this.pool.length + this.blocks.length + this.dels.length;
  }
}
