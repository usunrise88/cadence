import { create } from "zustand";

// Light / dark / system. Dark mode swaps the Radix scales through the root class; popout windows get the same
// class (dockview-adapter → DockHost.onPopoutWindow).

export type ThemeMode = "light" | "dark" | "system";

function stored(): ThemeMode {
  try {
    const v = localStorage.getItem("cadence.theme");
    return v === "light" || v === "dark" ? v : "system";
  } catch {
    return "system";
  }
}

function systemDark(): boolean {
  return typeof matchMedia !== "undefined" && matchMedia("(prefers-color-scheme: dark)").matches;
}

type ThemeState = {
  mode: ThemeMode;
  dark: boolean;
  setMode(m: ThemeMode): void;
};

export const useTheme = create<ThemeState>((set) => ({
  mode: stored(),
  dark: stored() === "dark" || (stored() === "system" && systemDark()),
  setMode(mode) {
    try {
      localStorage.setItem("cadence.theme", mode);
    } catch {
      /* storage unavailable */
    }
    set({ mode, dark: mode === "dark" || (mode === "system" && systemDark()) });
  },
}));

/** Applies the theme class to a document (the main one and every popout). */
export function applyTheme(doc: Document, dark: boolean): void {
  doc.documentElement.classList.toggle("dark", dark);
  doc.documentElement.classList.toggle("light", !dark);
  doc.documentElement.style.colorScheme = dark ? "dark" : "light";
}

export function installTheme(doc: Document): () => void {
  applyTheme(doc, useTheme.getState().dark);
  const unsub = useTheme.subscribe((s) => applyTheme(doc, s.dark));
  const mq = typeof matchMedia !== "undefined" ? matchMedia("(prefers-color-scheme: dark)") : null;
  const onChange = () => {
    const { mode, setMode } = useTheme.getState();
    if (mode === "system") setMode("system");
  };
  mq?.addEventListener("change", onChange);
  return () => {
    unsub();
    mq?.removeEventListener("change", onChange);
  };
}
