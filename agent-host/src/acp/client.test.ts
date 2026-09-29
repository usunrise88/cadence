// AcpClient against an in-process ACP agent built with the SDK: permission answers, cancellation of a pending
// permission, and the file-system guard.

import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { after, before, describe, test } from "node:test";
import * as acp from "@agentclientprotocol/sdk";
import { AcpClient } from "./client.ts";

function pair(): [acp.Stream, acp.Stream] {
  const toAgent = new TransformStream<acp.AnyMessage, acp.AnyMessage>();
  const toClient = new TransformStream<acp.AnyMessage, acp.AnyMessage>();
  return [
    { readable: toClient.readable, writable: toAgent.writable },
    { readable: toAgent.readable, writable: toClient.writable },
  ];
}

type Script = (client: acp.AgentContext, sessionId: string, text: string) => Promise<acp.PromptResponse>;

function fakeAgent(stream: acp.Stream, script: Script): acp.AgentConnection {
  return acp
    .agent({ name: "fake" })
    .onRequest(acp.methods.agent.initialize, () => ({ protocolVersion: acp.PROTOCOL_VERSION, agentCapabilities: {} }))
    .onRequest(acp.methods.agent.session.new, () => ({ sessionId: "s1" }))
    .onRequest(acp.methods.agent.session.prompt, (ctx) => {
      const first = ctx.params.prompt[0];
      return script(ctx.client, ctx.params.sessionId, first?.type === "text" ? first.text : "");
    })
    .onNotification(acp.methods.agent.session.cancel, () => undefined)
    .connect(stream);
}

const askPermission = (client: acp.AgentContext, sessionId: string): Promise<acp.RequestPermissionResponse> =>
  client.request(acp.methods.client.session.requestPermission, {
    sessionId,
    toolCall: { toolCallId: "t1", title: "rm -rf /", kind: "execute" },
    options: [
      { optionId: "yes", name: "Allow", kind: "allow_once" },
      { optionId: "no", name: "Reject", kind: "reject_once" },
    ],
  });

describe("AcpClient", () => {
  let root: string;
  before(async () => {
    root = await mkdtemp(join(tmpdir(), "acp-client-"));
    await writeFile(join(root, "in.txt"), "inside");
  });
  after(() => rm(root, { recursive: true, force: true }));

  test("streams updates and answers permission requests through the host callback", async () => {
    const [c, a] = pair();
    const seen: string[] = [];
    const agent = fakeAgent(a, async (client, sessionId) => {
      await client.notify(acp.methods.client.session.update, {
        sessionId,
        update: { sessionUpdate: "agent_message_chunk", content: { type: "text", text: "hi" } },
      });
      const r = await askPermission(client, sessionId);
      return { stopReason: r.outcome.outcome === "selected" && r.outcome.optionId === "no" ? "refusal" : "end_turn" };
    });
    const client = new AcpClient(c, {
      onUpdate: (n) => seen.push(n.update.sessionUpdate),
      requestPermission: async (req) => ({
        outcome: { outcome: "selected", optionId: req.options.find((o) => o.kind === "reject_once")?.optionId ?? "" },
      }),
    });
    await client.initialize();
    const { sessionId } = await client.newSession({ cwd: root, mcpServers: [] });
    const res = await client.prompt(sessionId, [{ type: "text", text: "go" }]);
    assert.equal(res.stopReason, "refusal");
    assert.deepEqual(seen, ["agent_message_chunk"]);
    client.close();
    agent.close();
  });

  test("cancelling the turn answers a pending permission request with `cancelled`", async () => {
    const [c, a] = pair();
    let outcome: string | undefined;
    const agent = fakeAgent(a, async (client, sessionId) => {
      const r = await askPermission(client, sessionId);
      outcome = r.outcome.outcome;
      return { stopReason: "cancelled" };
    });
    let asked!: () => void;
    const permissionAsked = new Promise<void>((r) => (asked = r));
    const client = new AcpClient(c, {
      onUpdate: () => undefined,
      requestPermission: () => {
        asked();
        return new Promise(() => undefined); // a person who never answers
      },
    });
    await client.initialize();
    const { sessionId } = await client.newSession({ cwd: root, mcpServers: [] });
    const turn = client.prompt(sessionId, [{ type: "text", text: "go" }]);
    await permissionAsked;
    await client.cancel(sessionId);
    assert.equal((await turn).stopReason, "cancelled");
    assert.equal(outcome, "cancelled");
    client.close();
    agent.close();
  });

  test("fs methods serve the session cwd and refuse everything else", async () => {
    const [c, a] = pair();
    const results: string[] = [];
    const agent = fakeAgent(a, async (client, sessionId) => {
      for (const path of [join(root, "in.txt"), "/etc/hostname", join(root, "..", "x.txt")]) {
        try {
          const r = await client.request(acp.methods.client.fs.readTextFile, { sessionId, path });
          results.push(`ok:${r.content}`);
        } catch (err) {
          results.push(`err:${(err as { code?: number }).code}`);
        }
      }
      await client.request(acp.methods.client.fs.writeTextFile, { sessionId, path: join(root, "out.txt"), content: "w" });
      try {
        await client.request(acp.methods.client.fs.writeTextFile, { sessionId: "other", path: join(root, "o.txt"), content: "w" });
      } catch (err) {
        results.push(`err:${(err as { code?: number }).code}`);
      }
      return { stopReason: "end_turn" };
    });
    const client = new AcpClient(c, {
      onUpdate: () => undefined,
      requestPermission: async () => ({ outcome: { outcome: "cancelled" } }),
    });
    await client.initialize();
    const { sessionId } = await client.newSession({ cwd: root, mcpServers: [] });
    await client.prompt(sessionId, [{ type: "text", text: "go" }]);
    assert.deepEqual(results, ["ok:inside", "err:-32602", "err:-32602", "err:-32602"]);
    client.close();
    agent.close();
  });
});
