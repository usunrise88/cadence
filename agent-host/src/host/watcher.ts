// The worktree watcher (docs/spec/05-agents.md "Worktree, drafts and merge"): while a turn runs, file-system events
// in the session's worktree are coalesced for a short window (300 ms), then git lists the uncommitted files — so
// .gitignore and .git/info/exclude apply exactly as they do to the turn's commit — and a list that differs from the
// last one goes to the control plane (hostSessions.report `working`: paths, status, sizes and line counts; never
// content). The control plane turns it into recipe.{path} events for the open Recipe document and the Chat.

import { type FSWatcher, watch } from "node:fs";
import type { Clock, Timer } from "./clock.ts";

export interface WorkingChange {
  path: string;
  status: "added" | "modified" | "deleted";
  bytes?: number;
  additions?: number;
  deletions?: number;
}

export interface WorkingReport {
  files: WorkingChange[];
  truncated: boolean;
}

/** Events are coalesced for this long before git is asked. */
export const WATCH_DEBOUNCE_MS = 300;
/** A report lists at most this many files (the first by path). */
export const MAX_WORKING_FILES = 200;
/** Without fs.watch (inotify exhausted, an unsupported file system) the worktree is scanned this often. */
export const POLL_MS = 2_000;

/** Paths git keeps to itself: events there never trigger a scan (git's own work would feed back into the watcher). */
export function ignoredPath(rel: string): boolean {
  const p = rel.replaceAll("\\", "/").replace(/^\.\//, "");
  return p === ".git" || p.startsWith(".git/");
}

/** Reads `git status --porcelain=v1 -z --untracked-files=all --no-renames`: `XY path` records separated by NUL. */
export function parsePorcelain(out: string): Array<Pick<WorkingChange, "path" | "status">> {
  const files: Array<Pick<WorkingChange, "path" | "status">> = [];
  for (const rec of out.split("\0")) {
    if (rec.length < 4) continue;
    const xy = rec.slice(0, 2);
    const path = rec.slice(3);
    if (ignoredPath(path)) continue;
    let status: WorkingChange["status"] = "modified";
    if (xy === "??" || xy.includes("A")) status = "added";
    else if (xy.includes("D")) status = "deleted";
    files.push({ path, status });
  }
  return files;
}

/** Reads `git diff --numstat -z HEAD`: `added TAB deleted TAB path` NUL; binary files count "-". */
export function parseNumstat(out: string): Map<string, { additions: number; deletions: number }> {
  const stats = new Map<string, { additions: number; deletions: number }>();
  for (const rec of out.split("\0")) {
    const [a, d, path] = rec.split("\t");
    if (!path || a === "-" || d === "-") continue;
    stats.set(path, { additions: Number(a), deletions: Number(d) });
  }
  return stats;
}

/** Orders by path and cuts the list at MAX_WORKING_FILES. */
export function capReport(files: WorkingChange[]): WorkingReport {
  const sorted = [...files].sort((a, b) => (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
  return { files: sorted.slice(0, MAX_WORKING_FILES), truncated: sorted.length > MAX_WORKING_FILES };
}

export interface WatcherOptions {
  dir: string;
  clock: Clock;
  /** Lists the worktree's uncommitted changes (Worktree.status). */
  scan: () => Promise<WorkingReport>;
  /** Receives a list that differs from the last one. */
  onReport: (r: WorkingReport) => void;
  debounceMs?: number;
  /** Tests replace fs.watch: it returns a closer, or throws when the directory cannot be watched; onFail ends it. */
  watchFn?: (dir: string, onPath: (rel: string) => void, onFail: (err: unknown) => void) => { close(): void };
  onError?: (err: unknown) => void;
}

function fsWatch(dir: string, onPath: (rel: string) => void, onFail: (err: unknown) => void): FSWatcher {
  const w = watch(dir, { recursive: true, persistent: false }, (_ev, name) => onPath(name ? String(name) : ""));
  w.on("error", onFail);
  return w;
}

export class WorktreeWatcher {
  private watcher: { close(): void } | undefined;
  private poll: Timer | undefined;
  private timer: Timer | undefined;
  private running: Promise<void> | undefined;
  private again = false;
  private last: string | undefined;
  private active = false;

  constructor(private readonly opts: WatcherOptions) {}

  get watching(): boolean {
    return this.active;
  }

  /** Starts watching (a turn began); the first scan reports the worktree as it is. */
  start(): void {
    if (this.active) return;
    this.active = true;
    try {
      this.watcher = (this.opts.watchFn ?? fsWatch)(this.opts.dir, (rel) => this.poke(rel), (err) => this.fallBack(err));
    } catch (err) {
      this.fallBack(err);
    }
    this.poke();
  }

  // fallBack polls instead of watching (fs.watch failed to start or broke).
  private fallBack(err: unknown): void {
    this.opts.onError?.(err);
    this.watcher?.close();
    this.watcher = undefined;
    if (this.active && !this.poll) this.schedulePoll();
  }

  private schedulePoll(): void {
    this.poll = this.opts.clock.after(POLL_MS, () => {
      if (!this.active) return;
      this.poke();
      this.schedulePoll();
    });
  }

  /** A change at rel (relative to the worktree; empty when unknown): scan once the window closes. */
  poke(rel = ""): void {
    if (!this.active || (rel && ignoredPath(rel))) return;
    if (this.running) {
      this.again = true;
      return;
    }
    if (this.timer) return;
    this.timer = this.opts.clock.after(this.opts.debounceMs ?? WATCH_DEBOUNCE_MS, () => {
      this.timer = undefined;
      void this.run();
    });
  }

  private run(): Promise<void> {
    const p = (async () => {
      try {
        await this.scanAndReport();
      } catch (err) {
        this.opts.onError?.(err);
      }
    })().finally(() => {
      this.running = undefined;
      if (this.again && this.active) {
        this.again = false;
        this.poke();
      }
    });
    this.running = p;
    return p;
  }

  private async scanAndReport(): Promise<WorkingReport> {
    const r = await this.opts.scan();
    const key = JSON.stringify(r);
    if (key !== this.last) {
      this.last = key;
      this.opts.onReport(r);
    }
    return r;
  }

  /** Stops watching (the turn ended); a scan in flight finishes first. */
  async stop(): Promise<void> {
    this.active = false;
    this.again = false;
    this.watcher?.close();
    this.watcher = undefined;
    this.timer?.cancel();
    this.timer = undefined;
    this.poll?.cancel();
    this.poll = undefined;
    await this.running;
  }

  /** Scans now, whether watching or not, and reports the list when it changed (after the turn's commit). */
  async flush(): Promise<WorkingReport> {
    await this.running;
    return this.scanAndReport();
  }
}
