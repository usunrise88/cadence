import { describe, expect, it } from "vitest";
import type { TranscriptionSession } from "@/api/gen/types.gen";
import { closeMeaning, LiveClient, parseServerMessage, socketUrl, type SocketLike } from "./client";

class FakeSocket implements SocketLike {
  binaryType = "blob";
  readyState = 0;
  bufferedAmount = 0;
  sent: (string | ArrayBuffer)[] = [];
  closed: { code?: number; reason?: string } | null = null;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;
  readonly url: string;
  constructor(url: string) {
    this.url = url;
  }
  send(data: string | ArrayBuffer) {
    this.sent.push(data);
  }
  close(code?: number, reason?: string) {
    this.closed = { code, reason };
  }
  open() {
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }
  receive(m: unknown) {
    this.onmessage?.({ data: JSON.stringify(m) } as MessageEvent);
  }
  serverClose(code: number, reason = "") {
    this.readyState = 3;
    this.onclose?.({ code, reason } as CloseEvent);
  }
  json(): { type: string }[] {
    return this.sent.filter((s): s is string => typeof s === "string").map((s) => JSON.parse(s) as { type: string });
  }
}

const session = {
  id: "trs_1",
  streamUrl: "/api/transcriptions/trs_1/stream?ticket=abc",
  limits: { maxMessageBytes: 4 },
} as unknown as TranscriptionSession;

function setup() {
  let sock: FakeSocket | undefined;
  const got: { type: string }[] = [];
  const closes: number[] = [];
  let t = 0;
  const c = new LiveClient(session, { onMessage: (m) => got.push(m), onClose: (code) => closes.push(code) }, {
    socket: (u) => (sock = new FakeSocket(u)),
    location: { protocol: "https:", host: "cadence.example" },
    now: () => ++t,
    keepaliveMs: 0,
    sleep: () => Promise.resolve(),
  });
  return { c, sock: () => sock!, got, closes };
}

describe("socketUrl", () => {
  it("turns a relative stream URL into ws or wss on the page's host", () => {
    expect(socketUrl("/api/transcriptions/x/stream?ticket=t", { protocol: "https:", host: "h:8443" })).toBe("wss://h:8443/api/transcriptions/x/stream?ticket=t");
    expect(socketUrl("/api/x", { protocol: "http:", host: "localhost:5173" })).toBe("ws://localhost:5173/api/x");
    expect(socketUrl("http://a/b", { protocol: "https:", host: "x" })).toBe("ws://a/b");
  });
});

describe("parseServerMessage", () => {
  it("accepts the contract's types only", () => {
    expect(parseServerMessage('{"type":"partial","target":"A","segment":0,"seq":1,"text":"x","audioEnd":0.2}')?.type).toBe("partial");
    expect(parseServerMessage('{"type":"bogus"}')).toBeUndefined();
    expect(parseServerMessage("not json")).toBeUndefined();
    expect(parseServerMessage(new ArrayBuffer(2))).toBeUndefined();
  });
});

describe("LiveClient", () => {
  it("opens the socket, sends start first and gates microphone audio until started", async () => {
    const { c, sock, got } = setup();
    const p = c.connect();
    expect(sock().url).toBe("wss://cadence.example/api/transcriptions/trs_1/stream?ticket=abc");
    expect(sock().binaryType).toBe("arraybuffer");
    sock().open();
    await p;
    c.start({ kind: "microphone", sampleRate: 48000, frameMs: 20 });
    expect(() => c.start({ kind: "microphone" })).toThrow();
    expect(c.sendAudio(new ArrayBuffer(8))).toBe(false); // the job is still queued
    sock().receive({ type: "waiting", state: "queued", position: 2, reason: "no card has 6000 MB free" });
    expect(c.sendAudio(new ArrayBuffer(8))).toBe(false);
    sock().receive({ type: "started", targets: [], resampler: "polyphase" });
    expect(c.started).toBe(true);
    expect(c.sendAudio(new ArrayBuffer(8))).toBe(true);
    c.finalize();
    c.end();
    c.end(); // once
    expect(c.sendAudio(new ArrayBuffer(8))).toBe(false); // nothing after end
    expect(sock().json().map((m) => m.type)).toEqual(["start", "finalize", "end"]);
    expect(sock().sent.filter((s) => typeof s !== "string")).toHaveLength(1);
    expect(got.map((m) => m.type)).toEqual(["waiting", "started"]);
  });

  it("sends a file in chunks no larger than the limit, then fileEnd", async () => {
    const { c, sock } = setup();
    const p = c.connect();
    sock().open();
    await p;
    c.start({ kind: "file", fileName: "a.wav", fileBytes: 10 });
    const progress: number[] = [];
    await c.sendFile(new Uint8Array([0, 1, 2, 3, 4, 5, 6, 7, 8, 9]).buffer, 4, (n) => progress.push(n));
    const bins = sock().sent.filter((s): s is ArrayBuffer => typeof s !== "string");
    expect(bins.map((b) => b.byteLength)).toEqual([4, 4, 2]);
    expect(Array.from(new Uint8Array(bins[2]!))).toEqual([8, 9]);
    expect(progress).toEqual([4, 8, 10]);
    expect(sock().json().map((m) => m.type)).toEqual(["start", "fileEnd"]);
  });

  it("stamps ping and keepalive with the page clock and reports the close code", async () => {
    const { c, sock, closes } = setup();
    const p = c.connect();
    sock().open();
    await p;
    c.ping();
    c.keepalive();
    const [ping, keep] = sock().json() as { type: string; t: number }[];
    expect(ping!.type).toBe("ping");
    expect(keep!.type).toBe("keepalive");
    expect(keep!.t).toBeGreaterThan(ping!.t);
    sock().serverClose(4001, "idle");
    expect(closes).toEqual([4001]);
    expect(closeMeaning(4001)).toMatch(/silence/);
    expect(closeMeaning(4002)).toMatch(/cap/);
    expect(closeMeaning(4999, "x")).toBe("Closed (4999): x");
  });

  it("rejects when the socket closes before it opened (a spent ticket)", async () => {
    const { c, sock } = setup();
    const p = c.connect();
    sock().serverClose(1006);
    await expect(p).rejects.toThrow(/dropped/);
  });

  it("arms keepalive and ping on a timer", async () => {
    let fire: (() => void) | undefined;
    let sock: FakeSocket | undefined;
    const c = new LiveClient(session, { onMessage: () => undefined, onClose: () => undefined }, {
      socket: (u) => (sock = new FakeSocket(u)),
      location: { protocol: "http:", host: "h" },
      keepaliveMs: 20_000,
      setTimer: (fn) => {
        fire = fn;
        return 1;
      },
      clearTimer: () => (fire = undefined),
    });
    const p = c.connect();
    sock!.open();
    await p;
    fire!();
    expect(sock!.json().map((m) => m.type)).toEqual(["keepalive", "ping"]);
    c.close();
    expect(fire).toBeUndefined();
    expect(sock!.closed?.code).toBe(1000);
  });
});
