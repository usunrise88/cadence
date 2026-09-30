// Agent credentials configured in Settings → Agents (docs/spec/08-resolutions.md R3, R6): the host claims tasks from
// the control plane (hostCredentials.claim) — a value to write into the agent-credentials volume, a removal, a
// verification — carries them out one at a time, in order, and acknowledges each (hostCredentials.report); the
// control plane then deletes its transit copy of the value. What each agent stores and how it checks a login is the
// driver's (src/drivers); this file only sandboxes the checks: a throwaway 0700 directory under the host's data
// directory, a Unix user from the session pool, the clean session environment (egress proxy included) and a timeout
// that ends the whole process group. Values are never logged; details are redacted and shortened before reporting.

import { spawn } from "node:child_process";
import { chmod, mkdir, rm } from "node:fs/promises";
import { join } from "node:path";
import { signalGroup } from "../acp/transport.ts";
import type { CredentialTask, Driver, RunOptions, RunResult, VerifyResult } from "../drivers/types.ts";
import { ApiError, type CredentialPlane, type HostCredentialReport } from "./api.ts";
import { baseEnv, own, type SessionDirs, type SessionUser, type UidPool } from "./isolation.ts";
import { errText, type Logger } from "./log.ts";

export interface CredentialWorkerOptions {
  cp: CredentialPlane;
  hostId: string;
  dataDir: string;
  // The agent-credentials volume; without it (development) every task fails with a clear detail.
  credentials?: string;
  // The session uid pool (shared with the session manager); verifications run as one of its users.
  uids?: UidPool;
  hostEnv: NodeJS.ProcessEnv;
  log: Logger;
  driverFor: (name: string) => Driver;
  waitSeconds?: number;
  // Per command of a verification (default 120 s).
  timeoutMs?: number;
}

const DETAIL_MAX = 500;
const OUTPUT_MAX = 1024 * 1024;

