// Credential and file isolation of sessions (R3): every session runs as its own Unix user from a pool, in a
// directory only that user can open (0700) holding its worktree and a private HOME; the agent's own login is
// copied there from the agent-credentials volume by the driver (never the host's ~/.claude). Without root (a
// developer's machine, tests) sessions run as the host's own user and the directories are still 0700.

import { chmod, chown, mkdir, readdir, rm, stat } from "node:fs/promises";
import { join } from "node:path";

export interface SessionUser {
  uid: number;
  gid: number;
}

export interface SessionDirs {
  root: string; // <data>/sessions/<id>, 0700, the session user's
  worktree: string; // root/worktree
  home: string; // root/home: HOME, XDG dirs, the agent's copied login
  tmp: string; // root/tmp
}

// UidPool hands out uids from a range, one per live session.
export class UidPool {
  private readonly used = new Map<string, number>();

  constructor(
    private readonly first: number,
    private readonly size: number,
  ) {}

  acquire(sessionId: string): SessionUser {
    const have = this.used.get(sessionId);
    if (have !== undefined) return { uid: have, gid: have };
    const taken = new Set(this.used.values());
    for (let uid = this.first; uid < this.first + this.size; uid++) {
      if (!taken.has(uid)) {
        this.used.set(sessionId, uid);
        return { uid, gid: uid };
      }
    }
    throw new Error(`no free session user: all ${this.size} uids from ${this.first} are in use`);
  }

  release(sessionId: string): void {
    this.used.delete(sessionId);
  }

  get inUse(): number {
    return this.used.size;
  }
}

export function sessionDirs(dataDir: string, sessionId: string): SessionDirs {
  const root = join(dataDir, "sessions", sessionId);
  return { root, worktree: join(root, "worktree"), home: join(root, "home"), tmp: join(root, "tmp") };
}

async function chownTree(path: string, u: SessionUser): Promise<void> {
  await chown(path, u.uid, u.gid);
  const st = await stat(path);
  if (!st.isDirectory()) return;
  for (const e of await readdir(path)) await chownTree(join(path, e), u);
}

// prepareDirs creates the session's directories (the worktree itself is left for the clone) and hands them to the
// session user.
export async function prepareDirs(dataDir: string, sessionId: string, user?: SessionUser): Promise<SessionDirs> {
  const d = sessionDirs(dataDir, sessionId);
  await mkdir(join(dataDir, "sessions"), { recursive: true, mode: 0o711 });
  await mkdir(d.root, { recursive: true, mode: 0o700 });
  await chmod(d.root, 0o700);
  await mkdir(d.home, { recursive: true, mode: 0o700 });
  await mkdir(d.tmp, { recursive: true, mode: 0o700 });
  if (user) await own(d.root, user);
  return d;
}

// own hands a path (recursively) to the session user.
export async function own(path: string, user?: SessionUser): Promise<void> {
  if (user) await chownTree(path, user);
}

export async function removeDirs(d: SessionDirs): Promise<void> {
  await rm(d.root, { recursive: true, force: true });
}

// baseEnv is the environment an agent (and git on its behalf) starts from: a PATH, the session's HOME and temp
// directory, the egress proxy — and nothing else of the host's environment.
export function baseEnv(d: SessionDirs, host: NodeJS.ProcessEnv): NodeJS.ProcessEnv {
  const env: NodeJS.ProcessEnv = {
    PATH: host.PATH ?? "/usr/local/bin:/usr/bin:/bin",
    HOME: d.home,
    TMPDIR: d.tmp,
    LANG: host.LANG ?? "C.UTF-8",
    XDG_CONFIG_HOME: join(d.home, ".config"),
    XDG_DATA_HOME: join(d.home, ".local", "share"),
    XDG_CACHE_HOME: join(d.home, ".cache"),
    XDG_STATE_HOME: join(d.home, ".local", "state"),
  };
  for (const k of ["HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy", "NODE_EXTRA_CA_CERTS", "TZ"]) {
    if (host[k]) env[k] = host[k];
  }
  return env;
}
