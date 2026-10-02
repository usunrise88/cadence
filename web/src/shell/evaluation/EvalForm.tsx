import { useEffect, useId, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plus, Xmark } from "iconoir-react";
import { ProblemError } from "@/api/client";
import {
  adoptionsListOptions,
  gatesGetOptions,
  langpacksListOptions,
  modelFamiliesListOptions,
  projectsGetOptions,
  recipesListOptions,
  runsGetOptions,
} from "@/api/gen/@tanstack/react-query.gen";
import type { Eval, EvalPlan, EvalSubjectRef } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { cn } from "@/lib/utils";
import { runCommand } from "@/shell/panel/commands";
import { openDocument } from "@/shell/panel/actions";
import { isAugmentationProfile } from "@/shell/forms/augmentation";
import {
  augmentRef,
  boostRef,
  emptyEvalForm,
  evalBody,
  formProblems,
  languagesForRun,
  planLine,
  shortAugment,
  shortBoost,
  type EvalFormState,
} from "./evalForm";

// Run eval… (docs/spec/11-ui-panels.md "Commands": Run eval matrix, dry run first): the axes of evals.new — golden
// sets the project adopted, latency profiles of the subject's family, decoding variants (boost lists of the
// project's language packs with a weight), augmentation profiles (augment/*.yaml) and the languages map — then the
// plan (cells cached and to compute, the estimate) before Start. Shared by Checkpoints, the Experiment document and
// the Eval report (Run eval…, Re-run missing cells) through the panel SDK.

export type EvalFormProps = {
  project: string;
  subject: EvalSubjectRef;
  /** What is evaluated, for the heading. */
  subjectLabel: string;
  /** The subject's model family: its latency profiles are offered. */
  family?: string;
  /** The run the checkpoint comes from: a train step under target_lang prefills the languages map. */
  runId?: string;
  /** A filled form (Re-run missing cells: the eval's own axes). */
  initial?: EvalFormState;
  /** Ask for the plan as soon as the form opens. */
  planOnOpen?: boolean;
  onStarted?: (e: Eval) => void;
  onClose: () => void;
};

type Message = { error: boolean; text: string; fields?: string[] };

function messageOf(err: unknown): Message {
  if (err instanceof ProblemError) {
    const fields = (err.problem.errors ?? []).map((f) => (f.path ? `${f.path}: ${f.message}` : f.message));
    return { error: true, text: err.problem.detail ?? err.problem.title, fields };
  }
  return { error: true, text: err instanceof Error ? err.message : String(err) };
}

const toggle = (list: string[], v: string) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);

const fieldset = "flex min-w-0 flex-col gap-1 rounded-md border p-2";
const legend = "px-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase";
const check = "flex min-h-6 items-center gap-1.5";

