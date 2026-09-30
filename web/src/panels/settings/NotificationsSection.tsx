import { useId, useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  notificationRulesListOptions,
  notificationRulesListQueryKey,
  notificationSettingsGetOptions,
  notificationSettingsGetQueryKey,
} from "@/api/gen/@tanstack/react-query.gen";
import type { NotificationRule, NotificationRuleEdit, NotificationRuleList, NotificationSettings, NotificationTiming, TelegramBotVerify } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { errorMessage, problemOf, runCommand, useTopic } from "@/shell/panel";
import { WriteOnlyField } from "./SecretsSection";
import { Chip, Field, SectionHeading, Table, Td, when } from "./ui";

// Notifications (docs/spec/06-platform.md "Notifications"; docs/spec/11-ui-panels.md "Settings"): the routing table
// (one row per event class: channels and timing), quiet hours and the digest time, and the Telegram bot — its
// write-only token, the allow-listed chats, chats that wrote without being allowed, and a test message.

const TOKEN = /^[0-9]+:[A-Za-z0-9_-]{20,}$/;

/** The timings a rule may take: the digest row is daily or off; the others are immediate, held for the digest, or off. */
export function timingsFor(rule: NotificationRule): NotificationTiming[] {
  return rule.eventClass === "digest" ? ["daily", "none"] : ["immediate", "digest", "none"];
}

export const TIMING_LABEL: Record<NotificationTiming, string> = {
  immediate: "As it happens",
  digest: "In the daily digest",
  daily: "Daily at the digest time",
  none: "Never",
};

export function upsertRule(list: NotificationRuleList | undefined, r: NotificationRule): NotificationRuleList | undefined {
  return list ? { ...list, items: list.items.map((x) => (x.id === r.id ? r : x)) } : list;
}

/** Parses a chat id as typed ("-1001234", "42"); undefined when it is not one. */
export function parseChatId(s: string): number | undefined {
  const t = s.trim();
  if (!/^-?[0-9]{1,16}$/.test(t)) return undefined;
  const n = Number(t);
  return n === 0 ? undefined : n;
}

export function NotificationsSection() {
  const qc = useQueryClient();
  const rules = useQuery(notificationRulesListOptions());
  const settings = useQuery(notificationSettingsGetOptions());
  useTopic(["entity.notification_rule.*", "entity.notification_settings.*"], (batch) => {
    for (const e of batch) {
      const rule = (e.payload as { rule?: NotificationRule } | undefined)?.rule;
      if (rule) qc.setQueryData<NotificationRuleList>(notificationRulesListQueryKey(), (old) => upsertRule(old, rule));
      else void qc.invalidateQueries({ queryKey: notificationSettingsGetQueryKey() });
    }
  });
  return (
    <section aria-labelledby="settings-notifications" className="flex flex-col gap-4">
      <SectionHeading
        id="settings-notifications"
        title="Notifications"
        hint="Which events reach you where: the in-app history and a Telegram bot. Quiet hours hold Telegram back except for failures; approvals can be decided from the phone."
      />
      {rules.data ? <RulesTable rules={rules.data.items} /> : <p className="text-xs text-muted-foreground">Loading…</p>}
      {settings.data ? (
        <>
          <QuietHoursForm key={`q${settings.data.rev}`} settings={settings.data} />
          <TelegramBot key={`t${settings.data.rev}`} settings={settings.data} />
        </>
      ) : null}
    </section>
  );
}

function RulesTable({ rules }: { rules: NotificationRule[] }) {
  const qc = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const edit = async (rule: NotificationRule, body: NotificationRuleEdit) => {
    setBusy(rule.id);
    setError(null);
    try {
      const next = await runCommand("notificationRules.edit", { rule, body });
      qc.setQueryData<NotificationRuleList>(notificationRulesListQueryKey(), (old) => upsertRule(old, next));
    } catch (err) {
      if (problemOf(err)?.status === 412) {
        setError("The rule changed meanwhile; the latest table is loaded.");
        void qc.invalidateQueries({ queryKey: notificationRulesListQueryKey() });
      } else setError(errorMessage(err));
    } finally {
      setBusy(null);
    }
  };
  return (
    <div className="flex flex-col gap-1.5">
      <Table label="Routing rules" head={["Event class", "In-app", "Telegram", "Telegram timing", ""]}>
        {rules.map((r) => (
          <tr key={r.id} data-rule={r.eventClass}>
            <Td title={r.events.join(", ")}>
              {r.label}
              {r.bypassQuietHours ? <span className="ml-1 text-[11px] text-muted-foreground">· ignores quiet hours</span> : null}
            </Td>
            <Td>
              <input
                type="checkbox"
                className="size-3.5 accent-primary"
                aria-label={`In-app: ${r.label}`}
                checked={r.channels.inApp}
                disabled={busy === r.id}
                onChange={(e) => void edit(r, { channels: { inApp: e.target.checked } })}
              />
            </Td>
            <Td>
              <input
                type="checkbox"
                className="size-3.5 accent-primary"
                aria-label={`Telegram: ${r.label}`}
                checked={r.channels.telegram}
                disabled={busy === r.id}
                onChange={(e) => void edit(r, { channels: { telegram: e.target.checked }, ...(e.target.checked && r.eventClass === "progress" && r.timing !== "digest" ? { timing: "digest" as const } : {}) })}
              />
            </Td>
            <Td>
              <NativeSelect
                aria-label={`Telegram timing: ${r.label}`}
                value={r.timing}
                disabled={busy === r.id}
                onChange={(e) => void edit(r, { timing: e.target.value as NotificationTiming })}
                className="h-6 rounded-md border border-input bg-background px-1.5 text-xs"
              >
                {timingsFor(r).map((t) => (
                  <option key={t} value={t} disabled={t === "immediate" && r.eventClass === "progress" && r.channels.telegram}>
                    {TIMING_LABEL[t]}
                  </option>
                ))}
              </NativeSelect>
            </Td>
            <Td>{r.departures.length > 0 ? <Chip tone="accent" title={r.departures.join(", ")}>changed</Chip> : null}</Td>
          </tr>
        ))}
      </Table>
      {error ? (
        <span role="alert" className="text-xs text-destructive">
          {error}
        </span>
      ) : null}
    </div>
  );
}

