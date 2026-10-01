import type { DefaultRange, Defaults, DefaultValue } from "@/api/gen/types.gen";
import { lookupDefault } from "@/shell/entity/defaults";

// The parameter schemas forms render (docs/spec/03-pipelines-defaults.md "Model"): JSON Schema whose properties carry
// x-cadence {default, description, source, range[, defaultRef, unit]}. A defaultRef points into defaults.yaml and wins
// over the inline default, so the value, its description, source and safe range come from one place.

export type XCadence = {
  default?: unknown;
  defaultRef?: string;
  description?: string;
  source?: string;
  unit?: string;
  range?: DefaultRange & { maxLength?: number };
};

export type ParamSchema = {
  type?: string | string[];
  title?: string;
  description?: string;
  properties?: Record<string, ParamSchema>;
  required?: string[];
  items?: ParamSchema;
  enum?: unknown[];
  minimum?: number;
  maximum?: number;
  exclusiveMinimum?: number;
  exclusiveMaximum?: number;
  minItems?: number;
  maxItems?: number;
  multipleOf?: number;
  default?: unknown;
  "x-cadence"?: XCadence;
};

export type FieldKind = "object" | "number" | "integer" | "boolean" | "enum" | "string" | "multi" | "pair" | "json";

export type Values = Record<string, unknown>;

function typeOf(s: ParamSchema): string | undefined {
  return Array.isArray(s.type) ? s.type.find((t) => t !== "null") : s.type;
}

/** How a property renders. */
export function fieldKind(s: ParamSchema): FieldKind {
  const t = typeOf(s);
  if (t === "object" || (!t && s.properties)) return s.properties ? "object" : "json";
  if (s.enum?.length && t !== "array") return "enum";
  if (t === "number") return "number";
  if (t === "integer") return "integer";
  if (t === "boolean") return "boolean";
  if (t === "string") return "string";
  if (t === "array") {
    const it = s.items ?? {};
    if (it.enum?.length) return "multi";
    const itemType = typeOf(it);
    if ((itemType === "number" || itemType === "integer") && s.minItems === 2 && s.maxItems === 2) return "pair";
  }
  return "json";
}

/** The defaults.yaml entry behind a property, merged with its inline x-cadence (defaults.yaml wins). */
export function defaultInfo(s: ParamSchema, defaults: Defaults | undefined): DefaultValue | undefined {
  const x = s["x-cadence"];
  const ref = x?.defaultRef ? lookupDefault(defaults, x.defaultRef) : undefined;
  const value = ref ? ref.value : x && "default" in x ? x.default : s.default;
  if (value === undefined && !x?.description && !ref) return undefined;
  const range: DefaultRange | undefined = ref?.range ?? rangeOf(s, x);
  return {
    value,
    unit: ref?.unit ?? x?.unit,
    description: ref?.description ?? x?.description ?? s.description ?? "",
    source: ref?.source ?? x?.source ?? "",
    ...(range ? { range } : {}),
  };
}

function rangeOf(s: ParamSchema, x: XCadence | undefined): DefaultRange | undefined {
  const r = x?.range;
  if (r && (r.min !== undefined || r.max !== undefined || r.values?.length)) return { min: r.min, max: r.max, values: r.values };
  if (s.enum?.length) return { values: s.enum.map(String) };
  if (s.minimum !== undefined || s.maximum !== undefined) return { min: s.minimum, max: s.maximum };
  return undefined;
}

/** The default value of a property (defaults.yaml via defaultRef, else x-cadence.default, else the schema default). */
export function defaultFor(s: ParamSchema, defaults: Defaults | undefined): unknown {
  if (fieldKind(s) === "object") return recommended(s, defaults);
  return defaultInfo(s, defaults)?.value;
}

/** Every property at its default: what "Reset to recommended" writes. */
export function recommended(s: ParamSchema, defaults: Defaults | undefined): Values {
  const out: Values = {};
  for (const [k, p] of Object.entries(s.properties ?? {})) {
    const v = defaultFor(p, defaults);
    if (v !== undefined) out[k] = v;
  }
  return out;
}

export function getAt(obj: unknown, path: string[]): unknown {
  let cur = obj;
  for (const k of path) {
    if (!cur || typeof cur !== "object") return undefined;
    cur = (cur as Values)[k];
  }
  return cur;
}

/** A copy of obj with the value at path replaced (intermediate objects created). */
export function setAt(obj: Values, path: string[], value: unknown): Values {
  if (path.length === 0) return obj;
  const [head, ...rest] = path as [string, ...string[]];
  const child = obj[head];
  const next = rest.length ? setAt(child && typeof child === "object" && !Array.isArray(child) ? (child as Values) : {}, rest, value) : value;
  return { ...obj, [head]: next };
}

export function sameValue(a: unknown, b: unknown): boolean {
  return JSON.stringify(a) === JSON.stringify(b);
}

/** Dotted paths of the leaf properties whose value differs from the default (a missing value is the default). */
export function departuresOf(s: ParamSchema, values: Values | undefined, defaults: Defaults | undefined, prefix: string[] = []): string[] {
  const out: string[] = [];
  for (const [k, p] of Object.entries(s.properties ?? {})) {
    const path = [...prefix, k];
    if (fieldKind(p) === "object") {
      out.push(...departuresOf(p, values, defaults, path));
      continue;
    }
    const v = getAt(values, path);
    if (v === undefined) continue;
    const d = defaultFor(p, defaults);
    if (d !== undefined && !sameValue(v, d)) out.push(path.join("."));
  }
  return out;
}

/** A warning for a value outside the property's safe range, or undefined (a warning, never a block). */
export function outOfRange(s: ParamSchema, v: unknown, defaults: Defaults | undefined): string | undefined {
  const r = defaultInfo(s, defaults)?.range;
  if (!r || v === undefined || v === null) return undefined;
  const nums = typeof v === "number" ? [v] : Array.isArray(v) && v.every((x) => typeof x === "number") ? (v as number[]) : null;
  if (nums) {
    if (nums.some((n) => !Number.isFinite(n))) return "Not a number";
    if (r.min !== undefined && nums.some((n) => n < r.min!)) return `Below the safe range (min ${r.min})`;
    if (r.max !== undefined && nums.some((n) => n > r.max!)) return `Above the safe range (max ${r.max})`;
    if (nums.length === 2 && nums[0]! > nums[1]!) return "The low end is above the high end";
    return undefined;
  }
  const strs = typeof v === "string" ? [v] : Array.isArray(v) ? v.map(String) : [];
  if (r.values?.length && strs.some((x) => !r.values!.includes(x))) return `Outside the safe values (${r.values.join(", ")})`;
  return undefined;
}

/** A value as the read-only form shows it. */
export function displayValue(v: unknown, unit?: string): string {
  if (v === undefined) return "—";
  const s = Array.isArray(v) ? v.map((x) => (typeof x === "object" ? JSON.stringify(x) : String(x))).join(", ") : typeof v === "object" && v !== null ? JSON.stringify(v) : String(v);
  return unit ? `${s} ${unit}` : s;
}

/** A property's label: its title, else the key in words. */
export function labelOf(key: string, s: ParamSchema): string {
  if (s.title) return s.title;
  const words = key.replace(/([a-z0-9])([A-Z])/g, "$1 $2").replace(/[_-]+/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}
