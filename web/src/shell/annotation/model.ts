// Pure helpers of the Annotate view (docs/spec/04-blocks.md "Annotation workflow"): what an item's form starts from,
// how a text selection becomes an entity span, the keys of the keyboard-first flow and how the queue advances.
import type { AnnotationTag, BatchItem, EntitySpan } from "@/api/gen/types.gen";

export const TAGS: { tag: AnnotationTag; label: string; key: string; hint: string }[] = [
  { tag: "noise", label: "Noise", key: "1", hint: "Noise covers part of the speech; still transcribable (kept)" },
  { tag: "crosstalk", label: "Crosstalk", key: "2", hint: "The other party speaks at the same time (kept)" },
  { tag: "foreign", label: "Foreign", key: "3", hint: "Mostly not the batch's language (the item is excluded)" },
  { tag: "unintelligible", label: "Unintelligible", key: "4", hint: "Cannot be made out (the item is excluded)" },
];

export const ENTITY_CLASSES = ["name", "address", "number", "date", "phone", "amount"] as const;

/** The keys of the Annotate view, shown in its help line and its tooltips. Never browser-reserved ones. */
export const KEYS = {
  done: "Ctrl+Enter",
  flag: "Ctrl+Shift+Enter",
  skip: "Alt+S",
  play: "Alt+P",
  replay: "Alt+R",
  channel: "Alt+C",
  entity: "Alt+E",
  tag: "Alt+1…4",
} as const;

export type AnnotationForm = { text: string; tags: AnnotationTag[]; entities: EntitySpan[] };

/**
 * The form an item opens with: the caller's own annotation when there is one, otherwise the prefill (the best
 * machine hypothesis), no tags and no spans.
 */
export function formOf(item: BatchItem, userId: string | undefined): AnnotationForm {
  const own = item.annotations.find((a) => a.annotator.id === userId);
  if (own) return { text: own.text, tags: [...own.tags], entities: own.entities.map((e) => ({ ...e })) };
  return { text: item.prefill.text, tags: [], entities: [] };
}

/** The span a textarea selection marks (character offsets in code points, as the server counts them). */
export function spanOf(text: string, selStart: number, selEnd: number, cls: string): EntitySpan | undefined {
  if (selEnd <= selStart) return undefined;
  // Trim surrounding white space from the selection.
  let a = selStart;
  let b = selEnd;
  while (a < b && /\s/.test(text[a] ?? "")) a++;
  while (b > a && /\s/.test(text[b - 1] ?? "")) b--;
  if (b <= a) return undefined;
  const start = [...text.slice(0, a)].length;
  const end = start + [...text.slice(a, b)].length;
  return { start, end, class: cls, text: text.slice(a, b) };
}

/** Spans that still match the text after an edit (a span whose text moved or changed is dropped). */
export function keepSpans(text: string, spans: EntitySpan[]): EntitySpan[] {
  const cps = [...text];
  return spans.filter((s) => s.end <= cps.length && cps.slice(s.start, s.end).join("") === s.text);
}

/** Toggles a tag in a list. */
export function toggleTag(tags: AnnotationTag[], t: AnnotationTag): AnnotationTag[] {
  return tags.includes(t) ? tags.filter((x) => x !== t) : [...tags, t];
}

/** The segment's position in its window (seconds on the view's axis, which starts at the window's start). */
export function segmentSpan(item: BatchItem): { start: number; end: number } {
  return { start: Math.max(0, item.segment.start - item.window.start), end: Math.max(0, item.segment.end - item.window.start) };
}

/** The channel order the channel switch cycles: the target, the other parties, then every channel together. */
export function channelCycle(item: BatchItem): (number | undefined)[] {
  const n = Math.max(1, item.window.channels || 1);
  const target = item.segment.channel >= 0 ? item.segment.channel : undefined;
  if (n === 1 || target === undefined) return [undefined];
  const others = Array.from({ length: n }, (_, i) => i).filter((c) => c !== target);
  return [target, ...others, undefined];
}

/** The label of a channel of an item (its role when known). */
export function channelLabel(item: BatchItem, ch: number | undefined): string {
  if (ch === undefined) return "Both";
  const role = item.window.roles?.[ch];
  const name = role ? role[0]!.toUpperCase() + role.slice(1) : `Channel ${ch}`;
  return ch === item.segment.channel ? `${name} (target)` : name;
}

/**
 * The item the Annotate view shows: the one the person picked from the list, else the server's next one for them
 * (BatchItemList.next). When the queue is empty there is none — never an item already done (the annotation e2e found
 * the first done item shown in place of "Nothing left to annotate").
 */
export function shownItem(items: BatchItem[], next: string | undefined, picked: string | undefined): BatchItem | undefined {
  const id = picked ?? next;
  return id === undefined ? undefined : items.find((i) => i.id === id);
}

/** "1.2 s" with one decimal. */
export function seconds(v: number | undefined): string {
  return v === undefined || !Number.isFinite(v) ? "—" : `${v.toFixed(1)} s`;
}