function QuietHoursForm({ settings }: { settings: NotificationSettings }) {
  const qc = useQueryClient();
  const uid = useId();
  const [enabled, setEnabled] = useState(settings.quietHours.enabled);
  const [start, setStart] = useState(settings.quietHours.start);
  const [end, setEnd] = useState(settings.quietHours.end);
  const [digest, setDigest] = useState(settings.digestTime);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const dirty = enabled !== settings.quietHours.enabled || start !== settings.quietHours.start || end !== settings.quietHours.end || digest !== settings.digestTime;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const next = await runCommand("notificationSettings.edit", { settings, body: { quietHours: { enabled, start, end }, digestTime: digest } });
      qc.setQueryData(notificationSettingsGetQueryKey(), next);
    } catch (err) {
      if (problemOf(err)?.status === 412) void qc.invalidateQueries({ queryKey: notificationSettingsGetQueryKey() });
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <form onSubmit={submit} aria-label="Quiet hours and digest" className="flex flex-col gap-2 rounded-md border p-3">
      <h4 className="text-xs font-semibold">Quiet hours and the daily digest</h4>
      <p className="text-xs text-muted-foreground">
        Times are local to the instance timezone <span className="font-mono">{settings.timezone}</span> (Policies). The digest lists runs, evals, spend against budgets and open approvals.
      </p>
      <label className="flex items-center gap-1.5 text-xs">
        <input type="checkbox" className="size-3.5 accent-primary" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
        Quiet hours hold Telegram messages back (failures still go out)
      </label>
      <div className="grid gap-2 @md:grid-cols-3">
        <Field label="Quiet from" htmlFor={`${uid}-start`}>
          <Input id={`${uid}-start`} type="time" value={start} disabled={!enabled} onChange={(e) => setStart(e.target.value)} className="h-7 w-32 text-xs" required />
        </Field>
        <Field label="Quiet until" htmlFor={`${uid}-end`}>
          <Input id={`${uid}-end`} type="time" value={end} disabled={!enabled} onChange={(e) => setEnd(e.target.value)} className="h-7 w-32 text-xs" required />
        </Field>
        <Field label="Digest at" htmlFor={`${uid}-digest`}>
          <Input id={`${uid}-digest`} type="time" value={digest} onChange={(e) => setDigest(e.target.value)} className="h-7 w-32 text-xs" required />
        </Field>
      </div>
      <div className="flex items-center gap-2">
        <Button type="submit" size="xs" disabled={busy || !dirty} data-command="notificationSettings.edit">
          Save
        </Button>
        {error ? (
          <span role="alert" className="text-xs text-destructive">
            {error}
          </span>
        ) : null}
      </div>
    </form>
  );
}

