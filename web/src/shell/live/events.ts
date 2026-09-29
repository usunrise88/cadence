import type { CadenceEvent } from "@/api/gen/types.gen";
import { coverPatterns, topicMatches } from "./topics";

// One multiplexed server-sent-events stream per app (docs/spec/10-ui-shell.md "Performance"). Panels subscribe to
// topics only while visible; the stream reconnects with the union of live topics and resumes from the last seq,
// so a topic change never loses events. Delivery is coalesced to one batch per animation frame.

export type EventHandler = (events: CadenceEvent[]) => void;
export type ConnectionState = "idle" | "connecting" | "open" | "error";

type Subscription = { id: number; patterns: string[]; handler: EventHandler; owner: string };

type EventSourceLike = {
  onopen: ((this: EventSource, ev: Event) => unknown) | null;
  onerror: ((this: EventSource, ev: Event) => unknown) | null;
  onmessage: ((this: EventSource, ev: MessageEvent) => unknown) | null;
  close(): void;
  readyState: number;
};

export type EventStreamOptions = {
  url?: string;
  /** Injectable for tests. */
  createSource?: (url: string) => EventSourceLike;
  schedule?: (fn: () => void) => void;
  reconnectDelayMs?: number;
};

export class EventStream {
  private subs = new Map<number, Subscription>();
  private nextId = 1;
  private source: EventSourceLike | null = null;
  private connectedTopics = "";
  private project: string | undefined;
  private lastSeq = 0;
  private pending: CadenceEvent[] = [];
  private flushScheduled = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private state: ConnectionState = "idle";
  private stateListeners = new Set<(s: ConnectionState) => void>();
  private readonly url: string;
  private readonly createSource: (url: string) => EventSourceLike;
  private readonly schedule: (fn: () => void) => void;
  private readonly reconnectDelayMs: number;

  constructor(opts: EventStreamOptions = {}) {
    this.url = opts.url ?? "/api/events";
    this.createSource = opts.createSource ?? ((u) => new EventSource(u));
    this.schedule = opts.schedule ?? ((fn) => requestAnimationFrame(() => fn()));
    this.reconnectDelayMs = opts.reconnectDelayMs ?? 30;
  }

  /** Work events are filtered to this project; registry events always pass. */
  setProject(project: string | undefined): void {
    if (project === this.project) return;
    this.project = project;
    this.reconnectSoon(true);
  }

  subscribe(patterns: string[], handler: EventHandler, owner = "shell"): () => void {
    const id = this.nextId++;
    this.subs.set(id, { id, patterns, handler, owner });
    this.reconnectSoon();
    return () => {
      if (this.subs.delete(id)) this.reconnectSoon();
    };
  }

  /** Active subscriptions (for the S4 spike and the status bar). */
  activeSubscriptions(owner?: string): number {
    if (!owner) return this.subs.size;
    return [...this.subs.values()].filter((s) => s.owner === owner).length;
  }

  subscriptionOwners(): string[] {
    return [...this.subs.values()].map((s) => s.owner);
  }

  get connectionState(): ConnectionState {
    return this.state;
  }

  get resumeSeq(): number {
    return this.lastSeq;
  }

  onState(fn: (s: ConnectionState) => void): () => void {
    this.stateListeners.add(fn);
    return () => this.stateListeners.delete(fn);
  }

  close(): void {
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.source?.close();
    this.source = null;
    this.connectedTopics = "";
    this.setState("idle");
  }

  private setState(s: ConnectionState): void {
    if (s === this.state) return;
    this.state = s;
    for (const fn of this.stateListeners) fn(s);
  }

  private topicsParam(): string {
    const all: string[] = [];
    for (const s of this.subs.values()) all.push(...s.patterns);
    return coverPatterns(all).join(",");
  }

  private reconnectSoon(force = false): void {
    if (this.reconnectTimer) return;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.sync(force);
    }, this.reconnectDelayMs);
  }

  private sync(force: boolean): void {
    const topics = this.topicsParam();
    if (!force && topics === this.connectedTopics && this.source) return;
    this.source?.close();
    this.source = null;
    this.connectedTopics = topics;
    if (!topics) {
      this.setState("idle");
      return;
    }
    const q = new URLSearchParams({ topics });
    if (this.project) q.set("project", this.project);
    if (this.lastSeq > 0) q.set("after", String(this.lastSeq));
    this.setState("connecting");
    const src = this.createSource(`${this.url}?${q.toString()}`);
    src.onopen = () => this.setState("open");
    src.onerror = () => {
      // EventSource reconnects by itself and sends Last-Event-ID; we only reflect the state.
      this.setState(src.readyState === 2 ? "error" : "connecting");
      if (src.readyState === 2 && this.source === src) {
        this.source = null;
        this.connectedTopics = "";
        setTimeout(() => this.reconnectSoon(true), 2000);
      }
    };
    // Frames carry no event name (the type is inside the JSON), so every event reaches onmessage.
    src.onmessage = (ev) => this.receive(ev);
    this.source = src;
  }

  private receive(ev: MessageEvent): void {
    let e: CadenceEvent;
    try {
      e = JSON.parse(String(ev.data)) as CadenceEvent;
    } catch {
      return;
    }
    if (typeof e.seq !== "number" || e.seq <= this.lastSeq) return; // duplicate after a resume
    this.lastSeq = e.seq;
    this.pending.push(e);
    if (!this.flushScheduled) {
      this.flushScheduled = true;
      this.schedule(() => this.flush());
    }
  }

  /** Delivers the frame's events: each subscription gets one batch of the events matching its patterns. */
  flush(): void {
    this.flushScheduled = false;
    const batch = this.pending;
    this.pending = [];
    if (batch.length === 0) return;
    for (const s of [...this.subs.values()]) {
      const mine = batch.filter((e) => s.patterns.some((p) => topicMatches(p, e.topic)));
      if (mine.length > 0) s.handler(mine);
    }
  }

  /** Test hook: feed an event as if it came from the server. */
  inject(e: CadenceEvent): void {
    this.receive(new MessageEvent("message", { data: JSON.stringify(e) }));
  }
}
