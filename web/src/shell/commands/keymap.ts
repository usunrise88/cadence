// Key chords for the command registry. "Mod" is Cmd on macOS and Ctrl elsewhere.
// Browser-reserved shortcuts can't be intercepted by a page, so the registry refuses them (spec: Accessibility and
// input; R37 adds Ctrl/Cmd+Shift+P, which Firefox takes for a private window).

export type Chord = { mod: boolean; ctrl: boolean; alt: boolean; shift: boolean; key: string };

const MODIFIERS = new Set(["mod", "ctrl", "alt", "shift", "meta", "cmd"]);

/** Normalises the key part: single characters lower-case, named keys as in KeyboardEvent.key. */
function normaliseKey(k: string): string {
  if (k.length === 1) return k.toLowerCase();
  const named: Record<string, string> = { esc: "Escape", del: "Delete", space: " ", up: "ArrowUp", down: "ArrowDown", left: "ArrowLeft", right: "ArrowRight" };
  return named[k.toLowerCase()] ?? k;
}

export function parseChord(spec: string): Chord {
  const parts = spec.split("+").map((p) => p.trim());
  // "Mod+Alt++" style is not used; a trailing empty part means the key was "+".
  const key = parts.pop();
  if (key === undefined || key === "") throw new Error(`invalid key chord "${spec}"`);
  const chord: Chord = { mod: false, ctrl: false, alt: false, shift: false, key: normaliseKey(key) };
  for (const p of parts) {
    const m = p.toLowerCase();
    if (!MODIFIERS.has(m)) throw new Error(`unknown modifier "${p}" in "${spec}"`);
    if (m === "mod" || m === "cmd" || m === "meta") chord.mod = true;
    else if (m === "ctrl") chord.ctrl = true;
    else if (m === "alt") chord.alt = true;
    else chord.shift = true;
  }
  return chord;
}

export function isMac(platform: string = typeof navigator === "undefined" ? "" : navigator.platform): boolean {
  return /mac|iphone|ipad/i.test(platform);
}

/** Canonical string for comparison: modifiers in fixed order, platform-resolved. */
export function chordId(c: Chord, mac: boolean = isMac()): string {
  const ctrl = c.ctrl || (c.mod && !mac);
  const meta = c.mod && mac;
  return [ctrl && "Ctrl", meta && "Meta", c.alt && "Alt", c.shift && "Shift", c.key].filter(Boolean).join("+");
}

type KeyLike = Pick<KeyboardEvent, "key" | "code" | "ctrlKey" | "metaKey" | "altKey" | "shiftKey">;

/** The chord id of a keyboard event. Alt changes `key` on macOS (Alt+W → ∑), so letters and brackets use `code`. */
export function eventChordId(e: KeyLike): string {
  let key = e.key;
  if (/^Key[A-Z]$/.test(e.code)) key = e.code.slice(3).toLowerCase();
  else if (/^Digit[0-9]$/.test(e.code)) key = e.code.slice(5);
  else if (e.code === "BracketLeft") key = "[";
  else if (e.code === "BracketRight") key = "]";
  else if (e.code === "Backslash") key = "\\";
  else if (e.code === "Period") key = ".";
  else if (e.code === "Semicolon") key = ";";
  else if (key.length === 1) key = key.toLowerCase();
  return [e.ctrlKey && "Ctrl", e.metaKey && "Meta", e.altKey && "Alt", e.shiftKey && "Shift", key].filter(Boolean).join("+");
}

/** Chords a web page cannot own. Checked for both platforms so a mapping is safe everywhere. */
export const RESERVED: readonly string[] = [
  "Mod+W",
  "Mod+T",
  "Mod+N",
  "Mod+Shift+W",
  "Mod+Shift+T",
  "Mod+Shift+N",
  "Mod+Shift+P",
  "Ctrl+Tab",
  "Ctrl+Shift+Tab",
  "Mod+Q",
  "Mod+L",
  "F11",
  "F12",
];

export function isReserved(spec: string): boolean {
  const c = parseChord(spec);
  return [true, false].some((mac) => RESERVED.some((r) => chordId(parseChord(r), mac) === chordId(c, mac)));
}

/** Human label: ⌘K on macOS, Ctrl+K elsewhere. */
export function chordLabel(spec: string, mac: boolean = isMac()): string {
  const c = parseChord(spec);
  // Shifted punctuation is labelled by its character ("?", not "Shift+?").
  if (c.shift && !c.mod && !c.ctrl && !c.alt && /^[?!@#$%^&*()_+{}|:"<>~]$/.test(c.key)) c.shift = false;
  const key = c.key === " " ? "Space" : c.key.length === 1 ? c.key.toUpperCase() : c.key.replace(/^Arrow/, "");
  if (mac) {
    return [c.ctrl && "⌃", c.alt && "⌥", c.shift && "⇧", c.mod && "⌘", key].filter(Boolean).join("");
  }
  return [(c.ctrl || c.mod) && "Ctrl", c.alt && "Alt", c.shift && "Shift", key].filter(Boolean).join("+");
}
