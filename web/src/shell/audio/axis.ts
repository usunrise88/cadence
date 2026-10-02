import { useEffect, useState, useSyncExternalStore } from "react";

// The audio view's time axis (R51): visible range, zoom, playhead and loop span. Tracks render against it; it runs
// left to right in every locale. One axis can drive several views (Diff rows, Compare): pass the same AudioAxis.

/** Narrowest span a view zooms to (seconds): ten 20 ms frames. */
export const MIN_SPAN = 0.2;

export type AxisState = {
  /** Length of the audio in seconds. */
  duration: number;
  start: number;
  span: number;
  playhead: number;
  /** Loop span [in, out]; playback wraps inside it. */
  loop: [number, number] | null;
};

/** Clamps a range into [0, duration] with span in [MIN_SPAN, duration]. */
export function clampRange(start: number, span: number, duration: number): { start: number; span: number } {
  const d = Math.max(0, duration);
  const s = d <= 0 ? 0 : Math.min(d, Math.max(Math.min(MIN_SPAN, d), span));
  const a = Math.min(Math.max(0, d - s), Math.max(0, start));
  return { start: a, span: s };
}

/** The range after zooming by factor (< 1 zooms in) around time `at`, which keeps its place on screen. */
export function zoomAround(state: Pick<AxisState, "start" | "span" | "duration">, factor: number, at: number): { start: number; span: number } {
  const span = clampRange(0, state.span * factor, state.duration).span;
  const rel = state.span > 0 ? (at - state.start) / state.span : 0.5;
  return clampRange(at - rel * span, span, state.duration);
}

export function timeToX(t: number, start: number, span: number, width: number): number {
  return span > 0 ? ((t - start) / span) * width : 0;
}

export function xToTime(x: number, start: number, span: number, width: number): number {
  return width > 0 ? start + (x / width) * span : start;
}

/**
 * The pyramid level whose frames best fit the pixels: level L holds frames of hop0 × 2^L, so the level is
 * floor(log2(frames per pixel)), clamped to the levels there are.
 */
export function pyramidLevel(span: number, widthPx: number, hop0: number, levels: number): number {
  if (levels <= 0 || widthPx <= 0) return 0;
  const fpp = span / hop0 / widthPx;
  return Math.max(0, Math.min(levels - 1, Math.floor(Math.log2(Math.max(1, fpp)))));
}

const RULER_STEPS = [0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 1800, 3600];

/** Ruler tick spacing (seconds): the smallest step that keeps labels at least minPx apart. */
export function rulerStep(span: number, widthPx: number, minPx = 80): number {
  const pxPerS = span > 0 ? widthPx / span : 0;
  return RULER_STEPS.find((s) => s * pxPerS >= minPx) ?? RULER_STEPS[RULER_STEPS.length - 1]!;
}

/** m:ss with as many decimals as the visible span needs (2 under 5 s, 1 under a minute, none above). */
export function formatTime(t: number, span = 10): string {
  const neg = t < 0 ? "-" : "";
  const a = Math.abs(t);
  const dec = span < 5 ? 2 : span < 60 ? 1 : 0;
  const factor = 10 ** dec;
  const r = Math.round(a * factor) / factor;
  const m = Math.floor(r / 60);
  const s = r - m * 60;
  return `${neg}${m}:${s.toFixed(dec).padStart(dec ? 3 + dec : 2, "0")}`;
}

type Anim = { from: [number, number]; to: [number, number]; t0: number; ms: number };

/** An axis: state plus the operations the keys and the pointer perform. Views subscribe and redraw on change. */
export class AudioAxis {
  private state: AxisState;
  private listeners = new Set<() => void>();
  private anim: Anim | null = null;

  constructor(duration = 0, initial?: Partial<AxisState>) {
    const r = clampRange(initial?.start ?? 0, initial?.span ?? duration, duration);
    this.state = { duration, start: r.start, span: r.span, playhead: initial?.playhead ?? r.start, loop: initial?.loop ?? null };
  }

