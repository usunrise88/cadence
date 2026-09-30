import type { Page } from "@playwright/test";
import type { AgentCredentialList } from "../src/api/gen/types.gen";
import { expect, newProject, openWorkspace, test } from "./fixtures";
import { ScriptedHost } from "./host";

// Settings → Agents end to end, with the spec playing the agent host (e2e/host.ts): the admin connects Claude Code
// with a setup-token, the value reaches the host through hostCredentials.claim (and nowhere else), the host
// acknowledges the write and a verification; then MiniMax is added, verified with its model list, chosen as the
// default opencode model, and removed.

test.setTimeout(120_000);

async function openAgents(page: Page) {
  await page.getByTestId("user-menu").click();
  await page.getByRole("menuitem", { name: "Settings" }).click();
  await page.getByRole("tab", { name: "Agents" }).click();
}

test("Agents: connect Claude Code, verify; add MiniMax, verify its models, choose the default, remove it", async ({ page, request }) => {
  const slug = await newProject(request, "Agents");
  const host = await ScriptedHost.connect();
  const token = `sk-ant-oat01-e2e${Date.now().toString(36)}xyzw`;
  const key = `mm-e2e-${Date.now().toString(36)}-key9`;
  await openWorkspace(page, slug);
  await openAgents(page);

  // Claude Code: the token goes in once; the card shows it pending until the host has written it.
  const claude = page.getByTestId("agents-claude");
  await expect(claude).toContainText("not connected");
  await claude.getByLabel("Token", { exact: true }).fill(token);
  await claude.getByRole("button", { name: "Connect" }).click();
  await expect(claude.getByLabel("Replace token", { exact: true })).toHaveValue("");
  await expect(claude).toContainText("pending");
  await expect(claude).toContainText("…xyzw");

  const write = await host.credentialTask((t) => t.credentialId === "claude-code" && t.action === "write");
  expect(write.value).toBe(token);
  expect(await host.reportCredential(write.id, { ok: true, detail: "written" })).toMatchObject({ state: "done" });
  await expect(claude).toContainText("In the agent host's credential volume");
  await expect(page.getByTestId("agents-host-banner")).toHaveCount(0);

  await claude.getByRole("button", { name: "Verify", exact: true }).click();
  const verify = await host.credentialTask((t) => t.credentialId === "claude-code" && t.action === "verify");
  expect(verify.value).toBeUndefined();
  await host.reportCredential(verify.id, { ok: true, model: "haiku", detail: "OK" });
  await expect(claude).toContainText("Verified with haiku");

  // opencode: MiniMax from the catalogue; its API host joins the egress allowlist.
  const add = page.getByRole("form", { name: "Add opencode provider" });
  await add.getByLabel("Provider").selectOption("minimax");
  await add.getByLabel(/API key|Token Plan key|key/i).first().fill(key);
  await add.getByRole("button", { name: "Add provider" }).click();
  const row = page.getByTestId("agents-provider-minimax");
  await expect(row).toContainText("pending");
  const mmWrite = await host.credentialTask((t) => t.credentialId === "opencode.minimax" && t.action === "write");
  expect(mmWrite.value).toBe(key);
  await host.reportCredential(mmWrite.id, { ok: true });
  await expect(row).toContainText("written");

  await row.getByRole("button", { name: /^Verify/ }).click();
  const mmVerify = await host.credentialTask((t) => t.credentialId === "opencode.minimax" && t.action === "verify");
  await host.reportCredential(mmVerify.id, { ok: true, model: "minimax/MiniMax-M3", detail: "OK", models: ["minimax/MiniMax-M3", "minimax/MiniMax-M2.7"] });
  await expect(row).toContainText("Verified with minimax/MiniMax-M3");
  await expect(row.locator("td").nth(4)).toHaveText("2");

  const model = page.getByLabel("Default model for new projects", { exact: true });
  await expect(model).toHaveValue("minimax/MiniMax-M3");
  await model.selectOption("minimax/MiniMax-M2.7");
  await page.getByRole("button", { name: "Use as default" }).click();
  await expect(page.getByText("Chosen here: minimax/MiniMax-M2.7")).toBeVisible();

  // Values never come back: not in the list, not in the events.
  const list = (await (await request.get("/api/agent-credentials")).json()) as AgentCredentialList;
  expect(list.opencodeDefault).toEqual({ model: "minimax/MiniMax-M2.7", source: "configured" });
  const everything = JSON.stringify(list) + (await (await request.get("/api/events", { params: { topics: "entity.agent_credential.*" } })).text());
  expect(everything).not.toContain(token);
  expect(everything).not.toContain(key);

  // Remove MiniMax: the host deletes it from the volume, then the row goes.
  await row.getByRole("button", { name: "Remove", exact: true }).click();
  await row.getByRole("button", { name: "Remove MiniMax" }).click();
  const remove = await host.credentialTask((t) => t.credentialId === "opencode.minimax" && t.action === "remove");
  await host.reportCredential(remove.id, { ok: true });
  await expect(row).toHaveCount(0);
  await host.close();
});
