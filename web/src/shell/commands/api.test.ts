import { beforeAll, describe, expect, it, vi } from "vitest";
import type { AgentCredential } from "@/api/gen/types.gen";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import { registerApiCommands } from "./api";

// The agentCredentials commands send If-Match from the credential they act on; set sends none for a credential that
// does not exist yet or was archived (IfMatchOptional).

const sdk = vi.hoisted(() => ({
  agentCredentialsSet: vi.fn(),
  agentCredentialsVerify: vi.fn(),
  agentCredentialsArchive: vi.fn(),
}));
vi.mock("@/api/gen/sdk.gen", async (orig) => ({ ...(await orig<object>()), ...sdk }));

const c = { id: "claude-code", rev: 7 } as AgentCredential;

beforeAll(() => {
  if (!commands.get("agentCredentials.set")) registerApiCommands();
  for (const f of Object.values(sdk)) f.mockResolvedValue({ data: c });
});

describe("agentCredentials commands", () => {
  it("set: If-Match only for a live credential", async () => {
    await commands.run("agentCredentials.set", commandContext(), { id: "claude-code", credential: c, body: { value: "sk-ant-x" } });
    const live = sdk.agentCredentialsSet.mock.calls.at(-1)![0];
    expect(live.path).toEqual({ id: "claude-code" });
    expect(live.headers["If-Match"]).toBe('"7"');
    expect(live.headers["Idempotency-Key"]).toBeTruthy();
    await commands.run("agentCredentials.set", commandContext(), { id: "claude-code", credential: { ...c, archivedAt: "2026-09-30T00:00:00Z" }, body: { value: "sk-ant-y" } });
    expect(sdk.agentCredentialsSet.mock.calls.at(-1)![0].headers["If-Match"]).toBeUndefined();
    await commands.run("agentCredentials.set", commandContext(), { id: "opencode.minimax", body: { value: "k" } });
    expect(sdk.agentCredentialsSet.mock.calls.at(-1)![0].headers["If-Match"]).toBeUndefined();
  });

  it("verify and archive send the credential's rev", async () => {
    await commands.run("agentCredentials.verify", commandContext(), { credential: c });
    expect(sdk.agentCredentialsVerify.mock.calls.at(-1)![0]).toMatchObject({ path: { id: "claude-code" }, headers: { "If-Match": '"7"' } });
    await commands.run("agentCredentials.archive", commandContext(), { credential: c });
    expect(sdk.agentCredentialsArchive.mock.calls.at(-1)![0]).toMatchObject({ path: { id: "claude-code" }, headers: { "If-Match": '"7"' } });
  });
});
