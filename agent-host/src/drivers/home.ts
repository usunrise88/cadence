// Helpers the drivers use to copy an agent's login from the agent-credentials volume into a session HOME.

import { spawnSync } from "node:child_process";
import { cp, mkdir, readFile, stat } from "node:fs/promises";
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

export async function readSecretFile(path: string): Promise<string | undefined> {
  if (!(await exists(path))) return undefined;
  const v = (await readFile(path, "utf8")).trim();
  return v === "" ? undefined : v;
}