export function EvalForm({ project, subject, subjectLabel, family, runId, initial, planOnOpen, onStarted, onClose }: EvalFormProps) {
  const id = useId();
  const [form, setForm] = useState<EvalFormState>(initial ?? emptyEvalForm());
  const [plan, setPlan] = useState<{ plan: EvalPlan; key: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<Message | null>(null);

  const adopted = useQuery(adoptionsListOptions({ path: { p: project }, query: { kind: "golden_set" } }));
  const gates = useQuery({ ...gatesGetOptions({ path: { p: project } }), retry: false });
  const families = useQuery(modelFamiliesListOptions());
  const packs = useQuery(langpacksListOptions({ path: { p: project } }));
  const augment = useQuery({ ...recipesListOptions({ path: { p: project }, query: { prefix: "augment/" } }), retry: false });
  const proj = useQuery(projectsGetOptions({ path: { p: project } }));
  const run = useQuery({ ...runsGetOptions({ path: { id: runId ?? "" } }), enabled: !!runId });

  // A checkpoint trained under a neighbour's prompt (target_lang) decodes the project's locales in that language,
  // unless the form came filled or the person already wrote the map.
  const prefilled = useRef(!!initial);
  useEffect(() => {
    if (prefilled.current || !run.data || !proj.data) return;
    prefilled.current = true;
    const languages = languagesForRun(proj.data.locales, run.data.departures);
    if (languages.length) setForm((f) => (f.languages.length ? f : { ...f, languages }));
  }, [run.data, proj.data]);

  const goldenSets = (adopted.data?.items ?? []).map((a) => a.version);
  const fam = (families.data?.items ?? []).find((f) => f.modelFamily.name === family) ?? (family ? undefined : families.data?.items[0]);
  const profiles = fam?.modelFamily.latencyProfiles ?? [];
  const boostOptions = (packs.data?.items ?? []).flatMap((p) => p.boost.map((domain) => ({ ref: boostRef(p, `boost/${domain}.txt`), label: `${p.locale} ${domain}` })));
  const augmentOptions = (augment.data?.items ?? []).filter((f) => isAugmentationProfile(f.path)).map((f) => augmentRef(f.path, augment.data!.commit));
  const gateSets = [...(gates.data?.config.target?.goldenSets ?? []), ...(gates.data?.config.replay?.goldenSets ?? [])];

  const body = useMemo(() => evalBody(subject, form), [subject, form]);
  const key = JSON.stringify(body);
  const problems = formProblems(form);
  const set = (patch: Partial<EvalFormState>) => setForm((f) => ({ ...f, ...patch }));

  const act = async (dryRun: boolean) => {
    if (problems.length) return;
    setBusy(true);
    setMessage(null);
    try {
      const res = await runCommand("evals.new", { project, body, dryRun });
      if ("approvalId" in res) setMessage({ error: false, text: `The eval waits for an approval (${res.approvalId}); it starts once a person approves it in Approvals.` });
      else if ("cellsToCompute" in res) setPlan({ plan: res, key });
      else {
        openDocument(`eval:${res.id}`);
        onStarted?.(res);
        onClose();
      }
    } catch (err) {
      setMessage(messageOf(err));
    } finally {
      setBusy(false);
    }
  };
  const asked = useRef(false);
  useEffect(() => {
    if (!planOnOpen || asked.current) return;
    asked.current = true;
    void act(true);
  });

  const current = plan && plan.key === key ? plan.plan : null;
  const boostOf = (ref: string) => form.boosts.find((b) => b.ref === ref);
  return (
    <div className="flex flex-col gap-2 rounded-md border bg-tool p-2 text-xs" role="group" aria-labelledby={`${id}-title`} data-slot="eval-form">
      <p id={`${id}-title`} className="font-medium">
        Evaluate {subjectLabel} against the baseline
      </p>
      <div className="grid gap-2 @lg:grid-cols-2">
        <fieldset className={fieldset} data-axis="golden-sets">
          <legend className={legend}>Golden sets</legend>
          {goldenSets.length === 0 ? (
            <p className="text-muted-foreground">{adopted.isLoading ? "Loading…" : "The project adopted no golden set: open one in the Library and adopt it into the project."}</p>
          ) : (
            goldenSets.map((g) => (
              <label key={g.id} className={check}>
                <input type="checkbox" className="size-4 accent-primary" checked={form.goldenSets.includes(g.id)} onChange={() => set({ goldenSets: toggle(form.goldenSets, g.id) })} />
                <span className="truncate">
                  {g.name} <span className="text-muted-foreground">{g.version}</span>
                </span>
              </label>
            ))
          )}
          <p className="text-[11px] text-muted-foreground">
            {form.goldenSets.length ? `${form.goldenSets.length} chosen` : gateSets.length ? `None ticked: the sets gates.yaml names (${gateSets.join(", ")})` : "None ticked: every adopted golden set"}
          </p>
        </fieldset>
        <fieldset className={fieldset} data-axis="profiles">
          <legend className={legend}>Latency profiles</legend>
          {profiles.length === 0 ? <p className="text-muted-foreground">{families.isLoading ? "Loading…" : "The model family declares no profiles."}</p> : null}
          <div className="flex flex-wrap gap-x-3">
            {profiles.map((p) => (
              <label key={p.name} className={check}>
                <input type="checkbox" className="size-4 accent-primary" checked={form.profiles.includes(p.name)} onChange={() => set({ profiles: toggle(form.profiles, p.name) })} />
                {p.name}
              </label>
            ))}
          </div>
          <p className="text-[11px] text-muted-foreground">{form.profiles.length ? `${form.profiles.length} chosen` : "None ticked: eval.matrix_profiles plus the gate's primary profile"}</p>
        </fieldset>
        <fieldset className={fieldset} data-axis="decoding">
          <legend className={legend}>Decoding</legend>
          <label className={check}>
            <input type="checkbox" className="size-4 accent-primary" checked={form.none || form.boosts.length === 0} disabled={form.boosts.length === 0} onChange={() => set({ none: !form.none })} />
            No boosting
          </label>
          {boostOptions.map((o) => {
            const chosen = boostOf(o.ref);
            return (
              <div key={o.ref} className="flex flex-wrap items-center gap-1.5">
                <label className={check}>
                  <input
                    type="checkbox"
                    className="size-4 accent-primary"
                    checked={!!chosen}
                    onChange={() => set({ boosts: chosen ? form.boosts.filter((b) => b.ref !== o.ref) : [...form.boosts, { ref: o.ref, weight: "" }] })}
                  />
                  Boost {o.label}
                </label>
                {chosen ? (
                  <Input
                    className="h-6 w-28 text-xs"
                    inputMode="decimal"
                    aria-label={`Weight of ${shortBoost(o.ref)}`}
                    placeholder="the list's weight"
                    value={chosen.weight}
                    onChange={(e) => set({ boosts: form.boosts.map((b) => (b.ref === o.ref ? { ...b, weight: e.target.value } : b)) })}
                  />
                ) : null}
              </div>
            );
          })}
          {form.boosts
            .filter((b) => !boostOptions.some((o) => o.ref === b.ref))
            .map((b) => (
              <label key={b.ref} className={check}>
                <input type="checkbox" className="size-4 accent-primary" checked onChange={() => set({ boosts: form.boosts.filter((x) => x.ref !== b.ref) })} />
                Boost {shortBoost(b.ref)} <span className="text-muted-foreground">(at the eval's commit)</span>
              </label>
            ))}
          {boostOptions.length === 0 && form.boosts.length === 0 ? <p className="text-[11px] text-muted-foreground">No boost list in the project's language packs.</p> : null}
        </fieldset>
        <fieldset className={fieldset} data-axis="augmentations">
          <legend className={legend}>Robustness</legend>
          <label className={check}>
            <input type="checkbox" className="size-4 accent-primary" checked disabled />
            Clean audio (always)
          </label>
          {[...new Set([...augmentOptions, ...form.augmentations])].map((ref) => (
            <label key={ref} className={check}>
              <input type="checkbox" className="size-4 accent-primary" checked={form.augmentations.includes(ref)} onChange={() => set({ augmentations: toggle(form.augmentations, ref) })} />
              {shortAugment(ref)}
              {!augmentOptions.includes(ref) ? <span className="text-muted-foreground">(at the eval's commit)</span> : null}
            </label>
          ))}
          {augmentOptions.length === 0 && form.augmentations.length === 0 ? <p className="text-[11px] text-muted-foreground">No augmentation profile in augment/.</p> : null}
        </fieldset>
      </div>
      <fieldset className={fieldset} data-axis="languages">
        <legend className={legend}>Languages</legend>
        <p className="text-[11px] text-muted-foreground">
          Decode a golden set's locale in another language — for a model fine-tuned under a neighbour's prompt (target_lang). Empty: each set in its own
          locale.
        </p>
        {form.languages.map(([l, d], i) => (
          <div key={i} className="flex items-center gap-1.5">
            <Input className="h-6 w-28 text-xs" aria-label={`Language row ${i + 1}: golden-set locale`} placeholder="sr-RS" value={l} list={`${id}-locales`} onChange={(e) => set({ languages: form.languages.map((r, k) => (k === i ? [e.target.value, r[1]] : r)) })} />
            <span aria-hidden>→</span>
            <Input className="h-6 w-28 text-xs" aria-label={`Language row ${i + 1}: decode as`} placeholder="hr-HR" value={d} onChange={(e) => set({ languages: form.languages.map((r, k) => (k === i ? [r[0], e.target.value] : r)) })} />
            <Button size="icon-xs" variant="ghost" className="size-6" aria-label={`Remove language row ${i + 1}`} onClick={() => set({ languages: form.languages.filter((_, k) => k !== i) })}>
              <Xmark aria-hidden />
            </Button>
          </div>
        ))}
        <datalist id={`${id}-locales`}>
          {(proj.data?.locales ?? []).map((l) => (
            <option key={l} value={l} />
          ))}
        </datalist>
        <div>
          <Button size="xs" variant="ghost" onClick={() => set({ languages: [...form.languages, ["", ""]] })}>
            <Plus aria-hidden /> Map a locale
          </Button>
        </div>
      </fieldset>
      <label className="flex flex-wrap items-center gap-1.5">
        <span className="text-muted-foreground">Baseline</span>
        <Input className="h-6 w-64 text-xs" placeholder="@baseline, else the project's base model" value={form.baseline} onChange={(e) => set({ baseline: e.target.value })} aria-label="Baseline" />
      </label>
      {problems.length ? (
        <ul role="alert" className="list-disc pl-5 text-destructive">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      ) : null}
      {current ? (
        <p className="tabular-nums" data-slot="eval-plan">
          {planLine(current)}
        </p>
      ) : plan ? (
        <p className="text-muted-foreground">The form changed since the plan: plan again before starting.</p>
      ) : null}
      <div className="flex flex-wrap gap-1">
        <Button size="xs" variant="outline" disabled={busy || problems.length > 0} onClick={() => void act(true)} data-command="evals.new">
          {busy && !current ? "Planning…" : "Plan"}
        </Button>
        <Button size="xs" disabled={busy || !current} onClick={() => void act(false)} data-command="evals.new" title={current ? undefined : "Plan first: the dry run shows what is cached and what the rest costs"}>
          Start eval
        </Button>
        <Button size="xs" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </div>
      {message ? (
        <div role={message.error ? "alert" : "status"} className={cn(message.error ? "text-destructive" : "text-muted-foreground")}>
          <p>{message.text}</p>
          {message.fields?.length ? (
            <ul className="mt-1 list-disc pl-5">
              {message.fields.map((f) => (
                <li key={f}>{f}</li>
              ))}
            </ul>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
