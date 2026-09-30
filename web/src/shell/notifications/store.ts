import { create } from "zustand";
import { ProblemError } from "@/api/client";

// Notification history (chrome, not a panel) and the polite live region (WCAG 4.1.3).

export type Notice = {
  id: number;
  level: "info" | "success" | "warning" | "error";
  title: string;
  detail?: string;
  /** Help article for the notice, e.g. errors.precondition-failed. */
  helpId?: string;
  /** A panel that shows what the notice is about (Approvals for an approval request). */
  open?: { label: string; panel: string };
  /** Where the notice came from: a live event (its seq) is recorded once. */
  seq?: number;
  at: string;
  read: boolean;
};

type NoticeState = {
  items: Notice[];
  announcement: string;
  push(n: Omit<Notice, "id" | "at" | "read">): void;
  markAllRead(): void;
  clear(): void;
  announce(message: string): void;
};

let nextId = 1;

export const useNotices = create<NoticeState>((set) => ({
  items: [],
  announcement: "",
  push: (n) =>
    set((s) => (n.seq !== undefined && s.items.some((i) => i.seq === n.seq) ? s : {
      items: [{ ...n, id: nextId++, at: new Date().toISOString(), read: false }, ...s.items].slice(0, 200),
      announcement: `${n.title}${n.detail ? `: ${n.detail}` : ""}`,
    })),
  markAllRead: () => set((s) => ({ items: s.items.map((i) => ({ ...i, read: true })) })),
  clear: () => set({ items: [] }),
  announce: (message) => set({ announcement: message }),
}));

export function notify(n: Omit<Notice, "id" | "at" | "read">): void {
  useNotices.getState().push(n);
}

export function announce(message: string): void {
  useNotices.getState().announce(message);
}

/** Reports a failed command: the notice links the help article for the problem type. */
export function notifyError(title: string, err: unknown): void {
  if (err instanceof ProblemError) {
    notify({ level: "error", title, detail: err.problem.detail ?? err.problem.title, helpId: err.helpId });
  } else {
    notify({ level: "error", title, detail: err instanceof Error ? err.message : String(err) });
  }
}
