import { useMemo, useRef, useState, type KeyboardEvent } from "react";
import { NavArrowDown, NavArrowUp } from "iconoir-react";
import type { BranchCompare, BranchCompareFile, MergeHunk } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { conflictCount, layout, missingReason, SIDES, unified, type Line, type Side, type UnifiedTone } from "./threeway";

// The three-way view of a conflicting file (docs/spec/05-agents.md "Worktree, drafts and merge"; branches.compare):
// base · main · branch side by side — stacked when the panel is narrow (the Chat) — or one unified column with every
// conflict in three labelled parts. Conflicts are reachable from the keyboard (Next / Previous, or n / p inside the
// view); colours are the diff and status-warning tokens only.

type Mode = "split" | "unified";
type Tone = "none" | "added" | "removed";

const CONFLICT_LABEL: Record<NonNullable<BranchCompareFile["conflict"]>, string> = {
  content: "both changed the same lines",
  "add/add": "added on both sides",
  "modify/delete": "changed on one side, deleted on the other",
  binary: "binary file changed on both sides",
};

function sideTone(kind: MergeHunk["kind"], side: Side): Tone {
  switch (kind) {
    case "main":
      return side === "base" ? "removed" : side === "main" ? "added" : "none";
    case "branch":
      return side === "base" ? "removed" : side === "branch" ? "added" : "none";
    case "both":
      return side === "base" ? "removed" : "added";
    default:
      return "none";
  }
}

const PRESSED = "aria-pressed:bg-selected aria-pressed:text-foreground";

const TONE_CLASS: Record<Tone, string> = {
  none: "",
  added: "bg-diff-added text-diff-added-foreground",
  removed: "bg-diff-removed text-diff-removed-foreground",
};

function LineRows({ lines, tone, prefix }: { lines: Line[]; tone: Tone; prefix?: string }) {
  return (
    <>
      {lines.map((l) => (
        <div key={l.n} className={cn("grid grid-cols-[2.5rem_1fr]", TONE_CLASS[tone])}>
          <span aria-hidden className="pr-2 text-right text-muted-foreground tabular-nums select-none">
            {l.n}
          </span>
          <span className="pr-2 whitespace-pre">
            {prefix}
            {l.text || " "}
          </span>
        </div>
      ))}
    </>
  );
}

export interface ThreeWayDiffProps {
  file: BranchCompareFile;
  /** What to call the branch side: "session" in Session changes, the branch name elsewhere. */
  branchLabel?: string;
  /** The scrolling area's height limit (a Tailwind max-h class); the Chat keeps it low. */
  maxHeight?: string;
}

