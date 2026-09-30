import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { verbs } from "@/api/operations.gen";
import { Sparks } from "iconoir-react";
import { Button } from "@/components/ui/button";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";
import type { AgentReference } from "@/api/gen/types.gen";
import { askAgent, currentSelectionReferences, explainPrompt, explainReferences } from "@/shell/agents/bridge";
import { formatReference } from "@/shell/agents/references";
import { notifyError } from "@/shell/notifications/store";
import { commands, panels } from "@/shell/registries";
import { commandContext, useShell } from "@/shell/state";
import { ActorBadge } from "./actor";
import { PresenceChip } from "./drafts";
import { verbIconComponents } from "./icons";
import {
  LOOP_STEPS,
  STATE_TONES,
  type EntityData,
  type EntityManifest,
  type EntityVerb,
  type LoopStep,
  type StatusTone,
} from "./manifest";

// Shared primitives for the document anatomy (docs/spec/10-ui-shell.md "Document anatomy"). Document panels never
// draw their own header, status or actions: the shell renders these from the entity manifest (lint enforces it).

export const DOC_TABS = ["overview", "details", "lineage", "activity", "notes"] as const;
export type DocTab = (typeof DOC_TABS)[number];
const TAB_LABEL: Record<DocTab, string> = { overview: "Overview", details: "Details", lineage: "Lineage", activity: "Activity", notes: "Notes" };

const TONE_CLASS: Record<StatusTone, { dot: string; text: string }> = {
  neutral: { dot: "bg-muted-foreground", text: "text-muted-foreground" },
  running: { dot: "bg-status-running", text: "text-status-running-foreground" },
  done: { dot: "bg-status-done", text: "text-status-done-foreground" },
  warning: { dot: "bg-status-warning", text: "text-status-warning-foreground" },
  failed: { dot: "bg-status-failed", text: "text-status-failed-foreground" },
};

export function StatusChip({ state }: { state: string }) {
  const tone = TONE_CLASS[STATE_TONES[state] ?? "neutral"];
  return (
    <span data-slot="status-chip" className={cn("inline-flex items-center gap-1.5 text-xs font-medium capitalize", tone.text)}>
      <span aria-hidden className={cn("size-2 rounded-full", tone.dot)} />
      {state}
    </span>
  );
}

export { ActorBadge };

function commandIdFor(m: EntityManifest, v: EntityVerb): string {
  return `${m.apiEntity}.${v.verb}`;
}

function VerbButton({ m, v, entity, variant }: { m: EntityManifest; v: EntityVerb; entity: EntityData; variant: "default" | "outline" }) {
  const id = commandIdFor(m, v);
  const cmd = commands.get(id);
  const spec = verbs[v.verb];
  const Icon = verbIconComponents[spec.icon];
  const reason = !cmd ? `${id} is not available yet` : v.enabled ? v.enabled(entity) : true;
  const disabled = reason !== true;
  const button = (
    <Button
      size="sm"
      variant={variant}
      disabled={disabled}
      data-command={id}
      onClick={() => void commands.run(id, commandContext(), { entity })}
      className="h-6 px-2 text-xs"
    >
      {Icon ? <Icon aria-hidden /> : null}
      {cmd?.title ?? v.verb}
    </Button>
  );
  if (!disabled) return button;
  return (
    <Tooltip>
      <TooltipTrigger render={<span tabIndex={0} />}>{button}</TooltipTrigger>
      <TooltipContent>{reason}</TooltipContent>
    </Tooltip>
  );
}

/** The entity's verbs in one wrapping row: the primary first, then the rest. */
export function ActionBar({ manifest, entity }: { manifest: EntityManifest; entity: EntityData }) {
  if (manifest.verbs.length === 0) return null;
  const primary = manifest.verbs.find((v) => v.primary);
  const secondary = manifest.verbs.filter((v) => !v.primary);
  return (
    <div data-slot="action-bar" role="toolbar" aria-label="Actions" className="flex min-w-0 flex-wrap items-center gap-1">
      {primary ? <VerbButton m={manifest} v={primary} entity={entity} variant="default" /> : null}
      {secondary.map((v) => (
        <VerbButton key={v.verb} m={manifest} v={v} entity={entity} variant="outline" />
      ))}
    </div>
  );
}

export function LoopStepper({ current }: { current: LoopStep }) {
  const at = LOOP_STEPS.indexOf(current);
  return (
    <ol data-slot="loop-stepper" aria-label="Loop step" className="flex items-center text-[11px]">
      {LOOP_STEPS.map((s, i) => (
        <li key={s} className="flex items-center">
          {i > 0 ? <span aria-hidden className="mx-1 h-px w-3 bg-border" /> : null}
          <span
            aria-current={i === at ? "step" : undefined}
            className={cn(
              "rounded-full px-2 py-0.5 capitalize",
              i === at ? "bg-accent-soft font-medium text-accent-text" : i < at ? "text-foreground" : "text-muted-foreground",
            )}
          >
            {s}
          </span>
        </li>
      ))}
    </ol>
  );
}

