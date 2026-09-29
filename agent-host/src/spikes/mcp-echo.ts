// Spike A1: a throwaway MCP server with one tool, `echo`, over streamable HTTP (JSON responses, no SSE stream),
// that refuses any request without `Authorization: Bearer <token>`. It records what it saw so the spike can tell
// whether each agent forwarded the `mcpServers` header from session/new.

import { createServer, type IncomingMessage, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export interface McpEchoLog {
  requests: number;
  unauthorized: number;
  methods: string[];
  calls: { text: string; reply: string }[];
}

export interface McpEcho {
  url: string;
  log: McpEchoLog;
  close: () => Promise<void>;
}

type Rpc = { jsonrpc: "2.0"; id?: string | number | null; method?: string; params?: Record<string, unknown> };

const TOOL = {
  name: "echo",
  description: "Echo the given text back, with a server nonce.",
  inputSchema: {
    type: "object",
    properties: { text: { type: "string", description: "Text to echo" } },
    required: ["text"],
  },
};

async function body(req: IncomingMessage): Promise<string> {
  let s = "";
  for await (const chunk of req) s += String(chunk);
  return s;
}

export async function startMcpEcho(token: string, nonce: string): Promise<McpEcho> {
  const log: McpEchoLog = { requests: 0, unauthorized: 0, methods: [], calls: [] };

  const answer = (msg: Rpc): unknown => {
    switch (msg.method) {
      case "initialize":
        return {
          protocolVersion: typeof msg.params?.protocolVersion === "string" ? msg.params.protocolVersion : "2025-06-18",
          capabilities: { tools: {} },
          serverInfo: { name: "a1-echo", version: "0.0.1" },
        };
      case "tools/list":
        return { tools: [TOOL] };
      case "tools/call": {
        const args = msg.params?.arguments as { text?: unknown } | undefined;
        const text = typeof args?.text === "string" ? args.text : "";
        const reply = `echo:${text}:${nonce}`;
        log.calls.push({ text, reply });
        return { content: [{ type: "text", text: reply }] };
      }
      case "ping":
        return {};
      default:
        return undefined;
    }
  };

  const handle = async (req: IncomingMessage, res: ServerResponse): Promise<void> => {
    log.requests++;
    if (req.headers.authorization !== `Bearer ${token}`) {
      log.unauthorized++;
      res.writeHead(401, { "content-type": "application/json" }).end('{"error":"missing or wrong bearer token"}');
      return;
    }
    if (req.method === "DELETE") return void res.writeHead(200).end();
    if (req.method !== "POST") return void res.writeHead(405, { allow: "POST, DELETE" }).end();
    const parsed = JSON.parse(await body(req)) as Rpc | Rpc[];
    const msgs = Array.isArray(parsed) ? parsed : [parsed];
    const replies: unknown[] = [];
    for (const m of msgs) {
      if (m.method) log.methods.push(m.method);
      if (m.id === undefined) continue; // notification
      const result = answer(m);
      replies.push(
        result === undefined
          ? { jsonrpc: "2.0", id: m.id, error: { code: -32601, message: `method not found: ${m.method}` } }
          : { jsonrpc: "2.0", id: m.id, result },
      );
    }
    if (replies.length === 0) return void res.writeHead(202).end();
    res
      .writeHead(200, { "content-type": "application/json", "mcp-session-id": "a1" })
      .end(JSON.stringify(Array.isArray(parsed) ? replies : replies[0]));
  };

  const server = createServer((req, res) => {
    handle(req, res).catch((err: unknown) => {
      res.writeHead(500).end(String(err));
    });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  const { port } = server.address() as AddressInfo;
  return {
    url: `http://127.0.0.1:${port}/mcp`,
    log,
    close: () =>
      new Promise((r) => {
        server.closeAllConnections();
        server.close(() => r());
      }),
  };
}
