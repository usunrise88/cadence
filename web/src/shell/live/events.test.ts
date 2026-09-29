import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { CadenceEvent } from "@/api/gen/types.gen";
import { EventStream } from "./events";

class FakeSource {
  static all: FakeSource[] = [];
  onopen: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  readyState = 0;
  closed = false;
  readonly url: string;
  constructor(url: string) {
    this.url = url;
    FakeSource.all.push(this);
  }
  close() {
    this.closed = true;
    this.readyState = 2;
  }
  emit(e: Partial<CadenceEvent>) {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(e) }));
  }
}

const ev = (seq: number, topic: string): Partial<CadenceEvent> => ({
  seq,
  topic,
  type: "t",
  actor: { kind: "user", id: "usr_admin" },
  at: "2026-09-29T00:00:00Z",
});

describe("EventStream", () => {
  let frames: (() => void)[];
  let stream: EventStream;
  const open = () => FakeSource.all.filter((s) => !s.closed);
  const flushFrame = () => frames.splice(0).forEach((f) => f());

  beforeEach(() => {
    vi.useFakeTimers();
    FakeSource.all = [];
    frames = [];
    stream = new EventStream({
      createSource: (u) => new FakeSource(u) as unknown as EventSource,
      schedule: (fn) => frames.push(fn),
      reconnectDelayMs: 10,
    });
  });
  afterEach(() => vi.useRealTimers());

  it("opens one stream with the union of subscribed topics", () => {
    stream.subscribe(["run.1.*"], () => {});
    stream.subscribe(["run.1.metrics", "queue"], () => {});
    vi.advanceTimersByTime(20);
    expect(open()).toHaveLength(1);
    const url = new URL(open()[0]!.url, "http://x");
    expect(url.searchParams.get("topics")).toBe("queue,run.1.*");
  });

  it("stays idle without subscriptions and closes when the last one leaves", () => {
    vi.advanceTimersByTime(20);
    expect(open()).toHaveLength(0);
    const off = stream.subscribe(["queue"], () => {});
    vi.advanceTimersByTime(20);
    expect(open()).toHaveLength(1);
    off();
    vi.advanceTimersByTime(20);
    expect(open()).toHaveLength(0);
    expect(stream.activeSubscriptions()).toBe(0);
  });

  it("delivers one coalesced batch per frame, only matching events", () => {
    const got: number[][] = [];
    stream.subscribe(["run.1.*"], (b) => got.push(b.map((e) => e.seq)));
    vi.advanceTimersByTime(20);
    const src = open()[0]!;
    src.emit(ev(1, "run.1.metrics"));
    src.emit(ev(2, "run.2.metrics"));
    src.emit(ev(3, "run.1.status"));
    expect(got).toEqual([]);
    flushFrame();
    expect(got).toEqual([[1, 3]]);
  });

  it("resumes after the last seq when topics change, and drops duplicates", () => {
    const seen: number[] = [];
    stream.subscribe(["a"], (b) => seen.push(...b.map((e) => e.seq)));
    vi.advanceTimersByTime(20);
    open()[0]!.emit(ev(5, "a"));
    flushFrame();
    stream.subscribe(["b"], () => {});
    vi.advanceTimersByTime(20);
    const second = open();
    expect(second).toHaveLength(1);
    const url = new URL(second[0]!.url, "http://x");
    expect(url.searchParams.get("after")).toBe("5");
    second[0]!.emit(ev(5, "a")); // replayed duplicate
    second[0]!.emit(ev(6, "a"));
    flushFrame();
    expect(seen).toEqual([5, 6]);
  });

  it("filters work events by project", () => {
    stream.setProject("demo");
    stream.subscribe(["*"], () => {});
    vi.advanceTimersByTime(20);
    expect(new URL(open()[0]!.url, "http://x").searchParams.get("project")).toBe("demo");
  });

  it("counts subscriptions per owner", () => {
    stream.subscribe(["a"], () => {}, "panel:library");
    stream.subscribe(["b"], () => {}, "chrome");
    expect(stream.activeSubscriptions("panel:library")).toBe(1);
    expect(stream.activeSubscriptions()).toBe(2);
  });
});
