import { useId } from "react";
import type { AvailabilityWindow, AvailabilityWindows, JobKind } from "@/api/gen/types.gen";
import { Plus, Trash } from "iconoir-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { DAY_LABEL, DAYS, formatDays, windowError } from "@/shell/panel";

// Availability windows of one card (docs/spec/08-resolutions.md R19): per job kind, the days and hours a job of that
// kind may run. A kind without windows may run any time; a training job running at the close is paused and resumes
// from its training state when a window opens. Saved with the card through compute.edit.

type Row = { kind: JobKind; window: AvailabilityWindow };

/** The windows map as rows (kind + window), in kind order. */
export function windowRows(w: AvailabilityWindows | undefined): Row[] {
  return Object.entries(w ?? {}).flatMap(([kind, list]) => (list ?? []).map((window) => ({ kind: kind as JobKind, window })));
}

/** Rows back to the contract's map; kinds without rows are left out (they may run any time). */
export function windowsOf(rows: Row[]): AvailabilityWindows {
  const out: AvailabilityWindows = {};
  for (const r of rows) out[r.kind] = [...(out[r.kind] ?? []), r.window];
  return out;
}

const NEW_WINDOW: Row = { kind: "training", window: { days: ["mon", "tue", "wed", "thu", "fri"], start: "20:00", end: "08:00" } };

export function WindowsEditor({ card, kinds, value, onChange }: { card: number; kinds: JobKind[]; value: AvailabilityWindows | undefined; onChange: (w: AvailabilityWindows) => void }) {
  const rows = windowRows(value);
  const set = (i: number, r: Row) => onChange(windowsOf(rows.map((x, j) => (j === i ? r : x))));
  const remove = (i: number) => onChange(windowsOf(rows.filter((_, j) => j !== i)));
  const add = () => onChange(windowsOf([...rows, NEW_WINDOW]));
  return (
    <fieldset className="flex min-w-0 flex-col gap-2" data-testid={`windows-editor-${card}`}>
      <legend className="sr-only">Availability windows of card {card}</legend>
      {rows.length === 0 ? <p className="rounded-md border border-dashed px-3 py-2 text-xs text-muted-foreground">No windows: every job kind may run at any time.</p> : null}
      {rows.map((r, i) => (
        <WindowRow key={i} index={i} row={r} kinds={kinds} onChange={(next) => set(i, next)} onRemove={() => remove(i)} />
      ))}
      <Button type="button" size="xs" variant="outline" className="w-fit" onClick={add}>
        <Plus aria-hidden />
        Add window
      </Button>
    </fieldset>
  );
}

function WindowRow({ index, row: r, kinds, onChange, onRemove }: { index: number; row: Row; kinds: JobKind[]; onChange: (r: Row) => void; onRemove: () => void }) {
  const id = useId();
  const err = windowError(r.window);
  const setWindow = (w: Partial<AvailabilityWindow>) => onChange({ ...r, window: { ...r.window, ...w } });
  const overnight = r.window.end !== "24:00" && r.window.end <= r.window.start;
  const errId = err ? `${id}-err` : undefined;
  return (
    <div role="group" aria-label={`Window ${index + 1}`} className={cn("flex flex-col gap-2.5 rounded-md border bg-background p-2.5 text-xs", err && "border-status-warning")}>
      <div className="flex flex-wrap items-end gap-x-4 gap-y-2.5">
        <div className="flex flex-col gap-1">
          <label htmlFor={`${id}-kind`} className="text-muted-foreground">
            Job kind
          </label>
          <NativeSelect id={`${id}-kind`} className="w-28 text-xs" value={r.kind} onChange={(e) => onChange({ ...r, kind: e.target.value as JobKind })}>
            {kinds.map((k) => (
              <option key={k} value={k}>
                {k}
              </option>
            ))}
          </NativeSelect>
        </div>
        <div className="flex flex-col gap-1" role="group" aria-labelledby={`${id}-days`}>
          <span id={`${id}-days`} className="text-muted-foreground">
            Days
          </span>
          <div className="flex flex-wrap gap-1">
            {DAYS.map((d, di) => (
              <label key={d} className="relative">
                <input
                  type="checkbox"
                  className="peer sr-only"
                  checked={r.window.days.includes(d)}
                  aria-describedby={err?.startsWith("Pick") ? errId : undefined}
                  onChange={(e) => setWindow({ days: e.target.checked ? DAYS.filter((x) => x === d || r.window.days.includes(x)) : r.window.days.filter((x) => x !== d) })}
                />
                <span className="inline-flex h-7 min-w-9 cursor-pointer items-center justify-center rounded-md border px-1.5 text-muted-foreground select-none peer-checked:border-accent-line peer-checked:bg-accent-soft peer-checked:font-medium peer-checked:text-accent-text peer-focus-visible:ring-2 peer-focus-visible:ring-ring hover:bg-hover">
                  {DAY_LABEL[di]}
                </span>
              </label>
            ))}
          </div>
        </div>
        <Button type="button" size="icon-xs" variant="ghost" className="ml-auto size-7 text-muted-foreground" aria-label={`Remove window ${index + 1}`} onClick={onRemove}>
          <Trash aria-hidden />
        </Button>
      </div>
      <div className="flex flex-wrap items-end gap-x-4 gap-y-2.5">
        <TimeField id={`${id}-start`} label="Opens at" placeholder="20:00" value={r.window.start} invalid={!!err?.startsWith("Opens at")} describedBy={errId} onChange={(v) => setWindow({ start: v })} />
        <TimeField id={`${id}-end`} label="Closes at" placeholder="08:00" value={r.window.end} invalid={!!err?.startsWith("Closes at")} describedBy={errId} onChange={(v) => setWindow({ end: v })} />
        <div className="flex min-w-40 flex-1 flex-col gap-1">
          <label htmlFor={`${id}-tz`} className="text-muted-foreground">
            Time zone
          </label>
          <Input id={`${id}-tz`} className="text-xs" placeholder="Instance time zone" spellCheck={false} value={r.window.timezone ?? ""} onChange={(e) => setWindow({ timezone: e.target.value || undefined })} />
        </div>
      </div>
      {err ? (
        <p id={errId} className="text-status-warning-foreground">
          {err}
        </p>
      ) : (
        <p className="text-[11px] text-muted-foreground">
          {r.kind} may run {formatDays(r.window.days)}, {r.window.start}–{r.window.end}
          {overnight ? " (closes the next day)" : ""}, {r.window.timezone ?? "instance time"}.
        </p>
      )}
    </div>
  );
}

function TimeField({ id, label, placeholder, value, invalid, describedBy, onChange }: { id: string; label: string; placeholder: string; value: string; invalid: boolean; describedBy?: string; onChange: (v: string) => void }) {
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-muted-foreground">
        {label}
      </label>
      <Input
        id={id}
        className="w-20 text-xs tabular-nums"
        inputMode="numeric"
        placeholder={placeholder}
        value={value}
        aria-invalid={invalid || undefined}
        aria-describedby={invalid ? describedBy : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
    </div>
  );
}
