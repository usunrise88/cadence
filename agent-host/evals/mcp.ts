// A minimal MCP client over Streamable HTTP for the scripted agent: initialize, then tools/call on the MCP session,
// with the headers the host put into ACP session/new (Authorization: the session token, Cadence-Project).

import type { ToolResult } from "./types.ts";

const PROTOCOL = "2025-11-25";

export class McpClient {
  private id = 1;
  private session: string | undefined;

  constructor(
    private readonly url: string,
    private readonly headers: Record<string, string>,
  ) {}

  private async post(body: unknown): Promise<Response> {
    const headers: Record<string, string> = {
      ...this.headers,
      "Content-Type": "application/json",
      Accept: "application/json, text/event-stream",
    };
    if (this.session) {
      headers["Mcp-Session-Id"] = this.session;
      headers["MCP-Protocol-Version"] = PROTOCOL;
    }
    return fetch(this.url, { method: "POST", headers, body: JSON.stringify(body) });
  }

  async connect(): Promise<void> {
    const res = await this.post({
      jsonrpc: "2.0",
      id: 0,
      method: "initialize",
      params: { protocolVersion: PROTOCOL, capabilities: {}, clientInfo: { name: "cadence-evals-scripted-agent", version: "0" } },
    });
    const text = await res.text();
    if (res.status !== 200) throw new Error(`MCP initialize answered ${res.status}: ${text.slice(0, 300)}`);
    this.session = res.headers.get("mcp-session-id") ?? undefined;
    await (await this.post({ jsonrpc: "2.0", method: "notifications/initialized" })).text();
  }

  /** Calls a tool. toolUseId goes where Claude Code puts it (_meta claudecode/toolUseId → causedBy.toolCallId). */
  async call(name: string, args: Record<string, unknown>, toolUseId?: string): Promise<{ result: ToolResult; isError: boolean }> {
    const params: Record<string, unknown> = { name, arguments: args };
    if (toolUseId) params._meta = { "claudecode/toolUseId": toolUseId };
    const res = await this.post({ jsonrpc: "2.0", id: this.id++, method: "tools/call", params });
    const text = await res.text();
    if (res.status !== 200) throw new Error(`MCP tools/call ${name} answered ${res.status}: ${text.slice(0, 300)}`);
    const json = parseRpc(res.headers.get("content-type") ?? "", text) as {
      result?: { content?: { text?: string }[]; isError?: boolean };
      error?: { message?: string };
    };
    if (json.error) throw new Error(`MCP tools/call ${name}: ${json.error.message ?? "error"}`);
    const body = json.result?.content?.[0]?.text ?? "{}";
    return { result: JSON.parse(body) as ToolResult, isError: !!json.result?.isError };
  }
}

/** The JSON-RPC message of a response: plain JSON, or the first `data:` line of an event stream. */
export function parseRpc(contentType: string, text: string): unknown {
  if (!contentType.includes("text/event-stream")) return JSON.parse(text);
  const line = text.split("\n").find((l) => l.startsWith("data:"));
  if (!line) throw new Error("MCP event stream without data");
  return JSON.parse(line.slice(5));
}
