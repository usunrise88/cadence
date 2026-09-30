// Helpers the drivers use to copy an agent's login from the agent-credentials volume into a session HOME, and to
// write credentials into the volume (0600 files in 0700 directories).

import { spawnSync } from "node:child_process";
import { chmod, cp, mkdir, readFile, rename, rm, stat, writeFile } from "node:fs/promises";
import { dirname } from "node:path";
import { createInterface } from "node:readline/promises";

// ask reads one line from the terminal (the login commands).
export async function ask(question: string): Promise<string> {
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try {
    return (await rl.question(question)).trim();
  } finally {
    rl.close();
  }
}

// runInteractive runs a login command on the terminal.
export function runInteractive(cmd: string, args: string[], env: NodeJS.ProcessEnv): void {
  const r = spawnSync(cmd, args, { stdio: "inherit", env });
  if (r.status !== 0) throw new Error(`${cmd} ${args.join(" ")} exited with ${r.status ?? r.signal}`);
}

export async function exists(path: string): Promise<boolean> {
  try {
    await stat(path);
    return true;
  } catch {
    return false;
  }
}

// copyIfPresent copies a file or directory when it exists (0600/0700 kept by the session's umask and cp).
export async function copyIfPresent(from: string, to: string, filter?: (src: string) => boolean): Promise<boolean> {
  if (!(await exists(from))) return false;
  await mkdir(dirname(to), { recursive: true, mode: 0o700 });
  await cp(from, to, { recursive: true, force: true, ...(filter ? { filter } : {}) });
  return true;
}

// writeSecretFile writes content to path atomically: the directory 0700, the file 0600 (a temp file renamed over it).
export async function writeSecretFile(path: string, content: string): Promise<void> {
  const dir = dirname(path);
  await mkdir(dir, { recursive: true, mode: 0o700 });
  await chmod(dir, 0o700);
  const tmp = `${path}.tmp-${process.pid}-${Date.now().toString(36)}`;
  try {
    await writeFile(tmp, content, { mode: 0o600 });
    await chmod(tmp, 0o600);
    await rename(tmp, path);
  } finally {
    await rm(tmp, { force: true });
  }
}

// readJsonObject reads a JSON object file; a missing file is {}. A file that is not a JSON object is an error, so a
// merge never throws away what an interactive login wrote.
export async function readJsonObject(path: string): Promise<Record<string, unknown>> {
  if (!(await exists(path))) return {};
  const parsed: unknown = JSON.parse(await readFile(path, "utf8"));
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) throw new Error(`${path} is not a JSON object`);
  return parsed as Record<string, unknown>;
}

export async function writeJsonSecret(path: string, value: Record<string, unknown>): Promise<void> {
  await writeSecretFile(path, `${JSON.stringify(value, null, 2)}\n`);
}

export async function readSecretFile(path: string): Promise<string | undefined> {
  if (!(await exists(path))) return undefined;
  const v = (await readFile(path, "utf8")).trim();
  return v === "" ? undefined : v;
}
