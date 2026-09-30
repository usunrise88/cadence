import { useEffect, useState, type ReactNode } from "react";
import { useQuery, type QueryClient } from "@tanstack/react-query";
import { ProblemError } from "@/api/client";
import { draftsListOptions, draftsListQueryKey } from "@/api/gen/@tanstack/react-query.gen";
import type { CadenceEvent, Draft, DraftChange, DraftableKind, DraftList, Presence } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { agentLabel } from "@/shell/agents/attribution";
import { notifyError } from "@/shell/notifications/store";
import { commands } from "@/shell/registries";
import { commandContext } from "@/shell/state";
import { ActorBadge } from "./actor";

// Agent drafts look the same on every draftable kind (docs/spec/10-ui-shell.md "Lists, compare, drafts"): an
// accent-8 dashed outline, the diff on hover, Accept and Revert in the same place. The kind renders the draft's
// content inside the outline; everything else — loading, live patching, presence, stale drafts, the commands — is
// here, so a gate, a note or a language pack reuses it as it is.

/** The query of an entity's open drafts; the same key is patched from draft.* events. */
export function draftsQuery(kind: DraftableKind, entityId: string) {
  return draftsListOptions({ query: { entityKind: kind, entityId } });
}

export function useDrafts(kind: DraftableKind, entityId: string): { drafts: Draft[]; isLoading: boolean } {
  const q = useQuery({ ...draftsQuery(kind, entityId), enabled: !!entityId });
  return { drafts: q.data?.items ?? [], isLoading: q.isLoading };
}

/**
 * Patches open-draft lists from events: draft.* carries the whole draft (open ones are upserted, decided ones
 * dropped); any revision of the entity refreshes the drafts' staleness.
 */
export function patchDrafts(qc: QueryClient, batch: CadenceEvent[]): void {
  for (const e of batch) {
    const draft = (e.payload as { draft?: Draft } | undefined)?.draft;
    if (e.type.startsWith("draft.") && draft) {
      const key = draftsListQueryKey({ query: { entityKind: draft.entityKind, entityId: draft.entityId } });
      if (!qc.getQueryData(key)) {
        void qc.invalidateQueries({ queryKey: key });
        continue;
      }
      qc.setQueryData<DraftList>(key, (old) => {
        const rest = (old?.items ?? []).filter((d) => d.id !== draft.id);
        return { items: draft.state === "open" ? [draft, ...rest] : rest };
      });
    } else if (e.entity) {
      const { kind, id, rev } = e.entity;
      const key = draftsListQueryKey({ query: { entityKind: kind as DraftableKind, entityId: id } });
      qc.setQueryData<DraftList>(key, (old) =>
        old ? { items: old.items.map((d) => ({ ...d, currentRev: rev, stale: d.state === "open" && d.baseRev !== rev })) } : old,
      );
    }
  }
}

/** Presence still in force: open drafts, and direct edits whose window has not passed. */
export function activePresence(ps: Presence[] | undefined, now = Date.now()): Presence[] {
  return (ps ?? []).filter((p) => !p.until || Date.parse(p.until) > now);
}

/** activePresence that re-renders when the next direct-edit window closes. */
export function useActivePresence(ps: Presence[] | undefined): Presence[] {
  const [now, setNow] = useState(() => Date.now());
  const active = activePresence(ps, now);
  const next = Math.min(...active.filter((p) => p.until).map((p) => Date.parse(p.until!)));
  useEffect(() => {
    if (!Number.isFinite(next)) return;
    const t = setTimeout(() => setNow(Date.now()), Math.max(0, next - Date.now()) + 50);
    return () => clearTimeout(t);
  }, [next]);
  return active;
}

/** Who is editing, for reasons and notices: "opencode · session 9" (and "+1"). */
export function presenceLabel(ps: Presence[]): string {
  if (ps.length === 0) return "";
  const first = agentLabel(ps[0]!.actor);
  return ps.length > 1 ? `${first} +${ps.length - 1}` : first;
}

/** The "agent editing" chip of a document header (a live region: agent status is announced). */
export function PresenceChip({ presence }: { presence: Presence[] | undefined }) {
  const active = useActivePresence(presence);
  return (
    <span role="status" aria-live="polite" data-slot="presence" className="inline-flex">
      {active.length > 0 ? (
        <span className="inline-flex h-5 items-center gap-1.5 rounded-full bg-agent px-2 text-xs font-medium text-agent-foreground" title={`${presenceLabel(active)} is editing`}>
          <span aria-hidden className="size-1.5 animate-pulse rounded-full bg-accent-line" />
          agent editing
        </span>
      ) : null}
    </span>
  );
}

/** A notice above controls that presence disables: who is editing and why the controls wait. */
export function PresenceNotice({ presence, noun }: { presence: Presence[]; noun: string }) {
  if (presence.length === 0) return null;
  const p = presence[0]!;
  return (
    <div data-slot="presence-notice" className="flex items-center gap-2 rounded-md border px-3 py-1.5 text-xs">
      <ActorBadge actor={p.actor} toolCallId={p.toolCallId} />
      <span>
        is editing this {noun}
        {p.draftId ? ": its edits land as a draft. Editing here waits until the draft is accepted or reverted." : ". Editing here waits a few seconds."}
      </span>
    </div>
  );
}

