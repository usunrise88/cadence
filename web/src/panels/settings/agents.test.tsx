import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { agentCredentialsListQueryKey, agentProvidersListQueryKey, defaultsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentCredential, AgentCredentialList, AgentProviderList, Defaults } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { AgentsSection, credentialForModel, defaultModelOptions, deliveryLabel, expiryState } from "./AgentsSection";

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

const DAY = 24 * 60 * 60 * 1000;
const iso = (offsetDays: number) => new Date(Date.now() + offsetDays * DAY).toISOString();

const providers: AgentProviderList = {
  items: [
    { id: "claude-subscription", agent: "claude-code", name: "Claude subscription", description: "", keyLabel: "Token", keyPrefix: "sk-ant-", keyRequired: true, custom: false, hosts: ["api.anthropic.com"], verifyModel: "haiku" },
    { id: "minimax", agent: "opencode", name: "MiniMax", description: "The Token Plan", keyLabel: "API key", keyRequired: true, custom: false, hosts: ["api.minimax.io"], defaultModel: "minimax/MiniMax-M3" },
    { id: "deepseek", agent: "opencode", name: "DeepSeek", description: "", keyLabel: "API key", keyRequired: true, custom: false, hosts: ["api.deepseek.com"] },
    { id: "openai-compatible", agent: "opencode", name: "OpenAI-compatible (custom base URL)", description: "", keyLabel: "API key", keyRequired: false, custom: true, hosts: [] },
  ],
};

function cred(over: Partial<AgentCredential> & Pick<AgentCredential, "id">): AgentCredential {
  const opencode = over.id.startsWith("opencode.");
  return {
    agent: opencode ? "opencode" : "claude-code",
    provider: opencode ? over.id.slice("opencode.".length) : "anthropic",
    catalogueId: opencode ? over.id.slice("opencode.".length) : "claude-subscription",
    name: opencode ? "MiniMax" : "Claude subscription",
    hasValue: true,
    hint: "…ab12",
    setAt: iso(-1),
    setBy: { kind: "user", id: "usr_admin", name: "admin" },
    delivery: { state: "written" },
    verification: { state: "none" },
    models: [],
    hosts: opencode ? ["api.minimax.io"] : ["api.anthropic.com"],
    rev: 3,
    createdAt: iso(-1),
    updatedAt: iso(-1),
    ...over,
  };
}

function list(items: AgentCredential[], connected = true, over: Partial<AgentCredentialList> = {}): AgentCredentialList {
  return { items, host: { connected }, opencodeDefault: { model: "minimax/MiniMax-M3", source: "defaults" }, ...over };
}

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(defaultsGetQueryKey(), { version: 1, wizard: { opencode_model: { value: "minimax/MiniMax-M3", description: "Model of opencode sessions", source: "R6" } } } as unknown as Defaults);
  qc.setQueryData(agentProvidersListQueryKey(), providers);
  runCommand.mockReset();
});
afterEach(() => cleanup());

describe("agents: helpers", () => {
  it("expiry warns 30 days ahead and after", () => {
    const now = Date.parse("2026-09-30T00:00:00Z");
    expect(expiryState(undefined, now)).toBeUndefined();
    expect(expiryState("2027-09-30T00:00:00Z", now)?.state).toBe("ok");
    expect(expiryState("2026-10-20T00:00:00Z", now)).toEqual({ state: "soon", days: 20 });
    expect(expiryState("2026-09-29T00:00:00Z", now)?.state).toBe("expired");
  });
  it("pending delivery waits for the agent host when none is connected", () => {
    const c = cred({ id: "claude-code", delivery: { state: "pending" } });
    expect(deliveryLabel(c, false)).toBe("Waiting for the agent host");
    expect(deliveryLabel(c, true)).toBe("Being written by the agent host");
  });
  it("default model options come from verified, configured opencode providers", () => {
    const items = [
      cred({ id: "opencode.minimax", models: ["minimax/MiniMax-M3", "minimax/MiniMax-M2.7"] }),
      cred({ id: "opencode.deepseek", models: ["deepseek/deepseek-v4-flash"], archivedAt: iso(0) }),
    ];
    expect(defaultModelOptions(items)).toEqual(["minimax/MiniMax-M2.7", "minimax/MiniMax-M3"]);
    expect(credentialForModel(items, "minimax/MiniMax-M3")?.id).toBe("opencode.minimax");
    expect(credentialForModel(items, "deepseek/deepseek-v4-flash")).toBeUndefined();
  });
});

