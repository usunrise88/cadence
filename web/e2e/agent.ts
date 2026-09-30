import { execFileSync } from "node:child_process";
import path from "node:path";
import { expect, request as playwrightRequest, type APIRequestContext, type Page } from "@playwright/test";
import { newProject } from "./fixtures";

// Acting as an agent session in the specs: a cst_ token minted on the e2e database (e2e/stack.sh builds the tool)
// and the MCP endpoint (/mcp) called the way Claude Code calls it — tools/call with its tool-use id in _meta.

export const API_URL = `http://127.0.0.1:${process.env.E2E_API_PORT ?? 18081}`;

/** Mints an agent session token for the project (test tooling; the agent-session stream mints its own). */
export function mintAgentToken(project: string, session: string): string {
  const bin = path.resolve(import.meta.dirname, "../.e2e/mint-agent-token");
  return execFileSync(bin, ["--project", project, "--session", session], { encoding: "utf8" }).trim();
}

/** The envelope of a Cadence tool result (internal/mcp). */
export type ToolResult = { operation: string; status: number; etag?: string; data?: unknown; error?: unknown; help?: string };

/** A minimal MCP client over Streamable HTTP: initialize, then tools/call on the session. */
export class McpAgent {
  private id = 1;
  private readonly ctx: APIRequestContext;
  private readonly session: string | undefined;
  private constructor(ctx: APIRequestContext, session: string | undefined) {
    this.ctx = ctx;
    this.session = session;
  }

  static async connect(token: string, project: string): Promise<McpAgent> {
    const ctx = await playwrightRequest.newContext({
      baseURL: API_URL,
      extraHTTPHeaders: { Authorization: `Bearer ${token}`, "Cadence-Project": project, Accept: "application/json, text/event-stream" },
    });
    const init = await ctx.post("/mcp", {
      data: { jsonrpc: "2.0", id: 0, method: "initialize", params: { protocolVersion: "2025-11-25", capabilities: {}, clientInfo: { name: "cadence-e2e", version: "0" } } },
    });
    expect(init.status(), await init.text()).toBe(200);
    const session = init.headers()["mcp-session-id"];
    const agent = new McpAgent(ctx, session);
    await ctx.post("/mcp", { data: { jsonrpc: "2.0", method: "notifications/initialized" }, headers: agent.sessionHeader() });
    return agent;
  }

  private sessionHeader(): Record<string, string> {
    return this.session ? { "Mcp-Session-Id": this.session, "MCP-Protocol-Version": "2025-11-25" } : {};
  }

  /** Calls a tool; toolUseId goes where Claude Code puts it (_meta claudecode/toolUseId → causedBy.toolCallId). */
  async call(name: string, args: Record<string, unknown>, toolUseId?: string): Promise<{ result: ToolResult; isError: boolean }> {
    const params: Record<string, unknown> = { name, arguments: args };
    if (toolUseId) params._meta = { "claudecode/toolUseId": toolUseId };
    const res = await this.ctx.post("/mcp", { data: { jsonrpc: "2.0", id: this.id++, method: "tools/call", params }, headers: this.sessionHeader() });
    const text = await res.text();
    expect(res.status(), text).toBe(200);
    const json = res.headers()["content-type"]?.includes("text/event-stream")
      ? JSON.parse(text.split("\n").find((l) => l.startsWith("data:"))!.slice(5))
      : JSON.parse(text);
    const r = json.result as { content: { text: string }[]; isError?: boolean };
    return { result: JSON.parse(r.content[0]!.text) as ToolResult, isError: !!r.isError };
  }

  async close(): Promise<void> {
    await this.ctx.dispose();
  }
}

export type MixBody = { id: string; rev: number; name: string };

/** A project with a mix over the fixture dataset versions, created through the API as a person. */
export async function projectWithMix(request: APIRequestContext, name = "he-smoke"): Promise<{ slug: string; mix: MixBody }> {
  const slug = await newProject(request, "Mix");
  const res = await request.post(`/api/projects/${slug}/mixes`, {
    data: { name, groups: [{ name: "target", datasets: ["dataset/fleurs-he-smoke"] }] },
    headers: { "Idempotency-Key": `mix-${slug}` },
  });
  expect(res.status(), await res.text()).toBe(201);
  return { slug, mix: (await res.json()) as MixBody };
}

/** Opens the mix as a document the way a person does: double-click in the Library. */
export async function openMix(page: Page, name: string): Promise<void> {
  await page.locator('[data-slot="entity-list"] [role="row"]', { hasText: name }).first().dblclick();
  await expect(page.locator('[data-panel="mix"] [data-slot="entity-header"]')).toContainText(name);
}
