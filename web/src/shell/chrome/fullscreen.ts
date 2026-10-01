import { useSyncExternalStore } from "react";

// Full screen of the whole shell (the Fullscreen API on the document element). F11 stays the browser's own; this is
// the command and the user menu's item. The browser leaves full screen on Esc by itself.

export function isFullScreen(): boolean {
  return typeof document !== "undefined" && document.fullscreenElement != null;
}

export function canFullScreen(): true | string {
  return typeof document !== "undefined" && document.fullscreenEnabled ? true : "This browser does not allow full screen here";
}

export async function toggleFullScreen(): Promise<void> {
  if (isFullScreen()) await document.exitFullscreen();
  else await document.documentElement.requestFullscreen();
}

function subscribe(onChange: () => void): () => void {
  document.addEventListener("fullscreenchange", onChange);
  return () => document.removeEventListener("fullscreenchange", onChange);
}

/** Whether the shell is in full screen, following the browser (Esc leaves it without the shell's help). */
export function useFullScreen(): boolean {
  return useSyncExternalStore(subscribe, isFullScreen, () => false);
}
