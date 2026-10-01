import { useId } from "react";
import type { Defaults } from "@/api/gen/types.gen";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { formatDefault, useDefaults, WhyDefault } from "@/shell/entity/defaults";
import { defaultFor, defaultInfo, displayValue, fieldKind, getAt, labelOf, outOfRange, sameValue, setAt, type ParamSchema, type Values } from "./schema";

// A form rendered from a parameter schema (docs/spec/11-ui-panels.md "Progressive disclosure"): every field shows
// its default and "Why this default?" (description, default, source, safe range); a value that departs from the
// default carries a chip, and a value outside the safe range is a warning, not a block. Read-only for a pipeline
// step's resolved parameters; editable for recipe files such as an augmentation profile.

export type SchemaFormProps = {
  schema: ParamSchema;
  value: Values;
  /** Absent: read-only. */
  onChange?: (next: Values) => void;
  /** Dotted paths that depart from their default (the server's departures); computed from the schema otherwise. */
  departures?: string[];
  label: string;
  disabled?: boolean;
  className?: string;
};

export function SchemaForm({ schema, value, onChange, departures, label, disabled, className }: SchemaFormProps) {
  const defaults = useDefaults().data;
  const props = Object.entries(schema.properties ?? {});
  if (props.length === 0) return <p className={cn("text-xs text-muted-foreground", className)}>No parameters.</p>;
  return (
    <div role="group" aria-label={label} className={cn("flex flex-col gap-2 text-xs", className)} data-slot="schema-form" data-readonly={onChange ? undefined : "true"}>
      <Fields schema={schema} root={value} path={[]} defaults={defaults} onChange={onChange} departures={departures} disabled={disabled} />
    </div>
  );
}

type FieldsProps = {
  schema: ParamSchema;
  root: Values;
  path: string[];
  defaults: Defaults | undefined;
  onChange?: (next: Values) => void;
  departures?: string[];
  disabled?: boolean;
};

function Fields({ schema, root, path, defaults, onChange, departures, disabled }: FieldsProps) {
  return (
    <>
      {Object.entries(schema.properties ?? {}).map(([key, p]) => {
        const at = [...path, key];
        if (fieldKind(p) === "object") {
          return (
            <fieldset key={key} className="flex flex-col gap-2 rounded-md border px-3 pt-1 pb-2" data-field={at.join(".")}>
              <legend className="px-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">{labelOf(key, p)}</legend>
              {p.description ? <p className="text-muted-foreground">{p.description}</p> : null}
              <Fields schema={p} root={root} path={at} defaults={defaults} onChange={onChange} departures={departures} disabled={disabled} />
            </fieldset>
          );
        }
        return <Field key={key} name={key} schema={p} root={root} path={at} defaults={defaults} onChange={onChange} departures={departures} disabled={disabled} />;
      })}
    </>
  );
}

function Field({ name, schema, root, path, defaults, onChange, departures, disabled }: FieldsProps & { name: string }) {
  const id = useId();
  const dotted = path.join(".");
  const info = defaultInfo(schema, defaults);
  const raw = getAt(root, path);
  const def = defaultFor(schema, defaults);
  const value = raw === undefined ? def : raw;
  const departs = departures ? departures.includes(dotted) : raw !== undefined && def !== undefined && !sameValue(raw, def);
  const warning = outOfRange(schema, value, defaults);
  const label = labelOf(name, schema);
  const set = onChange ? (v: unknown) => onChange(setAt(root, path, v)) : undefined;
  return (
    <div className="grid grid-cols-[minmax(7rem,11rem)_1fr] items-start gap-x-3 gap-y-0.5" data-field={dotted} data-departs={departs || undefined}>
      <div className="flex min-h-6 items-center gap-1">
        <label htmlFor={set ? id : undefined} className="truncate text-muted-foreground" title={info?.description || label}>
          {label}
        </label>
        <WhyDefault label={label} value={info} />
      </div>
      <div className="flex min-w-0 flex-col gap-0.5">
        <div className="flex min-h-6 flex-wrap items-center gap-1.5">
          {set ? <Editor id={id} label={label} schema={schema} value={value} onChange={set} disabled={disabled} /> : <span className="tabular-nums">{displayValue(value, info?.unit)}</span>}
          {set && info?.unit && fieldKind(schema) !== "boolean" ? <span className="text-muted-foreground">{info.unit}</span> : null}
          {departs ? (
            <span className="rounded-full bg-accent-soft px-1.5 text-[11px] text-accent-text" title={info && info.value !== undefined ? `Default ${formatDefault(info)}` : undefined} data-slot="departure">
              departs from default{info && info.value !== undefined ? ` (${displayValue(info.value, info.unit)})` : ""}
            </span>
          ) : null}
        </div>
        {warning ? <span className="text-status-warning-foreground">{warning}</span> : null}
      </div>
    </div>
  );
}

