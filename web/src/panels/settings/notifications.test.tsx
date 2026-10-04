import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { backupsListQueryKey, defaultsGetQueryKey, notificationRulesListQueryKey, notificationSettingsGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Backup, BackupList, Defaults, NotificationRule, NotificationSettings } from "@/api/gen/types.gen";
import { TooltipProvider } from "@/components/ui/tooltip";
import { PanelContext } from "@/shell/panel/context";
import { BackupsSection, canRestore, formatBytes } from "./BackupsSection";
import { NotificationsSection, parseChatId, silentApplies, timingsFor } from "./NotificationsSection";

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

const rule = (eventClass: NotificationRule["eventClass"], over: Partial<NotificationRule> = {}): NotificationRule => ({
  id: `ntr_${eventClass}`,
  eventClass,
  label: `${eventClass} label`,
  events: [],
  channels: { inApp: true, telegram: true },
  timing: eventClass === "digest" ? "daily" : "immediate",
  silent: eventClass === "outcome" || eventClass === "digest",
  bypassQuietHours: eventClass === "failure",
  rev: 1,
  updatedAt: "2026-09-30T00:00:00Z",
  departures: [],
  ...over,
});

const settings: NotificationSettings = {
  rev: 4,
  updatedAt: "2026-09-30T00:00:00Z",
  timezone: "Europe/Berlin",
  quietHours: { enabled: false, start: "22:00", end: "08:00" },
  digestTime: "09:00",
  telegram: { tokenSet: true, botUsername: "cadence_bot", chats: [{ id: 1001 }], pendingChats: [{ id: -2002, title: "Ops group", seenAt: "2026-09-30T08:00:00Z" }], polling: true },
};

beforeEach(() => {
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
  qc.setQueryData(defaultsGetQueryKey(), { version: 1 } as unknown as Defaults);
  runCommand.mockReset();
});
afterEach(() => cleanup());

describe("notifications: helpers", () => {
  it("offers daily only to the digest and reads chat ids", () => {
    expect(timingsFor(rule("digest"))).toEqual(["daily", "none"]);
    expect(timingsFor(rule("outcome"))).toEqual(["immediate", "digest", "none"]);
    expect(parseChatId(" -1001234 ")).toBe(-1001234);
    expect(parseChatId("0")).toBeUndefined();
    expect(parseChatId("12a")).toBeUndefined();
    expect(silentApplies(rule("outcome"))).toBe(true);
    expect(silentApplies(rule("outcome", { timing: "none" }))).toBe(false);
    expect(silentApplies(rule("failure", { channels: { inApp: true, telegram: false } }))).toBe(false);
    expect(silentApplies(rule("progress", { timing: "digest" }))).toBe(false);
  });
});

describe("notifications: the routing table", () => {
  it("edits a rule's channel with its revision", async () => {
    const outcome = rule("outcome");
    qc.setQueryData(notificationRulesListQueryKey(), { items: [rule("approval_requested"), rule("failure"), outcome] });
    qc.setQueryData(notificationSettingsGetQueryKey(), settings);
    runCommand.mockResolvedValue({ ...outcome, rev: 2, channels: { inApp: true, telegram: false }, departures: ["channels.telegram"] });
    wrap(<NotificationsSection />);
    expect(screen.getByText("· ignores quiet hours")).toBeTruthy();
    fireEvent.click(screen.getByLabelText("Telegram: outcome label"));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("notificationRules.edit", { rule: outcome, body: { channels: { telegram: false } } }));
    await waitFor(() => expect(screen.getByText("changed")).toBeTruthy());
  });

  it("makes a class ring or arrive silently", async () => {
    const failure = rule("failure");
    qc.setQueryData(notificationRulesListQueryKey(), { items: [failure, rule("outcome")] });
    runCommand.mockResolvedValue({ ...failure, rev: 2, silent: true, departures: ["silent"] });
    wrap(<NotificationsSection />);
    expect((screen.getByLabelText("Silent: outcome label") as HTMLInputElement).checked).toBe(true);
    fireEvent.click(screen.getByLabelText("Silent: failure label"));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("notificationRules.edit", { rule: failure, body: { silent: true } }));
    await waitFor(() => expect((screen.getByLabelText("Silent: failure label") as HTMLInputElement).checked).toBe(true));
  });

  it("switching progress onto Telegram holds it for the digest", async () => {
    const progress = rule("progress", { channels: { inApp: true, telegram: false }, timing: "none" });
    qc.setQueryData(notificationRulesListQueryKey(), { items: [progress] });
    runCommand.mockResolvedValue({ ...progress, rev: 2 });
    wrap(<NotificationsSection />);
    fireEvent.click(screen.getByLabelText("Telegram: progress label"));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("notificationRules.edit", { rule: progress, body: { channels: { telegram: true }, timing: "digest" } }));
  });
});