export function ThreeWayDiff({ file, branchLabel = "branch", maxHeight = "max-h-[28rem]" }: ThreeWayDiffProps) {
  const [mode, setMode] = useState<Mode>("split");
  const blocks = useMemo(() => layout(file), [file]);
  const parts = useMemo(() => unified(blocks), [blocks]);
  const total = conflictCount(file);
  const reason = missingReason(file);
  const conflicts = useRef<Array<HTMLElement | null>>([]);
  const [current, setCurrent] = useState(-1);
  const label: Record<Side, string> = { base: "base", main: "main", branch: branchLabel };
  const exists: Record<Side, boolean> = { base: file.base.exists, main: file.main.exists, branch: file.branch.exists };

  const go = (delta: number) => {
    if (total === 0) return;
    const next = current < 0 ? (delta > 0 ? 0 : total - 1) : (current + delta + total) % total;
    setCurrent(next);
    const el = conflicts.current[next];
    el?.focus();
    el?.scrollIntoView?.({ block: "nearest" });
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.altKey || e.ctrlKey || e.metaKey) return;
    if (e.key === "n") go(1);
    else if (e.key === "p") go(-1);
    else return;
    e.preventDefault();
  };
  const conflictRef = (i: number | undefined) => (el: HTMLElement | null) => {
    if (i !== undefined) conflicts.current[i] = el;
  };
  const conflictProps = (i: number) => ({
    ref: conflictRef(i),
    tabIndex: -1,
    role: "group",
    "aria-label": `Conflict ${i + 1} of ${total}`,
    "data-conflict": i,
    onFocus: () => setCurrent(i),
  });

  return (
    <div className="@container min-w-0 rounded-md border bg-background text-xs" data-slot="three-way" data-path={file.path} data-mode={mode}>
      <div role="toolbar" aria-label={`Three-way view of ${file.path}`} className="flex flex-wrap items-center gap-x-2 gap-y-1 border-b px-2 py-1">
        <span className="min-w-0 truncate font-medium" title={file.path}>
          {file.path}
        </span>
        {file.conflict ? <span className="text-status-warning-foreground">{CONFLICT_LABEL[file.conflict]}</span> : null}
        {total ? (
          <span className="text-muted-foreground tabular-nums" data-slot="conflict-count">
            {total} conflict{total === 1 ? "" : "s"}
          </span>
        ) : null}
        {reason ? null : (
          <span className="ml-auto flex flex-wrap items-center gap-1">
            <Button size="xs" variant="ghost" className={PRESSED} aria-pressed={mode === "split"} onClick={() => setMode("split")}>
              Side by side
            </Button>
            <Button size="xs" variant="ghost" className={PRESSED} aria-pressed={mode === "unified"} onClick={() => setMode("unified")}>
              Unified
            </Button>
            <Button size="icon-xs" variant="outline" disabled={total === 0} onClick={() => go(-1)} aria-label="Previous conflict" title="Previous conflict (p)">
              <NavArrowUp aria-hidden />
            </Button>
            <Button size="icon-xs" variant="outline" disabled={total === 0} onClick={() => go(1)} aria-label="Next conflict" title="Next conflict (n)">
              <NavArrowDown aria-hidden />
            </Button>
          </span>
        )}
      </div>
      {reason ? (
        <p className="px-2 py-1.5 text-muted-foreground">{reason}</p>
      ) : (
        <div
          role="region"
          aria-label={`${file.path}: ${mode === "split" ? `base, main and ${branchLabel}` : "unified"} (n and p move between conflicts)`}
          tabIndex={0}
          onKeyDown={onKey}
          className={cn(maxHeight, "overflow-auto font-mono text-[11px] leading-5 focus-visible:outline-2 focus-visible:outline-ring")}
        >
          {mode === "split" ? (
            <>
              <div className="sticky top-0 z-10 hidden border-b bg-chrome font-sans font-medium text-muted-foreground @2xl:grid @2xl:grid-cols-3">
                {SIDES.map((s) => (
                  <span key={s} className="border-l px-2 first:border-l-0">
                    {label[s]}
                    {exists[s] ? "" : " (no file)"}
                  </span>
                ))}
              </div>
              {blocks.map((b, i) =>
                b.type === "gap" ? (
                  <div key={`g${i}`} className="bg-muted px-2 font-sans text-muted-foreground">
                    ⋯ {b.lines} unchanged line{b.lines === 1 ? "" : "s"}
                  </div>
                ) : (
                  <div
                    key={b.index}
                    data-hunk={b.kind}
                    {...(b.conflict !== undefined ? conflictProps(b.conflict) : {})}
                    className={cn(
                      "grid grid-cols-1 @2xl:grid-cols-3",
                      b.kind === "conflict" && "my-0.5 border-y border-l-4 border-status-warning bg-status-warning/10 outline-none focus-visible:ring-2 focus-visible:ring-ring",
                    )}
                  >
                    {SIDES.map((s) => (
                      <div
                        key={s}
                        data-side={s}
                        className={cn("min-w-0 @2xl:border-l @2xl:first:border-l-0", b.kind === "same" && s !== "main" && "hidden @2xl:block")}
                      >
                        {b.kind !== "same" ? (
                          <div className="px-2 font-sans text-[10px] font-medium tracking-wide text-muted-foreground uppercase @2xl:hidden">{label[s]}</div>
                        ) : null}
                        {b[s].length ? (
                          <LineRows lines={b[s]} tone={sideTone(b.kind, s)} />
                        ) : b.kind !== "same" ? (
                          <div className="px-2 font-sans text-muted-foreground italic">{exists[s] ? "no lines here" : "no file"}</div>
                        ) : null}
                      </div>
                    ))}
                  </div>
                ),
              )}
            </>
          ) : (
            parts.map((p, i) =>
              p.type === "gap" ? (
                <div key={`g${i}`} className="bg-muted px-2 font-sans text-muted-foreground">
                  ⋯ {p.lines} unchanged line{p.lines === 1 ? "" : "s"}
                </div>
              ) : p.type === "lines" ? (
                <div key={`l${i}`}>
                  {p.lines.map((l, j) => (
                    <div key={j} className={cn("grid grid-cols-[2.5rem_1fr]", UNIFIED_CLASS[l.tone])} data-tone={l.tone}>
                      <span aria-hidden className="pr-2 text-right text-muted-foreground tabular-nums select-none">
                        {l.n}
                      </span>
                      <span className="pr-2 whitespace-pre">
                        {UNIFIED_PREFIX[l.tone]}
                        {l.text || " "}
                      </span>
                    </div>
                  ))}
                </div>
              ) : (
                <div key={`c${i}`} data-hunk="conflict" {...conflictProps(p.conflict)} className="my-0.5 border-y border-l-4 border-status-warning bg-status-warning/10 outline-none focus-visible:ring-2 focus-visible:ring-ring">
                  {(["main", "base", "branch"] as const).map((s) => (
                    <div key={s} data-side={s} className="border-t border-status-warning/40 first:border-t-0">
                      <div className="px-2 font-sans text-[10px] font-medium tracking-wide text-status-warning-foreground uppercase">
                        {s === "base" ? "base (before both)" : label[s]}
                      </div>
                      {p[s].length ? (
                        <LineRows lines={p[s]} tone="none" />
                      ) : (
                        <div className="px-2 font-sans text-muted-foreground italic">{exists[s] ? "no lines here" : "no file"}</div>
                      )}
                    </div>
                  ))}
                </div>
              ),
            )
          )}
        </div>
      )}
    </div>
  );
}