export function EntityHeader({ manifest, entity }: { manifest: EntityManifest; entity: EntityData }) {
  const Icon = manifest.icon;
  return (
    <header data-slot="entity-header" className="flex flex-col gap-2.5 border-b px-4 pt-3 pb-2.5">
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1.5">
        <span className="flex size-6 shrink-0 items-center justify-center rounded-md bg-accent-soft text-accent-text">
          <Icon aria-hidden className="size-3.5" />
        </span>
        <h2 className="truncate text-sm font-semibold">{entity.name}</h2>
        {entity.version ? <span className="text-xs text-muted-foreground tabular-nums">{entity.version}</span> : null}
        <StatusChip state={entity.state} />
        <ActorBadge actor={entity.actor} toolCallId={entity.toolCallId} />
        {entity.presence ? <PresenceChip presence={entity.presence} /> : null}
        <div className="ml-auto flex shrink-0 items-center">
          <EntityAgentMenu manifest={manifest} entity={entity} />
        </div>
      </div>
      <ActionBar manifest={manifest} entity={entity} />
      <LoopStepper current={manifest.loopStep(entity)} />
      <dl data-slot="facts" className="grid grid-cols-4 gap-3">
        {manifest.facts.map((f) => (
          <div key={f.label} className="min-w-0">
            <dt className="truncate text-[11px] text-muted-foreground">{f.label}</dt>
            <dd className="truncate text-[13px] tabular-nums">{f.value(entity)}</dd>
          </div>
        ))}
      </dl>
    </header>
  );
}

/** Tool panels share one header row and a filter bar instead of the stepper. */
export function PanelToolbar({ children, className }: { children: ReactNode; className?: string }) {
  return <div className={cn("flex h-9 shrink-0 items-center gap-2 border-b px-2", className)}>{children}</div>;
}

export function EntityTabs({ value, onChange }: { value: DocTab; onChange: (t: DocTab) => void }) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const onKey = (e: KeyboardEvent, i: number) => {
    const d = e.key === "ArrowRight" ? 1 : e.key === "ArrowLeft" ? -1 : 0;
    if (!d) return;
    e.preventDefault();
    const next = (i + d + DOC_TABS.length) % DOC_TABS.length;
    onChange(DOC_TABS[next]!);
    refs.current[next]?.focus();
  };
  return (
    <div role="tablist" aria-label="Document sections" className="flex gap-3 border-b px-4">
      {DOC_TABS.map((t, i) => (
        <button
          key={t}
          ref={(el) => {
            refs.current[i] = el;
          }}
          role="tab"
          type="button"
          aria-selected={value === t}
          tabIndex={value === t ? 0 : -1}
          onKeyDown={(e) => onKey(e, i)}
          onClick={() => onChange(t)}
          className={cn(
            "-mb-px min-h-6 border-b-2 py-1.5 text-xs",
            value === t ? "border-accent-line font-medium text-foreground" : "border-transparent text-muted-foreground hover:text-foreground",
          )}
        >
          {TAB_LABEL[t]}
        </button>
      ))}
    </div>
  );
}

export function NextStep({ manifest, entity }: { manifest: EntityManifest; entity: EntityData }) {
  const s = manifest.nextStep(entity);
  const cmd = s.command ? commands.get(s.command) : undefined;
  return (
    <div data-slot="next-step" className="flex h-9 shrink-0 items-center gap-2 border-t bg-chrome px-4 text-xs">
      <span className="shrink-0 rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-medium whitespace-nowrap text-accent-text capitalize">Next · {s.step}</span>
      <span className="min-w-0 truncate text-muted-foreground" title={s.title}>
        {s.title}
      </span>
      <div className="ml-auto flex shrink-0 gap-1">
        {cmd ? (
          <Button size="xs" onClick={() => void commands.run(cmd.id, commandContext(), { entity })}>
            {cmd.title}
          </Button>
        ) : null}
      </div>
    </div>
  );
}

function capitalise(t: string): string {
  return t.charAt(0).toUpperCase() + t.slice(1);
}

/** The reference an entity attaches to a prompt (`@mix:mix_1`), labelled with its name. */
export function entityReference(m: EntityManifest, e: EntityData): AgentReference {
  return { ref: formatReference(m.kind, e.id), label: `${m.kind} ${e.name}` };
}

/** The help article of an entity kind: its document panel's. */
function helpOf(kind: string): string | undefined {
  return panels.all().find((p) => p.kind === "document" && p.entity === kind)?.help;
}

/**
 * Ask agent (docs/spec/05-agents.md "Context bridge"): Chat opens with the references attached and a prefilled prompt
 * naming them and the intent — never blank. Without references it attaches the current selection.
 */
