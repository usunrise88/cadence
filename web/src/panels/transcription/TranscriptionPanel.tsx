import { useCallback, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { Microphone, Pause, Play, Square, Upload } from "iconoir-react";
import type { LiveInput, LiveServerMessage, TranscriptionInput, TranscriptionNew, TranscriptionSession } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { AudioView, LocalAudioView, decodeAudioFile, parseAudioItem, spanFragment, useAudioItem, type AudioEngine, type WordTrackData } from "@/shell/audio";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import {
  audioInputs,
  captureUnavailable,
  closeMeaning,
  errorMessage,
  initialLive,
  laneWords,
  liveReducer,
  LiveClient,
  pcm16ToFloat,
  runCommand,
  sentAt,
  startCapture,
  useProject,
  type Capture,
  type LaneTarget,
  type PanelProps,
  type PcmFrame,
} from "@/shell/panel";
import { cn } from "@/lib/utils";
import { Lanes } from "./Lanes";
import { emptyTarget, targetBody, TargetsForm, type TargetForm } from "./Targets";

// Transcription (docs/spec/11-ui-panels.md "Panel catalogue"; R47–R50, 06 "Media"): a manual test of one to three
// models on the microphone, a file or an utterance span. transcriptions.new answers the session and its single-use
// ticket; the page opens the live socket at once, shows the queue place while the job waits for a card, streams
// audio once the worker says started, and shows partial and final words per lane. After the summary the audio view
// shows every target's words. Nothing is stored: the audio and the words live in this page until it is reset.

type InputKind = TranscriptionInput["kind"];
type Codec = "ulaw" | "alaw" | "none";
type ViewSource =
  | { kind: "local"; samples: Float32Array; sampleRate: number; mediaUrl?: string }
  | { kind: "span"; utterance: string; start?: number; end?: number };

/** The capture frame (transcriptions.frame_ms): 20 ms, so a chunk never waits for a long frame (spike A5). */
const FRAME_MS = 20;

export function TranscriptionEmpty() {
  return <EmptyState step="review" title="No project open" hint="Open a project: a transcription test runs its checkpoints, model versions or base models." />;
}

export function TranscriptionPanel(_props: PanelProps) {
  const project = useProject();
  if (!project) return <TranscriptionEmpty />;
  return <Transcription key={project} project={project} />;
}

function LevelMeter({ peak, clipped }: { peak: number; clipped: boolean }) {
  const pct = Math.min(100, Math.round(Math.sqrt(peak) * 100));
  return (
    <span className="flex items-center gap-1 text-[11px] text-muted-foreground" data-slot="transcription-level">
      <span className="relative h-2 w-24 overflow-hidden rounded-sm bg-muted" role="meter" aria-label="Input level" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
        <span className="absolute inset-y-0 left-0 bg-primary" style={{ width: `${pct}%` }} />
      </span>
      <span className={cn("rounded-sm px-1", clipped ? "bg-destructive/20 text-destructive" : "opacity-40")} title="The input reached full scale (clipping): move back or lower the gain">
        CLIP
      </span>
    </span>
  );
}

function Transcription({ project }: { project: string }) {
  const audioItem = useAudioItem();
  const [kind, setKind] = useState<InputKind>("microphone");
  const [devices, setDevices] = useState<{ deviceId: string; label: string }[]>([]);
  const [deviceId, setDeviceId] = useState("");
  const [raw, setRaw] = useState(true);
  const [file, setFile] = useState<File | null>(null);
  const [spanText, setSpanText] = useState("");
  const [targets, setTargets] = useState<TargetForm[]>([emptyTarget()]);
  const [telephony, setTelephony] = useState(false);
  const [codec, setCodec] = useState<Codec>("ulaw");
  const [pace, setPace] = useState<"realtime" | "fast">("realtime");
  const [blind, setBlind] = useState(false);
  const [reference, setReference] = useState("");
  const [session, setSession] = useState<TranscriptionSession | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [level, setLevel] = useState({ peak: 0, clipped: false });
  // A microphone check before going live: the level meter runs, nothing is sent.
  const [checking, setChecking] = useState(false);
  const [view, setView] = useState<ViewSource | null>(null);
  const [state, dispatch] = useReducer(liveReducer, initialLive);
  const [playing, setPlaying] = useState(false);
  const [engine, setEngine] = useState<AudioEngine | null>(null);

  const client = useRef<LiveClient | null>(null);
  const capture = useRef<Capture | null>(null);
  const sent = useRef<{ audioS: number; at: number }[]>([]);
  const samples = useRef(0);
  const recorded = useRef<Int16Array[]>([]);
  const captureRate = useRef(0);
  const fileUrl = useRef<string | null>(null);
  const lastLevel = useRef(0);
  const clipUntil = useRef(0);

  const micWhy = kind === "microphone" ? captureUnavailable() : undefined;
  const span = useMemo(() => parseAudioItem(spanText || undefined), [spanText]);

  useEffect(() => {
    void audioInputs().then(setDevices).catch(() => setDevices([]));
  }, []);
  useEffect(() => {
    if (!spanText && audioItem) setSpanText(`${audioItem.utterance}${audioItem.start !== undefined ? `#${spanFragment(audioItem.start, audioItem.end)}` : ""}`);
  }, [audioItem, spanText]);

  const stopCapture = useCallback(() => {
    capture.current?.stop();
    capture.current = null;
    setChecking(false);
    setLevel({ peak: 0, clipped: false });
  }, []);
  const teardown = useCallback(() => {
    stopCapture();
    client.current?.close();
    client.current = null;
  }, [stopCapture]);
  useEffect(
    () => () => {
      teardown();
      if (fileUrl.current) URL.revokeObjectURL(fileUrl.current);
    },
    [teardown],
  );

  const phase = state.phase;
  const running = phase === "connecting" || phase === "queued" || phase === "loading" || phase === "live";

  /** The recording the page holds (microphone) or the chosen file, for the audio view after the session. */
  const showView = useCallback(async () => {
    if (kind === "span" && span) {
      setView({ kind: "span", utterance: span.utterance, start: span.start, end: span.end });
      return;
    }
    if (kind === "microphone" && recorded.current.length && captureRate.current) {
      const total = recorded.current.reduce((n, a) => n + a.length, 0);
      const all = new Int16Array(total);
      let off = 0;
      for (const a of recorded.current) {
        all.set(a, off);
        off += a.length;
      }
      setView({ kind: "local", samples: pcm16ToFloat(all.buffer), sampleRate: captureRate.current });
      return;
    }
    if (kind === "file" && file) {
      try {
        const decoded = await decodeAudioFile(await file.arrayBuffer());
        if (fileUrl.current) URL.revokeObjectURL(fileUrl.current);
        fileUrl.current = URL.createObjectURL(file);
        setView({ kind: "local", samples: decoded.samples, sampleRate: decoded.sampleRate, mediaUrl: fileUrl.current });
      } catch (e) {
        setError(`The page cannot show this file's audio (${errorMessage(e)}); the words above are complete.`);
      }
    }
  }, [kind, span, file]);

  const onMessage = useCallback(
    (m: LiveServerMessage, at: number) => {
      const sentTime = m.type === "final" && kind === "microphone" ? sentAt(sent.current, m.audioEnd) : undefined;
      dispatch({ type: "message", msg: m, at, sentTime });
      if (m.type === "summary") void showView();
    },
    [kind, showView],
  );

  const onFrame = useCallback((f: PcmFrame) => {
    const now = performance.now();
    if (f.clipped > 0) clipUntil.current = now + 1000;
    if (now - lastLevel.current > 100) {
      lastLevel.current = now;
      setLevel({ peak: f.peak, clipped: now < clipUntil.current });
    }
    const c = client.current;
    // Before started nothing is sent: the meter shows the level while the job waits for a card or loads.
    if (!c || !c.sendAudio(f.pcm)) return;
    samples.current += f.pcm.byteLength / 2;
    sent.current.push({ audioS: samples.current / Math.max(1, captureRate.current), at: performance.now() });
    recorded.current.push(new Int16Array(f.pcm.slice(0)));
  }, []);

  const check = async () => {
    if (checking) {
      stopCapture();
      return;
    }
    setError(null);
    try {
      capture.current = await startCapture({ deviceId: deviceId || undefined, raw, frameMs: FRAME_MS, onFrame });
      setChecking(true);
      void audioInputs().then(setDevices);
    } catch (e) {
      setError(errorMessage(e));
    }
  };

  const start = async () => {
    // A running check yields the microphone to the session (started again below, inside this click).
    stopCapture();
    setError(null);
    setView(null);
    setSession(null);
    sent.current = [];
    samples.current = 0;
    recorded.current = [];
    const bodies = targets.map(targetBody);
    if (bodies.some((b) => !b)) {
      setError("Choose a model for every target.");
      return;
    }
    let input: TranscriptionInput = { kind };
    if (kind === "span") {
      if (!span) {
        setError("Name an utterance span: utt_…#t=1.2,3.4");
        return;
      }
      input = { kind, utteranceId: span.utterance, start: span.start, end: span.end, channel: span.channel };
    }
    if (kind === "file" && !file) {
      setError("Choose a file.");
      return;
    }
    const body: TranscriptionNew = { input, targets: bodies.filter((b) => b !== undefined), pace, blind };
    if (telephony) body.telephony = { codec };
    setBusy(true);
    dispatch({ type: "connecting" });
    // The microphone starts inside the click (Safari resumes an AudioContext only during a gesture); frames are
    // dropped until the worker said started.
    const capP = kind === "microphone" ? startCapture({ deviceId: deviceId || undefined, raw, frameMs: FRAME_MS, onFrame }) : undefined;
    try {
      const s = await runCommand("transcriptions.new", { project, body });
      setSession(s);
      if (capP) {
        const cap = await capP;
        capture.current = cap;
        captureRate.current = cap.sampleRate;
        void audioInputs().then(setDevices);
      }
      const c = new LiveClient(s, {
        onMessage,
        onClose: (code, reason) => {
          dispatch({ type: "closed", code, reason });
          stopCapture();
          client.current = null;
          // A session that closed without its summary (idle, cap, a lost worker) still shows what it heard.
          if (code !== 1000) void showView();
        },
      });
      client.current = c;
      await c.connect();
      let li: LiveInput = { kind };
      if (kind === "microphone" && capture.current) {
        li = { kind, sampleRate: capture.current.sampleRate, frameMs: FRAME_MS, settings: capture.current.settings, raw };
      } else if (kind === "file" && file) {
        li = { kind, fileName: file.name, fileBytes: file.size };
      }
      c.start(li);
      if (kind === "file" && file) {
        if (file.size > s.limits.maxFileBytes) throw new Error(`The file is larger than ${Math.round(s.limits.maxFileBytes / 1e6)} MB`);
        await c.sendFile(await file.arrayBuffer(), s.limits.maxMessageBytes);
      }
    } catch (e) {
      setError(errorMessage(e));
      void capP?.then((cap) => cap.stop()).catch(() => undefined);
      teardown();
      dispatch({ type: "closed", code: 1000, reason: "" });
    } finally {
      setBusy(false);
    }
  };

  const finalize = () => {
    client.current?.finalize();
    dispatch({ type: "finalize", at: performance.now() });
  };
  const stop = () => {
    stopCapture();
    client.current?.end();
  };

  const lanes = useMemo(() => session?.targets ?? [], [session]);
  const hiddenLabels = !!session?.blind;
  const tracks: WordTrackData[] = useMemo(
    () =>
      lanes.map((l) => ({
        id: l.target,
        label: hiddenLabels ? `Lane ${l.target}` : `${l.target} · ${l.label} · ${l.profile}`,
        lang: l.language,
        words: laneWords(state.lanes[l.target as LaneTarget], view?.kind === "span" ? (view.start ?? 0) : 0),
      })),
    [lanes, state.lanes, view, hiddenLabels],
  );

  return (
    <div className="flex h-full min-h-0 flex-col" data-slot="transcription-panel">
      <PanelToolbar className="flex-wrap gap-1.5">
        <NativeSelect className="h-7 w-32" aria-label="Input" value={kind} disabled={running} onChange={(e) => {
            stopCapture();
            setKind(e.target.value as InputKind);
          }}>
          <option value="microphone">Microphone</option>
          <option value="file">File</option>
          <option value="span">Utterance span</option>
        </NativeSelect>
        {kind === "microphone" ? (
          <>
            <NativeSelect className="h-7 w-44" aria-label="Microphone" value={deviceId} disabled={running} onChange={(e) => {
                stopCapture();
                setDeviceId(e.target.value);
              }}>
              <option value="">Default microphone</option>
              {devices.map((d) => (
                <option key={d.deviceId} value={d.deviceId}>
                  {d.label}
                </option>
              ))}
            </NativeSelect>
            <label className="flex items-center gap-1 text-xs" title="Echo cancellation, noise suppression and automatic gain off, as recognition wants; turn it off to hear what a call stack does">
              <input type="checkbox" className="size-4 accent-primary" checked={raw} disabled={running} onChange={(e) => {
                stopCapture();
                setRaw(e.target.checked);
              }} />
              Raw microphone
            </label>
            <LevelMeter peak={level.peak} clipped={level.clipped} />
            {running ? null : (
              <Button size="xs" variant={checking ? "secondary" : "ghost"} disabled={busy || !!micWhy} onClick={() => void check()} aria-pressed={checking} title="Listen to the microphone and show its level; nothing is sent">
                {checking ? "Stop check" : "Check"}
              </Button>
            )}
          </>
        ) : kind === "file" ? (
          <label className="flex items-center gap-1 text-xs">
            <Upload aria-hidden className="size-4" />
            <input type="file" accept="audio/*,.wav,.flac,.mp3,.ogg,.opus,.m4a" className="max-w-56 text-xs" disabled={running} onChange={(e) => setFile(e.target.files?.[0] ?? null)} aria-label="Audio file" />
          </label>
        ) : (
          <input
            className="h-7 w-64 rounded-md border bg-background px-2 text-xs"
            placeholder="utt_…#t=1.2,3.4"
            aria-label="Utterance span"
            value={spanText}
            disabled={running}
            onChange={(e) => setSpanText(e.target.value)}
          />
        )}
        {kind !== "microphone" ? (
          <NativeSelect className="h-7 w-28" aria-label="Pace" value={pace} disabled={running} onChange={(e) => setPace(e.target.value as "realtime" | "fast")}>
            <option value="realtime">Real time</option>
            <option value="fast">As fast as possible</option>
          </NativeSelect>
        ) : null}
        <label className="flex items-center gap-1 text-xs" title="16 → 8 kHz, the codec, back to 16 kHz with the training resampler">
          <input type="checkbox" className="size-4 accent-primary" checked={telephony} disabled={running} onChange={(e) => setTelephony(e.target.checked)} />
          Telephony
        </label>
        {telephony ? (
          <NativeSelect className="h-7 w-24" aria-label="Codec" value={codec} disabled={running} onChange={(e) => setCodec(e.target.value as Codec)}>
            <option value="ulaw">G.711 μ-law</option>
            <option value="alaw">G.711 A-law</option>
            <option value="none">8 kHz only</option>
          </NativeSelect>
        ) : null}
        <label className="flex items-center gap-1 text-xs" title="Lanes are shuffled and unnamed until you pick the better one; the pick is not recorded">
          <input type="checkbox" className="size-4 accent-primary" checked={blind} disabled={running} onChange={(e) => setBlind(e.target.checked)} />
          Blind
        </label>
        <span className="ml-auto flex items-center gap-1">
          {running ? (
            <>
              <Button size="xs" variant="outline" disabled={phase !== "live"} onClick={finalize} title="A segment boundary: the words so far become final">
                <Pause aria-hidden />
                Finalize
              </Button>
              <Button size="xs" variant="outline" onClick={stop} title="Flush every target, then the summary">
                <Square aria-hidden />
                Stop
              </Button>
            </>
          ) : (
            <Button size="xs" disabled={busy || (kind === "microphone" && !!micWhy)} onClick={() => void start()}>
              {kind === "microphone" ? <Microphone aria-hidden /> : <Play aria-hidden />}
              Go live
            </Button>
          )}
        </span>
      </PanelToolbar>
      <div className="min-h-0 flex-1 overflow-auto p-2">
        {micWhy ? (
          <p className="mb-2 rounded-md bg-muted p-2 text-xs" role="note" data-slot="transcription-insecure">
            {micWhy}
          </p>
        ) : null}
        <TargetsForm project={project} targets={targets} onChange={setTargets} disabled={running} />
        <SessionLine session={session} phase={phase} waiting={state.waiting} />
        {error ? (
          <p className="my-1 text-xs text-destructive" role="alert">
            {error}
          </p>
        ) : null}
        {state.errors.map((e, i) => (
          <p key={i} className="my-1 text-xs text-destructive" role="alert">
            {e.problem.detail ?? e.problem.title}
          </p>
        ))}
        {state.close && state.close.code !== 1000 ? <p className="my-1 text-xs text-muted-foreground">{closeMeaning(state.close.code, state.close.reason)}</p> : null}
        {lanes.length ? (
          <div className="mt-2">
            <Lanes lanes={lanes} state={state} blind={!!session?.blind} reference={reference} />
          </div>
        ) : null}
        <label className="mt-2 block text-xs text-muted-foreground">
          Reference (typed; WER and a diff per lane, on this page only)
          <Textarea className="mt-1 min-h-12 text-[13px]" dir="auto" value={reference} onChange={(e) => setReference(e.target.value)} />
        </label>
        {view ? (
          <div className="mt-2" data-slot="transcription-view">
            <div className="mb-1 flex items-center gap-1">
              <Button size="icon-xs" variant="ghost" aria-label={playing ? "Pause" : "Play"} disabled={!engine} onClick={() => engine?.togglePlay()}>
                {playing ? <Pause aria-hidden /> : <Play aria-hidden />}
              </Button>
              <span className="text-xs text-muted-foreground">Every target's words on the audio (kept in this page only)</span>
            </div>
            {view.kind === "local" ? (
              <LocalAudioView samples={view.samples} sampleRate={view.sampleRate} mediaUrl={view.mediaUrl} tracks={tracks} title="The session's audio" onEngine={setEngine} onPlayState={setPlaying} />
            ) : (
              <AudioView utterance={view.utterance} span={{ start: view.start, end: view.end }} tracks={tracks} title={`Audio of ${view.utterance}`} onEngine={setEngine} onPlayState={setPlaying} />
            )}
          </div>
        ) : null}
      </div>
    </div>
  );
}

function SessionLine({ session, phase, waiting }: { session: TranscriptionSession | null; phase: string; waiting?: { state: string; position?: number; reason?: string } }) {
  if (!session && phase === "idle") return null;
  const parts: string[] = [];
  if (phase === "connecting") parts.push("Opening the session…");
  if (phase === "queued") parts.push(`Waiting for a card${waiting?.position ? ` — place ${waiting.position}` : ""}${waiting?.reason ? `: ${waiting.reason}` : ""}. Live mode starts when it has room.`);
  if (phase === "loading") parts.push("The worker is loading the models (about 20–30 s).");
  if (phase === "live") parts.push("Live");
  if (phase === "ended") parts.push("Ended");
  if (session) {
    const a = session.allowance;
    parts.push(`${session.reservationMb} MB reserved`);
    parts.push(`today ${a.usedGpuHours.toFixed(2)} of ${a.gpuHoursPerDay} GPU-h of manual tests used`);
    parts.push(`at most ${Math.round(session.limits.sessionSeconds / 60)} min, closed after ${Math.round(session.limits.idleSeconds / 60)} min idle`);
  }
  return (
    <p className="mt-2 text-xs text-muted-foreground" aria-live="polite" data-slot="transcription-session">
      {parts.join(" · ")}
    </p>
  );
}
