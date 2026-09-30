import { useRef, useState, type KeyboardEvent } from "react";
import { cn } from "@/lib/utils";
import { EmptyState, PanelToolbar } from "@/shell/entity/primitives";
import { ApprovalCard, useDecidedApprovals, usePendingApprovals, type PanelProps } from "@/shell/panel";

// Approvals (docs/spec/11-ui-panels.md "Panel catalogue"): pending requests from agents, automations and registry
// actions, oldest first, with the decided history below. The list stays live from the `approvals` topic through the
// shell's cache patching (the same subscription feeds the status-bar badge and notification history).

type Scope = "all" | "project" | "registry";

export function ApprovalsEmpty() {
  return (
    <EmptyState
      step="decide"
      title="Nothing waits for you"
      hint="Gated commands — over-budget runs, baselines, gates, golden-set freezes, deployments and registry changes — stop here until a person decides."
    />
  );
}

export function ApprovalsPanel(_props: PanelProps) {
  const pending = usePendingApprovals();
  const [showDecided, setShowDecided] = useState(true);
  const decided = useDecidedApprovals(showDecided);
  const [scope, setScope] = useState<Scope>("all");
  const listRef = useRef<HTMLDivElement>(null);
  const keep = (s: string) => scope === "all" || s === scope;
  const items = (pending.data?.items ?? []).filter((a) => keep(a.scope));
  const history = (decided.data?.items ?? []).filter((a) => keep(a.scope));

  // Arrow keys move between cards; after a decision the next pending card takes focus.
  const cards = () => [...(listRef.current?.querySelectorAll<HTMLElement>("[data-approval-card]") ?? [])];
  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
    const all = cards();
    const i = all.findIndex((c) => c === e.target);
    if (i < 0) return;
    e.preventDefault();
    all[Math.max(0, Math.min(all.length - 1, i + (e.key === "ArrowDown" ? 1 : -1)))]?.focus();
  };
  const focusNext = (id: string) => {
    requestAnimationFrame(() => {
      const next = cards().find((c) => c.dataset.state === "pending" && c.dataset.approvalCard !== id);
      next?.focus();
    });
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PanelToolbar>
        <div role="radiogroup" aria-label="Scope" className="inline-flex rounded-md border bg-background p-0.5 text-xs">
          {(["all", "project", "registry"] as const).map((s) => (
            <button
              key={s}
              type="button"
              role="radio"
              aria-checked={scope === s}
              onClick={() => setScope(s)}
              className={cn("h-6 rounded-[4px] px-2.5 capitalize", scope === s ? "bg-selected font-medium text-foreground" : "text-muted-foreground hover:text-foreground")}
            >
              {s}
            </button>
          ))}
        </div>
        <span className="text-xs text-muted-foreground tabular-nums" data-testid="approvals-pending-count">
          {items.length} pending
        </span>
        <label className="ml-auto flex items-center gap-1.5 text-xs text-muted-foreground">
          <input type="checkbox" checked={showDecided} onChange={(e) => setShowDecided(e.target.checked)} className="size-3.5 accent-primary" />
          Show decided
        </label>
      </PanelToolbar>
      <div ref={listRef} onKeyDown={onKey} className="min-h-0 flex-1 overflow-auto">
        <section aria-labelledby="approvals-pending" className="flex flex-col gap-2 p-2">
          <h3 id="approvals-pending" className="sr-only">
            Pending
          </h3>
          {pending.isLoading ? <p className="p-2 text-xs text-muted-foreground">Loading…</p> : null}
          {!pending.isLoading && items.length === 0 ? (
            <div className="h-56">
              <ApprovalsEmpty />
            </div>
          ) : null}
          {items.map((a) => (
            <ApprovalCard key={a.id} approval={a} onDecided={() => focusNext(a.id)} />
          ))}
        </section>
        {showDecided && history.length > 0 ? (
          <section aria-labelledby="approvals-decided" className="flex flex-col gap-2 border-t p-2">
            <h3 id="approvals-decided" className="px-1 text-[11px] font-medium tracking-wide text-muted-foreground uppercase">
              Decided
            </h3>
            {history.map((a) => (
              <ApprovalCard key={a.id} approval={a} />
            ))}
          </section>
        ) : null}
      </div>
    </div>
  );
}