export function AskAgentButton({ refs, intent, variant = "outline" }: { refs?: AgentReference[]; intent?: string; variant?: "outline" | "ghost" }) {
  const project = useShell((st) => st.project);
  const button = (
    <Button
      size="xs"
      variant={variant}
      disabled={!project}
      className="text-xs"
      data-command="view.askAgent"
      onClick={() => askAgent({ refs: refs ?? currentSelectionReferences(), intent: intent ?? "Help me with this" })}
    >
      Ask agent
    </Button>
  );
  if (project) return button;
  return (
    <Tooltip>
      <TooltipTrigger render={<span tabIndex={0} />}>{button}</TooltipTrigger>
      <TooltipContent>Open a project first</TooltipContent>
    </Tooltip>
  );
}

/**
 * Explain this (docs/spec/11-ui-panels.md "Help"): a read-only agent session — one turn, no mutating verbs — with
 * the entity and its help article attached, shown in Chat.
 */
export async function explainThis(entity: { kind: string; id: string; label?: string } | null, article: string | undefined, what: string): Promise<void> {
  try {
    await commands.run("agentSessions.new", commandContext(), {
      body: { kind: "read-only", prompt: explainPrompt(what, article), references: explainReferences(entity, article) },
      open: true,
    });
  } catch (err) {
    notifyError("Explain this could not start", err);
  }
}

export type AgentMenuItem = { label: string; run: () => void; command?: string; disabled?: boolean };

/**
 * The agent's actions on what a panel shows (Ask agent, Explain this) behind one AI icon in the corner, apart from
 * the entity's own verbs.
 */
