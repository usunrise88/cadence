// Time for the session clocks (R5): the real one, and a fake that tests advance by hand.

export interface Timer {
  cancel(): void;
}

export interface Clock {
  now(): number;
  after(ms: number, fn: () => void): Timer;
}

export const realClock: Clock = {
  now: () => Date.now(),
  after(ms, fn) {
    const t = setTimeout(fn, ms);
    t.unref?.();
    return { cancel: () => clearTimeout(t) };
  },
};

// A clock that only moves when told to: advance() fires every timer that falls due, in order.
export class FakeClock implements Clock {
  private t = 0;
  private seq = 0;
  private readonly timers = new Map<number, { at: number; fn: () => void }>();

  now(): number {
    return this.t;
  }

  after(ms: number, fn: () => void): Timer {
    const id = ++this.seq;
    this.timers.set(id, { at: this.t + ms, fn });
    return { cancel: () => this.timers.delete(id) };
  }

  advance(ms: number): void {
    const end = this.t + ms;
    for (;;) {
      let next: [number, { at: number; fn: () => void }] | undefined;
      for (const e of this.timers) if (e[1].at <= end && (!next || e[1].at < next[1].at)) next = e;
      if (!next) break;
      this.timers.delete(next[0]);
      this.t = next[1].at;
      next[1].fn();
    }
    this.t = end;
  }

  pending(): number {
    return this.timers.size;
  }
}
