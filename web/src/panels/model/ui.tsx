import type { ReactNode } from "react";
import type { PipelineEstimate } from "@/api/gen/types.gen";
import { cn } from "@/lib/utils";

// Small building blocks of the Model document's deploy sections.

export function Section({ id, title, slot, children, actions }: { id: string; title: string; slot: string; children: ReactNode; actions?: ReactNode }) {
  return (
    <section aria-labelledby={id} className="flex flex-col gap-1.5" data-slot={slot}>
      <div className="flex flex-wrap items-center gap-2">
        <h3 id={id} className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
          {title}
        </h3>
        {actions ? <div className="ml-auto flex flex-wrap gap-1.5">{actions}</div> : null}
      </div>
      {children}
    </section>
  );
}

export function Message({ m }: { m: { error: boolean; text: string } | null }) {
  if (!m) return null;
  return (
    <p role={m.error ? "alert" : "status"} className={m.error ? "text-destructive" : "text-muted-foreground"}>
      {m.text}
    </p>
  );
}

const VERDICT_CLASS: Record<string, string> = {
  passed: "text-status-done-foreground",
  confirmed: "text-status-done-foreground",
  active: "text-status-done-foreground",
  failed: "text-status-failed-foreground",
  withdrawn: "text-status-failed-foreground",
  "rolled-back": "text-status-failed-foreground",
  inconclusive: "text-status-warning-foreground",
  warning: "text-status-warning-foreground",
  pending: "text-status-warning-foreground",
  "pending-delivery": "text-status-warning-foreground",
};

/** A verdict or state word with its glyph (the colour never carries the meaning alone). */
export function Verdict({ value }: { value: string }) {
  const glyph = ["passed", "confirmed", "active"].includes(value) ? "✓" : ["failed", "withdrawn", "rolled-back"].includes(value) ? "✗" : "•";
  return (
    <span className={cn("font-medium", VERDICT_CLASS[value])} data-verdict={value}>
      <span aria-hidden>{glyph} </span>
      {value}
    </span>
  );
}

export const pct = (v: number | undefined, digits = 1) => (v === undefined ? "—" : `${(v * 100).toFixed(digits)}%`);
export const ms = (v: number | undefined) => (v === undefined ? "—" : `${v.toFixed(1)} ms`);
export const hours = (v: number | undefined) => (v === undefined ? "—" : `${v.toFixed(1)} h`);

/** One line for a pipeline estimate: GPU-hours and wall time. */
export function estimateText(e: PipelineEstimate | undefined): string {
  if (!e) return "";
  const parts: string[] = [];
  if (e.gpuHours !== undefined) parts.push(`${e.gpuHours.toFixed(2)} GPU-hours`);
  if (e.seconds !== undefined) parts.push(`about ${Math.max(1, Math.round(e.seconds / 60))} min`);
  if (!e.known) parts.push("some steps have no estimate");
  return parts.join(", ") || "no estimate";
}

export const shortHash = (h: string | undefined, n = 12) => (h ? (h.startsWith("b3:") ? h.slice(3, 3 + n) : h.slice(0, n)) : "—");
