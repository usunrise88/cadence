import type { AgentSession } from "../src/api/gen/types.gen";
import { openMix, projectWithMix } from "./agent";
import { expect, openWorkspace, test } from "./fixtures";
import { ScriptedHost } from "./host";

// Chat and the context bridge end to end, without a real agent: the spec plays the agent host through the host
// protocol (e2e/host.ts). New session from Chat with the selection attached → the streamed reply renders with its
// reference as a link → a permission request is allowed inline → a Cadence tool call drafts the mix and the draft's
// badge jumps to the tool call in Chat → the session ends and its changes are accepted into main.

test.setTimeout(120_000);

test("Chat: new session, streamed reply, inline permission, tool call with badge click-through, end and accept", async ({ page, request }) => {
  const { slug, mix } = await projectWithMix(request, "chat-mix");
  // Session changes wait for a person at the end (auto-merge never), so the merge area has something to accept.
  const profile = await request.get(`/api/projects/${slug}/agent-profile`);
  expect(profile.status(), await profile.text()).toBe(200);
  const edited = await request.patch(`/api/projects/${slug}/agent-profile`, {
    data: { autoMerge: "never" },
    headers: { "If-Match": `"${((await profile.json()) as { rev: number }).rev}"`, "Idempotency-Key": `profile-${slug}` },
  });
  expect(edited.status(), await edited.text()).toBe(200);
  const host = await ScriptedHost.connect();

  await openWorkspace(page, slug);
  await page.locator('[data-tab="chat"]').click();
  const chat = page.locator('[data-panel="chat"]').first();
  await expect(chat.getByRole("button", { name: "Start session" })).toBeVisible();

  // Ctrl/Cmd+I on the open mix: Chat takes focus with the mix attached as a reference chip.
  await openMix(page, mix.name);
  await page.keyboard.press("ControlOrMeta+i");
  const composer = chat.locator('[data-slot="composer"]');
  await expect(composer.locator(`[data-slot="reference-chip"][data-ref="@mix:${mix.id}"]`)).toBeVisible();
  await expect(chat.getByLabel("Message to the agent")).toBeFocused();
  await page.keyboard.type("Make the mix warmer");
  await page.keyboard.press("Enter");

  // The workspace's Chat is pinned to the new session.
  const root = chat.locator("[data-chat-session]");
  await expect(root).toHaveAttribute("data-chat-session", /^ses_/);
  const sessionId = (await root.getAttribute("data-chat-session"))!;
  const session = (await (await request.get(`/api/agent-sessions/${sessionId}`)).json()) as AgentSession;
  const label = `${session.driver} · session ${session.number}`;
  // The tab names the session the short way.
  const chatTab = page.locator('[data-tab="chat"]');
  await expect(chatTab.locator('[data-slot="chat-tab-label"]')).toHaveText(`${session.driver === "opencode" ? "OC" : "CC"} · S${session.number}`);
  await expect(chat.locator('[data-kind="user_message"]')).toContainText("Make the mix warmer");
  await expect(chat.locator(`[data-kind="user_message"] [data-ref="@mix:${mix.id}"]`)).toBeVisible();

  // The host takes the session and the message; the turn streams.
  await host.start(sessionId);
  expect(await host.message(sessionId)).toBe("Make the mix warmer");
  await host.report(sessionId, { state: { state: "running", busy: true, turn: 1 }, entries: [{ key: "t1:start", kind: "turn", turn: 1, turnInfo: { state: "started" } }] });
  await expect(chat.locator('[data-slot="session-state"]')).toHaveText("running");
  const tabIcon = chatTab.locator('[data-slot="chat-tab-icon"]');
  await expect(tabIcon).toHaveAttribute("data-tone", "working");
  await host.entries(sessionId, 1, [{ key: "t1:m1", kind: "agent_message", text: "Looking at", final: false }]);
  const reply = chat.locator('[data-kind="agent_message"]');
  await expect(reply).toContainText("Looking at");
  await host.entries(sessionId, 1, [{ key: "t1:m1", kind: "agent_message", text: `Looking at @mix:${mix.id}: I will raise the **temperature**.`, final: true }]);
  await expect(reply.locator('[data-streamdown="strong"]')).toHaveText("temperature");
  await expect(reply.locator(`[data-ref="@mix:${mix.id}"]`)).toBeVisible();

  // A permission request the preset cannot answer: the approval card inline, allowed once from Chat.
  const ask = await host.ask(sessionId, 1, { id: "toolu_perm", title: "Open the dataset card in a browser", class: "other", status: "pending" });
  expect(ask.outcome).toBe("pending");
  const card = chat.locator(`[data-approval-card="${ask.approvalId}"]`);
  await expect(card).toBeVisible();
  await expect(chat.locator('[data-slot="session-state"]')).toHaveAttribute("data-state", "waiting_approval");
  await card.getByRole("button", { name: "Allow once" }).click();
  expect((await host.decision(sessionId, ask.approvalId!)).outcome).toBe("allow_once");
  await expect(card).toHaveAttribute("data-state", "approved");

  // A Cadence tool call through MCP with the session token: the edit lands as a draft on the mix.
  const agent = await host.mcp(sessionId, slug);
  const read = await agent.call("mixes.get", { id: mix.id });
  const edit = await agent.call("mixes.edit", { id: mix.id, ifMatch: read.result.etag, body: { temperature: 2 } }, "toolu_mix");
  expect(edit.isError, JSON.stringify(edit.result)).toBe(false);
  await host.entries(sessionId, 1, [
    {
      key: "tool:toolu_mix",
      kind: "tool_call",
      toolCall: { id: "toolu_mix", title: "mcp__cadence__mixes.edit", class: "mcp", status: "completed", operation: "mixes.edit", server: "cadence", input: { id: mix.id, body: { temperature: 2 } }, output: edit.result },
    },
  ]);
  const toolCall = chat.locator('[data-tool-call="toolu_mix"]');
  // Collapsed to one line by default; a click opens the card.
  await expect(toolCall.locator('[data-slot="tool-operation"]')).toHaveText("mixes.edit");
  const toolLine = toolCall.locator('[data-slot="tool-line"]');
  await expect(toolLine).toHaveAttribute("aria-expanded", "false");
  await expect(toolCall.locator('[data-slot="tool-draft"]')).toHaveCount(0);
  await toolLine.click();
  await expect(toolCall.locator(`[data-ref="@mix:${mix.id}"]`)).toBeVisible();
  await expect(toolCall.locator('[data-slot="tool-draft"]')).toBeVisible();
  await toolLine.click();
  await expect(toolLine).toHaveAttribute("aria-expanded", "false");

  // The draft on the open Mix carries the session's badge; its click jumps to the tool call in Chat.
  const draft = page.locator('[data-panel="mix"] [data-slot="draft"]');
  await expect(draft).toBeVisible();
  const badge = draft.locator('[data-slot="actor-badge"]');
  await expect(badge).toHaveText(label);
  await expect(badge).toHaveAttribute("data-tool-call", "toolu_mix");
  await chat.locator('[data-testid="chat-transcript"]').evaluate((el) => (el.scrollTop = 0));
  await badge.click();
  await expect(toolCall).toHaveAttribute("data-highlighted", "true");
  await expect(toolCall).toBeFocused();
  await expect(toolLine).toHaveAttribute("aria-expanded", "true");

  // A click in the transcript, then typing: the keys go to the composer.
  const input = chat.getByLabel("Message to the agent");
  await reply.click({ position: { x: 2, y: 2 } });
  await page.keyboard.type("Thanks");
  await expect(input).toBeFocused();
  await expect(input).toHaveValue("Thanks");
  await input.fill("");
  await agent.close();

  // The turn ends with a commit on the session branch; the finished turn reaches the live region.
  const sha = host.commit(sessionId, slug, "NOTES-agent.md", "Warmer mixes help the phone golden set.\n");
  await host.entries(sessionId, 1, [
    { key: "t1:commit", kind: "commit", commit: { sha, files: ["NOTES-agent.md"] } },
    { key: "t1:end", kind: "turn", turnInfo: { state: "ended", stopReason: "end_turn", inputTokens: 1200, outputTokens: 80 } },
  ]);
  await expect(chat.locator('[data-slot="commit"]')).toContainText(sha.slice(0, 7));
  // The turn finishes while another tab hides the Chat: its tab gets the unread dot until the Chat is shown again.
  await page.locator('[data-tab="inspector"]').click();
  await host.report(sessionId, { state: { state: "running", busy: false, turn: 1 }, use: { turns: 1, inputTokens: 1200, outputTokens: 80 } });
  await expect(page.getByTestId("live-region")).toContainText(`${label} finished its turn`);
  const unread = chatTab.locator('[data-slot="chat-tab-unread"]');
  await expect(unread).toBeVisible();
  await expect(tabIcon).toHaveAttribute("data-tone", "ready");
  await chatTab.click();
  await expect(unread).toHaveCount(0);

  // End the session: the host is told, commits nothing more and reports done; the changes wait for a person.
  await chat.getByRole("button", { name: "End session…" }).click();
  await chat.getByRole("button", { name: "End the session" }).click();
  await host.control(sessionId, "end");
  await host.report(sessionId, { state: { state: "done" } });
  const merge = chat.locator('[data-slot="merge-area"]');
  await expect(merge).toHaveAttribute("data-merge", "pending");
  await expect(merge.getByLabel("Changed files")).toContainText("NOTES-agent.md");
  await merge.getByRole("button", { name: "Accept into main" }).click();
  await expect(merge).toHaveAttribute("data-merge", "merged");
  await expect(merge).toContainText("Merged into main");

  // The workspace remembers which session its Chat is pinned to.
  await expect
    .poll(async () => {
      const ws = (await (await request.get(`/api/me/projects/${slug}/workspaces/Training`)).json()) as { panels: Record<string, { pinnedTo?: string }> };
      return ws.panels.chat?.pinnedTo;
    })
    .toBe(`agent_session:${sessionId}`);
  await page.reload();
  await expect(page.locator('[data-panel="chat"] [data-chat-session]').first()).toHaveAttribute("data-chat-session", sessionId);
  await host.close();
});
