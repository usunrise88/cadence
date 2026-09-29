// Client-side file system for ACP `fs/read_text_file` and `fs/write_text_file`, confined to a session's roots
// (its cwd plus any additional directories). Paths must be absolute; symlinks are resolved before the check so a
// link inside the worktree cannot reach outside it.

import { mkdir, readFile, realpath, writeFile } from "node:fs/promises";
import { dirname, isAbsolute, relative, resolve, sep } from "node:path";

export class OutsideWorkspaceError extends Error {
  constructor(readonly path: string) {
    super(`path is outside the session workspace: ${path}`);
    this.name = "OutsideWorkspaceError";
  }
}

function within(root: string, path: string): boolean {
  const rel = relative(root, path);
  return rel === "" || (!rel.startsWith(`..${sep}`) && rel !== ".." && !isAbsolute(rel));
}

// realpath of the nearest existing ancestor, with the missing tail appended: new files resolve through their parent.
async function resolveExisting(path: string): Promise<string> {
  const tail: string[] = [];
  let cur = path;
  for (;;) {
    try {
      const real = await realpath(cur);
      return tail.length === 0 ? real : resolve(real, ...tail.reverse());
    } catch (err) {
      if ((err as NodeJS.ErrnoException).code !== "ENOENT") throw err;
      const parent = dirname(cur);
      if (parent === cur) return path;
      tail.push(cur.slice(parent.length + (parent.endsWith(sep) ? 0 : 1)));
      cur = parent;
    }
  }
}

export class WorkspaceFs {
  private constructor(private readonly roots: readonly string[]) {}

  static async create(roots: readonly string[]): Promise<WorkspaceFs> {
    if (roots.length === 0) throw new Error("WorkspaceFs needs at least one root");
    for (const r of roots) if (!isAbsolute(r)) throw new Error(`workspace root must be absolute: ${r}`);
    return new WorkspaceFs(await Promise.all(roots.map((r) => realpath(r))));
  }

  async check(path: string): Promise<string> {
    if (!isAbsolute(path)) throw new OutsideWorkspaceError(path);
    const real = await resolveExisting(resolve(path));
    if (!this.roots.some((root) => within(root, real))) throw new OutsideWorkspaceError(path);
    return real;
  }

  // `line` is 1-based; `limit` counts lines, as in ACP's ReadTextFileRequest.
  async read(path: string, line?: number | null, limit?: number | null): Promise<string> {
    const real = await this.check(path);
    const text = await readFile(real, "utf8");
    if (line == null && limit == null) return text;
    const lines = text.split("\n");
    const start = Math.max(0, (line ?? 1) - 1);
    const end = limit == null ? lines.length : start + limit;
    return lines.slice(start, end).join("\n");
  }

  async write(path: string, content: string): Promise<void> {
    const real = await this.check(path);
    await mkdir(dirname(real), { recursive: true });
    await writeFile(real, content, "utf8");
  }
}