// redactDetail makes a detail fit for the control plane: the value itself and anything key-shaped are replaced,
// escapes and runs of white space dropped, the length capped.
export function redactDetail(detail: string, secrets: readonly string[] = []): string {
  let s = detail;
  for (const v of secrets) if (v && v.length >= 4) s = s.split(v).join("[redacted]");
  s = s
    .replace(/\u001b\[[0-9;]*[A-Za-z]/g, "")
    .replace(/\b(?:sk|pk|rk)-[A-Za-z0-9_-]{6,}/g, "[redacted]")
    .replace(/\b(?:cst|cdk|cwk|cah|cws|cep)_[a-z2-7]{8,}/g, "[redacted]")
    .replace(/\bBearer\s+\S+/gi, "Bearer [redacted]")
    .replace(/[A-Za-z0-9_\-+/=]{40,}/g, "[redacted]")
    .replace(/\s+/g, " ")
    .trim();
  return s.length > DETAIL_MAX ? `${s.slice(0, DETAIL_MAX - 1)}…` : s;
}

// runSandboxed runs one command as user (when set) in cwd with exactly env; on timeout it kills the process group.
export function runSandboxed(
  command: string,
  args: readonly string[],
  o: { cwd: string; env: NodeJS.ProcessEnv; user?: SessionUser; timeoutMs: number },
): Promise<RunResult> {
  return new Promise((resolve) => {
    let stdout = "";
    let stderr = "";
    let timedOut = false;
    const child = spawn(command, [...args], {
      cwd: o.cwd,
      env: o.env,
      stdio: ["ignore", "pipe", "pipe"],
      detached: true,
      ...(o.user ? { uid: o.user.uid, gid: o.user.gid } : {}),
    });
    child.stdout.setEncoding("utf8");
    child.stderr.setEncoding("utf8");
    child.stdout.on("data", (c: string) => {
      if (stdout.length < OUTPUT_MAX) stdout += c;
    });
    child.stderr.on("data", (c: string) => {
      if (stderr.length < OUTPUT_MAX) stderr += c;
    });
    const timer = setTimeout(() => {
      timedOut = true;
      if (child.pid !== undefined) signalGroup(child.pid, "SIGKILL");
    }, o.timeoutMs);
    const done = (code: number | null, signal: NodeJS.Signals | null): void => {
      clearTimeout(timer);
      // Whatever the command left running (opencode's server, a CLI's helper) ends with it.
      if (child.pid !== undefined) signalGroup(child.pid, "SIGKILL");
      resolve({ code, signal, stdout, stderr, timedOut });
    };
    child.on("error", (err) => {
      stderr += `${err.message}\n`;
      done(null, null);
    });
    child.on("close", (code, signal) => done(code, signal));
  });
}

export class CredentialWorker {
  constructor(private readonly o: CredentialWorkerOptions) {}

  // run claims tasks until signal aborts.
  async run(signal: AbortSignal): Promise<void> {
    let backoff = 1000;
    while (!signal.aborted) {
      try {
        const work = await this.o.cp.claimCredentials({ hostId: this.o.hostId, wait: this.o.waitSeconds ?? 20 }, signal);
        backoff = 1000;
        for (const task of work.tasks) {
          if (signal.aborted) break;
          await this.process(task);
        }
      } catch (err) {
        if (signal.aborted) break;
        const level = err instanceof ApiError && err.status === 401 ? "error" : "warn";
        this.o.log.log(level, "credential claim failed", { err: errText(err), retryInMs: backoff });
        await new Promise((r) => setTimeout(r, backoff));
        backoff = Math.min(30_000, backoff * 2);
      }
    }
  }

  // process carries out one task and reports it; a report that fails leaves the task to be offered again.
  async process(task: CredentialTask): Promise<HostCredentialReport> {
    const started = Date.now();
    const result = await this.handle(task).catch((err: unknown): VerifyResult => ({ ok: false, detail: errText(err) }));
    const report: HostCredentialReport = {
      hostId: this.o.hostId,
      ok: result.ok,
      detail: redactDetail(result.detail, task.value ? [task.value, task.value.trim()] : []),
    };
    if (result.model) report.model = result.model;
    if (result.models) report.models = result.models;
    this.o.log.log(result.ok ? "info" : "warn", "credential task", {
      task: task.id, action: task.action, credential: task.credentialId, ok: result.ok, ms: Date.now() - started,
    });
    for (let attempt = 1; ; attempt++) {
      try {
        await this.o.cp.reportCredential(task.id, report);
        break;
      } catch (err) {
        if (attempt >= 3 || (err instanceof ApiError && err.permanent)) {
          this.o.log.log("warn", "credential report failed", { task: task.id, err: errText(err) });
          break;
        }
        await new Promise((r) => setTimeout(r, 500 * attempt));
      }
    }
    return report;
  }

  private async handle(task: CredentialTask): Promise<VerifyResult> {
    const root = this.o.credentials;
    if (!root) {
      return { ok: false, detail: "this agent host has no agent-credentials volume (CADENCE_AGENT_CREDENTIALS is not set)" };
    }
    const driver = this.o.driverFor(task.agent);
    switch (task.action) {
      case "write":
        if (!driver.writeCredential) return { ok: false, detail: `the ${driver.name} driver cannot store credentials` };
        await driver.writeCredential(root, task);
        return { ok: true, detail: "written to the agent-credentials volume" };
      case "remove":
        if (!driver.removeCredential) return { ok: false, detail: `the ${driver.name} driver cannot remove credentials` };
        await driver.removeCredential(root, task);
        return { ok: true, detail: "removed from the agent-credentials volume" };
      case "verify":
        if (!driver.verify) return { ok: false, detail: `the ${driver.name} driver cannot verify credentials` };
        return this.verify(driver, task, root);
    }
    return { ok: false, detail: `unknown task action ${String(task.action)}` };
  }

  // verify runs the driver's check in a sandbox like a session's: own user, own 0700 directory, clean environment.
  private async verify(driver: Driver, task: CredentialTask, credentials: string): Promise<VerifyResult> {
    const key = `verify-${task.id}`;
    const user = this.o.uids?.acquire(key);
    const parent = join(this.o.dataDir, "verify");
    const root = join(parent, task.id.replace(/[^A-Za-z0-9_-]/g, "_"));
    const dirs: SessionDirs = { root, worktree: join(root, "work"), home: join(root, "home"), tmp: join(root, "tmp") };
    try {
      await mkdir(parent, { recursive: true, mode: 0o711 });
      await rm(root, { recursive: true, force: true });
      await mkdir(root, { mode: 0o700 });
      await chmod(root, 0o700);
      for (const d of [dirs.worktree, dirs.home, dirs.tmp]) await mkdir(d, { mode: 0o700 });
      const env = baseEnv(dirs, this.o.hostEnv);
      const homeEnv = (await driver.prepareHome?.(dirs.home, credentials)) ?? {};
      await own(root, user);
      const timeoutMs = this.o.timeoutMs ?? 120_000;
      return await driver.verify!({
        task,
        credentials,
        home: dirs.home,
        run: async (command: string, args: readonly string[], opts?: RunOptions) => {
          await own(dirs.home, user); // the driver may have copied files in again (as root)
          return runSandboxed(command, args, {
            cwd: dirs.worktree,
            env: { ...env, ...homeEnv, ...opts?.env },
            ...(user ? { user } : {}),
            timeoutMs: Math.min(opts?.timeoutMs ?? timeoutMs, timeoutMs),
          });
        },
      });
    } finally {
      await rm(root, { recursive: true, force: true }).catch(() => undefined);
      this.o.uids?.release(key);
    }
  }
}
