import type { AvailabilityWindow, AvailabilityWindows, JobKind } from "@/api/gen/types.gen";
import { Plus, Trash } from "iconoir-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { DAY_LABEL, DAYS, windowError } from "@/shell/panel";

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

export function WindowsEditor({
  card,
  kinds,
  value,
  onChange,
}: {
  card: number;
  kinds: JobKind[];
  value: AvailabilityWindows | undefined;
  onChange: (w: AvailabilityWindows) => void;
}) {
  const rows = windowRows(value);
  const set = (i: number, r: Row) => onChange(windowsOf(rows.map((x, j) => (j === i ? r : x))));
  const remove = (i: number) => onChange(windowsOf(rows.filter((_, j) => j !== i)));
  const add = () =>
    onChange(
      windowsOf([
        ...rows,
        {
          kind: "training",
          window: {
            days: ["mon", "tue", "wed", "thu", "fri"],
            start: "20:00",
            end: "08:00",
          },
        },
      ]),
    );
  return (
    <fieldset className="flex flex-col gap-1.5" data-testid={`windows-editor-${card}`}>
      <legend className="pb-1 text-muted-foreground">Availability windows of card {card} — a job kind without windows may run any time</legend>
      {rows.map((r, i) => {
        const err = windowError(r.window);
        return (
          <div key={i} className="flex flex-wrap items-center gap-2 rounded-md border p-1.5" role="group" aria-label={`Window ${i + 1}`}>
            <NativeSelect
              className="h-6 w-auto text-xs"
              aria-label="Job kind"
              value={r.kind}
              onChange={(e) => set(i, { ...r, kind: e.target.value as JobKind })}
            >
              {kinds.map((k) => (
                <option key={k} value={k}>
                  {k}
                </option>
              ))}
            </NativeSelect>
            <span className="inline-flex flex-wrap gap-1.5" role="group" aria-label="Days">
              {DAYS.map((d, di) => (
                <label key={d} className="inline-flex min-h-6 items-center gap-0.5">
                  <input
                    type="checkbox"
                    className="size-3.5 accent-primary"
                    checked={r.window.days.includes(d)}
                    onChange={(e) =>
                      set(i, {
                        ...r,
                        window: {
                          ...r.window,
                          days: e.target.checked ? DAYS.filter((x) => x === d || r.window.days.includes(x)) : r.window.days.filter((x) => x !== d),
                        },
                      })
                    }
                  />
                  {DAY_LABEL[di]}
                </label>
              ))}
            </span>
            <label className="inline-flex items-center gap-1">
              <span className="text-muted-foreground">from</span>
              <Input
                className="h-6 w-16 text-xs tabular-nums"
                aria-label="Opens at"
                placeholder="20:00"
                value={r.window.start}
                onChange={(e) =>
                  set(i, {
                    ...r,
                    window: { ...r.window, start: e.target.value },
                  })
                }
              />
            </label>
            <label className="inline-flex items-center gap-1">
              <span className="text-muted-foreground">to</span>
              <Input
                className="h-6 w-16 text-xs tabular-nums"
                aria-label="Closes at"
                placeholder="08:00"
                value={r.window.end}
                onChange={(e) => set(i, { ...r, window: { ...r.window, end: e.target.value } })}
              />
            </label>
            <Input
              className="h-6 w-36 text-xs"
              aria-label="Time zone"
              placeholder="instance time zone"
              value={r.window.timezone ?? ""}
              onChange={(e) =>
                set(i, {
                  ...r,
                  window: {
                    ...r.window,
                    timezone: e.target.value || undefined,
                  },
                })
              }
            />
            <Button type="button" size="icon-xs" variant="ghost" className="ml-auto size-6" aria-label={`Remove window ${i + 1}`} onClick={() => remove(i)}>
              <Trash aria-hidden />
            </Button>
            {err ? <span className="w-full text-status-warning-foreground">{err}</span> : null}
          </div>
        );
      })}
      <Button type="button" size="xs" variant="outline" className="w-fit" onClick={add}>
        <Plus aria-hidden />
        Add window
      </Button>
    </fieldset>
  );
}
