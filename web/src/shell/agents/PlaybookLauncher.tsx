import { useId, useMemo, useState, type FormEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Play } from "iconoir-react";
import { ProblemError } from "@/api/client";
import { playbooksListOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { AgentDriver, Playbook, PlaybookRunResult } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { cn } from "@/lib/utils";
import { runCommand } from "@/shell/commands/api";
import { askedInputs, budgetNote, estimateLine, initialValues, inputProblem, runInputs, type PlaybookFormValues } from "./playbooks";

// Start a playbook session (docs/spec/11-ui-panels.md "Agent sessions": New session → from a playbook; Project home):
// the playbook's inputs as a small form built from its schema, the estimate shown before starting (a playbooks.run
// dry run answers the estimate and the plan), then playbooks.run for real opens the session's Chat. Shared by the
// Agent sessions panel and the Project home through the panel SDK.

export type PlaybookLauncherProps = {
  project: string;
  /** Preselect a playbook (the Project home's button); else the first runnable one. */
  initial?: string;
  driver?: AgentDriver;
  model?: string;
  onStarted?: (result: PlaybookRunResult) => void;
  onCancel?: () => void;
};

function errorText(err: unknown): string {
  if (err instanceof ProblemError) {
    const fields = err.problem.errors?.map((f) => `${f.path.replace("/inputs/", "")}: ${f.message}`) ?? [];
    return [err.problem.detail ?? err.problem.title, ...fields].join(" · ");
  }
  return err instanceof Error ? err.message : String(err);
}

export function PlaybookLauncher(props: PlaybookLauncherProps) {
  const list = useQuery(playbooksListOptions({ query: { project: props.project } }));
  const items = useMemo(() => list.data?.items ?? [], [list.data]);
  const [name, setName] = useState<string | undefined>(props.initial);
  const playbook: Playbook | undefined = items.find((p) => p.name === name) ?? items.find((p) => p.runnable);
  if (list.isLoading) return <p className="text-xs text-muted-foreground">Loading playbooks…</p>;
  if (list.error)
    return (
      <p role="alert" className="text-xs text-destructive">
        {errorText(list.error)}
      </p>
    );
  if (!playbook) return <p className="text-xs text-muted-foreground">No playbook can run yet.</p>;
  // One form per playbook: switching playbooks starts from that playbook's defaults.
  return <LauncherForm key={playbook.name} {...props} playbook={playbook} items={items} onPick={setName} />;
}

function LauncherForm({
  project,
  initial,
  driver,
  model,
  onStarted,
  onCancel,
  playbook,
  items,
  onPick,
}: PlaybookLauncherProps & {
  playbook: Playbook;
  items: Playbook[];
  onPick: (name: string) => void;
}) {
  const id = useId();
  const [values, setValues] = useState<PlaybookFormValues>(() => initialValues(playbook));
  const [dry, setDry] = useState<PlaybookRunResult | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const asked = askedInputs(playbook);
  const problems = Object.fromEntries(asked.map((i) => [i.name, inputProblem(i, values[i.name] ?? "")]));
  const valid = playbook.runnable && Object.values(problems).every((p) => !p);
  const body = {
    inputs: runInputs(playbook, values),
    ...(driver ? { driver } : {}),
    ...(model ? { model } : {}),
  };
  const run = async (dryRun: boolean) => {
    setBusy(true);
    setError(null);
    try {
      const r = await runCommand("playbooks.run", {
        project,
        name: playbook.name,
        ...(playbook.version ? { version: playbook.version } : {}),
        body,
        dryRun,
        open: !dryRun,
      });
      if (dryRun) setDry(r ?? null);
      else if (r) onStarted?.(r);
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void run(!dry);
  };
  const shown = dry?.estimate ?? playbook.estimate;
  return (
    <form onSubmit={submit} className="flex flex-col gap-2 text-xs" aria-label="Start a playbook" data-slot="playbook-launcher">
      {initial ? null : (
        <div className="grid grid-cols-[4.5rem_1fr] items-center gap-2">
          <label htmlFor={`${id}-playbook`} className="text-muted-foreground">
            Playbook
          </label>
          <NativeSelect id={`${id}-playbook`} value={playbook.name} onChange={(e) => onPick(e.target.value)}>
            {items.map((p) => (
              <option key={p.name} value={p.name} disabled={!p.runnable}>
                {p.title}
                {p.runnable ? "" : ` (from phase ${p.availableFrom})`}
              </option>
            ))}
          </NativeSelect>
        </div>
      )}
      <p className="text-muted-foreground">
        {playbook.description}
        {playbook.typicalCost ? ` Typically ${playbook.typicalCost}.` : ""}
      </p>
      {playbook.unavailable ? <p className="text-status-warning-foreground">{playbook.unavailable}</p> : null}
      <div className="grid grid-cols-[6.5rem_1fr] items-start gap-x-2 gap-y-1.5">
        {asked.map((i) => (
          <div key={i.name} className="contents">
            <label htmlFor={`${id}-${i.name}`} className="pt-1 text-muted-foreground" title={i.defaultRef ? `defaults.yaml ${i.defaultRef}` : undefined}>
              {i.name}
              {i.required ? " *" : ""}
            </label>
            <div className="flex flex-col gap-0.5">
              <Input
                id={`${id}-${i.name}`}
                value={values[i.name] ?? ""}
                inputMode={i.type === "integer" || i.type === "number" ? "decimal" : undefined}
                placeholder={i.type === "dataset_version" ? (i.multiple ? "dataset/… (comma-separated)" : "dataset/…") : undefined}
                aria-invalid={!!problems[i.name] && (values[i.name] ?? "") !== ""}
                aria-describedby={`${id}-${i.name}-hint`}
                onChange={(e) => {
                  setValues((v) => ({ ...v, [i.name]: e.target.value }));
                  setDry(null);
                }}
              />
              <span
                id={`${id}-${i.name}-hint`}
                className={cn("text-[11px]", problems[i.name] && (values[i.name] ?? "") !== "" ? "text-destructive" : "text-muted-foreground")}
              >
                {problems[i.name] && (values[i.name] ?? "") !== "" ? problems[i.name] : i.description}
                {i.min !== undefined && i.max !== undefined ? ` (${i.min}–${i.max})` : ""}
              </span>
            </div>
          </div>
        ))}
        {playbook.inputs
          .filter((i) => i.from)
          .map((i) => (
            <div key={i.name} className="contents">
              <span className="text-muted-foreground">{i.name}</span>
              <span className="truncate" title={i.description}>
                {i.default !== undefined && i.default !== null
                  ? String(i.default)
                  : i.from === "adoption"
                    ? `none (${i.collection} not adopted)`
                    : "from the project"}
              </span>
            </div>
          ))}
      </div>
      <section aria-label="Estimate" className="rounded-md border bg-background px-2 py-1.5" data-slot="playbook-estimate">
        <p>
          <span className="font-medium">{dry ? "Estimate" : "Estimate (with defaults)"}:</span>{" "}
          {shown ? estimateLine(shown) : (playbook.estimateError ?? "unknown")}
        </p>
        {shown && budgetNote(shown) ? <p className="text-muted-foreground">{budgetNote(shown)}</p> : null}
        {dry ? (
          <ol className="mt-1 list-decimal pl-4" aria-label="Plan">
            {dry.plan.map((it) => (
              <li key={it.id} className={cn(it.state === "skipped" && "text-muted-foreground")} data-state={it.state}>
                {it.title} <code className="text-[11px]">{it.command}</code>
                {it.state === "skipped" ? " — skipped" : it.spending ? " — dry run first" : ""}
              </li>
            ))}
          </ol>
        ) : null}
      </section>
      <div className="flex items-center gap-1">
        <Button type="submit" size="xs" disabled={busy || !valid} data-command="playbooks.run">
          {dry ? <Play aria-hidden /> : null}
          {dry ? "Start playbook" : "Show the estimate"}
        </Button>
        {onCancel ? (
          <Button type="button" size="xs" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        ) : null}
      </div>
      {error ? (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      ) : null}
    </form>
  );
}
