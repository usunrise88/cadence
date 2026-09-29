import { describe, expect, it } from "vitest";
import { chordId, chordLabel, eventChordId, isReserved, parseChord } from "./keymap";

describe("keymap", () => {
  it("resolves Mod per platform", () => {
    expect(chordId(parseChord("Mod+K"), false)).toBe("Ctrl+k");
    expect(chordId(parseChord("Mod+K"), true)).toBe("Meta+k");
    expect(chordId(parseChord("Mod+Alt+]"), false)).toBe("Ctrl+Alt+]");
  });

  it.each(["Mod+W", "Mod+T", "Mod+N", "Ctrl+Tab", "Mod+Shift+P", "Ctrl+W", "Cmd+T"])("%s is reserved", (k) => {
    expect(isReserved(k)).toBe(true);
  });

  it.each(["Mod+K", "Alt+W", "F6", "Shift+F6", "Mod+Alt+[", "Mod+\\", "Mod+.", "Mod+Alt+P"])("%s is allowed", (k) => {
    expect(isReserved(k)).toBe(false);
  });

  it("matches events by physical key so Alt on macOS still works", () => {
    const e = { key: "∑", code: "KeyW", ctrlKey: false, metaKey: false, altKey: true, shiftKey: false };
    expect(eventChordId(e)).toBe(chordId(parseChord("Alt+W"), false));
  });

  it("labels chords", () => {
    expect(chordLabel("Mod+K", true)).toBe("⌘K");
    expect(chordLabel("Mod+K", false)).toBe("Ctrl+K");
    expect(chordLabel("Shift+F6", false)).toBe("Shift+F6");
  });

  it("rejects unknown modifiers", () => {
    expect(() => parseChord("Hyper+K")).toThrow();
  });
});
