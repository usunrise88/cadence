// The audio view that has the keyboard focus: `view.audio.*` commands act on it and are enabled only while one has
// it (R51: keys are active only while a view has focus). A view registers on focus and leaves on blur or destroy.

export interface AudioController {
  readonly title: string;
  togglePlay(): void;
  /** Moves the playhead by a fraction of the visible span (negative: back). */
  seekBy(fraction: number): void;
  /** Zooms by factor around the playhead (< 1: in). */
  zoomBy(factor: number): void;
  loopIn(): void;
  loopOut(): void;
  stepWord(dir: 1 | -1): void;
}

let focused: AudioController | null = null;
const listeners = new Set<() => void>();

export function setFocusedAudio(c: AudioController | null): void {
  if (focused === c) return;
  focused = c;
  for (const fn of listeners) fn();
}

/** Clears the focus when it is c (blur, destroy). */
export function blurAudio(c: AudioController): void {
  if (focused === c) setFocusedAudio(null);
}

export function focusedAudio(): AudioController | null {
  return focused;
}

export function onFocusedAudio(fn: () => void): () => void {
  listeners.add(fn);
  return () => listeners.delete(fn);
}
