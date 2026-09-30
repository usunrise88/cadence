import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { policiesGetOptions, policiesGetQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { Policies, PolicyBudgets } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { errorMessage, formatDefault, formatRange, lookupDefault, problemOf, rangeWarning, runCommand, useDefaults, useTopic, WhyDefault } from "@/shell/panel";
import { Chip, Field, SectionHeading, when } from "./ui";

// Policies (docs/spec/11-ui-panels.md "Settings"): the instance-wide default budgets now; retention, PII redaction
// and cache quotas join in phases 4–5. Every field shows its default and "Why this default?"; departures from
// defaults.yaml show as chips; "Reset to recommended" puts every field back.

type BudgetKey = keyof PolicyBudgets;

export const BUDGET_FIELDS: { key: BudgetKey; ref: string; label: string; integer: boolean }[] = [
  { key: "gpuHoursPerProjectPerDay", ref: "budgets.gpu_hours_per_project_per_day", label: "GPU-hours per project per day", integer: false },
  { key: "agentTurnsPerSession", ref: "budgets.agent_turns_per_session", label: "Agent turns per session", integer: true },
];

/** "budgets.gpuHoursPerProjectPerDay" → the field label. */
export function departureLabel(path: string): string {
  const key = path.split(".").pop();
  return BUDGET_FIELDS.find((f) => f.key === key)?.label ?? path;
}

export function PoliciesSection() {
  const qc = useQueryClient();
  const { data } = useQuery(policiesGetOptions());
  useTopic(["entity.policies.*"], (batch) => {
    for (const e of batch) {
      const p = (e.payload as { policies?: Policies } | undefined)?.policies;
      if (p) qc.setQueryData(policiesGetQueryKey(), p);
    }
  });
  return (
    <section aria-labelledby="settings-policies" className="flex flex-col gap-3">
      <SectionHeading id="settings-policies" title="Policies" hint="Default budgets every project and agent session starts with. Retention, PII redaction and cache quotas arrive with their blocks (phases 4–5)." />
      {data ? <PoliciesForm key={data.rev} policies={data} /> : <p className="text-xs text-muted-foreground">Loading…</p>}
    </section>
  );
}

function PoliciesForm({ policies }: { policies: Policies }) {
  const qc = useQueryClient();
  const defaults = useDefaults();
  const [values, setValues] = useState<Record<BudgetKey, string>>({
    gpuHoursPerProjectPerDay: String(policies.budgets.gpuHoursPerProjectPerDay),
    agentTurnsPerSession: String(policies.budgets.agentTurnsPerSession),
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState<string | null>(null);

  const send = async (budgets: PolicyBudgets, what: string) => {
    setBusy(true);
    setError(null);
    setSaved(null);
    try {
      const next = await runCommand("policies.edit", { policies, body: { budgets } });
      qc.setQueryData(policiesGetQueryKey(), next);
      setSaved(what);
    } catch (err) {
      if (problemOf(err)?.status === 412) {
        setError("The policies changed meanwhile (another tab or person). The latest values are loaded; check them and save again.");
        void qc.invalidateQueries({ queryKey: policiesGetQueryKey() });
      } else setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void send({ gpuHoursPerProjectPerDay: Number(values.gpuHoursPerProjectPerDay), agentTurnsPerSession: Number(values.agentTurnsPerSession) }, "Saved");
  };
  const recommended = (): PolicyBudgets | undefined => {
    const g = lookupDefault(defaults.data, "budgets.gpu_hours_per_project_per_day");
    const t = lookupDefault(defaults.data, "budgets.agent_turns_per_session");
    return g && t ? { gpuHoursPerProjectPerDay: Number(g.value), agentTurnsPerSession: Number(t.value) } : undefined;
  };
  const rec = recommended();
  const dirty = BUDGET_FIELDS.some((f) => Number(values[f.key]) !== policies.budgets[f.key]);

  return (
    <form onSubmit={submit} aria-label="Default budgets" className="flex flex-col gap-3 rounded-md border p-3">
      <div className="flex flex-wrap items-center gap-1">
        <span className="text-xs text-muted-foreground">Departures from defaults:</span>
        {policies.departures.length === 0 ? <Chip>none — recommended values</Chip> : null}
        {policies.departures.map((d) => (
          <Chip key={d} tone="accent" title={d}>
            {departureLabel(d)}
          </Chip>
        ))}
      </div>
      <div className="grid gap-3 @md:grid-cols-2">
        {BUDGET_FIELDS.map((f) => {
          const def = lookupDefault(defaults.data, f.ref);
          const id = `policy-${f.key}`;
          const v = Number(values[f.key]);
          return (
            <Field
              key={f.key}
              label={`${f.label}${def?.unit ? ` (${def.unit})` : ""}`}
              htmlFor={id}
              hint={def ? `Default ${formatDefault(def)}${def.range ? ` · safe range ${formatRange(def.range, def.unit)}` : ""}` : undefined}
              warning={values[f.key] === "" ? "Required" : rangeWarning(v, def?.range)}
              extra={<WhyDefault label={f.label} value={def} />}
            >
              <Input
                id={id}
                type="number"
                step={f.integer ? 1 : 0.5}
                value={values[f.key]}
                onChange={(e) => setValues((s) => ({ ...s, [f.key]: e.target.value }))}
                className="h-7 w-40 text-xs"
                required
              />
            </Field>
          );
        })}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" size="xs" disabled={busy || !dirty} data-command="policies.edit">
          Save
        </Button>
        <Button type="button" size="xs" variant="outline" disabled={busy || !rec || policies.departures.length === 0} onClick={() => rec && void send(rec, "Reset to recommended")}>
          Reset to recommended
        </Button>
        <span className="text-[11px] text-muted-foreground">
          rev {policies.rev} · updated {when(policies.updatedAt)}
        </span>
        {saved ? (
          <span role="status" className="text-xs text-muted-foreground">
            {saved}.
          </span>
        ) : null}
        {error ? (
          <span role="alert" className="text-xs text-destructive">
            {error}
          </span>
        ) : null}
      </div>
    </form>
  );
}