function TelegramBot({ settings }: { settings: NotificationSettings }) {
  const qc = useQueryClient();
  const uid = useId();
  const tg = settings.telegram;
  const [token, setToken] = useState("");
  const [chat, setChat] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [status, setStatus] = useState<string | null>(null);
  const [verify, setVerify] = useState<TelegramBotVerify | null>(null);
  const chats = tg.chats.map((c) => c.id);

  const act = async (what: () => Promise<void>) => {
    setBusy(true);
    setError(null);
    setStatus(null);
    try {
      await what();
    } catch (err) {
      if (problemOf(err)?.status === 412) void qc.invalidateQueries({ queryKey: notificationSettingsGetQueryKey() });
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const saveChats = (next: number[]) =>
    act(async () => {
      const s = await runCommand("notificationSettings.edit", { settings, body: { telegramChats: next } });
      qc.setQueryData(notificationSettingsGetQueryKey(), s);
    });
  const saveToken = (e: FormEvent) => {
    e.preventDefault();
    void act(async () => {
      const s = await runCommand("telegramBot.set", { settings, token });
      setToken(""); // the value leaves the page with the request
      qc.setQueryData(notificationSettingsGetQueryKey(), s);
      setStatus("Token stored. It is never shown again.");
    });
  };
  const chatId = parseChatId(chat);
  const tokenError = token && !TOKEN.test(token) ? "A bot token looks like 123456789:AAE… (from @BotFather)" : undefined;

  return (
    <div className="flex flex-col gap-3 rounded-md border p-3" aria-label="Telegram bot" role="group">
      <div className="flex flex-wrap items-center gap-1.5">
        <h4 className="mr-1 text-xs font-semibold">Telegram bot</h4>
        <Chip tone={tg.tokenSet ? "accent" : "warning"}>{tg.tokenSet ? "token stored" : "no token"}</Chip>
        {tg.botUsername ? <Chip>@{tg.botUsername}</Chip> : null}
        {tg.tokenSet ? <Chip tone={tg.polling ? "neutral" : "warning"}>{tg.polling ? "listening for buttons" : "not polling"}</Chip> : null}
        {tg.lastSentAt ? <span className="text-[11px] text-muted-foreground">last message {when(tg.lastSentAt)}</span> : null}
      </div>
      {tg.lastError ? (
        <p role="alert" className="text-xs text-destructive">
          Last Bot API error: {tg.lastError}
        </p>
      ) : null}
      <form onSubmit={saveToken} aria-label="Bot token">
        <WriteOnlyField
          id={`${uid}-token`}
          label={tg.tokenSet ? "Replace the bot token" : "Bot token"}
          value={token}
          onChange={setToken}
          required
          action={
            <Button type="submit" size="xs" disabled={busy || !token || !!tokenError} data-command="telegramBot.set">
              {tg.tokenSet ? "Replace" : "Store"}
            </Button>
          }
        />
        {tokenError ? <span className="text-xs text-status-warning-foreground">{tokenError}</span> : null}
      </form>

      <div className="flex flex-col gap-1.5">
        <span className="text-xs text-muted-foreground">Allow-listed chats — the bot talks to no one else.</span>
        {chats.length === 0 ? <p className="text-xs text-muted-foreground">No chat yet: write to the bot from Telegram, then allow the chat below, or add its id.</p> : null}
        <ul className="flex flex-wrap gap-1.5" aria-label="Allowed chats">
          {chats.map((id) => (
            <li key={id} className="inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-mono text-[11px]">
              {id}
              <Button size="xs" variant="ghost" aria-label={`Remove chat ${id}`} disabled={busy} onClick={() => void saveChats(chats.filter((c) => c !== id))}>
                ×
              </Button>
            </li>
          ))}
        </ul>
        <div className="flex items-end gap-2">
          <Field label="Chat id" htmlFor={`${uid}-chat`} error={chat && chatId === undefined ? "A chat id is a whole number, negative for groups" : undefined}>
            <Input id={`${uid}-chat`} value={chat} inputMode="numeric" onChange={(e) => setChat(e.target.value)} placeholder="123456789" className="h-7 w-44 font-mono text-xs" />
          </Field>
          <Button
            size="xs"
            variant="outline"
            disabled={busy || chatId === undefined || chats.includes(chatId ?? 0)}
            onClick={() => {
              if (chatId === undefined) return;
              setChat("");
              void saveChats([...chats, chatId]);
            }}
          >
            Allow chat
          </Button>
        </div>
        {tg.pendingChats.length > 0 ? (
          <Table label="Chats that wrote to the bot" head={["Chat", "Id", "Seen", ""]} className="mt-1">
            {tg.pendingChats.map((c) => (
              <tr key={c.id}>
                <Td>{c.title ?? "—"}</Td>
                <Td className="font-mono">{c.id}</Td>
                <Td className="tabular-nums">{when(c.seenAt)}</Td>
                <Td>
                  <Button size="xs" variant="outline" disabled={busy} onClick={() => void saveChats([...chats, c.id])}>
                    Allow
                  </Button>
                </Td>
              </tr>
            ))}
          </Table>
        ) : null}
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <Button
          size="xs"
          variant="outline"
          disabled={busy || !tg.tokenSet}
          data-command="telegramBot.verify"
          onClick={() =>
            void act(async () => {
              const v = await runCommand("telegramBot.verify", undefined);
              setVerify(v);
              void qc.invalidateQueries({ queryKey: notificationSettingsGetQueryKey() });
            })
          }
        >
          Send test message
        </Button>
        {verify ? (
          <span role="status" className="text-xs text-muted-foreground" data-testid="telegram-verify">
            {verify.ok
              ? `Delivered to ${verify.chats.filter((c) => c.delivered).length} chat${verify.chats.length === 1 ? "" : "s"} as @${verify.botUsername ?? "bot"}.`
              : (verify.error ?? verify.chats.find((c) => c.error)?.error ?? "Not delivered.")}
          </span>
        ) : null}
        {status ? (
          <span role="status" className="text-xs text-muted-foreground">
            {status}
          </span>
        ) : null}
        {error ? (
          <span role="alert" className="text-xs text-destructive">
            {error}
          </span>
        ) : null}
      </div>
    </div>
  );
}
