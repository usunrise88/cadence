// The runaway rule (docs/spec/05-agents.md "Budgets and runaway protection"): the same tool called with the same
// arguments `limit` times in a row pauses the session. Each tool call counts once, when its arguments are known.

function canonical(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(canonical).join(",")}]`;
  if (typeof v === "object" && v !== null) {
    const entries = Object.entries(v as Record<string, unknown>).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
    return `{${entries.map(([k, x]) => `${JSON.stringify(k)}:${canonical(x)}`).join(",")}}`;
  }
  return JSON.stringify(v) ?? "undefined";
}

export class RunawayDetector {
  private readonly seen = new Set<string>();
  private last = "";
  private count = 0;

  constructor(readonly limit: number) {}

  // Records a tool call; returns the repeat count when it reaches the limit.
  observe(callId: string, tool: string, input: unknown): number | undefined {
    if (this.seen.has(callId) || input === undefined) return undefined;
    this.seen.add(callId);
    const sig = `${tool}\u0000${canonical(input)}`;
    this.count = sig === this.last ? this.count + 1 : 1;
    this.last = sig;
    return this.count >= this.limit ? this.count : undefined;
  }

  // A new message from the person starts a fresh count.
  reset(): void {
    this.last = "";
    this.count = 0;
  }
}
