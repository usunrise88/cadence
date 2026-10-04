import type { LiveError, LiveFinal, LiveServerMessage, LiveStarted, LiveSummary, LiveWaiting, LiveWord } from "@/api/gen/types.gen";

// The state of a live session on the page (R48, R50): one lane per target, where a partial replaces the segment's
// previous partial and a final is appended and never changes; the latency figures come from the session's own
// events and the page's clock (R54) and go with it — nothing here is stored.

export type LaneTarget = "A" | "B" | "C";

export type FinalSegment = {
  segment: number;
  seq: number;
  text: string;
  words: LiveWord[];
  endpoint: LiveFinal["endpoint"];
  audioEnd: number;
  /** false: continues the previous final's last word (join without a space). */
  space: boolean;
  receivedAt: number;
  /** Time to final (ms): receipt minus when the audio up to audioEnd was sent; absent without a send log. */
  latencyMs?: number;
};

export type Lane = {
  target: LaneTarget;
  finals: FinalSegment[];
  /** space false: the partial continues the last final's word (join without a space). */
  partial?: { segment: number; seq: number; text: string; audioEnd: number; space: boolean };
  /** The highest seq seen (partials and finals share it). */
  seq: number;
};

export type LivePhase = "idle" | "connecting" | "queued" | "loading" | "live" | "ended";

export type LiveState = {
  phase: LivePhase;
  waiting?: LiveWaiting;
  started?: LiveStarted;
  lanes: Partial<Record<LaneTarget, Lane>>;
  /** Cumulative seconds of audio sent and the wall time (ms) the send completed, in order. */
  sent: { audioS: number; at: number }[];
  finalizeAt?: number;
  /** Time to final per final (ms), every target. */
  latencies: number[];
  /** finalize → final (ms), finals that a finalize closed. */
  finalizeLatencies: number[];
  rtf?: number;
  audioS?: number;
  relayUpP50Us?: number;
  relayDownP50Us?: number;
  rtt: { relay?: number; worker?: number };
  summary?: LiveSummary;
  errors: LiveError[];
  close?: { code: number; reason: string };
};

export type LiveAction =
  | { type: "reset" }
  | { type: "connecting" }
  /** sentTime: when the audio up to a final's audioEnd was sent (the page's own send log); else the state's log. */
  | { type: "message"; msg: LiveServerMessage; at: number; sentTime?: number }
  | { type: "sent"; audioS: number; at: number }
  | { type: "finalize"; at: number }
  | { type: "closed"; code: number; reason: string };

export const initialLive: LiveState = { phase: "idle", lanes: {}, sent: [], latencies: [], finalizeLatencies: [], rtt: {}, errors: [] };

function lane(s: LiveState, t: LaneTarget): Lane {
  return s.lanes[t] ?? { target: t, finals: [], seq: 0 };
}

/** The wall time at which the audio up to audioS had been sent (the first send that covered it), or undefined. */
export function sentAt(sent: LiveState["sent"], audioS: number): number | undefined {
  if (sent.length === 0) return undefined;
  let lo = 0;
  let hi = sent.length;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (sent[mid]!.audioS + 1e-6 < audioS) lo = mid + 1;
    else hi = mid;
  }
  // Past the last send (finalize pads the right context): the last send is when the audio was all there.
  return sent[Math.min(lo, sent.length - 1)]!.at;
}

export function liveReducer(s: LiveState, a: LiveAction): LiveState {
  switch (a.type) {
    case "reset":
      return initialLive;
    case "connecting":
      return { ...initialLive, phase: "connecting" };
    case "sent":
      return { ...s, sent: [...s.sent, { audioS: a.audioS, at: a.at }] };
    case "finalize":
      return { ...s, finalizeAt: a.at };
    case "closed":
      return { ...s, phase: "ended", close: { code: a.code, reason: a.reason } };
    case "message":
      return onMessage(s, a.msg, a.at, a.sentTime);
  }
}

