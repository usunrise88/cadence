import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { commandHeaders, ProblemError } from "@/api/client";
import { baseModelsListOptions, projectsGetOptions, projectsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { ProjectEdit } from "@/api/gen/types.gen";
import { projectsEdit, projectsNote } from "@/api/gen/sdk.gen";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { Textarea } from "@/components/ui/textarea";
import { chordLabel } from "@/shell/commands/keymap";
import { commands } from "@/shell/registries";
import { notify } from "@/shell/notifications/store";
import { PortalContainerContext } from "@/lib/portal";
import { useDialogs, useFocusedDocument, type DialogRequest } from "./dialogs";
import { TwoFactorDialog } from "@/shell/auth/TwoFactorDialog";
import { Palette } from "./Palette";
import { Field, fieldErrors, ProjectWizard } from "./ProjectWizard";

// Modal flows opened by commands. Each submits exactly one API command.

export function Dialogs({ onSwitchProject }: { onSwitchProject: (slug: string) => void }) {
  const doc = useFocusedDocument((s) => s.doc);
  // Open in the window that has focus (a popout on a second monitor, or the main window).
  const body = doc && doc !== document && doc.defaultView && !doc.defaultView.closed ? doc.body : null;
  return (
    <PortalContainerContext.Provider value={body}>
      <DialogSwitch onSwitchProject={onSwitchProject} />
    </PortalContainerContext.Provider>
  );
}

function DialogSwitch({ onSwitchProject }: { onSwitchProject: (slug: string) => void }) {
  const { open, close } = useDialogs();
  if (!open) return null;
  switch (open.kind) {
    case "palette":
      return <Palette prefix={open.prefix} onClose={close} onSwitchProject={onSwitchProject} />;
    case "newProject":
      return <ProjectWizard onClose={close} onCreated={onSwitchProject} />;
    case "projectNote":
      return <NoteDialog slug={open.slug} rev={open.rev} onClose={close} />;
    case "editProject":
      return <EditProjectDialog slug={open.slug} onClose={close} />;
    case "confirm":
      return <ConfirmDialog req={open} onClose={close} />;
    case "shortcuts":
      return <ShortcutsDialog onClose={close} />;
    case "twoFactor":
      return <TwoFactorDialog onClose={close} />;
  }
}

type EditValues = { name: string; description: string; locales: string; domain: string; baseModel: string; gpuHoursPerDay: string; agentTokensPerDay: string };

/** projects.edit: what the wizard set and a person may change later (name, locales, domain, base model, budgets). */
function EditProjectDialog({ slug, onClose }: { slug: string; onClose: () => void }) {
  const qc = useQueryClient();
  const { data } = useQuery(projectsGetOptions({ path: { p: slug } }));
  const baseModels = useQuery(baseModelsListOptions({ query: { state: "frozen" } }));
  const [edits, setEdits] = useState<Partial<EditValues>>({});
  const [error, setError] = useState<unknown>(null);
  if (!data) return null;
  const initial: EditValues = {
    name: data.name,
    description: data.description ?? "",
    locales: data.locales.join(", "),
    domain: data.domain,
    baseModel: data.baseModel?.versionId ?? "",
    gpuHoursPerDay: String(data.budgets.gpuHoursPerDay),
    agentTokensPerDay: String(data.budgets.agentTokensPerDay),
  };
  const v = { ...initial, ...edits };
  const set = (patch: Partial<EditValues>) => setEdits((e) => ({ ...e, ...patch }));
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const body: ProjectEdit = {};
    if (v.name !== initial.name) body.name = v.name;
    if (v.description !== initial.description) body.description = v.description;
    if (v.locales !== initial.locales) body.locales = v.locales.split(/[\s,]+/).filter(Boolean);
    if (v.domain !== initial.domain) body.domain = v.domain;
    if (v.baseModel !== initial.baseModel && v.baseModel) body.baseModel = v.baseModel;
    const budgets: NonNullable<ProjectEdit["budgets"]> = {};
    if (v.gpuHoursPerDay !== initial.gpuHoursPerDay) budgets.gpuHoursPerDay = Number(v.gpuHoursPerDay);
    if (v.agentTokensPerDay !== initial.agentTokensPerDay) budgets.agentTokensPerDay = Number(v.agentTokensPerDay);
    if (Object.keys(budgets).length) body.budgets = budgets;
    if (Object.keys(body).length === 0) return onClose();
    try {
      await projectsEdit({ path: { p: slug }, body, headers: commandHeaders(data.rev) });
      await qc.invalidateQueries({ queryKey: projectsListQueryKey() });
      onClose();
    } catch (err) {
      setError(err);
    }
  };
  const conflict = error instanceof ProblemError && error.status === 412;
  const errs = fieldErrors(error);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-3">
          <DialogHeader>
            <DialogTitle>Edit project</DialogTitle>
            <DialogDescription>{slug} — changes to project facts re-render project.yaml and AGENTS.md and commit them to main.</DialogDescription>
          </DialogHeader>
          <Field label="Name" error={errs.name}>
            <Input value={v.name} onChange={(e) => set({ name: e.target.value })} required autoFocus />
          </Field>
          <Field label="Description" error={errs.description}>
            <Input value={v.description} onChange={(e) => set({ description: e.target.value })} />
          </Field>
          <div className="grid grid-cols-2 gap-2.5">
            <Field label="Locales (comma-separated)" error={errs.locales}>
              <Input value={v.locales} onChange={(e) => set({ locales: e.target.value })} required />
            </Field>
            <Field label="Domain" error={errs.domain}>
              <Input value={v.domain} onChange={(e) => set({ domain: e.target.value })} required />
            </Field>
            <Field label="Base model" error={errs.baseModel} className="col-span-2">
              <NativeSelect value={v.baseModel} onChange={(e) => set({ baseModel: e.target.value })}>
                {!v.baseModel ? <option value="">—</option> : null}
                {(baseModels.data?.items ?? []).map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.baseModel.hfRepo} · {b.version}
                  </option>
                ))}
              </NativeSelect>
            </Field>
            <Field label="GPU-hours per day" error={errs.budgets}>
              <Input type="number" min={0} max={192} step="0.5" value={v.gpuHoursPerDay} onChange={(e) => set({ gpuHoursPerDay: e.target.value })} />
            </Field>
            <Field label="Agent tokens per day">
              <Input type="number" min={0} step={1000} value={v.agentTokensPerDay} onChange={(e) => set({ agentTokensPerDay: e.target.value })} />
            </Field>
          </div>
          {conflict ? (
            <p role="alert" className="text-xs text-destructive">
              Someone changed this project meanwhile (now rev {error.problem.currentRev}). Close and try again.
            </p>
          ) : error && Object.keys(errs).length === 0 ? (
            <p role="alert" className="text-xs text-destructive">
              {error instanceof Error ? error.message : String(error)}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit">Save</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** projects.note from a document header or the palette: one dated learning, committed to NOTES.md. */
function NoteDialog({ slug, rev, onClose }: { slug: string; rev?: number; onClose: () => void }) {
  const qc = useQueryClient();
  const project = useQuery({ ...projectsGetOptions({ path: { p: slug } }), enabled: rev === undefined });
  const [text, setText] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      await projectsNote({ path: { p: slug }, body: { text }, headers: commandHeaders(rev ?? project.data?.rev) });
      await qc.invalidateQueries({ queryKey: projectsGetOptions({ path: { p: slug } }).queryKey });
      notify({ level: "success", title: "Note committed to NOTES.md" });
      onClose();
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-lg">
        <form onSubmit={submit} className="flex flex-col gap-3">
          <DialogHeader>
            <DialogTitle>Add a project note</DialogTitle>
            <DialogDescription>One learning in a sentence or two: what was tried, what happened, what to do next time. Every agent session reads NOTES.md.</DialogDescription>
          </DialogHeader>
          <Field label="Note" error={fieldErrors(error).text}>
            <Textarea value={text} onChange={(e) => setText(e.target.value)} required autoFocus maxLength={4000} rows={4} className="text-[13px]" />
          </Field>
          {error && !fieldErrors(error).text ? (
            <p role="alert" className="text-xs text-destructive">
              {error instanceof Error ? error.message : String(error)}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !text.trim()}>
              Commit note
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ConfirmDialog({ req, onClose }: { req: Extract<DialogRequest, { kind: "confirm" }>; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{req.title}</DialogTitle>
          {req.detail ? <DialogDescription>{req.detail}</DialogDescription> : null}
        </DialogHeader>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            disabled={busy}
            onClick={async () => {
              setBusy(true);
              await req.onConfirm();
              onClose();
            }}
          >
            {req.confirmLabel}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function ShortcutsDialog({ onClose }: { onClose: () => void }) {
  const bound = commands.all().filter((c) => c.keys?.length);
  const groups = [...new Set(bound.map((c) => c.group))];
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Keyboard shortcuts</DialogTitle>
          <DialogDescription>Generated from the command registry. Browser-reserved keys are never assigned.</DialogDescription>
        </DialogHeader>
        <div className="grid max-h-[60vh] grid-cols-2 gap-4 overflow-auto text-xs">
          {groups.map((g) => (
            <section key={g}>
              <h3 className="mb-1 font-medium">{g}</h3>
              <dl className="grid grid-cols-[1fr_auto] gap-x-3 gap-y-1">
                {bound
                  .filter((c) => c.group === g)
                  .map((c) => (
                    <div key={c.id} className="contents">
                      <dt>{c.title}</dt>
                      <dd className="text-muted-foreground tabular-nums">{c.keys!.map((k) => chordLabel(k)).join(" / ")}</dd>
                    </div>
                  ))}
              </dl>
            </section>
          ))}
        </div>
      </DialogContent>
    </Dialog>
  );
}
