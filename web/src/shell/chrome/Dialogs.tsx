import { useState, type FormEvent } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { commandHeaders, ProblemError } from "@/api/client";
import { projectsGetOptions, projectsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import { projectsEdit, projectsNew } from "@/api/gen/sdk.gen";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { chordLabel } from "@/shell/commands/keymap";
import { commands } from "@/shell/registries";
import { notify } from "@/shell/notifications/store";
import { useDialogs, type DialogRequest } from "./dialogs";
import { Palette } from "./Palette";

// Modal flows opened by commands. Each submits exactly one API command.

export function Dialogs({ onSwitchProject }: { onSwitchProject: (slug: string) => void }) {
  const { open, close } = useDialogs();
  if (!open) return null;
  switch (open.kind) {
    case "palette":
      return <Palette prefix={open.prefix} onClose={close} onSwitchProject={onSwitchProject} />;
    case "newProject":
      return <NewProjectDialog onClose={close} onCreated={onSwitchProject} />;
    case "editProject":
      return <EditProjectDialog slug={open.slug} onClose={close} />;
    case "confirm":
      return <ConfirmDialog req={open} onClose={close} />;
    case "shortcuts":
      return <ShortcutsDialog onClose={close} />;
  }
}

export function slugify(name: string): string {
  return name
    .toLowerCase()
    .normalize("NFKD")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40);
}

function Field({ label, error, children }: { label: string; error?: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs">
      <span className="text-muted-foreground">{label}</span>
      {children}
      {error ? <span className="text-destructive">{error}</span> : null}
    </label>
  );
}

function fieldErrors(err: unknown): Record<string, string> {
  if (!(err instanceof ProblemError)) return {};
  const out: Record<string, string> = {};
  for (const e of err.problem.errors ?? []) out[e.path.replace(/^\/?(body\/)?/, "")] = e.message;
  return out;
}

function NewProjectDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (slug: string) => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugTouched, setSlugTouched] = useState(false);
  const [description, setDescription] = useState("");
  const [error, setError] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const effectiveSlug = slugTouched ? slug : slugify(name);
  const errs = fieldErrors(error);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await projectsNew({ body: { name, slug: effectiveSlug, ...(description ? { description } : {}) }, headers: commandHeaders() });
      await qc.invalidateQueries({ queryKey: projectsListQueryKey() });
      notify({ level: "success", title: `Project “${name}” created` });
      onClose();
      onCreated(effectiveSlug);
    } catch (err) {
      setError(err);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <DialogHeader>
            <DialogTitle>New project</DialogTitle>
            <DialogDescription>A unit of work with its own repository, budgets and gates. Repository bootstrap arrives in phase 1.</DialogDescription>
          </DialogHeader>
          <Field label="Name" error={errs.name}>
            <Input value={name} onChange={(e) => setName(e.target.value)} required autoFocus name="name" />
          </Field>
          <Field label="Slug (lowercase, digits, dashes)" error={errs.slug}>
            <Input
              value={effectiveSlug}
              onChange={(e) => {
                setSlugTouched(true);
                setSlug(e.target.value);
              }}
              pattern="[a-z][a-z0-9\-]{1,38}[a-z0-9]"
              required
              name="slug"
            />
          </Field>
          <Field label="Description (optional)" error={errs.description}>
            <Input value={description} onChange={(e) => setDescription(e.target.value)} name="description" />
          </Field>
          {error && Object.keys(errs).length === 0 ? (
            <p role="alert" className="text-xs text-destructive">
              {error instanceof Error ? error.message : String(error)}
            </p>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" disabled={busy || !name || !effectiveSlug}>
              Create project
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function EditProjectDialog({ slug, onClose }: { slug: string; onClose: () => void }) {
  const qc = useQueryClient();
  const { data } = useQuery(projectsGetOptions({ path: { p: slug } }));
  const [name, setName] = useState<string | null>(null);
  const [error, setError] = useState<unknown>(null);
  if (!data) return null;
  const value = name ?? data.name;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    try {
      await projectsEdit({ path: { p: slug }, body: { name: value }, headers: commandHeaders(data.rev) });
      await qc.invalidateQueries({ queryKey: projectsListQueryKey() });
      onClose();
    } catch (err) {
      setError(err);
    }
  };
  const conflict = error instanceof ProblemError && error.status === 412;
  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <form onSubmit={submit} className="flex flex-col gap-3">
          <DialogHeader>
            <DialogTitle>Rename project</DialogTitle>
            <DialogDescription>{slug}</DialogDescription>
          </DialogHeader>
          <Field label="Name" error={fieldErrors(error).name}>
            <Input value={value} onChange={(e) => setName(e.target.value)} required autoFocus />
          </Field>
          {conflict ? (
            <p role="alert" className="text-xs text-destructive">
              Someone changed this project meanwhile (now rev {error.problem.currentRev}). Close and try again.
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