const UNIFIED_CLASS: Record<UnifiedTone, string> = {
  context: "",
  main: "",
  both: "",
  added: TONE_CLASS.added,
  removed: TONE_CLASS.removed,
};
const UNIFIED_PREFIX: Record<UnifiedTone, string> = { context: "  ", main: "  ", both: "  ", added: "+ ", removed: "- " };

export interface BranchConflictsProps {
  compare: BranchCompare;
  branchLabel?: string;
  maxHeight?: string;
  /** Open the first conflicting file's view at once. */
  openFirst?: boolean;
}

/** The conflicting files of a comparison, each with a button that opens its three-way view. */
export function BranchConflicts({ compare, branchLabel, maxHeight, openFirst = false }: BranchConflictsProps) {
  const files = compare.files.filter((f) => !f.clean);
  const [open, setOpen] = useState<Record<string, boolean>>(() => (openFirst && files[0] ? { [files[0].path]: true } : {}));
  if (files.length === 0) return null;
  return (
    <ul className="flex flex-col gap-1" aria-label="Conflicting files" data-slot="branch-conflicts">
      {files.map((f) => {
        const expanded = !!open[f.path];
        return (
          <li key={f.path} className="flex min-w-0 flex-col gap-1">
            <div className="flex min-h-6 min-w-0 items-center gap-2">
              <span aria-hidden className="size-2 shrink-0 rounded-full bg-status-warning" />
              <span className="min-w-0 truncate font-mono text-[11px]" title={f.path}>
                {f.path}
              </span>
              <Button
                size="xs"
                variant="outline"
                className="ml-auto"
                aria-expanded={expanded}
                onClick={() => setOpen((o) => ({ ...o, [f.path]: !expanded }))}
                data-slot="three-way-toggle"
              >
                {expanded ? "Hide three-way" : "Three-way"}
              </Button>
            </div>
            {expanded ? <ThreeWayDiff file={f} {...(branchLabel ? { branchLabel } : {})} {...(maxHeight ? { maxHeight } : {})} /> : null}
          </li>
        );
      })}
      {compare.cut ? <li className="text-[11px] text-muted-foreground">Some files are too large to compare here.</li> : null}
    </ul>
  );
}
