import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { approvalsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentMessage, Approval } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { message } from "@/shell/agents/testdata";
import { Entry, RefChips } from "./entries";

const openReference = vi.fn();
vi.mock("@/shell/agents/bridge", async (orig) => ({ ...(await orig<object>()), openReference: (r: string) => openReference(r) }));

let qc: QueryClient;
function wrap(ui: ReactNode) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>{ui}</TooltipProvider>
    </QueryClientProvider>,
  );
}
const entry = (m: AgentMessage) => wrap(<Entry m={m} highlighted={false} />);

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  openReference.mockReset();
});
afterEach(() => cleanup());

describe("Chat entries by kind", () => {
  it("agent message: Markdown with entity references as links that open the document", async () => {
    entry(message({ kind: "agent_message", text: "I edited **the mix** @mix:mix_1 for you.", final: true }));
    expect(await screen.findByText("the mix")).toBeTruthy();
    const link = document.querySelector<HTMLButtonElement>('[data-ref="@mix:mix_1"]')!;
    expect(link.textContent).toBe("@mix:mix_1");
    fireEvent.click(link);
    expect(openReference).toHaveBeenCalledWith("@mix:mix_1");
  });

  it("streaming text is marked busy until it is final", () => {
    entry(message({ id: "s1", kind: "agent_message", text: "Working on", final: false }));
    expect(document.querySelector('[aria-busy="true"]')).not.toBeNull();
  });

  it("thought: a collapsed thinking block", () => {
    entry(message({ kind: "thought", text: "Let me look at the groups first", final: true }));
    const details = document.querySelector<HTMLDetailsElement>('[data-slot="thought"]')!;
    expect(details.open).toBe(false);
    expect(details.textContent).toContain("Thinking");
  });

  it("plan: a checklist that shows what is done", () => {
    entry(
      message({
        kind: "plan",
        plan: [
          { content: "Read the mix", status: "completed" },
          { content: "Edit the temperature", status: "in_progress" },
          { content: "Report", status: "pending" },
        ],
      }),
    );
    expect(screen.getByRole("region", { name: "Plan, 1 of 3 done" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "done" })).toBeTruthy();
    expect(screen.getByRole("img", { name: "in progress" })).toBeTruthy();
  });

  it("tool calls: one collapsed line that opens into the card on click", () => {
    entry(message({ kind: "tool_call", toolCall: { id: "toolu_1", title: "mixes.get", class: "mcp", status: "completed", operation: "mixes.get", input: { id: "mix_1" } } }));
    const card = document.querySelector('[data-tool-call="toolu_1"]')!;
    const line = card.querySelector<HTMLButtonElement>('[data-slot="tool-line"]')!;
    expect(line.textContent).toBe("mixes.get");
    expect(line.getAttribute("aria-expanded")).toBe("false");
    expect(card.textContent).not.toContain("Arguments");
    fireEvent.click(line);
    expect(line.getAttribute("aria-expanded")).toBe("true");
    expect(card.textContent).toContain("Arguments");
    fireEvent.click(line);
    expect(card.textContent).not.toContain("Arguments");
  });

  it("a highlighted tool call (the badge's jump) opens itself", () => {
    wrap(<Entry m={message({ kind: "tool_call", toolCall: { id: "toolu_2", title: "mixes.edit", class: "mcp", status: "completed", input: { id: "mix_1" } } })} highlighted />);
    expect(document.querySelector('[data-tool-call="toolu_2"] [data-slot="tool-line"]')!.getAttribute("aria-expanded")).toBe("true");
  });

  it("Cadence tool call: operation, entity link, draft and dry-run estimate", () => {
    entry(
      message({
        kind: "tool_call",
        toolCall: {
          id: "toolu_9",
          title: "mcp__cadence__runs.new",
          class: "mcp",
          status: "completed",
          operation: "runs.new",
          input: { dryRun: true, body: { init: "base" } },
          output: { operation: "runs.new", status: 200, data: { gpuHours: { value: 2, low: 1, high: 3 }, plusMinus: 0.5 } },
        },
      }),
    );
    const card = document.querySelector('[data-tool-call="toolu_9"]')!;
    expect(card.querySelector('[data-slot="tool-operation"]')!.textContent).toBe("runs.new");
    expect(card.querySelector('[data-slot="tool-line"]')!.textContent).toContain("dry run");
    fireEvent.click(card.querySelector('[data-slot="tool-line"]')!);
    expect(card.querySelector('[data-slot="dry-run"]')!.textContent).toBe("Dry run: 2.0 GPU-h ±50%");
    entry(
      message({
        kind: "tool_call",
        toolCall: { id: "toolu_10", title: "mixes.edit", class: "mcp", status: "completed", operation: "mixes.edit", input: { id: "mix_1" }, output: { status: 200, data: { draft: { id: "drf_1", rev: 3 } } } },
      }),
    );
    fireEvent.click(document.querySelector('[data-tool-call="toolu_10"] [data-slot="tool-line"]')!);
    expect(document.querySelector('[data-tool-call="toolu_10"] [data-ref="@mix:mix_1"]')).not.toBeNull();
    expect(document.querySelector('[data-tool-call="toolu_10"] [data-slot="tool-draft"]')!.textContent).toContain("Draft rev 3");
  });

  it("file edit: +/− on the line and an inline diff; shell: command and exit code on the line, output inside", () => {
    entry(message({ kind: "tool_call", toolCall: { id: "e1", title: "Edit NOTES.md", class: "edit", status: "completed", diffs: [{ path: "NOTES.md", oldText: "a\nb\n", newText: "a\nc\n" }] } }));
    const edit = document.querySelector('[data-tool-call="e1"] [data-slot="tool-line"]')!;
    expect(edit.textContent).toBe("Edit NOTES.md+1 −1");
    fireEvent.click(edit);
    expect(screen.getByLabelText("Diff of NOTES.md").textContent).toContain("removed: b");
    entry(message({ kind: "tool_call", toolCall: { id: "s1", title: "ls", class: "shell", status: "failed", shell: { command: "ls /nope", exitCode: 2, output: "ls: cannot access" } } }));
    expect(document.querySelector('[data-slot="shell-command"]')!.textContent).toBe("$ ls /nope");
    expect(screen.getByText("exit 2")).toBeTruthy();
    expect(document.querySelector('[data-slot="shell-output"]')).toBeNull();
    fireEvent.click(document.querySelector('[data-tool-call="s1"] [data-slot="tool-line"]')!);
    expect(document.querySelector('[data-slot="shell-output"]')!.textContent).toBe("ls: cannot access");
  });

  it("permission: the approval card inline with Allow once / for the session / Deny", () => {
    const approval = {
      id: "apr_1",
      state: "pending",
      scope: "project",
      operation: "agent.permission",
      actor: { kind: "agent", id: "crd_1", sessionId: "ses_1" },
      rule: "shell.ask",
      reason: "The agent wants to run a shell command",
      request: { method: "POST", path: "/host-sessions/ses_1:ask", headers: {} },
      rev: 1,
      createdAt: new Date().toISOString(),
      expiresAt: new Date(Date.now() + 86_400_000).toISOString(),
      kind: "agent_permission",
      permission: { sessionId: "ses_1", toolCallId: "toolu_3", title: "Run pytest", class: "shell", command: "pytest -q", options: ["allow_once", "allow_always", "reject_once"] },
    } as Approval;
    qc.setQueryData(approvalsGetQueryKey({ path: { id: "apr_1" } }), approval);
    entry(message({ kind: "permission", permission: { source: "agent", approvalId: "apr_1", toolCallId: "toolu_3", state: "pending" } }));
    expect(screen.getByRole("button", { name: "Allow once" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Allow for this session" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Deny" })).toBeTruthy();
    expect(screen.getByText("$ pytest -q")).toBeTruthy();
  });

  it("permission answered by the preset: one line with the rule", () => {
    entry(message({ kind: "permission", permission: { source: "agent", title: "Read data.lock", state: "approved", rule: "read.allow" } }));
    expect(document.querySelector('[data-slot="permission-line"]')!.textContent).toContain("allowed by the preset rule read.allow");
  });

  it("commit: files, or the refusal with its findings", () => {
    entry(message({ kind: "commit", commit: { sha: "abcdef1234", files: ["NOTES.md", "pipelines/train.yaml"] } }));
    expect(document.querySelector('[data-slot="commit"]')!.textContent).toContain("abcdef1 · 2 files");
    entry(message({ kind: "commit", commit: { refused: true, findings: [{ path: ".env", kind: "cst_", line: 3 }] } }));
    expect(screen.getByText("Nothing committed: the changes held a credential")).toBeTruthy();
    expect(screen.getByText(".env:3 — cst_")).toBeTruthy();
  });

  it("turn end: usage; a user message: its reference chips", () => {
    entry(message({ kind: "turn", turn: 2, turnInfo: { state: "ended", stopReason: "end_turn", inputTokens: 1500, outputTokens: 200 } }));
    expect(document.querySelector('[data-slot="turn-end"]')!.textContent).toContain("Turn 2 · finished · 1.5k in / 200 out");
    entry(message({ kind: "user_message", actor: { kind: "user", id: "usr_admin", name: "admin" }, text: "Look at this", references: [{ ref: "@mix:mix_1", label: "mix he-smoke" }], delivery: "pending" }));
    expect(screen.getByText("queued for the agent")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "mix he-smoke" }));
    expect(openReference).toHaveBeenCalledWith("@mix:mix_1");
  });
});

describe("reference chips", () => {
  it("show labels, open on click and remove with a 24 px button", () => {
    const remove = vi.fn();
    wrap(<RefChips refs={[{ ref: "@run:123" }, { ref: "@mix:mix_1", label: "mix he-smoke" }]} onRemove={remove} />);
    expect(screen.getAllByRole("listitem").map((li) => li.getAttribute("data-ref"))).toEqual(["@run:123", "@mix:mix_1"]);
    fireEvent.click(screen.getByRole("button", { name: "Remove @run:123" }));
    expect(remove).toHaveBeenCalledWith("@run:123");
    expect(screen.getByRole("button", { name: "Remove @mix:mix_1" }).className).toContain("size-6");
  });
});
