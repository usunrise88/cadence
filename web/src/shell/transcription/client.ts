import type { LiveClientMessage, LiveInput, LiveServerMessage, TranscriptionSession } from "@/api/gen/types.gen";

// The live channel's client (R48; contract components LiveClientMessage / LiveServerMessage): one WebSocket per
// session, opened with the session's single-use ticket. Configuration first (start), then audio in binary frames;
// finalize, keepalive, ping and end as JSON. Microphone audio is refused until the worker said started (while the job
// waits for a card or loads, the relay answers waiting); a file may be sent meanwhile (the relay holds it).

/** What the client needs of a WebSocket (the browser's, or a fake in tests). */
export type SocketLike = {
  binaryType: string;
  readonly readyState: number;
  readonly bufferedAmount: number;
  send(data: string | ArrayBuffer): void;
  close(code?: number, reason?: string): void;
  onopen: ((ev: Event) => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onclose: ((ev: CloseEvent) => void) | null;
  onerror: ((ev: Event) => void) | null;
};

export type SocketFactory = (url: string) => SocketLike;

export type LiveHandlers = {
  onMessage: (m: LiveServerMessage, at: number) => void;
  onClose: (code: number, reason: string) => void;
};

export type LiveClientOptions = {
  socket?: SocketFactory;
  now?: () => number;
  location?: { protocol: string; host: string };
  /** keepalive interval (ms); 0 turns it off. */
  keepaliveMs?: number;
  setTimer?: (fn: () => void, ms: number) => unknown;
  clearTimer?: (h: unknown) => void;
  /** Wait (ms) between file chunks while the socket's send buffer is above highWater bytes. */
  sleep?: (ms: number) => Promise<void>;
  highWater?: number;
};

const OPEN = 1;

/** Close codes of the live socket and what they mean to a person. */
export const CLOSE_MEANING: Readonly<Record<number, string>> = {
  1000: "The session ended",
  1001: "The control plane is stopping",
  1006: "The connection dropped",
  1013: "The worker fell behind (backpressure); start a new session",
  4001: "Closed after a long silence (no audio or keepalive)",
  4002: "The session reached its time cap",
  4003: "The worker running the session was lost",
  4004: "The session's job could not start",
};

export function closeMeaning(code: number, reason = ""): string {
  const base = CLOSE_MEANING[code] ?? `Closed (${code})`;
  return reason && !base.includes(reason) ? `${base}: ${reason}` : base;
}

/** The socket URL of a session's streamUrl (relative to the server: ws or wss on the page's host). */
export function socketUrl(streamUrl: string, loc: { protocol: string; host: string }): string {
  if (/^wss?:\/\//.test(streamUrl)) return streamUrl;
  if (/^https?:\/\//.test(streamUrl)) return streamUrl.replace(/^http/, "ws");
  return `${loc.protocol === "https:" ? "wss:" : "ws:"}//${loc.host}${streamUrl.startsWith("/") ? "" : "/"}${streamUrl}`;
}

const SERVER_TYPES = new Set(["waiting", "started", "partial", "final", "stats", "pong", "error", "summary"]);

/** A server text frame as a message, or undefined when it is not one. */
export function parseServerMessage(data: unknown): LiveServerMessage | undefined {
  if (typeof data !== "string") return undefined;
  try {
    const v: unknown = JSON.parse(data);
    if (v && typeof v === "object" && SERVER_TYPES.has(String((v as { type?: unknown }).type))) return v as LiveServerMessage;
  } catch {
    return undefined;
  }
  return undefined;
}

export class LiveClient {
  readonly session: TranscriptionSession;
  private ws: SocketLike | null = null;
  private h: LiveHandlers;
  private o: Required<Pick<LiveClientOptions, "now" | "keepaliveMs" | "highWater">> & LiveClientOptions;
  private timer: unknown = null;
  private startedFlag = false;
  private startSent = false;
  private ended = false;

  constructor(session: TranscriptionSession, handlers: LiveHandlers, opts: LiveClientOptions = {}) {
    this.session = session;
    this.h = handlers;
    this.o = { now: () => performance.now(), keepaliveMs: 20_000, highWater: 4 << 20, ...opts };
  }

  get started(): boolean {
    return this.startedFlag;
  }

  get open(): boolean {
    return this.ws?.readyState === OPEN;
  }

  /** Opens the socket; resolves once it is open (the ticket is spent then), rejects when it fails first. */
  connect(): Promise<void> {
    if (!this.session.streamUrl) return Promise.reject(new Error("The session has no stream URL (a dry run?)"));
    const loc = this.o.location ?? window.location;
    const url = socketUrl(this.session.streamUrl, loc);
    const make: SocketFactory = this.o.socket ?? ((u) => new WebSocket(u) as unknown as SocketLike);
    const ws = make(url);
    ws.binaryType = "arraybuffer";
    this.ws = ws;
    return new Promise((resolve, reject) => {
      let opened = false;
      ws.onopen = () => {
        opened = true;
        this.armKeepalive();
        resolve();
      };
      ws.onerror = () => {
        if (!opened) reject(new Error("The live socket could not open (ticket used or expired, or the origin is not allowed)"));
      };
      ws.onmessage = (ev) => {
        const m = parseServerMessage(ev.data);
        if (!m) return;
        if (m.type === "started") this.startedFlag = true;
        this.h.onMessage(m, this.o.now());
      };
      ws.onclose = (ev) => {
        this.disarm();
        if (!opened) reject(new Error(closeMeaning(ev.code, ev.reason)));
        this.h.onClose(ev.code, ev.reason);
      };
    });
  }

  private send(m: LiveClientMessage): void {
    if (this.ws?.readyState === OPEN) this.ws.send(JSON.stringify(m));
  }

  /** The first message: what the audio is. */
  start(input: LiveInput): void {
    if (this.startSent) throw new Error("start was already sent");
    this.startSent = true;
    this.send({ type: "start", input });
  }

  /**
   * One microphone frame (PCM16 little-endian at the capture rate). Refused (false) before started, so nothing is
   * captured into a queue while the job waits for a card or loads its models.
   */
  sendAudio(pcm: ArrayBuffer): boolean {
    if (!this.startedFlag || !this.startSent || this.ended || this.ws?.readyState !== OPEN) return false;
    this.ws.send(pcm);
    return true;
  }

  /** A file's bytes in chunks of at most maxBytes, then fileEnd; waits while the socket's buffer is full. */
  async sendFile(data: ArrayBuffer, maxBytes: number, onProgress?: (sent: number, total: number) => void): Promise<void> {
    if (!this.startSent) throw new Error("send start before the file");
    const sleep = this.o.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
    const size = Math.max(1, maxBytes);
    for (let off = 0; off < data.byteLength; off += size) {
      while (this.ws && this.ws.readyState === OPEN && this.ws.bufferedAmount > this.o.highWater) await sleep(20);
      if (this.ws?.readyState !== OPEN) throw new Error("The live socket closed while the file was being sent");
      this.ws.send(data.slice(off, Math.min(data.byteLength, off + size)));
      onProgress?.(Math.min(data.byteLength, off + size), data.byteLength);
    }
    this.send({ type: "fileEnd" });
  }

  finalize(): void {
    this.send({ type: "finalize" });
  }

  keepalive(): void {
    this.send({ type: "keepalive", t: this.o.now() });
  }

  /** The relay answers ping itself: its pong measures the control-plane hop alone. */
  ping(): void {
    this.send({ type: "ping", t: this.o.now() });
  }

  /** Flush every target, then summary and close 1000 from the server. */
  end(): void {
    if (this.ended) return;
    this.ended = true;
    this.send({ type: "end" });
  }

  /** Drops the socket without end (the server closes the session). */
  close(): void {
    this.disarm();
    this.ws?.close(1000, "closed by the page");
  }

  private armKeepalive(): void {
    if (!this.o.keepaliveMs) return;
    const set = this.o.setTimer ?? ((fn: () => void, ms: number) => setInterval(fn, ms));
    this.timer = set(() => {
      this.keepalive();
      this.ping();
    }, this.o.keepaliveMs);
  }

  private disarm(): void {
    if (this.timer === null) return;
    const clear = this.o.clearTimer ?? ((h: unknown) => clearInterval(h as ReturnType<typeof setInterval>));
    clear(this.timer);
    this.timer = null;
  }
}
