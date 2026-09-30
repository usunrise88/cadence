import { useQuery } from "@tanstack/react-query";
import { HelpCircle } from "iconoir-react";
import { defaultsGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { DefaultRange, Defaults, DefaultValue } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

// Progressive disclosure (docs/spec/11-ui-panels.md "First run and progressive disclosure"): every field shows its
// default, and "Why this default?" is a popover rendered from defaults.yaml — description, default, source and safe
// range. A value outside the range is a warning, not a block.

/** defaults.get, cached for the session (the file is versioned with the binary). */
export function useDefaults() {
  return useQuery({ ...defaultsGetOptions(), staleTime: Infinity });
}

/** Looks up `<section>.<key>` (x-cadence.defaultRef), e.g. `budgets.gpu_hours_per_project_per_day`. */
export function lookupDefault(d: Defaults | undefined, ref: string): DefaultValue | undefined {
  if (!d) return undefined;
  const [section, key] = ref.split(".");
  const s = (d as unknown as Record<string, unknown>)[section ?? ""];
  if (!s || typeof s !== "object" || !key) return undefined;
  const v = (s as Record<string, unknown>)[key];
  return v && typeof v === "object" && "value" in v ? (v as DefaultValue) : undefined;
}

export function formatDefault(v: DefaultValue): string {
  const val = Array.isArray(v.value) ? v.value.join(", ") : String(v.value);
  return v.unit ? `${val} ${v.unit}` : val;
}

export function formatRange(r: DefaultRange | undefined, unit?: string): string | undefined {
  if (!r) return undefined;
  if (r.values?.length) return r.values.join(" · ");
  const u = unit ? ` ${unit}` : "";
  if (r.min !== undefined && r.max !== undefined) return `${r.min}–${r.max}${u}`;
  if (r.min !== undefined) return `≥ ${r.min}${u}`;
  if (r.max !== undefined) return `≤ ${r.max}${u}`;
  return undefined;
}

/** A warning for a value outside the safe range, or undefined. */
export function rangeWarning(value: number | string, r: DefaultRange | undefined): string | undefined {
  if (!r) return undefined;
  if (typeof value === "string") return r.values?.length && !r.values.includes(value) ? `Outside the safe values (${r.values.join(", ")})` : undefined;
  if (!Number.isFinite(value)) return "Not a number";
  if (r.min !== undefined && value < r.min) return `Below the safe range (min ${r.min})`;
  if (r.max !== undefined && value > r.max) return `Above the safe range (max ${r.max})`;
  return undefined;
}

/** "Why this default?" — the popover every defaulted field carries. */
export function WhyDefault({ label, value }: { label: string; value: DefaultValue | undefined }) {
  if (!value) return null;
  const range = formatRange(value.range, value.unit);
  return (
    <Popover>
      <PopoverTrigger
        render={<Button type="button" variant="ghost" size="icon-xs" className="size-6 text-muted-foreground" aria-label={`Why this default? (${label})`} />}
      >
        <HelpCircle aria-hidden />
      </PopoverTrigger>
      <PopoverContent align="start" className="w-80 text-xs">
        <p className="font-medium">Why this default?</p>
        <p>{value.description}</p>
        <dl className="grid grid-cols-[4.5rem_1fr] gap-x-2 gap-y-1">
          <dt className="text-muted-foreground">Default</dt>
          <dd className="tabular-nums">{formatDefault(value)}</dd>
          {range ? (
            <>
              <dt className="text-muted-foreground">Safe range</dt>
              <dd className="tabular-nums">{range}</dd>
            </>
          ) : null}
          <dt className="text-muted-foreground">Source</dt>
          <dd>{value.source}</dd>
        </dl>
      </PopoverContent>
    </Popover>
  );
}
