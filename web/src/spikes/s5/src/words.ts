// Word tracks are DOM (R51): each word is its own bidi-isolated <bdi dir="auto"> run positioned on the shared time
// axis, which runs left to right in every locale. Only words inside the visible range exist as elements (a pool);
// when more than MAX_WORDS are visible the track shows density blocks instead of words.
import type { Word, WordTrackData } from "./sources";

const MAX_WORDS = 300;
const TEXT_MIN_PX = 14;

export class WordTrack {
  readonly el: HTMLDivElement;
  private pool: HTMLElement[] = [];
  private blocks: HTMLElement[] = [];
  private starts: Float64Array;
  hovered = -1;
  visibleCount = 0;
  mode: "words" | "density" = "words";
  constructor(
    readonly doc: Document,
    readonly data: WordTrackData,
    private onHover: (track: WordTrack, index: number) => void,
  ) {
    this.el = doc.createElement("div");
    this.el.className = "s5-words";
    this.el.dataset.track = data.id;
    this.el.setAttribute("lang", data.lang);
    this.el.setAttribute("aria-hidden", "true"); // the summary and the transcript carry the words for assistive tech
    const label = doc.createElement("span");
    label.className = "s5-track-label";
    label.textContent = data.label;
    this.el.append(label);
    this.starts = Float64Array.from(data.words, (w) => w.s);
    this.el.addEventListener("pointerover", (e) => {
      const t = (e.target as HTMLElement).closest<HTMLElement>("[data-i]");
      if (t) this.onHover(this, Number(t.dataset.i));
    });
  }
  /** First word whose end is after t. */
  indexAt(t: number): number {
    let lo = 0;
    let hi = this.starts.length;
    while (lo < hi) {
      const mid = (lo + hi) >> 1;
      if (this.data.words[mid]!.e < t) lo = mid + 1;
      else hi = mid;
    }
    return lo;
  }
  word(i: number): Word | undefined {
    return this.data.words[i];
  }
  render(start: number, span: number, width: number) {
    const words = this.data.words;
    const pxPerS = width / span;
    const end = start + span;
    const first = this.indexAt(start);
    let last = first;
    while (last < words.length && words[last]!.s <= end) last++;
    const n = last - first;
    this.visibleCount = n;
    if (n > MAX_WORDS) {
      this.mode = "density";
      this.hide(this.pool, 0);
      const bucketPx = 4;
      const buckets = Math.ceil(width / bucketPx);
      const counts = new Uint16Array(buckets);
      for (let i = first; i < last; i++) {
        const b = Math.floor(((words[i]!.s - start) * pxPerS) / bucketPx);
        if (b >= 0 && b < buckets) counts[b]!++;
      }
      let used = 0;
      for (let b = 0; b < buckets; b++) {
        if (!counts[b]) continue;
        const el = this.get(this.blocks, used++, "i", "s5-density");
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
      const el = this.get(this.pool, k, "bdi", "s5-word");
      el.dir = "auto";
      const x = (w.s - start) * pxPerS;
      const px = Math.max(1, (w.e - w.s) * pxPerS);
      el.style.transform = `translateX(${x.toFixed(1)}px)`;
      el.style.width = `${px.toFixed(1)}px`;
      if (el.dataset.i !== String(i)) {
        el.dataset.i = String(i);
        el.dataset.op = w.op;
        el.style.setProperty("--conf", String(w.c));
        el.title = `${w.w} · ${w.s.toFixed(2)}–${w.e.toFixed(2)} s · confidence ${w.c}`;
      }
      const text = px >= TEXT_MIN_PX ? w.w : "";
      if (el.textContent !== text) el.textContent = text;
      el.classList.toggle("s5-hover", i === this.hovered);
    }
    this.hide(this.pool, n);
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
  private hide(list: HTMLElement[], from: number) {
    for (let i = from; i < list.length; i++) if (list[i]!.style.display !== "none") list[i]!.style.display = "none";
  }
  domCount() {
    return this.pool.length + this.blocks.length;
  }
}