export function AgentMenu({ items, className }: { items: AgentMenuItem[]; className?: string }) {
  const project = useShell((st) => st.project);
  return (
    <DropdownMenu>
      <DropdownMenuTrigger
        render={
          <Button
            size="icon-xs"
            variant="ghost"
            className={cn("size-6 text-accent-text [&_svg]:size-3.5", className)}
            disabled={!project}
            aria-label="Agent"
            title={project ? "Ask the agent" : "Open a project first"}
            data-slot="agent-menu"
          />
        }
      >
        <Sparks aria-hidden />
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-max min-w-52">
        {items.map((it) => (
          <DropdownMenuItem key={it.label} onClick={it.run} disabled={it.disabled} data-command={it.command}>
            {it.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  );
}

/** The header's agent menu: ask about this entity or its next step, or explain it. */
function EntityAgentMenu({ manifest, entity }: { manifest: EntityManifest; entity: EntityData }) {
  const ref = entityReference(manifest, entity);
  const next = manifest.nextStep(entity);
  return (
    <AgentMenu
      items={[
        { label: `Ask agent about this ${manifest.kind}`, command: "view.askAgent", run: () => askAgent({ refs: [ref], intent: `Help me with the ${manifest.kind} ${entity.name}` }) },
        { label: `Ask agent about the next step`, command: "view.askAgent", run: () => askAgent({ refs: [ref], intent: `${capitalise(next.step)}: ${next.title}` }) },
        {
          label: "Explain this",
          command: "agentSessions.new",
          run: () => void explainThis({ kind: manifest.kind, id: entity.id, label: ref.label }, helpOf(manifest.kind), `the ${manifest.kind} ${entity.name} (${ref.ref})`),
        },
      ]}
    />
  );
}

/** Every empty state names the loop step to take next and offers Ask agent; no panel is ever blank. */
export function EmptyState({ step, title, hint, action }: { step: LoopStep; title: string; hint?: string; action?: ReactNode }) {
  return (
    <div data-slot="empty-state" className="flex h-full flex-col items-center justify-center gap-1.5 p-6 text-center">
      <span className="rounded-full bg-accent-soft px-2 py-0.5 text-[11px] font-medium text-accent-text capitalize">{step}</span>
      <p className="mt-1 text-[13px] font-medium">{title}</p>
      {hint ? <p className="max-w-72 text-xs leading-relaxed text-muted-foreground">{hint}</p> : null}
      <div className="mt-2 flex gap-2">
        {action}
        <AskAgentButton intent={`Help me with the next step (${step}): ${title}`} />
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- lists

export type ListRow = EntityData & { tags?: string[] };
const ROW_HEIGHT = 28;
const VIRTUALIZE_AFTER = 200;

/**
 * Every list has the same first columns — name, version, status, actor, updated, tags — and the same keys: arrows
 * move, Enter opens, Space previews in Inspector. Lists that can exceed 200 rows are virtualized.
 */
export function EntityList({
  rows,
  label,
  onOpen,
  onPreview,
  onCursor,
  height,
  versionLabel = "Version",
}: {
  rows: ListRow[];
  label: string;
  onOpen: (r: ListRow) => void;
  onPreview?: (r: ListRow) => void;
  /** The highlighted row (click or arrows), for actions on "this row" outside the list. */
  onCursor?: (r: ListRow | undefined) => void;
  /** Viewport height for virtualization; defaults to the container's. */
  height?: number;
  /** Heading of the second column; mixed-kind lists (search results) show the kind there. */
  versionLabel?: string;
}) {
  const [cursor, setCursor] = useState(0);
  const current = rows[cursor];
  useEffect(() => onCursor?.(current), [current, onCursor]);
  const [scrollTop, setScrollTop] = useState(0);
  const virtual = rows.length > VIRTUALIZE_AFTER;
  const viewport = height ?? 480;
  const first = virtual ? Math.max(0, Math.floor(scrollTop / ROW_HEIGHT) - 10) : 0;
  const last = virtual ? Math.min(rows.length, Math.ceil((scrollTop + viewport) / ROW_HEIGHT) + 10) : rows.length;
  const visible = useMemo(() => rows.slice(first, last), [rows, first, last]);
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const r = rows[cursor];
    if (e.key === "ArrowDown") setCursor((c) => Math.min(rows.length - 1, c + 1));
    else if (e.key === "ArrowUp") setCursor((c) => Math.max(0, c - 1));
    else if (e.key === "Enter" && r) onOpen(r);
    else if (e.key === " " && r && onPreview) onPreview(r);
    else return;
    e.preventDefault();
  };
  return (
    <div
      role="grid"
      aria-label={label}
      aria-rowcount={rows.length}
      tabIndex={0}
      onKeyDown={onKey}
      onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
      className="relative h-full overflow-auto text-xs"
      data-slot="entity-list"
    >
      <div role="row" className="sticky top-0 z-10 grid grid-cols-[2fr_1fr_1fr_1fr_1fr_1fr] gap-2 border-b bg-tool px-2 py-1 text-muted-foreground">
        {["Name", versionLabel, "Status", "Actor", "Updated", "Tags"].map((h) => (
          <span key={h} role="columnheader">
            {h}
          </span>
        ))}
      </div>
      <div style={virtual ? { height: rows.length * ROW_HEIGHT, position: "relative" } : undefined}>
        {visible.map((r, i) => {
          const index = first + i;
          return (
            <div
              key={`${r.id}`}
              role="row"
              aria-rowindex={index + 1}
              aria-selected={index === cursor}
              onClick={() => setCursor(index)}
              onDoubleClick={() => onOpen(r)}
              style={virtual ? { position: "absolute", top: index * ROW_HEIGHT, left: 0, right: 0 } : undefined}
              className={cn("grid h-7 cursor-default grid-cols-[2fr_1fr_1fr_1fr_1fr_1fr] items-center gap-2 px-2 select-none hover:bg-hover", index === cursor && "bg-selected")}
            >
              <span role="gridcell" className="truncate">
                {r.name}
              </span>
              <span role="gridcell" className="truncate text-muted-foreground">
                {r.version ?? "—"}
              </span>
              <span role="gridcell">
                <StatusChip state={r.state} />
              </span>
              <span role="gridcell" className="truncate">
                <ActorBadge actor={r.actor} />
              </span>
              <span role="gridcell" className="truncate text-muted-foreground tabular-nums">
                {r.updatedAt ? new Date(r.updatedAt).toLocaleString() : "—"}
              </span>
              <span role="gridcell" className="truncate text-muted-foreground">
                {r.tags?.join(", ") ?? ""}
              </span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- compare

export type FactDiff = { label: string; left: string; right: string; changed: boolean };

export function diffFacts(m: EntityManifest, left: EntityData, right: EntityData): FactDiff[] {
  return m.facts.map((f) => {
    const l = f.value(left);
    const r = f.value(right);
    return { label: f.label, left: l, right: r, changed: l !== r };
  });
}

/** Side by side, differences highlighted, facts strip diffed; the selection bus pins the left side. */
export function CompareView({ manifest, left, right }: { manifest: EntityManifest; left: EntityData; right: EntityData }) {
  if (!manifest.comparable) return <EmptyState step="review" title={`${manifest.kind} is not comparable`} />;
  const diffs = diffFacts(manifest, left, right);
  return (
    <table data-slot="compare" className="w-full text-xs">
      <thead>
        <tr className="text-muted-foreground">
          <th className="text-left font-normal" />
          <th className="text-left font-normal">{left.name}</th>
          <th className="text-left font-normal">{right.name}</th>
        </tr>
      </thead>
      <tbody>
        {diffs.map((d) => (
          <tr key={d.label} className={d.changed ? "bg-hover" : undefined}>
            <td className="text-muted-foreground">{d.label}</td>
            <td>{d.left}</td>
            <td className={d.changed ? "font-medium" : undefined}>{d.right}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