function num(s: string): number | undefined {
  if (s.trim() === "") return undefined;
  const n = Number(s);
  return Number.isFinite(n) ? n : undefined;
}

function Editor({ id, label, schema, value, onChange, disabled }: { id: string; label: string; schema: ParamSchema; value: unknown; onChange: (v: unknown) => void; disabled?: boolean }) {
  const kind = fieldKind(schema);
  const step = schema.multipleOf ?? (kind === "integer" ? 1 : "any");
  switch (kind) {
    case "number":
    case "integer":
      return (
        <Input
          id={id}
          type="number"
          className="h-6 w-28 text-xs tabular-nums"
          step={step}
          min={schema.minimum}
          max={schema.maximum}
          value={typeof value === "number" ? value : ""}
          disabled={disabled}
          onChange={(e) => {
            const n = num(e.target.value);
            if (n !== undefined) onChange(kind === "integer" ? Math.round(n) : n);
          }}
        />
      );
    case "boolean":
      return <input id={id} type="checkbox" className="size-4 accent-primary" checked={value === true} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />;
    case "enum":
      return (
        <NativeSelect id={id} className="h-6 w-auto text-xs" value={String(value ?? "")} disabled={disabled} onChange={(e) => onChange(e.target.value)}>
          {(schema.enum ?? []).map((v) => (
            <option key={String(v)} value={String(v)}>
              {String(v)}
            </option>
          ))}
        </NativeSelect>
      );
    case "string":
      return <Input id={id} className="h-6 w-56 text-xs" value={typeof value === "string" ? value : ""} disabled={disabled} onChange={(e) => onChange(e.target.value)} />;
    case "multi": {
      const chosen = Array.isArray(value) ? value.map(String) : [];
      return (
        <fieldset id={id} className="flex flex-wrap gap-x-3 gap-y-1" disabled={disabled}>
          <legend className="sr-only">{label}</legend>
          {(schema.items?.enum ?? []).map((opt) => {
            const o = String(opt);
            return (
              <label key={o} className="inline-flex min-h-6 items-center gap-1">
                <input
                  type="checkbox"
                  className="size-3.5 accent-primary"
                  checked={chosen.includes(o)}
                  onChange={(e) => onChange(e.target.checked ? (schema.items?.enum ?? []).map(String).filter((x) => x === o || chosen.includes(x)) : chosen.filter((x) => x !== o))}
                />
                {o}
              </label>
            );
          })}
        </fieldset>
      );
    }
    case "pair": {
      const pair = Array.isArray(value) && value.length === 2 ? (value as number[]) : [0, 0];
      const it = schema.items ?? {};
      const set = (i: 0 | 1, s: string) => {
        const n = num(s);
        if (n === undefined) return;
        onChange(i === 0 ? [n, pair[1]] : [pair[0], n]);
      };
      return (
        <span id={id} className="inline-flex items-center gap-1" role="group" aria-label={label}>
          <Input type="number" aria-label={`${label}, low`} className="h-6 w-20 text-xs tabular-nums" step={it.multipleOf ?? "any"} value={pair[0]} disabled={disabled} onChange={(e) => set(0, e.target.value)} />
          <span className="text-muted-foreground">to</span>
          <Input type="number" aria-label={`${label}, high`} className="h-6 w-20 text-xs tabular-nums" step={it.multipleOf ?? "any"} value={pair[1]} disabled={disabled} onChange={(e) => set(1, e.target.value)} />
        </span>
      );
    }
    default:
      return <code className="text-[11px]">{displayValue(value)}</code>;
  }
}
