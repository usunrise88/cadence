import { useEffect, useState } from "react";
import { Bell, HalfMoon, SunLight } from "iconoir-react";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { cn } from "@/lib/utils";
import { useSnap } from "@/shell/floating-snap/dockview-adapter";
import { useHelp } from "@/shell/help/store";
import type { ConnectionState } from "@/shell/live/events";
import { useNotices } from "@/shell/notifications/store";
import { openPanel } from "@/shell/dock/layout";
import { events } from "@/shell/registries";
import { useTheme } from "@/shell/theme/store";
import { useWorkspaceSync } from "@/shell/workspaces/persistence";

// Status bar: live connection, workspace save state, GPU / queue / agent slots (filled by later phases), snapping,
// theme and the notification history. The polite live region lives here too (WCAG 4.1.3).

function useConnection(): ConnectionState {
  const [s, setS] = useState(events.connectionState);
  useEffect(() => events.onState(setS), []);
  return s;
}

const CONN_LABEL: Record<ConnectionState, string> = { idle: "Live: idle", connecting: "Live: connecting", open: "Live", error: "Live: offline" };

export function StatusBar() {
  const conn = useConnection();
  const sync = useWorkspaceSync();
  const snap = useSnap((s) => s.enabled);
  const { dark, setMode } = useTheme();
  return (
    <footer className="flex h-6 shrink-0 items-center gap-3 border-t bg-chrome px-2.5 text-[11px] text-muted-foreground [&>span+span]:border-l [&>span+span]:pl-3">
      <span data-testid="connection" className="inline-flex items-center gap-1">
        <span aria-hidden className={cn("size-2 rounded-full", conn === "open" ? "bg-status-done" : conn === "error" ? "bg-status-failed" : "bg-muted-foreground")} />
        {CONN_LABEL[conn]}
      </span>
      <span data-testid="workspace-sync">{sync.restoring ? "Restoring…" : sync.saving ? "Saving…" : sync.rev ? `Saved · rev ${sync.rev}` : "Not saved yet"}</span>
      <span title="GPU memory and compute arrive with training (phase 2)">GPU —</span>
      <span title="The job queue arrives with the agent loop (phase 1)">Queue —</span>
      <span title="Agent sessions arrive in phase 1">Agent —</span>
      <div className="ml-auto flex items-center gap-1">
        <Button variant="ghost" size="xs" className="h-5 px-1.5 text-[11px] font-normal text-muted-foreground" onClick={() => useSnap.getState().setEnabled(!snap)} aria-pressed={snap}>
          Snap {snap ? "on" : "off"}
        </Button>
        <Button variant="ghost" size="icon-xs" className="size-6 text-muted-foreground [&_svg]:size-3.5" aria-label={dark ? "Light mode" : "Dark mode"} onClick={() => setMode(dark ? "light" : "dark")}>
          {dark ? <SunLight aria-hidden /> : <HalfMoon aria-hidden />}
        </Button>
        <NotificationHistory />
      </div>
      <LiveRegion />
    </footer>
  );
}

function NotificationHistory() {
  const { items, markAllRead, clear } = useNotices();
  const unread = items.filter((i) => !i.read).length;
  return (
    <Popover onOpenChange={(open) => open || markAllRead()}>
      <PopoverTrigger render={<Button variant="ghost" size="icon-xs" className="relative size-6 text-muted-foreground [&_svg]:size-3.5" aria-label={`Notifications${unread ? ` (${unread} unread)` : ""}`} />}>
        <Bell aria-hidden />
        {unread ? <span className="absolute top-0.5 right-0.5 size-1.5 rounded-full bg-status-failed" /> : null}
      </PopoverTrigger>
      <PopoverContent align="end" className="w-96 p-0">
        <div className="flex items-center border-b px-3 py-2 text-xs font-medium">
          Notifications
          <Button variant="ghost" size="xs" className="ml-auto" onClick={clear}>
            Clear
          </Button>
        </div>
        <ul className="max-h-80 overflow-auto text-xs">
          {items.length === 0 ? <li className="p-3 text-muted-foreground">Nothing yet.</li> : null}
          {items.map((n) => (
            <li key={n.id} className="border-b px-3 py-2 last:border-0">
              <div className="flex items-center gap-2">
                <span className={cn("size-2 rounded-full", n.level === "error" ? "bg-status-failed" : n.level === "warning" ? "bg-status-warning" : n.level === "success" ? "bg-status-done" : "bg-muted-foreground")} />
                <span className="font-medium">{n.title}</span>
                <time className="ml-auto text-muted-foreground">{new Date(n.at).toLocaleTimeString()}</time>
              </div>
              {n.detail ? <p className="mt-1 text-muted-foreground">{n.detail}</p> : null}
              {n.helpId ? (
                <button
                  type="button"
                  className="mt-1 text-primary underline-offset-2 hover:underline"
                  onClick={() => {
                    useHelp.getState().show(n.helpId!);
                    openPanel("help");
                  }}
                >
                  What does this mean?
                </button>
              ) : null}
            </li>
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}

function LiveRegion() {
  const message = useNotices((s) => s.announcement);
  return (
    <div role="status" aria-live="polite" className="sr-only" data-testid="live-region">
      {message}
    </div>
  );
}
