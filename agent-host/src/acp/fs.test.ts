import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";
import { OutsideWorkspaceError, WorkspaceFs } from "./fs.ts";

describe("WorkspaceFs", () => {
  let root: string;
  let ws: string;
  let fs: WorkspaceFs;

  before(async () => {
    root = await mkdtemp(join(tmpdir(), "ws-fs-"));
    ws = join(root, "ws");
    await mkdir(ws);
    await writeFile(join(ws, "a.txt"), "l1\nl2\nl3\nl4\n");
    await writeFile(join(root, "secret.txt"), "nope");
    await symlink(join(root, "secret.txt"), join(ws, "link.txt"));
    await symlink(root, join(ws, "up"));
    fs = await WorkspaceFs.create([ws]);
  });
  after(() => rm(root, { recursive: true, force: true }));

  test("reads whole files and line windows", async () => {
    assert.equal(await fs.read(join(ws, "a.txt")), "l1\nl2\nl3\nl4\n");
    assert.equal(await fs.read(join(ws, "a.txt"), 2, 2), "l2\nl3");
    assert.equal(await fs.read(join(ws, "a.txt"), 3), "l3\nl4\n");
  });

  test("writes new files, creating directories inside the workspace", async () => {
    await fs.write(join(ws, "d", "e", "b.txt"), "hi");
    assert.equal(await readFile(join(ws, "d", "e", "b.txt"), "utf8"), "hi");
  });

  for (const [name, path] of [
    ["a relative path", "a.txt"],
    ["a path outside", "/etc/hostname"],
    ["a dot-dot escape", "WS/../secret.txt"],
    ["a symlinked file pointing out", "WS/link.txt"],
    ["a new file under a symlinked dir pointing out", "WS/up/new.txt"],
  ] as const) {
    test(`refuses ${name}`, async () => {
      const p = path.replace("WS", ws);
      await assert.rejects(fs.read(p), OutsideWorkspaceError);
      await assert.rejects(fs.write(p, "x"), OutsideWorkspaceError);
    });
  }

  test("a sibling directory with the same prefix is outside", async () => {
    await mkdir(`${ws}2`);
    await assert.rejects(fs.write(join(`${ws}2`, "x.txt"), "x"), OutsideWorkspaceError);
  });
});
