import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { computeListQueryKey, defaultsGetQueryKey, policiesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { ComputeHost, Credential, Defaults, Policies } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { capDefault, cardDraftError, cardEdits, cardFieldErrors, ComputeSection } from "./ComputeSection";
import { isActive, mergeCredential, TokenOnce } from "./CredentialsSection";
import { departureLabel, PoliciesSection } from "./PoliciesSection";
import { SecretForm } from "./SecretsSection";

const runCommand = vi.fn();
vi.mock("@/shell/panel/commands", async (orig) => ({ ...(await orig<object>()), runCommand: (...a: unknown[]) => runCommand(...a) }));

let qc: QueryClient;
function wrap(ui: ReactNode) {
  return render(
    <QueryClientProvider client={qc}>
      <TooltipProvider>
        <PanelContext.Provider value={{ instanceId: "settings", panelId: "settings", visible: false }}>{ui}</PanelContext.Provider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

const host: ComputeHost = {
  id: "cmp_1",
  name: "staging",
  description: "The shared staging host",
  health: { state: "unknown" },
  rev: 1,
  createdAt: "2026-09-30T00:00:00Z",
  updatedAt: "2026-09-30T00:00:00Z",
  cards: [{ index: 0, name: "Staging card", cardClass: "blackwell-48gb", memoryGb: 48, memoryCapGb: 24, allowedJobKinds: ["training", "eval", "shadow", "export"] }],
};

const defaults = {
  version: 1,
  wizard: {},
  timeouts: {},
  training: {},
  cache: {},
  estimates: { bytes_per_audio_hour: { value: 1, description: "", source: "" }, training: [] },
  budgets: {
    gpu_hours_per_project_per_day: { value: 8, unit: "GPU-h", description: "GPU-hours a project may spend per day", source: "Cadence recommendation", range: { min: 0, max: 192 } },
    agent_turns_per_session: { value: 200, unit: "turns", description: "Turns per session", source: "Cadence recommendation", range: { min: 1, max: 2000 } },
  },
  compute: { hosts: [{ name: "staging", description: "", source: "docs/spikes/A3", cards: [{ index: 0, name: "Staging card", card_class: "blackwell-48gb", memory_gb: 48, memory_cap_gb: 24, allowed_job_kinds: ["training"] }] }] },
} as unknown as Defaults;

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(defaultsGetQueryKey(), defaults);
  runCommand.mockReset();
});
afterEach(() => cleanup());

describe("secrets: the write-only value", () => {
  it("is a password field, is sent once and is cleared after the secret is stored", async () => {
    runCommand.mockResolvedValue({ id: "sec_1", name: "hf-token", kind: "huggingface", scope: "instance", rev: 1, actor: { kind: "user", id: "u" }, createdAt: "" });
    const { container } = wrap(<SecretForm projects={["demo"]} />);
    const value = screen.getByLabelText("Value") as HTMLInputElement;
    expect(value.type).toBe("password");
    expect(value.autocomplete).toBe("new-password");
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "hf-token" } });
    fireEvent.change(value, { target: { value: "hf_secret_value" } });
    fireEvent.click(screen.getByRole("button", { name: "Add secret" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("secrets.new", { body: { name: "hf-token", kind: "huggingface", scope: "instance", value: "hf_secret_value" } }));
    await waitFor(() => expect(value.value).toBe(""));
    expect(screen.getByRole("status").textContent).toContain("never shown again");
    expect(container.innerHTML).not.toContain("hf_secret_value");
  });

  it("checks the name before sending", () => {
    wrap(<SecretForm projects={[]} />);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "Bad Name" } });
    fireEvent.change(screen.getByLabelText("Value"), { target: { value: "x" } });
    expect(screen.getByRole("alert").textContent).toContain("Lowercase");
    expect((screen.getByRole("button", { name: "Add secret" }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("credentials", () => {
  const key: Credential = { id: "crd_1", kind: "api_key", name: "ci", scope: { project: "demo" }, rev: 1, createdAt: "2026-09-30T00:00:00Z" };
  it("active-only lists drop a revoked credential; lists with revoked keep it", () => {
    const revoked = { ...key, rev: 2, revokedAt: "2026-09-30T01:00:00Z" };
    expect(isActive(revoked)).toBe(false);
    expect(mergeCredential({ items: [key] }, revoked, false).items).toEqual([]);
    expect(mergeCredential({ items: [key] }, revoked, true).items).toEqual([revoked]);
  });
  it("shows the token once, with copy", () => {
    const onDone = vi.fn();
    wrap(<TokenOnce created={{ credential: key, token: "cdk_abcdef123456" }} onDone={onDone} />);
    expect((screen.getByLabelText("API key token") as HTMLInputElement).value).toBe("cdk_abcdef123456");
    fireEvent.click(screen.getByRole("button", { name: "I have stored it" }));
    expect(onDone).toHaveBeenCalled();
  });
  it("shows nothing for an idempotent replay without a token", () => {
    const { container } = wrap(<TokenOnce created={{ credential: key }} onDone={() => {}} />);
    expect(container.textContent).toBe("");
  });
});

describe("compute", () => {
  it("sends only the cards that changed", () => {
    expect(cardEdits(host, { 0: { memoryCapGb: "24", allowedJobKinds: ["training", "eval", "shadow", "export"] } })).toEqual([]);
    expect(cardEdits(host, { 0: { memoryCapGb: "32", allowedJobKinds: ["eval", "training", "shadow", "export"] } })).toEqual([{ index: 0, memoryCapGb: 32 }]);
    expect(cardEdits(host, { 0: { memoryCapGb: "24", allowedJobKinds: ["training"] } })).toEqual([{ index: 0, allowedJobKinds: ["training"] }]);
    // A wrongly seeded card is corrected: name, class and memory go out only when they changed.
    expect(cardEdits(host, { 0: { memoryCapGb: "22", allowedJobKinds: ["training", "eval", "shadow", "export", "data"], name: "Staging card", cardClass: "blackwell-48gb", memoryGb: "48" } })).toEqual([
      { index: 0, memoryCapGb: 22, allowedJobKinds: ["training", "eval", "shadow", "export", "data"] },
    ]);
    expect(cardEdits(host, { 0: { memoryCapGb: "24", allowedJobKinds: host.cards[0]!.allowedJobKinds, name: "RTX PRO 5000", cardClass: "rtx-pro-5000", memoryGb: "64" } })).toEqual([
      { index: 0, name: "RTX PRO 5000", cardClass: "rtx-pro-5000", memoryGb: 64 },
    ]);
    expect(cardDraftError({ memoryCapGb: "50", allowedJobKinds: [], memoryGb: "48" }, host.cards[0]!)).toMatch(/cannot exceed/);
    expect(cardDraftError({ memoryCapGb: "22", allowedJobKinds: [], cardClass: "Bad Class" }, host.cards[0]!)).toMatch(/class/);
    expect(cardDraftError({ memoryCapGb: "22", allowedJobKinds: [], memoryGb: "48" }, host.cards[0]!)).toBeUndefined();
  });

  it("explains the cap's default from the seeded host", () => {
    const d = capDefault(defaults.compute.hosts[0], host.cards[0]!);
    expect(d).toMatchObject({ value: 24, unit: "GB", source: "docs/spikes/A3", range: { min: 1, max: 48 } });
  });

  it("a 412 on save keeps the edit and offers both ways out", async () => {
    qc.setQueryData(computeListQueryKey(), { items: [host] });
    const { ProblemError } = await import("@/api/client");
    runCommand.mockRejectedValue(new ProblemError({ type: "https://cadence.local/help/errors/precondition-failed", title: "Precondition failed", status: 412, currentRev: 2 }));
    wrap(<ComputeSection />);
    fireEvent.click(screen.getByRole("button", { name: "Edit card 0" }));
    fireEvent.change(screen.getByLabelText("Memory cap of card 0 (GB)"), { target: { value: "32" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    const alert = await screen.findByTestId("compute-conflict");
    expect(alert.textContent).toContain("you started from rev 1; it is now rev 2");
    expect(runCommand).toHaveBeenCalledWith("compute.edit", { host, body: { cards: [{ index: 0, memoryCapGb: 32 }] } });
    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true);
    // Discard reloads the draft from the host as it is now and clears the banner.
    fireEvent.click(screen.getByRole("button", { name: "Discard mine and reload" }));
    expect(screen.queryByTestId("compute-conflict")).toBeNull();
    expect((screen.getByLabelText("Memory cap of card 0 (GB)") as HTMLInputElement).value).toBe("24");
  });

  it("edits availability windows per card and sends them only when they changed", async () => {
    const w = { training: [{ days: ["mon" as const], start: "20:00", end: "08:00" }] };
    expect(cardEdits(host, { 0: { memoryCapGb: "24", allowedJobKinds: host.cards[0]!.allowedJobKinds, windows: {} } })).toEqual([]);
    expect(cardEdits(host, { 0: { memoryCapGb: "24", allowedJobKinds: host.cards[0]!.allowedJobKinds, windows: w } })).toEqual([{ index: 0, windows: w }]);

    qc.setQueryData(computeListQueryKey(), { items: [host] });
    runCommand.mockResolvedValue({ ...host, rev: 2, cards: [{ ...host.cards[0]!, windows: w }] });
    wrap(<ComputeSection />);
    expect(screen.getByTestId("windows-0").textContent).toBe("any time");
    fireEvent.click(screen.getByRole("button", { name: "Edit card 0" }));
    expect(screen.getByTestId("windows-editor-0").textContent).toContain("every job kind may run at any time");
    fireEvent.click(screen.getByRole("button", { name: "Add window" }));
    const editor = screen.getByTestId("windows-editor-0");
    // A bad time blocks Save with the reason.
    fireEvent.change(screen.getByLabelText("Opens at"), { target: { value: "25:00" } });
    expect(editor.textContent).toContain("Opens at HH:MM");
    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true);
    fireEvent.change(screen.getByLabelText("Opens at"), { target: { value: "20:00" } });
    for (const d of ["Tue", "Wed", "Thu", "Fri"]) fireEvent.click(screen.getByLabelText(d));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(runCommand).toHaveBeenCalledWith("compute.edit", {
        host,
        body: { cards: [{ index: 0, windows: { training: [{ days: ["mon"], start: "20:00", end: "08:00" }] } }] },
      }),
    );
  });
});

describe("compute: the card dialog", () => {
  it("shows each validation message under its field and blocks Save", () => {
    expect(cardFieldErrors({ memoryCapGb: "50", allowedJobKinds: [], memoryGb: "48" }, host.cards[0]!)).toEqual({ memoryCapGb: "Cannot exceed the card's memory (48 GB)" });
    expect(cardFieldErrors({ memoryCapGb: "0", allowedJobKinds: [], name: " ", cardClass: "Bad" }, host.cards[0]!)).toMatchObject({ name: expect.any(String), cardClass: expect.any(String), memoryCapGb: "Must be above 0 GB" });
    expect(cardFieldErrors({ memoryCapGb: "22", allowedJobKinds: [], memoryGb: "48", name: "x", cardClass: "a-1" }, host.cards[0]!)).toEqual({});

    qc.setQueryData(computeListQueryKey(), { items: [host] });
    wrap(<ComputeSection />);
    fireEvent.click(screen.getByRole("button", { name: "Edit card 0" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("Edit card 0 · Staging card");
    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true); // nothing changed yet
    const cap = screen.getByLabelText("Memory cap of card 0 (GB)");
    fireEvent.change(cap, { target: { value: "50" } });
    expect(cap.getAttribute("aria-invalid")).toBe("true");
    const err = document.getElementById(cap.getAttribute("aria-describedby")!.split(" ")[0]!);
    expect(err?.textContent).toBe("Cannot exceed the card's memory (48 GB)");
    expect(screen.getByRole("button", { name: "Save" })).toHaveProperty("disabled", true);
    // Raising the card's memory makes the same cap valid.
    fireEvent.change(screen.getByLabelText("Card memory (GB)"), { target: { value: "64" } });
    expect(cap.getAttribute("aria-invalid")).toBeNull();
    const cls = screen.getByLabelText("Card class");
    fireEvent.change(cls, { target: { value: "Blackwell 48" } });
    expect(cls.getAttribute("aria-invalid")).toBe("true");
    expect(dialog.textContent).toContain("Lowercase letters, digits and dashes");
    expect(cls.getAttribute("aria-describedby")).toContain("-hint");
  });

  it("sends the job kinds that changed and closes; Cancel sends nothing", async () => {
    qc.setQueryData(computeListQueryKey(), { items: [host] });
    runCommand.mockResolvedValue({ ...host, rev: 2, cards: [{ ...host.cards[0]!, allowedJobKinds: ["training", "eval", "shadow", "export", "data"] }] });
    wrap(<ComputeSection />);
    fireEvent.click(screen.getByRole("button", { name: "Edit card 0" }));
    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(runCommand).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Edit card 0" }));
    const group = screen.getByRole("group", { name: "Allowed job kinds of card 0" });
    const data = screen.getByRole("checkbox", { name: /^data/ });
    expect(group.contains(data)).toBe(true);
    expect(data.getAttribute("aria-describedby")).toBeTruthy();
    fireEvent.click(data);
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("compute.edit", { host, body: { cards: [{ index: 0, allowedJobKinds: ["training", "eval", "shadow", "export", "data"] }] } }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("warns when no job kind is left", () => {
    qc.setQueryData(computeListQueryKey(), { items: [host] });
    wrap(<ComputeSection />);
    fireEvent.click(screen.getByRole("button", { name: "Edit card 0" }));
    for (const k of ["training", "eval", "shadow", "export"]) fireEvent.click(screen.getByRole("checkbox", { name: new RegExp(`^${k}`) }));
    expect(screen.getByRole("dialog").textContent).toContain("nothing will be scheduled on this card");
  });

  it("draws the cap against the card and the last memory report", () => {
    const reported = { ...host, cards: [{ ...host.cards[0]!, telemetry: { index: 0, memoryUsedMb: 24576, reportedAt: "2026-09-30T00:00:00Z" } }] };
    qc.setQueryData(computeListQueryKey(), { items: [reported] });
    wrap(<ComputeSection />);
    fireEvent.click(screen.getByRole("button", { name: "Edit card 0" }));
    const bar = screen.getByTestId("compute-memory-bar");
    expect(screen.getByRole("img", { name: /Card 48 GB; Cadence cap 24 GB; 24 GB left for resident services; 24 GB in use/ })).toBeTruthy();
    expect(bar.textContent).not.toContain("may not fit");
    fireEvent.change(screen.getByLabelText("Memory cap of card 0 (GB)"), { target: { value: "30" } });
    expect(bar.textContent).toContain("may not fit beside those services");
  });
});

describe("policies", () => {
  const policies: Policies = { rev: 3, updatedAt: "2026-09-30T00:00:00Z", budgets: { gpuHoursPerProjectPerDay: 12, agentTurnsPerSession: 200 }, timezone: "UTC", departures: ["budgets.gpuHoursPerProjectPerDay"] };

  it("labels departures, warns outside the safe range and resets to recommended", async () => {
    qc.setQueryData(policiesGetQueryKey(), policies);
    runCommand.mockResolvedValue({ ...policies, rev: 4, budgets: { gpuHoursPerProjectPerDay: 8, agentTurnsPerSession: 200 }, departures: [] });
    wrap(<PoliciesSection />);
    expect(departureLabel("budgets.gpuHoursPerProjectPerDay")).toBe("GPU-hours per project per day");
    expect(screen.getAllByText("GPU-hours per project per day").length).toBeGreaterThan(0);
    expect(screen.getByText(/Default 8 GPU-h · safe range 0–192 GPU-h/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("GPU-hours per project per day (GPU-h)"), { target: { value: "500" } });
    expect(screen.getByText("Above the safe range (max 192)")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reset to recommended" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("policies.edit", { policies, body: { budgets: { gpuHoursPerProjectPerDay: 8, agentTurnsPerSession: 200 } } }));
  });

  it("has a Why this default? popover per field", () => {
    qc.setQueryData(policiesGetQueryKey(), policies);
    wrap(<PoliciesSection />);
    expect(screen.getByRole("button", { name: "Why this default? (Agent turns per session)" })).toBeTruthy();
  });
});
