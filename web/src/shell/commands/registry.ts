import type { IconoirIcon } from "@/shell/registry/panels";
import { operations, verbs, type OperationId } from "@/api/operations.gen";
import { chordId, eventChordId, isReserved, parseChord } from "./keymap";

// One registry of commands (docs/spec/10-ui-shell.md "Shell concepts"): menus, buttons, shortcuts and the palette
// all run the same entry. Ids:
//   <entity>.<verb>  — calls exactly one API operation with that operationId (same string as the MCP tool);
//   view.<name>      — client-only window and view state (float, dock, theme…), never touches the backend.

export type CommandContext = {
  project: string | undefined;
  activeDoc: string | null;
  /** Dockview instance id of the focused panel, if any. */
  focusedPanel: string | undefined;
};

export type Command = {
  id: string;
  title: string;
  group: "File" | "Edit" | "View" | "Window" | "Workspace" | "Project" | "Help" | "Go";
  icon?: IconoirIcon;
  keys?: string[];
  /** API-backed commands: the operation they call. Must equal id. */
  operation?: OperationId;
  /** true, or the reason the command is unavailable (shown greyed with this tooltip). */
  enabled?: (ctx: CommandContext) => true | string;
  run: (ctx: CommandContext, args?: unknown) => unknown;
  /** Hidden from the palette (still reachable by key). */
  hidden?: boolean;
  /** Keys that work while typing in a text field (Escape-like chords with modifiers). */
  allowInInput?: boolean;
};

const VIEW_ID = /^view\.[a-z][a-zA-Z0-9]*$/;
const OP_ID = /^[a-z][a-zA-Z0-9]*\.[a-z]+$/;

export class CommandRegistry {
  private byId = new Map<string, Command>();
  private byChord = new Map<string, string>();
  private listeners = new Set<() => void>();
  private recent: string[] = [];

  register(cmd: Command): () => void {
    if (this.byId.has(cmd.id)) throw new Error(`command "${cmd.id}" registered twice`);
    if (!VIEW_ID.test(cmd.id)) {
      if (!OP_ID.test(cmd.id)) throw new Error(`command id "${cmd.id}" must be <entity>.<verb> or view.<name>`);
      const op = operations[cmd.id as OperationId];
      if (!op) throw new Error(`command "${cmd.id}" has no API operation of that name (api/openapi.yaml)`);
      if (cmd.operation !== cmd.id) throw new Error(`command "${cmd.id}" must declare operation "${cmd.id}"`);
      if (op.planned > 0) throw new Error(`command "${cmd.id}": operation is planned for phase ${op.planned}`);
      if (!(op.verb in verbs)) throw new Error(`command "${cmd.id}": verb "${op.verb}" is not in the vocabulary`);
    } else if (cmd.operation) {
      throw new Error(`view command "${cmd.id}" must not call an API operation`);
    }
    for (const k of cmd.keys ?? []) {
      if (isReserved(k)) throw new Error(`command "${cmd.id}": ${k} is browser-reserved and cannot be assigned`);
      for (const mac of [true, false]) {
        const id = chordId(parseChord(k), mac);
        const owner = this.byChord.get(`${mac}:${id}`);
        if (owner) throw new Error(`command "${cmd.id}": ${k} is already bound to "${owner}"`);
      }
    }
    this.byId.set(cmd.id, cmd);
    for (const k of cmd.keys ?? []) {
      for (const mac of [true, false]) this.byChord.set(`${mac}:${chordId(parseChord(k), mac)}`, cmd.id);
    }
    this.emit();
    return () => this.unregister(cmd.id);
  }

  unregister(id: string): void {
    const cmd = this.byId.get(id);
    if (!cmd) return;
    this.byId.delete(id);
    for (const [k, v] of this.byChord) if (v === id) this.byChord.delete(k);
    this.emit();
  }

  get(id: string): Command | undefined {
    return this.byId.get(id);
  }

  all(): Command[] {
    return [...this.byId.values()];
  }

  /** The command bound to a keyboard event on this platform, if any. */
  forEvent(e: KeyboardEvent, mac: boolean): Command | undefined {
    const id = this.byChord.get(`${mac}:${eventChordId(e)}`);
    return id ? this.byId.get(id) : undefined;
  }

  isEnabled(cmd: Command, ctx: CommandContext): true | string {
    return cmd.enabled ? cmd.enabled(ctx) : true;
  }

  async run(id: string, ctx: CommandContext, args?: unknown): Promise<unknown> {
    const cmd = this.byId.get(id);
    if (!cmd) throw new Error(`unknown command "${id}"`);
    const ok = this.isEnabled(cmd, ctx);
    if (ok !== true) throw new Error(ok);
    this.recent = [id, ...this.recent.filter((r) => r !== id)].slice(0, 10);
    try {
      localStorage.setItem("cadence.recentCommands", JSON.stringify(this.recent));
    } catch {
      /* storage unavailable */
    }
    return cmd.run(ctx, args);
  }

  recentIds(): string[] {
    if (this.recent.length === 0) {
      try {
        const raw = localStorage.getItem("cadence.recentCommands");
        if (raw) this.recent = (JSON.parse(raw) as string[]).filter((id) => this.byId.has(id));
      } catch {
        /* ignore */
      }
    }
    return this.recent;
  }

  subscribe(fn: () => void): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private emit(): void {
    for (const fn of this.listeners) fn();
  }
}

function isEditable(t: EventTarget | null): boolean {
  if (!(t instanceof HTMLElement)) return false;
  return t.isContentEditable || t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT";
}

/** Routes key presses in a document (the main window or a popout) to the registry. */
export function installKeyboard(doc: Document, registry: CommandRegistry, context: () => CommandContext, mac: boolean): () => void {
  const onKey = (e: KeyboardEvent) => {
    if (e.defaultPrevented || e.isComposing) return;
    const cmd = registry.forEvent(e, mac);
    if (!cmd) return;
    const hasModifier = e.ctrlKey || e.metaKey || e.altKey || /^F\d+$/.test(e.key);
    if (isEditable(e.target) && !hasModifier && !cmd.allowInInput) return;
    const ctx = context();
    if (registry.isEnabled(cmd, ctx) !== true) return;
    e.preventDefault();
    e.stopPropagation();
    void registry.run(cmd.id, ctx);
  };
  doc.addEventListener("keydown", onKey, true);
  return () => doc.removeEventListener("keydown", onKey, true);
}