describe("agents: Claude Code", () => {
  it("not connected: the banner, the how-to, and a write-only token field that is cleared after Connect", async () => {
    qc.setQueryData(agentCredentialsListQueryKey(), list([], false));
    const created = cred({ id: "claude-code", delivery: { state: "pending" }, rev: 1 });
    runCommand.mockResolvedValue(created);
    const { container } = wrap(<AgentsSection />);
    expect(screen.getByTestId("agents-host-banner").textContent).toContain("Waiting for the agent host");
    const card = screen.getByTestId("agents-claude");
    expect(within(card).getByText("not connected")).toBeTruthy();
    expect(within(card).getByText("claude setup-token")).toBeTruthy();
    const field = within(card).getByLabelText("Token") as HTMLInputElement;
    expect(field.type).toBe("password");
    expect(field.value).toBe("");
    fireEvent.change(field, { target: { value: "not-a-token" } });
    expect(within(card).getByRole("alert").textContent).toContain("sk-ant-");
    expect((within(card).getByRole("button", { name: "Connect" }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(field, { target: { value: "sk-ant-oat01-secretvalue" } });
    fireEvent.click(within(card).getByRole("button", { name: "Connect" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("agentCredentials.set", { id: "claude-code", credential: undefined, body: { value: "sk-ant-oat01-secretvalue" } }));
    expect(field.value).toBe("");
    await waitFor(() => expect(within(card).getByText("connected")).toBeTruthy());
    expect(container.innerHTML).not.toContain("secretvalue");
    expect(within(card).getAllByText("Waiting for the agent host").length).toBeGreaterThan(0);
  });

  it("connected: hint, expiry warning, last check; Verify and Disconnect send the credential (its rev is the If-Match)", async () => {
    const c = cred({ id: "claude-code", expiresAt: iso(10.5), verification: { state: "ok", model: "haiku", detail: "OK", at: iso(0) } });
    qc.setQueryData(agentCredentialsListQueryKey(), list([c]));
    runCommand.mockResolvedValue(c);
    wrap(<AgentsSection />);
    expect(screen.queryByTestId("agents-host-banner")).toBeNull();
    const card = screen.getByTestId("agents-claude");
    expect(within(card).getByText("…ab12")).toBeTruthy();
    expect(within(card).getByText(/expires in 10 days/)).toBeTruthy();
    expect(within(card).getByText("Verified with haiku")).toBeTruthy();
    expect(within(card).getByRole("button", { name: "Why this default? (Verify model)" })).toBeTruthy();
    expect(within(card).getByRole("button", { name: "Why this default? (Token lifetime)" })).toBeTruthy();
    expect((within(card).getByLabelText("Replace token") as HTMLInputElement).value).toBe("");
    fireEvent.click(within(card).getByRole("button", { name: "Verify" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("agentCredentials.verify", { credential: c }));
    fireEvent.click(within(card).getByRole("button", { name: "Disconnect" }));
    fireEvent.click(within(card).getByRole("button", { name: "Disconnect Claude Code" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("agentCredentials.archive", { credential: c }));
  });
});

describe("agents: opencode", () => {
  it("lists a verified provider with its models and saves MiniMax-M3 as the default on its credential", async () => {
    const mm = cred({ id: "opencode.minimax", models: ["minimax/MiniMax-M3", "minimax/MiniMax-M2.7"], verification: { state: "ok", model: "minimax/MiniMax-M3", at: iso(0) } });
    qc.setQueryData(agentCredentialsListQueryKey(), list([mm]));
    runCommand.mockResolvedValue({ ...mm, defaultModel: "minimax/MiniMax-M3", rev: 4 });
    wrap(<AgentsSection />);
    const row = screen.getByTestId("agents-provider-minimax");
    expect(within(row).getByText("2")).toBeTruthy();
    expect(within(row).getByText("api.minimax.io")).toBeTruthy();
    const select = screen.getByLabelText("Default model for new projects") as HTMLSelectElement;
    expect(select.value).toBe("minimax/MiniMax-M3");
    expect(screen.getByText(/From defaults.yaml/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Use as default" }));
    await waitFor(() =>
      expect(runCommand).toHaveBeenCalledWith("agentCredentials.set", { id: "opencode.minimax", credential: mm, body: { defaultModel: "minimax/MiniMax-M3" } }),
    );
  });

  it("adds a provider from the catalogue without offering configured ones; the custom entry asks id, name and base URL", async () => {
    const mm = cred({ id: "opencode.minimax" });
    qc.setQueryData(agentCredentialsListQueryKey(), list([mm]));
    runCommand.mockResolvedValue(cred({ id: "opencode.vllm", name: "vLLM", hasValue: false, delivery: { state: "pending" } }));
    const { container } = wrap(<AgentsSection />);
    const form = screen.getByRole("form", { name: "Add opencode provider" });
    const pick = within(form).getByLabelText("Provider") as HTMLSelectElement;
    expect([...pick.options].map((o) => o.value)).toEqual(["deepseek", "openai-compatible"]);
    fireEvent.change(pick, { target: { value: "openai-compatible" } });
    fireEvent.change(within(form).getByLabelText("Provider id"), { target: { value: "vllm" } });
    fireEvent.change(within(form).getByLabelText("Display name"), { target: { value: "vLLM" } });
    fireEvent.change(within(form).getByLabelText("Base URL"), { target: { value: "http://vllm.lan:8000/v1" } });
    fireEvent.change(within(form).getByLabelText("API key (optional)"), { target: { value: "local-key" } });
    fireEvent.click(within(form).getByRole("button", { name: "Add provider" }));
    await waitFor(() =>
      expect(runCommand).toHaveBeenCalledWith("agentCredentials.set", {
        id: "opencode.vllm",
        credential: undefined,
        body: { value: "local-key", catalogueId: "openai-compatible", baseUrl: "http://vllm.lan:8000/v1", name: "vLLM" },
      }),
    );
    expect(container.innerHTML).not.toContain("local-key");
  });

  it("replaces a key with If-Match and removes a provider after an inline confirm", async () => {
    const mm = cred({ id: "opencode.minimax", delivery: { state: "pending" } });
    qc.setQueryData(agentCredentialsListQueryKey(), list([mm], false));
    runCommand.mockResolvedValue(mm);
    wrap(<AgentsSection />);
    const row = screen.getByTestId("agents-provider-minimax");
    expect(within(row).getByText("Waiting for the agent host")).toBeTruthy();
    fireEvent.click(within(row).getByRole("button", { name: "Replace key" }));
    const field = screen.getByLabelText("New key for MiniMax") as HTMLInputElement;
    expect(field.value).toBe("");
    fireEvent.change(field, { target: { value: "mm-new-key" } });
    fireEvent.click(screen.getByRole("button", { name: "Save key" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("agentCredentials.set", { id: "opencode.minimax", credential: mm, body: { value: "mm-new-key" } }));
    fireEvent.click(within(row).getByRole("button", { name: "Remove" }));
    fireEvent.click(within(row).getByRole("button", { name: "Remove MiniMax" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("agentCredentials.archive", { credential: mm }));
  });
});
