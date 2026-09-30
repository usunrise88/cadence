// Live charts redraw at most 4 times per second (docs/spec/10-ui-shell.md "Throttled charts", R53).

export const REDRAW_INTERVAL_MS = 250;

export type Clock = {
  now(): number;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(id: unknown): void;
};

const realClock: Clock = {
  now: () => performance.now(),
  setTimeout: (fn, ms) => setTimeout(fn, ms),
  clearTimeout: (id) => clearTimeout(id as ReturnType<typeof setTimeout>),
};

export type Throttle = {
  /** Asks for a run: immediately when the last one is older than the interval, otherwise once at its end. */
  schedule(): void;
  /** Runs a pending call now. */
  flush(): void;
  cancel(): void;
};

/** Leading and trailing throttle: the first change draws at once, a burst draws once more at the end. */
export function createThrottle(fn: () => void, intervalMs = REDRAW_INTERVAL_MS, clock: Clock = realClock): Throttle {
  let last = -Infinity;
  let timer: unknown = null;
  const run = () => {
    timer = null;
    last = clock.now();
    fn();
  };
  return {
    schedule() {
      if (timer != null) return;
      const wait = last + intervalMs - clock.now();
      if (wait <= 0) run();
      else timer = clock.setTimeout(run, wait);
    },
    flush() {
      if (timer != null) {
        clock.clearTimeout(timer);
        run();
      }
    },
    cancel() {
      if (timer != null) clock.clearTimeout(timer);
      timer = null;
    },
  };
}