describe("notifications: the Telegram bot", () => {
  it("sends the token once as a write-only value and clears it", async () => {
    qc.setQueryData(notificationRulesListQueryKey(), { items: [] });
    qc.setQueryData(notificationSettingsGetQueryKey(), settings);
    runCommand.mockResolvedValue({ ...settings, rev: 5 });
    const { container } = wrap(<NotificationsSection />);
    const field = screen.getByLabelText("Replace the bot token") as HTMLInputElement;
    expect(field.type).toBe("password");
    fireEvent.change(field, { target: { value: "123456:ABCdefGHIjklMNOpqrSTUvwx" } });
    fireEvent.click(screen.getByRole("button", { name: "Replace" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("telegramBot.set", { settings, token: "123456:ABCdefGHIjklMNOpqrSTUvwx" }));
    await waitFor(() => expect(field.value).toBe(""));
    expect(container.innerHTML).not.toContain("ABCdefGHI");
  });

  it("allows a chat that wrote to the bot and sends a test message", async () => {
    qc.setQueryData(notificationRulesListQueryKey(), { items: [] });
    qc.setQueryData(notificationSettingsGetQueryKey(), settings);
    runCommand.mockImplementation((id: string) =>
      Promise.resolve(id === "telegramBot.verify" ? { ok: true, botUsername: "cadence_bot", chats: [{ id: 1001, delivered: true }] } : { ...settings, rev: 5 }),
    );
    wrap(<NotificationsSection />);
    expect(screen.getByText("Ops group")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Allow" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("notificationSettings.edit", { settings, body: { telegramChats: [1001, -2002] } }));
    fireEvent.click(screen.getByRole("button", { name: "Send test message" }));
    await waitFor(() => expect(screen.getByTestId("telegram-verify").textContent).toBe("Delivered to 1 chat as @cadence_bot."));
  });

  it("shows the timezone quiet hours follow", () => {
    qc.setQueryData(notificationRulesListQueryKey(), { items: [] });
    qc.setQueryData(notificationSettingsGetQueryKey(), settings);
    wrap(<NotificationsSection />);
    expect(screen.getByText("Europe/Berlin")).toBeTruthy();
    expect((screen.getByLabelText("Quiet from") as HTMLInputElement).disabled).toBe(true);
  });
});

describe("backups", () => {
  const set = (over: Partial<Backup>): Backup => ({ id: "bkp_1", state: "succeeded", trigger: "nightly", rev: 2, createdAt: "2026-09-30T03:00:00Z", dumpBytes: 1536, casCopied: 2, casBytesCopied: 2048, ...over });
  const list: BackupList = {
    items: [set({}), set({ id: "bkp_0", prunedAt: "2026-09-30T04:00:00Z" })],
    schedule: { directory: "/backups", nightlyAt: "03:00", restoreTestWeekday: "sunday", restoreTestAt: "04:00", keepNightly: 7, keepWeekly: 4, timezone: "UTC", nextBackupAt: "2026-10-01T03:00:00Z" },
    lastRestoreTest: {
      backupId: "bkp_1",
      report: { state: "failed", startedAt: "2026-09-30T04:00:00Z", durationMs: 2300, migrationVersion: 15, casChecked: 3, error: "table projects has 1 rows, the set recorded 2", tables: [{ name: "projects", backedUp: 2, restored: 1 }] },
    },
  };

  it("formats sizes and knows which sets can be restored", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KB");
    expect(canRestore(set({}))).toBe(true);
    expect(canRestore(set({ prunedAt: "x" }))).toBe(false);
    expect(canRestore(set({ restoreTest: { state: "running", startedAt: "" } }))).toBe(false);
  });

  it("shows the schedule and the last restore test, and backs up now", async () => {
    qc.setQueryData(backupsListQueryKey(), list);
    runCommand.mockResolvedValue({ jobId: "job_9" });
    wrap(<BackupsSection />);
    expect(screen.getByText("7 nightly + 4 weekly sets")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("the set recorded 2");
    expect(screen.getByText(/1 differ/)).toBeTruthy();
    const restore = screen.getAllByRole("button", { name: "Restore test" }) as HTMLButtonElement[];
    expect(restore.map((b) => b.disabled)).toEqual([false, true]);
    fireEvent.click(screen.getByRole("button", { name: "Back up now" }));
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("backups.new", undefined));
    await waitFor(() => expect(screen.getByRole("status").textContent).toBe("Backup queued (job job_9)."));
    fireEvent.click(restore[0]!);
    await waitFor(() => expect(runCommand).toHaveBeenCalledWith("backups.verify", { backup: list.items[0] }));
  });
});
