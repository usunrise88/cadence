// One frame loop per window (S5): every audio view of a window renders in that window's requestAnimationFrame —
// views in a popout use the popout's. The loop stops when no view is left, so an idle shell costs nothing.

export interface Frameable {
  /** Draws what changed; returns true while it wants more frames (animation, playback, a retry). */
  frame(now: number): boolean;
}

export class WindowScheduler {
  private views = new Set<Frameable>();
  private raf = 0;
  private win: Window;
  /** Time spent inside the last frame's callbacks (ms), for measurements. */
  lastWorkMs = 0;

  constructor(win: Window) {
    this.win = win;
  }

  add(v: Frameable): () => void {
    this.views.add(v);
    this.kick();
    return () => void this.views.delete(v);
  }

  /** Asks for a frame (a view became dirty). */
  kick(): void {
    if (this.raf || this.views.size === 0) return;
    this.raf = this.win.requestAnimationFrame(this.tick);
  }

  private tick = (now: number): void => {
    this.raf = 0;
    const t0 = performance.now();
    let again = false;
    for (const v of this.views) {
      try {
        if (v.frame(now)) again = true;
      } catch (e) {
        // One broken view must not stop the others in this window.
        console.error("audio view frame failed", e);
      }
    }
    this.lastWorkMs = performance.now() - t0;
    if (again) this.kick();
  };
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
