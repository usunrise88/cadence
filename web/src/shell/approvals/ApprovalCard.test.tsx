import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ProblemError } from "@/api/client";
import type { Approval } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { ApprovalCard } from "./ApprovalCard";
import { useFocusedApproval } from "./store";
import { approval } from "./testdata";

const runCommand = vi.fn();
vi.mock("@/shell/commands/api", () => ({ runCommand: (...a: unknown[]) => runCommand(...a) }));

function renderCard(a: Approval, onDecided?: (a: Approval) => void) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <ApprovalCard approval={a} onDecided={onDecided} />
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

const card = () => screen.getByRole("article");

describe("ApprovalCard", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-09-30T12:00:00Z"));
    runCommand.mockReset();
  });
  afterEach(() => {
    cleanup();
    vi.useRealTimers();
  });

  it("shows a pending request: action, reason, requester, rule, estimate, scope and the expiry countdown", () => {
    renderCard(approval({ estimate: { gpuHours: 3, remainingGpuHours: 5 }, actor: { kind: "automation", id: "crd_1", name: "ci" } }));
    expect(screen.getByRole("heading", { name: "aliases.set" })).toBeTruthy();
    expect(card().textContent).toContain("changing a project's baseline");
    expect(card().textContent).toContain("Automation · ci");
    expect(card().textContent).toContain("baseline-alias");
    expect(card().textContent).toContain("3.0 GPU-h · 5.0 GPU-h left today");
    expect(card().textContent).toContain("PUT /api/projects/demo/aliases/baseline");
    expect(card().textContent).toContain("Expires in 22 h 0 min");
    expect(card().getAttribute("aria-describedby")).toBeTruthy();
  });

  it("tags registry-scope requests", () => {
    renderCard(approval({ scope: "registry", operation: "mounts.new" }));
    expect(card().textContent).toContain("Registry");
  });

  it("Enter on the focused card approves once; the result reaches onDecided", async () => {
    const decided = approval({ state: "approved", rev: 2, decision: { grant: "once" } });
    runCommand.mockResolvedValue(decided);
    const onDecided = vi.fn();
    renderCard(approval(), onDecided);
    fireEvent.keyDown(card(), { key: "Enter" });
    await waitFor(() => expect(onDecided).toHaveBeenCalledWith(decided));
    expect(runCommand).toHaveBeenCalledWith("approvals.approve", { approval: approval(), grant: "once", note: undefined });
  });

  it("Backspace on the focused card denies with the note", async () => {
    runCommand.mockResolvedValue(approval({ state: "denied", rev: 2 }));
    renderCard(approval());
    fireEvent.click(screen.getByRole("button", { name: "Add note" }));
    fireEvent.change(screen.getByRole("textbox"), { target: { value: "use the phone golden set first" } });
    fireEvent.keyDown(card(), { key: "Backspace" });
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("approvals.deny", { approval: approval(), note: "use the phone golden set first" }));
  });

  it("ignores Enter and Backspace typed inside the note", () => {
    renderCard(approval());
    fireEvent.click(screen.getByRole("button", { name: "Add note" }));
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Backspace" });
    fireEvent.keyDown(screen.getByRole("textbox"), { key: "Enter" });
    expect(runCommand).not.toHaveBeenCalled();
  });

  it("offers Approve for this session only to an agent session's request", async () => {
    renderCard(approval());
    expect((screen.getByRole("button", { name: "Approve for this session" }) as HTMLButtonElement).disabled).toBe(true);
    cleanup();
    runCommand.mockResolvedValue(approval({ state: "approved", rev: 2 }));
    renderCard(approval({ actor: { kind: "agent", id: "crd_a", sessionId: "ses_1" } }));
    const btn = screen.getByRole("button", { name: "Approve for this session" }) as HTMLButtonElement;
    expect(btn.disabled).toBe(false);
    fireEvent.click(btn);
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("approvals.approve", expect.objectContaining({ grant: "session" })));
  });

  it("a registry approval is decided once, even from an agent session", () => {
    renderCard(approval({ scope: "registry", projectId: undefined, actor: { kind: "agent", id: "crd_a", sessionId: "ses_1" } }));
    expect((screen.getByRole("button", { name: "Approve for this session" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("an expired request cannot be decided", () => {
    renderCard(approval({ expiresAt: "2026-09-30T11:00:00Z" }));
    expect(card().textContent).toContain("Expired");
    expect((screen.getByRole("button", { name: "Approve once" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.keyDown(card(), { key: "Enter" });
    expect(runCommand).not.toHaveBeenCalled();
  });

  it("a request decided elsewhere (412) says so", async () => {
    runCommand.mockRejectedValue(new ProblemError({ type: "https://cadence.local/help/errors/precondition-failed", title: "Precondition failed", status: 412, currentRev: 2 }));
    renderCard(approval());
    fireEvent.click(screen.getByRole("button", { name: "Approve once" }));
    expect((await screen.findByRole("alert")).textContent).toContain("changed meanwhile");
  });

  it("shows a decided request with its decision and no actions", () => {
    renderCard(
      approval({
        state: "approved",
        decision: { grant: "once", note: "ok" },
        decidedBy: { kind: "user", id: "usr_admin", name: "admin" },
        decidedAt: "2026-09-30T11:00:00Z",
        result: { status: 200, commandId: "cmd_1" },
      }),
    );
    expect(card().textContent).toContain("Approved once by admin");
    expect(card().textContent).toContain("Replay answered 200");
    expect(screen.queryByRole("button", { name: "Approve once" })).toBeNull();
  });

  it("remembers the focused card for the palette", () => {
    renderCard(approval());
    fireEvent.focus(card());
    expect(useFocusedApproval.getState().approval?.id).toBe("apr_0001");
  });
});
