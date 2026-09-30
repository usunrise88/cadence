import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";
import { FakeClock, realClock } from "./clock.ts";
import { Worktree } from "./git.ts";
import {
  capReport,
  ignoredPath,
  MAX_WORKING_FILES,
  parseNumstat,
  parsePorcelain,
  POLL_MS,
  WATCH_DEBOUNCE_MS,
  type WorkingReport,
  WorktreeWatcher,
} from "./watcher.ts";

describe("worktree watcher: parsing and ignore rules", () => {
  test("ignoredPath keeps git's own directory out", () => {
    for (const p of [".git", ".git/index", ".git/objects/ab/cdef", "./.git/HEAD"]) assert.equal(ignoredPath(p), true, p);
    for (const p of ["a.txt", ".gitignore", ".github/workflows/x.yml", "sub/.git-notes", ""]) assert.equal(ignoredPath(p), false, p);
  });

  test("porcelain status: untracked and staged files are added, deletions deleted, the rest modified", () => {
    const out = ["?? new.txt", " M a.txt", "D  gone.txt", " D gone2.txt", "A  staged.txt", "MM both.txt", "?? .git/stray", ""].join("\0");
    assert.deepEqual(parsePorcelain(out), [
      { path: "new.txt", status: "added" },
      { path: "a.txt", status: "modified" },
      { path: "gone.txt", status: "deleted" },
      { path: "gone2.txt", status: "deleted" },
      { path: "staged.txt", status: "added" },
      { path: "both.txt", status: "modified" },
    ]);
  });

  test("numstat: line counts per path, binary files without counts", () => {
    const got = parseNumstat(["3\t1\ta.txt", "-\t-\timg.png", "0\t5\tsub/b.yaml", ""].join("\0"));
    assert.deepEqual([...got], [
      ["a.txt", { additions: 3, deletions: 1 }],
      ["sub/b.yaml", { additions: 0, deletions: 5 }],
    ]);
  });

  test("a report is ordered by path and capped", () => {
    const files = Array.from({ length: MAX_WORKING_FILES + 5 }, (_, i) => ({ path: `f${String(9999 - i)}.txt`, status: "added" as const }));
    const r = capReport(files);
    assert.equal(r.files.length, MAX_WORKING_FILES);
    assert.equal(r.truncated, true);
    assert.equal(r.files[0]?.path, `f${9999 - MAX_WORKING_FILES - 4}.txt`);
    assert.deepEqual(capReport([{ path: "b", status: "added" }, { path: "a", status: "deleted" }]), {
      files: [{ path: "a", status: "deleted" }, { path: "b", status: "added" }],
      truncated: false,
    });
  });
});

// A watcher on a fake clock with a scripted scan and a hand-driven fs.watch.
function rig(results: WorkingReport[] = []) {
  const clock = new FakeClock();
  const reports: WorkingReport[] = [];
  let scans = 0;
  let emit: ((rel: string) => void) | undefined;
  let closed = 0;
  let release: (() => void) | undefined;
  let hold = false;
  const w = new WorktreeWatcher({
    dir: "/nowhere",
    clock,
    scan: async () => {
      scans++;
      if (hold) await new Promise<void>((r) => (release = r));
      return results[Math.min(scans - 1, results.length - 1)] ?? { files: [], truncated: false };
    },
    onReport: (r) => reports.push(r),
    watchFn: (_dir, onPath) => {
      emit = onPath;
      return { close: () => closed++ };
    },
  });
  const settle = () => new Promise((r) => setImmediate(r));
  return {
    w,
    clock,
    reports,
    scans: () => scans,
    emit: (rel: string) => emit?.(rel),
    closed: () => closed,
    settle,
    holdScans: (on: boolean) => (hold = on),
    release: () => release?.(),
  };
}

const edited = (path: string, bytes: number): WorkingReport => ({ files: [{ path, status: "modified", bytes }], truncated: false });

