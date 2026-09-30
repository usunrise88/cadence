import { useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { commandHeaders, ProblemError } from "@/api/client";
import {
  agentModelsListOptions,
  baseModelsListOptions,
  defaultsGetOptions,
  projectsListQueryKey,
  templatesListOptions,
} from "@/api/gen/@tanstack/react-query.gen";
import { jobsGet, projectsGet, projectsNew } from "@/api/gen/sdk.gen";
import type { AgentDriver, Job, RepositoryKind } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { StatusChip } from "@/shell/entity/primitives";
import { notify } from "@/shell/notifications/store";
import { events } from "@/shell/registries";
import { defaultModel, departures, formatTokens, projectNewBody, recommended, slugify, templateNames, type WizardValues } from "./wizard";

// The project wizard (docs/spec/02-domain-projects-registry.md "Project wizard", docs/spec/11-ui-panels.md "The
// recommended path"): a modal flow, not a panel. Recommended mode is three fields; Customise shows every other
// choice with its default. Submitting answers 202 with the bootstrap job; the wizard follows it to the end and then
// opens the project.

const DRIVER_LABEL: Record<AgentDriver, string> = { "claude-code": "Claude Code", opencode: "opencode" };
const REPO_LABEL: Record<RepositoryKind, string> = { internal: "Internal (on this control plane)", github: "New GitHub repository", url: "Existing repository URL" };
const POLL_MS = 800;

export function Field({ label, error, hint, children, className }: { label: string; error?: string; hint?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <label className={cn("flex flex-col gap-1 text-xs", className)}>
      <span className="text-muted-foreground">{label}</span>
      {children}
      {hint ? <span className="text-[11px] text-muted-foreground">{hint}</span> : null}
      {error ? <span className="text-destructive">{error}</span> : null}
    </label>
  );
}

export function fieldErrors(err: unknown): Record<string, string> {
  if (!(err instanceof ProblemError)) return {};
  const out: Record<string, string> = {};
  for (const e of err.problem.errors ?? []) out[e.path.replace(/^\/?(body\/)?/, "").split("/")[0]!] = e.message;
  return out;
}

type Phase = { kind: "form" } | { kind: "progress"; jobId: string; slug: string; name: string };

export function ProjectWizard({ onClose, onCreated }: { onClose: () => void; onCreated: (slug: string) => void }) {
  const [phase, setPhase] = useState<Phase>({ kind: "form" });
  return (
    <Dialog open onOpenChange={(o) => !o && phase.kind === "form" && onClose()}>
      <DialogContent className="sm:max-w-xl" data-testid="project-wizard">
        {phase.kind === "form" ? (
          <WizardForm onClose={onClose} onAccepted={(jobId, slug, name) => setPhase({ kind: "progress", jobId, slug, name })} />
        ) : (
          <BootstrapProgress {...phase} onClose={onClose} onDone={onCreated} />
        )}
      </DialogContent>
    </Dialog>
  );
}

function WizardForm({ onClose, onAccepted }: { onClose: () => void; onAccepted: (jobId: string, slug: string, name: string) => void }) {
  const defaults = useQuery(defaultsGetOptions());
  const catalogue = useQuery(agentModelsListOptions());
  const baseModels = useQuery(baseModelsListOptions({ query: { state: "frozen" } }));
  const presets = useQuery(templatesListOptions({ query: { templateKind: "preset" } }));
  const instructions = useQuery(templatesListOptions({ query: { templateKind: "instructions" } }));
  const rec = useMemo(() => recommended(defaults.data, catalogue.data?.items), [defaults.data, catalogue.data]);
  const [edits, setEdits] = useState<Partial<WizardValues>>({});
  const [slugTouched, setSlugTouched] = useState(false);
  const [customise, setCustomise] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const v: WizardValues = { ...rec, ...edits };
  const slug = slugTouched ? v.slug : slugify(v.name);
  const set = (patch: Partial<WizardValues>) => setEdits((e) => ({ ...e, ...patch }));
  const errs = fieldErrors(error);

  const models = catalogue.data?.items.find((c) => c.driver === v.driver);
  const presetNames = templateNames(presets.data?.items.map((t) => t.name) ?? [], "preset");
  const instructionNames = templateNames(instructions.data?.items.map((t) => t.name) ?? [], "instructions");
  // The default base model is a collection name; the select shows versions, newest first.
  const bmItems = baseModels.data?.items ?? [];
  const defaultBm = bmItems.find((b) => b.name === rec.baseModel);
  const bmValue = v.baseModel === rec.baseModel ? (defaultBm?.id ?? rec.baseModel) : v.baseModel;
  const bmLabel = (id: string) => {
    const b = bmItems.find((x) => x.id === id || x.name === id);
    return b ? `${b.baseModel.hfRepo} @ ${b.baseModel.revision.slice(0, 7)}` : id.replace(/^base-model\//, "");
  };
  const departed = departures({ ...v, baseModel: bmValue === defaultBm?.id ? rec.baseModel : v.baseModel }, rec);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const body = projectNewBody({ ...v, slug }, rec, bmValue === (defaultBm?.id ?? rec.baseModel));
      const res = await projectsNew({ body, headers: commandHeaders() });
      const jobId = (res.data as { jobId?: string } | undefined)?.jobId;
      if (!jobId) throw new Error("the control plane did not answer with a bootstrap job");
      onAccepted(jobId, body.slug ?? slug, body.name);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };

  const summary: [string, string][] = [
    ["Domain", v.domain],
    ["Base model", bmLabel(bmValue)],
    ["Agent", `${DRIVER_LABEL[v.driver]} · ${v.model || "default model"}`],
    ["Permissions", v.preset],
    ["Instructions", `${v.instructions} template`],
    ["Repository", REPO_LABEL[v.repoKind]],
    ["Budgets", `${v.gpuHoursPerDay} GPU-h/day · ${formatTokens(v.agentTokensPerDay)} agent tokens/day`],
  ];

  return (
    <form onSubmit={submit} className="flex flex-col gap-3" aria-label="New project">
      <DialogHeader>
        <DialogTitle>New project</DialogTitle>
        <DialogDescription>Three fields; everything else starts from the recommended defaults and can be changed later.</DialogDescription>
      </DialogHeader>
      <Field label="Name" error={errs.name}>
        <Input value={v.name} onChange={(e) => set({ name: e.target.value })} required autoFocus name="name" maxLength={120} />
      </Field>
      <Field label="Language" error={errs.locales} hint="BCP 47 locale of the target language, e.g. he-IL">
        <Input value={v.locale} onChange={(e) => set({ locale: e.target.value })} required name="locale" pattern="[a-z]{2,3}(-[A-Za-z0-9]{2,8})*" />
      </Field>
      <Field label="Where the call recordings are" hint="Attaching a mount arrives with the Data block (phase 4); skip it for now.">
        <Input disabled placeholder="Skipped — attach a mount later" name="mount" />
      </Field>

      <section aria-labelledby="wizard-defaults" className="rounded-md border bg-muted/40 p-2.5">
        <div className="flex items-center gap-2">
          <h3 id="wizard-defaults" className="text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
            {departed.length ? `Customised (${departed.length})` : "Recommended defaults"}
          </h3>
          <div className="ml-auto flex gap-1">
            {customise && departed.length ? (
              <Button type="button" size="sm" variant="ghost" className="h-6 text-xs" onClick={() => setEdits((e) => ({ name: e.name, locale: e.locale, slug: e.slug }))}>
                Reset to recommended
              </Button>
            ) : null}
            <Button type="button" size="sm" variant="outline" className="h-6 text-xs" aria-expanded={customise} onClick={() => setCustomise((c) => !c)}>
              {customise ? "Hide" : "Customise"}
            </Button>
          </div>
        </div>
        {!customise ? (
          <dl className="mt-2 grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1 text-xs">
            {summary.map(([k, val]) => (
              <div key={k} className="contents">
                <dt className="text-muted-foreground">{k}</dt>
                <dd className="truncate" title={val}>
                  {val}
                </dd>
              </div>
            ))}
          </dl>
        ) : (
          <div className="mt-2 grid grid-cols-2 gap-2.5">
            <Field label="Slug (lowercase, digits, dashes)" error={errs.slug} className="col-span-2">
              <Input
                value={slug}
                onChange={(e) => {
                  setSlugTouched(true);
                  set({ slug: e.target.value });
                }}
                pattern="[a-z][a-z0-9\-]{1,38}[a-z0-9]"
                required
                name="slug"
              />
            </Field>
            <Field label="Domain" error={errs.domain}>
              <Input value={v.domain} onChange={(e) => set({ domain: e.target.value })} required name="domain" />
            </Field>
            <Field label="Base model" error={errs.baseModel}>
              <NativeSelect value={bmValue} onChange={(e) => set({ baseModel: e.target.value === defaultBm?.id ? rec.baseModel : e.target.value })} name="baseModel">
                {bmItems.length === 0 ? <option value={bmValue}>{bmLabel(bmValue)}</option> : null}
                {bmItems.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.baseModel.hfRepo} · {b.version}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field label="Agent driver">
              <NativeSelect
                value={v.driver}
                onChange={(e) => {
                  const driver = e.target.value as AgentDriver;
                  set({ driver, model: defaultModel(driver, catalogue.data?.items, defaults.data) });
                }}
                name="driver"
              >
                {(Object.keys(DRIVER_LABEL) as AgentDriver[]).map((d) => (
                  <option key={d} value={d}>
                    {DRIVER_LABEL[d]}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field label="Agent model" error={errs.agent} hint={models?.freeForm ? "Any provider/model configured for opencode" : undefined}>
              {models?.freeForm ? (
                <>
                  <Input value={v.model} onChange={(e) => set({ model: e.target.value })} list="wizard-models" name="model" required />
                  <datalist id="wizard-models">
                    {models.models.map((m) => (
                      <option key={m.id} value={m.id}>
                        {m.name}
                      </option>
                    ))}
                  </datalist>
                </>
              ) : (
                <NativeSelect value={v.model} onChange={(e) => set({ model: e.target.value })} name="model">
                  {(models?.models ?? [{ id: v.model, name: v.model }]).map((m) => (
                    <option key={m.id} value={m.id}>
                      {m.name}
                    </option>
                  ))}
                </NativeSelect>
              )}
            </Field>
            <Field label="Permission preset">
              <NativeSelect value={v.preset} onChange={(e) => set({ preset: e.target.value })} name="preset">
                {(presetNames.length ? presetNames : [v.preset]).map((n) => (
                  <option key={n}>{n}</option>
                ))}
              </NativeSelect>
            </Field>
            <Field label="Instructions template">
              <NativeSelect value={v.instructions} onChange={(e) => set({ instructions: e.target.value })} name="instructions">
                {(instructionNames.length ? instructionNames : [v.instructions]).map((n) => (
                  <option key={n}>{n}</option>
                ))}
              </NativeSelect>
            </Field>
            <Field label="Repository" error={errs.repository} className="col-span-2">
              <NativeSelect value={v.repoKind} onChange={(e) => set({ repoKind: e.target.value as RepositoryKind })} name="repository">
                {(Object.keys(REPO_LABEL) as RepositoryKind[]).map((k) => (
                  <option key={k} value={k}>
                    {REPO_LABEL[k]}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            {v.repoKind === "url" ? (
              <Field label="Repository URL" className="col-span-2">
                <Input value={v.repoUrl} onChange={(e) => set({ repoUrl: e.target.value })} placeholder="https://github.com/org/repo.git" required name="repoUrl" />
              </Field>
            ) : null}
            {v.repoKind === "github" ? (
              <Field label="GitHub owner" hint="Organisation; the token's user when empty">
                <Input value={v.repoOwner} onChange={(e) => set({ repoOwner: e.target.value })} name="repoOwner" />
              </Field>
            ) : null}
            {v.repoKind !== "internal" ? (
              <Field label="Token secret" hint="Name of a stored secret (Settings → Secrets)">
                <Input value={v.repoSecret} onChange={(e) => set({ repoSecret: e.target.value })} required={v.repoKind === "github"} name="repoSecret" />
              </Field>
            ) : null}
            <Field label="GPU-hours per day">
              <Input type="number" min={0} max={192} step="0.5" value={v.gpuHoursPerDay} onChange={(e) => set({ gpuHoursPerDay: e.target.value })} name="gpuHoursPerDay" />
            </Field>
            <Field label="Agent tokens per day">
              <Input type="number" min={0} step={1000} value={v.agentTokensPerDay} onChange={(e) => set({ agentTokensPerDay: e.target.value })} name="agentTokensPerDay" />
            </Field>
          </div>
        )}
      </section>

      {error && Object.keys(errs).length === 0 ? (
        <p role="alert" className="text-xs text-destructive">
          {error instanceof Error ? error.message : String(error)}
        </p>
      ) : null}
      {Object.keys(errs).length > 0 ? (
        <p role="alert" className="text-xs text-destructive">
          Some fields need a change{customise ? "" : " — open Customise"}.
        </p>
      ) : null}
      <DialogFooter>
        <Button type="button" variant="outline" onClick={onClose}>
          Cancel
        </Button>
        <Button type="submit" disabled={busy || !v.name.trim() || !slug}>
          Create project
        </Button>
      </DialogFooter>
    </form>
  );
}

function BootstrapProgress({ jobId, slug, name, onClose, onDone }: { jobId: string; slug: string; name: string; onClose: () => void; onDone: (slug: string) => void }) {
  const qc = useQueryClient();
  const [job, setJob] = useState<Job | null>(null);
  const [failure, setFailure] = useState<string | null>(null);
  const finished = useRef(false);
  const callbacks = useRef({ onClose, onDone });
  useEffect(() => {
    callbacks.current = { onClose, onDone };
  });

  useEffect(() => {
    let stop = false;
    const finish = async (j: Job) => {
      if (finished.current) return;
      finished.current = true;
      await qc.invalidateQueries({ queryKey: projectsListQueryKey() });
      if (j.state === "done") {
        notify({ level: "success", title: `Project “${name}” is ready` });
        callbacks.current.onDone(slug);
        callbacks.current.onClose();
        return;
      }
      let reason = j.error ?? `the bootstrap job ended ${j.state}`;
      try {
        const p = (await projectsGet({ path: { p: slug } })).data;
        if (p?.bootstrapError) reason = p.bootstrapError;
      } catch {
        /* keep the job's reason */
      }
      setFailure(reason);
    };
    const apply = (j: Job) => {
      if (stop) return;
      setJob(j);
      if (j.state === "done" || j.state === "failed" || j.state === "cancelled") void finish(j);
    };
    // The event stream is filtered to the open project, so the new project's job events may not arrive: poll too.
    const off = events.subscribe([`job.${jobId}`], (batch) => {
      for (const e of batch) {
        const j = (e.payload as { job?: Job } | undefined)?.job;
        if (j) apply(j);
      }
    }, "wizard");
    const tick = async () => {
      while (!stop && !finished.current) {
        try {
          const { data } = await jobsGet({ path: { id: jobId } });
          if (data) apply(data);
        } catch (err) {
          if (!stop) setFailure(err instanceof Error ? err.message : String(err));
          return;
        }
        await new Promise((r) => setTimeout(r, POLL_MS));
      }
    };
    void tick();
    return () => {
      stop = true;
      off();
    };
  }, [jobId, slug, name, qc]);

  const pct = Math.round((job?.progress ?? 0) * 100);
  return (
    <div className="flex flex-col gap-3" data-testid="bootstrap-progress">
      <DialogHeader>
        <DialogTitle>{failure ? `Bootstrap of “${name}” failed` : `Setting up “${name}”`}</DialogTitle>
        <DialogDescription>
          The bootstrap job writes the project repository (project.yaml, AGENTS.md, agent config, skills, starter pipelines), adopts the base model and
          creates the default workspaces.
        </DialogDescription>
      </DialogHeader>
      <div className="flex items-center gap-2 text-xs">
        <StatusChip state={failure ? "failed" : (job?.state ?? "queued")} />
        <span className="text-muted-foreground tabular-nums">{pct}%</span>
        <span className="ml-auto text-muted-foreground">{slug}</span>
      </div>
      <div role="progressbar" aria-label="Bootstrap progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct} className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div className={cn("h-full transition-[width]", failure ? "bg-status-failed" : "bg-primary")} style={{ width: `${Math.max(pct, 4)}%` }} />
      </div>
      <p aria-live="polite" className="min-h-4 text-xs text-muted-foreground">
        {failure ? null : (job?.message ?? "Queued…")}
      </p>
      {failure ? (
        <p role="alert" className="text-xs text-destructive">
          {failure}
        </p>
      ) : null}
      <DialogFooter>
        {failure ? (
          <>
            <Button variant="outline" onClick={onClose}>
              Close
            </Button>
            <Button
              onClick={() => {
                onDone(slug);
                onClose();
              }}
            >
              Open the project
            </Button>
          </>
        ) : (
          <Button variant="outline" onClick={onClose}>
            Continue in the background
          </Button>
        )}
      </DialogFooter>
    </div>
  );
}
