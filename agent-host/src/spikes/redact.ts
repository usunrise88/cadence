// Redaction for recorded transcripts: the workspace path becomes /workspace (the replayer maps it back), the home
// directory /home/user, the MCP token and URL fixed placeholders, and anything shaped like an e-mail address or an
// account/organisation identifier is blanked.

export interface RedactOptions {
  cwd: string;
  home: string;
  token: string;
  mcpUrl: string;
}

export const WORKSPACE = "/workspace";
export const MCP_URL = "http://127.0.0.1:0/mcp";
export const TOKEN = "cst_REDACTED";

const EMAIL = /[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g;
const SECRET_KEYS = /^(email|emailAddress|accountUuid|organization|organizationUuid|organizationName|orgId|userId|accountId|apiKey)$/i;

export function redactor(o: RedactOptions): <T>(value: T) => T {
  const pairs: [string, string][] = [
    [o.token, TOKEN],
    [o.mcpUrl, MCP_URL],
    [o.cwd, WORKSPACE],
    [o.home, "/home/user"],
  ];
  const str = (s: string): string => {
    let out = s;
    for (const [from, to] of pairs) out = out.split(from).join(to);
    return out.replace(EMAIL, "user@example.com");
  };
  const walk = (v: unknown, key?: string): unknown => {
    if (typeof v === "string") return key && SECRET_KEYS.test(key) ? "REDACTED" : str(v);
    if (Array.isArray(v)) return v.map((x) => walk(x));
    if (typeof v === "object" && v !== null) {
      // The command list mirrors the user's installed skills and plugins: keep a sample, not the inventory.
      return Object.fromEntries(
        Object.entries(v).map(([k, x]) => [k, walk(k === "availableCommands" && Array.isArray(x) ? x.slice(0, 3) : x, k)]),
      );
    }
    return v;
  };
  return <T>(value: T): T => walk(value) as T;
}
