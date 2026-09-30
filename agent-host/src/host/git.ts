// The session worktree: a clone of the project repository from the control plane (git over smart HTTP, R10) on the
// session branch, and the per-turn commit and push. The session token authenticates as an HTTP header passed in
// git's environment (GIT_CONFIG_*), so it is never written into .git/config or any file (R2). Git runs as the
// session's Unix user with hooks disabled: the worktree is the agent's, and nothing in it may run as the host.

import { execFile } from "node:child_process";
import { lstat, rm } from "node:fs/promises";
import { join } from "node:path";
import { type Finding, scanDiff } from "./scan.ts";
import { capReport, parseNumstat, parsePorcelain, type WorkingChange, type WorkingReport } from "./watcher.ts";

export interface GitOptions {
  // The token (cst_…) sent as the basic-auth password; absent for local repositories in tests.
  token?: string;
  user?: { uid: number; gid: number };
  // The environment git starts from (PATH, HOME of the session user); nothing of the host's own.
  baseEnv: NodeJS.ProcessEnv;
}

export class GitError extends Error {
  constructor(
    readonly args: readonly string[],
    readonly code: number | null,
    readonly stderr: string,
  ) {
    super(`git ${args[0] ?? ""} failed (${code}): ${stderr.trim().slice(-800)}`);
    this.name = "GitError";
  }
}

export interface TurnCommit {
  sha?: string;
  files: string[];
  refused?: Finding[];
}

export class Worktree {
  constructor(
    readonly dir: string,
    readonly branch: string,
    private readonly opts: GitOptions,
  ) {}

  private env(): NodeJS.ProcessEnv {
    const env: NodeJS.ProcessEnv = {
      ...this.opts.baseEnv,
      GIT_CONFIG_NOSYSTEM: "1",
      GIT_CONFIG_GLOBAL: "/dev/null",
      GIT_TERMINAL_PROMPT: "0",
      GIT_CONFIG_COUNT: "2",
      GIT_CONFIG_KEY_0: "core.hooksPath",
      GIT_CONFIG_VALUE_0: "/dev/null",
      GIT_CONFIG_KEY_1: "safe.directory",
      GIT_CONFIG_VALUE_1: "*",
    };
    if (this.opts.token) {
      const basic = Buffer.from(`x-token:${this.opts.token}`).toString("base64");
      Object.assign(env, { GIT_CONFIG_COUNT: "3", GIT_CONFIG_KEY_2: "http.extraHeader", GIT_CONFIG_VALUE_2: `Authorization: Basic ${basic}` });
    }
    return env;
  }

  git(args: readonly string[], cwd = this.dir, extraEnv: NodeJS.ProcessEnv = {}): Promise<string> {
    return new Promise((resolve, reject) => {
      execFile(
        "git",
        [...args],
        {
          cwd,
          env: { ...this.env(), ...extraEnv },
          maxBuffer: 64 * 1024 * 1024,
          ...(this.opts.user ? { uid: this.opts.user.uid, gid: this.opts.user.gid } : {}),
        },
        (err, stdout, stderr) => {
          if (err) reject(new GitError(args, typeof err.code === "number" ? err.code : null, String(stderr)));
          else resolve(String(stdout));
        },
      );
    });
  }

  // Clones url at branch into dir (the parent must exist and dir must not); name and e-mail sign the turns.
  async clone(url: string, author: { name: string; email: string }): Promise<void> {
    await this.git(["clone", "--quiet", "--branch", this.branch, "--single-branch", url, this.dir], "/");
    await this.git(["config", "user.name", author.name]);
    await this.git(["config", "user.email", author.email]);
  }

  async isRepo(): Promise<boolean> {
    try {
      return (await this.git(["rev-parse", "--is-inside-work-tree"])).trim() === "true";
    } catch {
      return false;
    }
  }

  async head(): Promise<string> {
    return (await this.git(["rev-parse", "HEAD"])).trim();
  }

  // Commits everything the turn changed and pushes it to the session branch; a staged diff holding a credential is
  // unstaged and refused (the files stay in the worktree for the agent to fix).
  async commitTurn(message: string): Promise<TurnCommit> {
    await this.git(["add", "--all"]);
    const files = (await this.git(["diff", "--cached", "--name-only", "-z"])).split("\0").filter((f) => f !== "");
    if (files.length === 0) return { files: [] };
    const findings = scanDiff(await this.git(["diff", "--cached", "-U0", "--no-color", "--no-ext-diff"]));
    if (findings.length > 0) {
      await this.git(["reset", "--quiet"]);
      return { files, refused: findings };
    }
    await this.git(["commit", "--quiet", "--no-verify", "-m", message]);
    await this.push();
    return { sha: await this.head(), files };
  }

  // The uncommitted changes against HEAD as the next turn commit would see them (ignore rules included): status, size
  // and line counts, for the watcher. Optional locks are off, so git never writes the index under the agent's feet.
  async status(): Promise<WorkingReport> {
    const opts = { GIT_OPTIONAL_LOCKS: "0" };
    const listed = parsePorcelain(
      await this.git(["-c", "core.quotePath=off", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--no-renames"], this.dir, opts),
    );
    if (listed.length === 0) return { files: [], truncated: false };
    const numstat = parseNumstat(await this.git(["diff", "--numstat", "-z", "--no-renames", "HEAD"], this.dir, opts).catch(() => ""));
    const { files, truncated } = capReport(listed);
    const out = await Promise.all(
      files.map(async (f): Promise<WorkingChange> => {
        const c: WorkingChange = { ...f };
        if (f.status !== "deleted") {
          const st = await lstat(join(this.dir, f.path)).catch(() => undefined);
          if (st?.isFile()) c.bytes = st.size;
        }
        const n = numstat.get(f.path);
        if (n) Object.assign(c, n);
        return c;
      }),
    );
    return { files: out, truncated };
  }

  push(): Promise<string> {
    return this.git(["push", "--quiet", "origin", `HEAD:refs/heads/${this.branch}`]);
  }

  // Unpushed commits (a push that failed earlier) go out now.
  async pushPending(): Promise<void> {
    const ahead = await this.git(["rev-list", "--count", `origin/${this.branch}..HEAD`]).catch(() => "0");
    if (Number(ahead.trim()) > 0) await this.push();
  }

  remove(): Promise<void> {
    return rm(this.dir, { recursive: true, force: true });
  }
}
