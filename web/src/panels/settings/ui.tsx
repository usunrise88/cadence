import { useId, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";

// Small layout pieces shared by the Settings sections.

export function SectionHeading({ id, title, hint, children }: { id?: string; title: string; hint?: string; children?: ReactNode }) {
  return (
    <div className="flex items-start gap-2 pb-2">
      <div className="min-w-0">
        <h3 id={id} className="text-[13px] font-semibold">
          {title}
        </h3>
        {hint ? <p className="text-xs text-muted-foreground">{hint}</p> : null}
      </div>
      {children ? <div className="ml-auto flex shrink-0 items-center gap-1">{children}</div> : null}
    </div>
  );
}

/**
 * The ids a field's control points at with aria-describedby: its hint, warning and error, when present. Field gives
 * those messages the same ids when it has `htmlFor`.
 */
export function describedBy(htmlFor: string, m: { hint?: unknown; warning?: unknown; error?: unknown }): string | undefined {
  const ids = [m.error ? `${htmlFor}-error` : "", m.warning ? `${htmlFor}-warning` : "", m.hint ? `${htmlFor}-hint` : ""].filter(Boolean);
  return ids.length ? ids.join(" ") : undefined;
}

export function Field({
  label,
  hint,
  error,
  warning,
  htmlFor,
  children,
  extra,
  className,
}: {
  label: string;
  hint?: ReactNode;
  error?: string;
  warning?: string;
  htmlFor?: string;
  children: ReactNode;
  extra?: ReactNode;
  className?: string;
}) {
  const id = (suffix: string) => (htmlFor ? `${htmlFor}-${suffix}` : undefined);
  return (
    <div className={cn("flex flex-col gap-1 text-xs", className)}>
      <div className="flex min-h-6 items-center gap-1">
        <label htmlFor={htmlFor} className="text-muted-foreground">
          {label}
        </label>
        {extra}
      </div>
      {children}
      {hint ? (
        <span id={id("hint")} className="text-[11px] text-muted-foreground">
          {hint}
        </span>
      ) : null}
      {warning ? (
        <span id={id("warning")} className="text-status-warning-foreground">
          {warning}
        </span>
      ) : null}
      {error ? (
        <span id={id("error")} role="alert" className="text-destructive">
          {error}
        </span>
      ) : null}
    </div>
  );
}

/** A titled group of fields inside a form: a fieldset whose legend reads like the wizard's small section headings. */
export function FieldGroup({ title, hint, children, className, testId }: { title: string; hint?: ReactNode; children: ReactNode; className?: string; testId?: string }) {
  return (
    <fieldset className={cn("min-w-0", className)} data-testid={testId}>
      <legend className="p-0 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">{title}</legend>
      {hint ? <p className="mt-0.5 text-[11px] text-muted-foreground">{hint}</p> : null}
      <div className="mt-2 flex flex-col gap-2.5">{children}</div>
    </fieldset>
  );
}

export function Table({ label, head, children, className }: { label: string; head: string[]; children: ReactNode; className?: string }) {
  return (
    <div className={cn("overflow-auto rounded-md border", className)}>
      <table aria-label={label} className="w-full text-xs">
        <thead className="bg-tool text-left text-muted-foreground">
          <tr>
            {head.map((h) => (
              <th key={h} scope="col" className="px-2 py-1.5 font-normal whitespace-nowrap">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody className="divide-y">{children}</tbody>
      </table>
    </div>
  );
}

export function Td({ children, className, title }: { children?: ReactNode; className?: string; title?: string }) {
  return (
    <td className={cn("px-2 py-1.5 align-middle", className)} title={title}>
      {children}
    </td>
  );
}

/**
 * Inline confirm (docs/spec/10-ui-shell.md "Verb vocabulary": revoke and cancel confirm inline): the first press
 * turns the button into "Confirm" / "Keep", no modal.
 */
export function InlineConfirm({ label, confirmLabel, onConfirm, disabled, busyLabel }: { label: string; confirmLabel: string; onConfirm: () => Promise<unknown>; disabled?: boolean | string; busyLabel?: string }) {
  const [armed, setArmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const id = useId();
  if (!armed) {
    return (
      <Button size="xs" variant="outline" disabled={!!disabled} title={typeof disabled === "string" ? disabled : undefined} onClick={() => setArmed(true)}>
        {label}
      </Button>
    );
  }
  return (
    <span role="group" aria-labelledby={id} className="inline-flex items-center gap-1">
      <span id={id} className="sr-only">
        {confirmLabel}?
      </span>
      <Button
        size="xs"
        variant="destructive"
        autoFocus
        disabled={busy}
        onClick={async () => {
          setBusy(true);
          try {
            await onConfirm();
          } finally {
            setBusy(false);
            setArmed(false);
          }
        }}
      >
        {busy ? (busyLabel ?? "Working…") : confirmLabel}
      </Button>
      <Button size="xs" variant="ghost" onClick={() => setArmed(false)}>
        Keep
      </Button>
    </span>
  );
}

export function when(iso: string | undefined): string {
  return iso ? new Date(iso).toLocaleString() : "—";
}

export function Chip({ children, tone = "neutral", title }: { children: ReactNode; tone?: "neutral" | "accent" | "warning"; title?: string }) {
  return (
    <span
      title={title}
      className={cn(
        "inline-flex h-5 items-center rounded-full px-2 text-[11px] whitespace-nowrap",
        tone === "accent" && "bg-accent-soft font-medium text-accent-text",
        tone === "warning" && "border border-status-warning text-status-warning-foreground",
        tone === "neutral" && "border text-muted-foreground",
      )}
    >
      {children}
    </span>
  );
}