describe("worktree watcher: debounce", () => {
  test("a burst of events is one scan once the window closes; the same list is not reported twice", async () => {
    const r = rig([edited("a.txt", 1), edited("a.txt", 1), edited("a.txt", 2)]);
    r.w.start();
    for (let i = 0; i < 10; i++) r.emit(`a.txt`);
    r.clock.advance(WATCH_DEBOUNCE_MS - 1);
    await r.settle();
    assert.equal(r.scans(), 0, "nothing before the window closes");
    r.clock.advance(1);
    await r.settle();
    assert.equal(r.scans(), 1, "one scan for the start and the burst");
    assert.deepEqual(r.reports, [edited("a.txt", 1)]);

    r.emit("a.txt");
    r.clock.advance(WATCH_DEBOUNCE_MS);
    await r.settle();
    assert.equal(r.scans(), 2);
    assert.equal(r.reports.length, 1, "an unchanged list is not reported again");

    r.emit("a.txt");
    r.clock.advance(WATCH_DEBOUNCE_MS);
    await r.settle();
    assert.deepEqual(r.reports.at(-1), edited("a.txt", 2));
    await r.w.stop();
    assert.equal(r.closed(), 1);
  });

  test("events inside .git never start a scan", async () => {
    const r = rig();
    r.w.start();
    r.clock.advance(WATCH_DEBOUNCE_MS);
    await r.settle();
    const base = r.scans();
    r.emit(".git/index.lock");
    r.emit(".git/objects/12/3456");
    assert.equal(r.clock.pending(), 0, "no timer armed");
    r.clock.advance(WATCH_DEBOUNCE_MS * 3);
    await r.settle();
    assert.equal(r.scans(), base);
    await r.w.stop();
  });

  test("events during a scan lead to exactly one more scan", async () => {
    const r = rig([edited("a.txt", 1), edited("a.txt", 5)]);
    r.w.start();
    r.holdScans(true);
    r.clock.advance(WATCH_DEBOUNCE_MS);
    await r.settle();
    assert.equal(r.scans(), 1);
    r.emit("a.txt");
    r.emit("b.txt");
    r.holdScans(false);
    r.release();
    await r.settle();
    await r.settle();
    r.clock.advance(WATCH_DEBOUNCE_MS);
    await r.settle();
    assert.equal(r.scans(), 2);
    assert.deepEqual(r.reports.at(-1), edited("a.txt", 5));
    await r.w.stop();
  });

  test("stopped: events are dropped; flush still reports what is left", async () => {
    const r = rig([edited("a.txt", 1), { files: [], truncated: false }]);
    r.w.start();
    r.clock.advance(WATCH_DEBOUNCE_MS);
    await r.settle();
    await r.w.stop();
    r.emit("a.txt");
    assert.equal(r.clock.pending(), 0);
    const left = await r.w.flush();
    assert.deepEqual(left, { files: [], truncated: false });
    assert.deepEqual(r.reports.at(-1), { files: [], truncated: false }, "the clean worktree is reported");
  });

  test("without fs.watch the worktree is polled", async () => {
    const clock = new FakeClock();
    let scans = 0;
    const errors: unknown[] = [];
    const w = new WorktreeWatcher({
      dir: "/nowhere",
      clock,
      scan: () => (scans++, Promise.resolve({ files: [], truncated: false })),
      onReport: () => undefined,
      onError: (e) => errors.push(e),
      watchFn: () => {
        throw new Error("ENOSPC");
      },
    });
    w.start();
    clock.advance(WATCH_DEBOUNCE_MS);
    await new Promise((r) => setImmediate(r));
    assert.equal(scans, 1);
    clock.advance(POLL_MS + WATCH_DEBOUNCE_MS);
    await new Promise((r) => setImmediate(r));
    assert.equal(scans, 2);
    assert.equal(errors.length, 1);
    await w.stop();
    assert.equal(clock.pending(), 0);
  });
});

describe("worktree watcher: a real worktree", () => {
  let dir: string;
  const git = (...args: string[]) =>
    execFileSync("git", args, {
      cwd: dir,
      env: { ...process.env, GIT_CONFIG_GLOBAL: "/dev/null", GIT_CONFIG_NOSYSTEM: "1", GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@x", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@x" },
    }).toString();

  before(async () => {
    dir = await mkdtemp(join(tmpdir(), "watcher-test-"));
    git("init", "-q", "-b", "main");
    writeFileSync(join(dir, ".gitignore"), "*.log\nbuild/\n");
    writeFileSync(join(dir, "a.txt"), "one\ntwo\n");
    writeFileSync(join(dir, "gone.txt"), "g\n");
    git("add", ".");
    git("commit", "-q", "-m", "seed");
  });
  after(async () => {
    await rm(dir, { recursive: true, force: true });
  });

  test("status lists what the next commit would take, with sizes and line counts, never ignored files", async () => {
    const wt = new Worktree(dir, "main", { baseEnv: process.env });
    assert.deepEqual(await wt.status(), { files: [], truncated: false });
    writeFileSync(join(dir, "a.txt"), "one\nTWO\nthree\n");
    mkdirSync(join(dir, "sub"), { recursive: true });
    writeFileSync(join(dir, "sub", "new file.yaml"), "k: v\n");
    writeFileSync(join(dir, "debug.log"), "noise\n");
    mkdirSync(join(dir, "build"), { recursive: true });
    writeFileSync(join(dir, "build", "out.bin"), "x");
    git("rm", "-q", "gone.txt");
    assert.deepEqual(await wt.status(), {
      files: [
        { path: "a.txt", status: "modified", bytes: 14, additions: 2, deletions: 1 },
        { path: "gone.txt", status: "deleted", additions: 0, deletions: 1 },
        { path: "sub/new file.yaml", status: "added", bytes: 5 },
      ],
      truncated: false,
    });
    git("checkout", "-q", "--", ".");
    git("reset", "-q", "--hard");
    git("clean", "-q", "-fd");
  });

  test("fs.watch feeds the watcher: an edit is reported within the window", async () => {
    const wt = new Worktree(dir, "main", { baseEnv: process.env });
    const reports: WorkingReport[] = [];
    const w = new WorktreeWatcher({ dir, clock: realClock, scan: () => wt.status(), onReport: (r) => reports.push(r) });
    w.start();
    const until = async (pred: () => boolean) => {
      const end = Date.now() + 5_000;
      while (!pred()) {
        if (Date.now() > end) throw new Error(`timed out; reports ${JSON.stringify(reports)}`);
        await new Promise((r) => setTimeout(r, 20));
      }
    };
    await until(() => reports.length === 1);
    assert.deepEqual(reports[0], { files: [], truncated: false });
    writeFileSync(join(dir, "a.txt"), "one\ntwo\nthree\n");
    await until(() => reports.length === 2);
    assert.deepEqual(reports[1]?.files.map((f) => [f.path, f.status]), [["a.txt", "modified"]]);
    writeFileSync(join(dir, "only.log"), "ignored\n");
    await new Promise((r) => setTimeout(r, WATCH_DEBOUNCE_MS * 3));
    assert.equal(reports.length, 2, "an ignored file changes nothing");
    await w.stop();
  });
});