function show(v: unknown): string {
  if (v === undefined) return "—";
  const s = typeof v === "string" ? v : JSON.stringify(v);
  return s.length > 80 ? `${s.slice(0, 77)}…` : s;
}

/** The draft's changes against its base revision: removed values on red-3, added ones on grass-3. */
export function DraftDiff({ changes }: { changes: DraftChange[] }) {
  if (changes.length === 0) return <p className="text-xs text-muted-foreground">No changes against the base revision.</p>;
  return (
    <ul data-slot="draft-diff" className="flex flex-col gap-1.5 text-xs">
      {changes.map((c) => (
        <li key={c.path} className="flex flex-col gap-0.5">
          <code className="text-muted-foreground">{c.path}</code>
          <span className="flex flex-wrap gap-1">
            {c.before !== undefined ? <span className="rounded bg-diff-removed px-1 text-diff-removed-foreground line-through">{show(c.before)}</span> : null}
            {c.after !== undefined ? <span className="rounded bg-diff-added px-1 text-diff-added-foreground">{show(c.after)}</span> : null}
          </span>
        </li>
      ))}
    </ul>
  );
}

/** True when a draft change touches path or anything under it (for highlighting cells the kind renders). */
export function changed(changes: DraftChange[], path: string): boolean {
  return changes.some((c) => c.path === path || c.path.startsWith(`${path}/`) || path.startsWith(`${c.path}/`));
}

/**
 * One open draft: the kind's rendering of the proposed content inside an accent-8 dashed outline, the author's
 * badge, the diff on hover and Accept / Revert (drafts.accept, drafts.revert) in the same place for every kind.
 */
export function DraftOutline({ draft, noun, children }: { draft: Draft; noun: string; children: ReactNode }) {
  const [busy, setBusy] = useState<"accept" | "revert" | null>(null);
  const [error, setError] = useState<ProblemError | null>(null);
  const run = async (verb: "accept" | "revert") => {
    setBusy(verb);
    setError(null);
    try {
      await commands.run(`drafts.${verb}`, commandContext(), { draft });
    } catch (err) {
      if (err instanceof ProblemError && err.status === 412) setError(err);
      else notifyError(verb === "accept" ? "Accept failed" : "Revert failed", err);
    } finally {
      setBusy(null);
    }
  };
  const acceptReason = draft.stale ? `The ${noun} moved to rev ${draft.currentRev} after this draft; revert it or have the agent redo the edit` : true;
  const accept = (
    <Button size="xs" disabled={acceptReason !== true || !!busy} onClick={() => void run("accept")} data-command="drafts.accept">
      Accept
    </Button>
  );
  return (
    <section
      data-slot="draft"
      data-draft-id={draft.id}
      data-draft-rev={draft.rev}
      data-draft-updated-at={draft.updatedAt}
      aria-label={`Draft by ${agentLabel(draft.author)}`}
      className="flex flex-col gap-2 rounded-md border border-dashed border-draft-outline p-2"
    >
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-medium text-accent-text">Draft</span>
        {draft.author.kind === "agent" ? <ActorBadge actor={draft.author} toolCallId={draft.toolCallId} /> : <span>{draft.author.name ?? draft.author.id}</span>}
        <span className="text-muted-foreground tabular-nums">
          rev {draft.rev} · based on {noun} rev {draft.baseRev}
        </span>
        <Popover>
          <PopoverTrigger openOnHover delay={150} render={<button type="button" className="h-6 rounded px-1.5 text-accent-text underline-offset-2 hover:underline" />}>
            {draft.changes.length} {draft.changes.length === 1 ? "change" : "changes"}
          </PopoverTrigger>
          <PopoverContent align="start" className="w-80">
            <DraftDiff changes={draft.changes} />
          </PopoverContent>
        </Popover>
        <div className="ml-auto flex gap-1">
          <Button size="xs" variant="outline" disabled={!!busy} onClick={() => void run("revert")} data-command="drafts.revert">
            Revert
          </Button>
          {acceptReason === true ? (
            accept
          ) : (
            <Tooltip>
              <TooltipTrigger render={<span tabIndex={0} />}>{accept}</TooltipTrigger>
              <TooltipContent>{acceptReason}</TooltipContent>
            </Tooltip>
          )}
        </div>
      </div>
      {draft.stale || error ? (
        <p role="alert" className="border-l-2 border-status-warning py-0.5 pl-2 text-xs text-status-warning-foreground">
          {error && error.slug !== "draft-stale"
            ? `The draft changed meanwhile (now rev ${error.problem.currentRev}); check it again before accepting.`
            : `Stale: the ${noun} is at rev ${error?.problem.currentRev ?? draft.currentRev}, this draft is based on rev ${draft.baseRev}. Revert it, or ask the agent to redo the edit on the current revision.`}
        </p>
      ) : null}
      {children}
    </section>
  );
}