function onMessage(s: LiveState, m: LiveServerMessage, at: number, sentTime?: number): LiveState {
  switch (m.type) {
    case "waiting":
      return { ...s, phase: m.state === "loading" ? "loading" : "queued", waiting: m };
    case "started":
      return { ...s, phase: "live", started: m, waiting: undefined };
    case "partial": {
      const l = lane(s, m.target);
      // A partial older than what the lane already shows (or than its last final) is dropped.
      if (m.seq <= l.seq) return s;
      return { ...s, lanes: { ...s.lanes, [m.target]: { ...l, seq: m.seq, partial: { segment: m.segment, seq: m.seq, text: m.text, audioEnd: m.audioEnd, space: m.space ?? true } } } };
    }
    case "final": {
      const l = lane(s, m.target);
      const sent = sentTime ?? sentAt(s.sent, m.audioEnd);
      const f: FinalSegment = {
        segment: m.segment,
        seq: m.seq,
        text: m.text,
        words: m.words,
        endpoint: m.endpoint,
        audioEnd: m.audioEnd,
        space: m.space,
        receivedAt: at,
        latencyMs: sent === undefined ? undefined : Math.max(0, at - sent),
      };
      const partial = l.partial && l.partial.segment > m.segment ? l.partial : undefined;
      const latencies = f.latencyMs === undefined || !m.text ? s.latencies : [...s.latencies, f.latencyMs];
      const finalizeLatencies =
        m.endpoint === "finalize" && s.finalizeAt !== undefined ? [...s.finalizeLatencies, Math.max(0, at - s.finalizeAt)] : s.finalizeLatencies;
      return {
        ...s,
        latencies,
        finalizeLatencies,
        lanes: { ...s.lanes, [m.target]: { ...l, seq: Math.max(l.seq, m.seq), finals: [...l.finals, f], partial } },
      };
    }
    case "stats":
      if (m.source === "worker") return { ...s, rtf: m.rtf ?? s.rtf, audioS: m.audioS ?? s.audioS };
      return { ...s, relayUpP50Us: m.upP50Us ?? s.relayUpP50Us, relayDownP50Us: m.downP50Us ?? s.relayDownP50Us };
    case "pong":
      if (m.t === undefined) return s;
      return { ...s, rtt: { ...s.rtt, [m.source]: Math.max(0, at - m.t) } };
    case "error":
      return { ...s, errors: [...s.errors, m] };
    case "summary":
      return { ...s, summary: m, rtf: m.rtf, audioS: m.audioS };
  }
  return s;
}

/** The lane's finals as one text: a final with space false continues the previous word. */
export function finalText(l: Lane | undefined): string {
  let out = "";
  for (const f of l?.finals ?? []) {
    if (!f.text) continue;
    out = out === "" ? f.text : f.space ? `${out} ${f.text}` : `${out}${f.text}`;
  }
  return out;
}

/** The lane's text including the current partial (what the person sees). */
export function laneText(l: Lane | undefined): string {
  const fin = finalText(l);
  const p = l?.partial?.text ?? "";
  if (!p) return fin;
  return fin ? (l?.partial?.space === false ? `${fin}${p}` : `${fin} ${p}`) : p;
}

/** Timed words of a lane's finals for a word track; a word split by an end of utterance is joined back. */
export function laneWords(l: Lane | undefined, offset = 0): { word: string; start: number; end: number; confidence?: number }[] {
  const out: { word: string; start: number; end: number; confidence?: number }[] = [];
  for (const f of l?.finals ?? []) {
    f.words.forEach((w, i) => {
      if (w.start === undefined || w.end === undefined) return;
      const prev = out[out.length - 1];
      if (i === 0 && !f.space && prev) {
        prev.word += w.word;
        prev.end = w.end + offset;
        if (w.confidence !== undefined) prev.confidence = Math.min(prev.confidence ?? 1, w.confidence);
        return;
      }
      out.push({ word: w.word, start: w.start + offset, end: w.end + offset, confidence: w.confidence });
    });
  }
  return out;
}

/** Percentile p (0–100) of xs, linear between closest ranks (type 7); undefined when empty. */
export function percentile(xs: readonly number[], p: number): number | undefined {
  if (xs.length === 0) return undefined;
  const s = [...xs].sort((a, b) => a - b);
  const r = (p / 100) * (s.length - 1);
  const lo = Math.floor(r);
  const hi = Math.ceil(r);
  return s[lo]! + (s[hi]! - s[lo]!) * (r - lo);
}