  get(): AxisState {
    return this.state;
  }

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  private set(next: Partial<AxisState>): void {
    const s = { ...this.state, ...next };
    if (
      s.duration === this.state.duration &&
      s.start === this.state.start &&
      s.span === this.state.span &&
      s.playhead === this.state.playhead &&
      s.loop === this.state.loop
    )
      return;
    this.state = s;
    for (const fn of this.listeners) fn();
  }

  /** A new audio length (data arrived): keeps the range when it still fits, else shows everything. */
  setDuration(duration: number): void {
    if (duration === this.state.duration) return;
    const whole = this.state.duration <= 0 || this.state.span >= this.state.duration;
    const r = whole ? clampRange(0, duration, duration) : clampRange(this.state.start, this.state.span, duration);
    this.set({ duration, ...r, playhead: Math.min(this.state.playhead, duration) });
  }

  setRange(start: number, span: number): void {
    this.anim = null;
    this.set(clampRange(start, span, this.state.duration));
  }

  /** Moves the range smoothly (ms) unless reduced motion is asked for; tick() advances it. */
  animateTo(start: number, span: number, ms = 160, reducedMotion = false): void {
    const r = clampRange(start, span, this.state.duration);
    if (reducedMotion || ms <= 0) {
      this.setRange(r.start, r.span);
      return;
    }
    this.anim = { from: [this.state.start, this.state.span], to: [r.start, r.span], t0: performance.now(), ms };
    this.tick(this.anim.t0);
  }

  /** Advances an animation to `now` (the frame loop calls it; repeated calls in one frame are harmless). */
  tick(now: number): boolean {
    const a = this.anim;
    if (!a) return false;
    const k = Math.min(1, Math.max(0, (now - a.t0) / a.ms));
    const e = 1 - (1 - k) * (1 - k);
    // Zoom in log space so the motion looks even.
    const span = Math.exp(Math.log(a.from[1]) + (Math.log(a.to[1]) - Math.log(a.from[1])) * e);
    this.set(clampRange(a.from[0] + (a.to[0] - a.from[0]) * e, span, this.state.duration));
    if (k >= 1) this.anim = null;
    return true;
  }

  get animating(): boolean {
    return this.anim !== null;
  }

  zoomBy(factor: number, at = this.state.playhead, reducedMotion = false): void {
    const r = zoomAround(this.state, factor, at);
    this.animateTo(r.start, r.span, 160, reducedMotion);
  }

  /** Puts the playhead at t; when it leaves the visible range, the range follows (30 % from the left). */
  seek(t: number, reducedMotion = false): void {
    const p = Math.min(this.state.duration, Math.max(0, t));
    this.set({ playhead: p });
    const { start, span } = this.state;
    if (p < start || p > start + span) this.animateTo(p - span * 0.3, span, 160, reducedMotion);
  }

  /** The playhead during playback: no follow here (the view decides how to follow). */
  setPlayhead(t: number): void {
    this.set({ playhead: Math.min(this.state.duration, Math.max(0, t)) });
  }

  setLoop(a: number | null, b?: number): void {
    if (a === null || b === undefined || !(b > a)) {
      this.set({ loop: null });
      return;
    }
    this.set({ loop: [Math.max(0, a), Math.min(this.state.duration, b)] });
  }
}

/**
 * An axis for one or more views (uncontrolled: the hook owns it). Pass it to several AudioViews to share zoom,
 * scroll, playhead and loop. The duration follows the argument.
 */
export function useAudioAxis(duration = 0, initial?: Partial<AxisState>): AudioAxis {
  const [axis] = useState(() => new AudioAxis(duration, initial));
  useEffect(() => {
    if (duration > 0) axis.setDuration(duration);
  }, [axis, duration]);
  return axis;
}

/** The axis state as React state (for labels and controls outside the view). */
export function useAxisState(axis: AudioAxis): AxisState {
  return useSyncExternalStore(axis.subscribe, () => axis.get());
}
