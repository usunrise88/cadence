import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Collapse, LogOut, Maximize, Settings, SoundHigh, SoundOff, User } from "iconoir-react";
import { authGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import { authLogout } from "@/api/gen/sdk.gen";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { markSignedOut } from "@/shell/auth/session";
import { openPanel } from "@/shell/dock/layout";
import { useNotificationSound } from "@/shell/notifications/sound";
import { notifyError } from "@/shell/notifications/store";
import { canFullScreen, toggleFullScreen, useFullScreen } from "./fullscreen";
import { useDialogs } from "./dialogs";

// The signed-in person: second factor and sign-out, and two per-browser conveniences — full screen (the
// view.toggleFullScreen command) and the notification sound. Sign-out is not a command (auth operations are not in
// the vocabulary and never MCP tools, R1), so it is a plain menu item.

export function UserMenu() {
  const qc = useQueryClient();
  const { data } = useQuery({ ...authGetOptions(), staleTime: Infinity });
  const fullScreen = useFullScreen();
  const sound = useNotificationSound();
  const actor = data?.actor;
  if (!actor) return null;
  const name = actor.name ?? actor.id;
  const signOut = async () => {
    try {
      await authLogout();
    } catch (err) {
      notifyError("Sign-out failed", err);
      return;
    }
    markSignedOut(qc);
  };
  return (
    <DropdownMenu>
      <DropdownMenuTrigger render={<Button variant="ghost" size="sm" className="h-6 gap-1 px-2 text-xs font-normal" data-testid="user-menu" aria-label={`Account: ${name}`} />}>
        <User aria-hidden className="size-3.5" />
        {name}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="min-w-56">
        <DropdownMenuGroup>
          <DropdownMenuLabel>Signed in as {name}</DropdownMenuLabel>
        </DropdownMenuGroup>
        {actor.kind === "user" ? (
          <DropdownMenuItem onClick={() => openPanel("settings")}>
            <Settings aria-hidden className="size-3.5" />
            Settings
          </DropdownMenuItem>
        ) : null}
        {actor.kind === "user" ? (
          <DropdownMenuItem onClick={() => useDialogs.getState().show({ kind: "twoFactor" })}>
            Two-factor authentication…
            <span className="ml-auto text-xs text-muted-foreground">{data.totpEnabled ? "On" : "Off"}</span>
          </DropdownMenuItem>
        ) : null}
        <DropdownMenuSeparator />
        <DropdownMenuItem
          disabled={canFullScreen() !== true}
          onClick={() => void toggleFullScreen().catch((err: unknown) => notifyError(fullScreen ? "Could not leave full screen" : "Could not enter full screen", err))}
          data-command="view.toggleFullScreen"
        >
          {fullScreen ? <Collapse aria-hidden className="size-3.5" /> : <Maximize aria-hidden className="size-3.5" />}
          {fullScreen ? "Exit full screen" : "Full screen"}
        </DropdownMenuItem>
        <DropdownMenuItem closeOnClick={false} onClick={() => sound.setEnabled(!sound.enabled)} data-testid="notification-sound">
          {sound.enabled ? <SoundHigh aria-hidden className="size-3.5" /> : <SoundOff aria-hidden className="size-3.5" />}
          Notification sound
          <span className="ml-auto text-xs text-muted-foreground">{sound.enabled ? "On" : "Off"}</span>
        </DropdownMenuItem>
        <DropdownMenuSeparator />
        <DropdownMenuItem onClick={() => void signOut()}>
          <LogOut aria-hidden className="size-3.5" />
          Sign out
        </DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
