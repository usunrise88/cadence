// One frame loop per window: hooks run first (axis animation, playback follow, measurement drivers), then every dirty
// view in that window renders. Views in a popout use the popout's requestAnimationFrame.

export interface Frameable {
  frame(now: number): void;
}

export class WindowScheduler {
  private views = new Set<Frameable>();
  private hooks = new Set<(now: number) => void>();
  private raf = 0;
  lastWorkMs = 0;
  constructor(readonly win: Window) {}
  add(v: Frameable) {
    this.views.add(v);
    this.kick();
    return () => void this.views.delete(v);
  }
  hook(fn: (now: number) => void) {
    this.hooks.add(fn);
    this.kick();
    return () => void this.hooks.delete(fn);
  }
  private kick() {
    if (this.raf) return;
    const tick = (now: number) => {
      const t0 = performance.now();
      for (const h of [...this.hooks]) h(now);
      for (const v of this.views) v.frame(now);
      this.lastWorkMs = performance.now() - t0;
      this.raf = this.win.requestAnimationFrame(tick);
    };
    this.raf = this.win.requestAnimationFrame(tick);
  }
}

const schedulers = new WeakMap<Window, WindowScheduler>();
export function schedulerFor(win: Window): WindowScheduler {
  let s = schedulers.get(win);
  if (!s) {
    s = new WindowScheduler(win);
    schedulers.set(win, s);
  }
  return s;
}
