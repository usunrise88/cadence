import { useEffect, useId, useRef, useState } from "react";
import { Plus, Xmark } from "iconoir-react";
import type { Experiment, SweepPlan } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { errorMessage, runCommand, useKeyedEstimate } from "@/shell/panel";
import { emptyRow, showValue, sweepBody, type ParamRow, type SweepFormState } from "./model";

// New sweep (sweeps.run): a grid or a random draw over recipe parameters with a GPU-hour cap. Estimate sends the dry
// run (the points with their estimates, the total against the cap and today's budget); Start sends exactly the body
// that estimate answered — any edit makes it stale until it is estimated again.

const KNOWN = ["peak_lr", "warmup_steps", "replayShare", "augmentation", "min_lr", "weight_decay", "steps"];
const n = (v: number | undefined, d = 2) => (v === undefined || !Number.isFinite(v) ? "—" : String(Math.round(v * 10 ** d) / 10 ** d));

export function SweepForm({ experiment, onClose, onStarted }: { experiment: Experiment; onClose: () => void; onStarted: () => void }) {
  const listId = useId();
  const [form, setForm] = useState<SweepFormState>({ mode: "grid", rows: [emptyRow()], runs: "", cap: "", seed: "", steps: "" });
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ error: boolean; text: string } | null>(null);
  const first = useRef<HTMLInputElement>(null);
  useEffect(() => first.current?.focus(), []);
  const built = sweepBody(form);
  const est = useKeyedEstimate<SweepPlan>(JSON.stringify(built.body ?? null));
  const plan = est.estimate;
  const known = [...new Set([...(experiment.parameters ?? []).map((p) => p.name), ...KNOWN])];
  const setRow = (i: number, patch: Partial<ParamRow>) => setForm((f) => ({ ...f, rows: f.rows.map((r, j) => (j === i ? { ...r, ...patch } : r)) }));
  const act = async (dryRun: boolean) => {
    if (!built.body || (!dryRun && !est.fresh)) return;
    const ticket = dryRun ? est.begin() : 0;
    let answer: SweepPlan | undefined;
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("sweeps.run", { experiment: { id: experiment.id, rev: experiment.rev }, body: built.body, dryRun });
      if (!res) return;
      if ("approvalId" in res) setMessage({ error: false, text: `The sweep waits for an approval (${res.approvalId}).` });
      else if ("withinCap" in res) answer = res;
      else {
        onStarted();
        onClose();
      }
    } catch (err) {
      setMessage({ error: true, text: errorMessage(err) });
    } finally {
      if (dryRun) est.settle(ticket, answer);
      setBusy(false);
    }
  };
  return (
    <form onSubmit={(ev) => (ev.preventDefault(), void act(true))} className="flex flex-col gap-2 rounded-md border bg-tool p-3" aria-label="New sweep" data-slot="sweep-form">
      <datalist id={listId}>
        {known.map((k) => (
          <option key={k} value={k} />
        ))}
      </datalist>
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor={`${listId}-mode`} className="text-muted-foreground">
          Mode
        </label>
        <NativeSelect id={`${listId}-mode`} className="h-6 w-auto text-xs" value={form.mode} onChange={(ev) => setForm((f) => ({ ...f, mode: ev.target.value as SweepFormState["mode"] }))}>
          <option value="grid">grid — every combination</option>
          <option value="random">random — a seeded draw</option>
        </NativeSelect>
      </div>
      <table className="w-full" aria-label="Swept parameters">
        <thead className="text-left text-muted-foreground">
          <tr>
            <th className="font-normal">Parameter</th>
            <th className="font-normal">Values (comma-separated or JSON)</th>
            {form.mode === "random" ? (
              <>
                <th className="font-normal">Min</th>
                <th className="font-normal">Max</th>
                <th className="font-normal">Scale</th>
                <th className="font-normal">Integer</th>
              </>
            ) : null}
            <th className="w-6 font-normal">
              <span className="sr-only">Remove</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {form.rows.map((r, i) => (
            <tr key={i} data-row={i}>
              <td className="pr-1">
                <Input ref={i === 0 ? first : undefined} list={listId} aria-label={`Parameter ${i + 1} name`} className="h-6 text-xs" placeholder="peak_lr" value={r.name} onChange={(ev) => setRow(i, { name: ev.target.value })} />
              </td>
              <td className="pr-1">
                <Input aria-label={`Parameter ${i + 1} values`} className="h-6 text-xs tabular-nums" placeholder="0.0001, 0.0003" value={r.values} onChange={(ev) => setRow(i, { values: ev.target.value })} />
              </td>
              {form.mode === "random" ? (
                <>
                  <td className="pr-1">
                    <Input aria-label={`Parameter ${i + 1} min`} className="h-6 w-20 text-xs" value={r.min} onChange={(ev) => setRow(i, { min: ev.target.value })} />
                  </td>
                  <td className="pr-1">
                    <Input aria-label={`Parameter ${i + 1} max`} className="h-6 w-20 text-xs" value={r.max} onChange={(ev) => setRow(i, { max: ev.target.value })} />
                  </td>
                  <td className="pr-1">
                    <NativeSelect aria-label={`Parameter ${i + 1} scale`} className="h-6 w-auto text-xs" value={r.scale} onChange={(ev) => setRow(i, { scale: ev.target.value as ParamRow["scale"] })}>
                      <option value="linear">linear</option>
                      <option value="log">log</option>
                    </NativeSelect>
                  </td>
                  <td className="pr-1 text-center">
                    <input type="checkbox" className="size-4 accent-primary" aria-label={`Parameter ${i + 1} integer`} checked={r.integer} onChange={(ev) => setRow(i, { integer: ev.target.checked })} />
                  </td>
                </>
              ) : null}
              <td>
                <Button type="button" size="icon-xs" variant="ghost" aria-label={`Remove parameter ${i + 1}`} disabled={form.rows.length === 1} onClick={() => setForm((f) => ({ ...f, rows: f.rows.filter((_, j) => j !== i) }))}>
                  <Xmark aria-hidden />
                </Button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <Button type="button" size="xs" variant="ghost" className="w-fit" disabled={form.rows.length >= 8} onClick={() => setForm((f) => ({ ...f, rows: [...f.rows, emptyRow()] }))}>
        <Plus aria-hidden />
        Add parameter
      </Button>
      <div className="grid grid-cols-[7rem_8rem] items-center gap-x-2 gap-y-1">
        {(
          [
            ["runs", form.mode === "random" ? "Runs" : "At most runs", "default"],
            ["cap", "GPU-hour cap", "default"],
            ["seed", "Seed", "default"],
            ["steps", "Steps per run", "default"],
          ] as const
        ).map(([key, label, ph]) =>
          key === "seed" && form.mode !== "random" ? null : (
            <div key={key} className="contents">
              <label htmlFor={`${listId}-${key}`} className="text-muted-foreground">
                {label}
              </label>
              <Input id={`${listId}-${key}`} className="h-6 text-xs tabular-nums" placeholder={ph} value={form[key]} onChange={(ev) => setForm((f) => ({ ...f, [key]: ev.target.value }))} />
            </div>
          ),
        )}
      </div>
      {built.error ? (
        <p className="text-muted-foreground" data-slot="sweep-form-problem">
          {built.error}
        </p>
      ) : null}
      {plan ? (
        <div className={cn("flex flex-col gap-1", est.stale && "text-muted-foreground line-through")} data-slot="sweep-plan" data-stale={est.stale || undefined}>
          <p className="tabular-nums">
            {plan.points.length} runs, ~{n(plan.estimateGpuHours.value)} GPU-h ({n(plan.estimateGpuHours.low)}–{n(plan.estimateGpuHours.high)}, {plan.basis}) against a cap of {n(plan.gpuHourCap)}
            {plan.withinCap ? "" : ` — over the cap: the first ${plan.fits} fit`}
            {plan.budget.withinDailyBudget ? "" : " — over today's budget"}
          </p>
          <ol className="flex flex-col" aria-label="Sweep points">
            {plan.points.map((p) => (
              <li key={p.index} className="flex gap-2 tabular-nums">
                <span className="w-6 text-muted-foreground">{p.index + 1}</span>
                <span className="font-mono">
                  {Object.entries(p.values)
                    .map(([k, v]) => `${k}=${showValue(v)}`)
                    .join(" ")}
                </span>
                <span className="ml-auto text-muted-foreground">~{n(p.estimateGpuHours)} GPU-h</span>
              </li>
            ))}
          </ol>
        </div>
      ) : null}
      {est.stale ? <p className="text-muted-foreground">{est.pending ? "Estimating…" : "The form changed since this estimate: estimate again to start."}</p> : null}
      <div className="flex gap-1">
        <Button type="submit" size="xs" variant="outline" disabled={busy || !built.body}>
          Estimate
        </Button>
        <Button type="button" size="xs" disabled={busy || !built.body || !est.fresh || !plan?.withinCap} onClick={() => void act(false)} data-command="sweeps.run">
          Start sweep
        </Button>
        <Button type="button" size="xs" variant="ghost" onClick={onClose}>
          Cancel
        </Button>
      </div>
      {message ? (
        <p role={message.error ? "alert" : "status"} className={message.error ? "text-destructive" : "text-muted-foreground"}>
          {message.text}
        </p>
      ) : null}
    </form>
  );
}
