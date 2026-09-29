import { Pin, PinSolid } from "iconoir-react";
import { Button } from "@/components/ui/button";
import { EmptyState, PanelToolbar, StatusChip } from "@/shell/entity/primitives";
import { useCurrentSelection, useFollowedDoc, useSelection, type PanelProps } from "@/shell/panel";
import { useEntity } from "@/shell/panel/entity";

// Inspector: properties of the current selection; follows the active document unless pinned.

export function InspectorEmpty() {
  return <EmptyState step="review" title="Nothing selected" hint="Open a document or select a row; its properties show here." />;
}

export function InspectorPanel({ instanceId }: PanelProps) {
  const { doc } = useFollowedDoc(instanceId);
  if (!doc) return <InspectorEmpty />;
  // Keyed by kind: each kind loads through its own hooks.
  return <InspectorBody key={doc.split(":")[0]} instanceId={instanceId} doc={doc} />;
}

function InspectorBody({ instanceId, doc }: { instanceId: string; doc: string }) {
  const { pinned } = useFollowedDoc(instanceId);
  const sel = useCurrentSelection(instanceId);
  const { pin, unpin } = useSelection();
  const { data, manifest } = useEntity(doc);
  return (
    <div className="flex h-full min-h-0 flex-col text-xs">
      <PanelToolbar>
        <span className="truncate text-[13px] font-medium">{data?.name ?? doc}</span>
        {sel?.item ? <span className="text-muted-foreground">· {sel.item}</span> : null}
        {pinned ? <span className="rounded-full bg-accent-soft px-1.5 text-[11px] text-accent-text">pinned</span> : null}
        <Button
          size="icon-xs"
          variant="ghost"
          className="ml-auto size-6"
          aria-pressed={pinned}
          aria-label={pinned ? "Unpin" : `Pin to ${doc}`}
          onClick={() => (pinned ? unpin(instanceId) : pin(instanceId, doc))}
        >
          {pinned ? <PinSolid aria-hidden /> : <Pin aria-hidden />}
        </Button>
      </PanelToolbar>
      {!data ? (
        <EmptyState step="review" title={manifest ? "Loading…" : `No inspector for ${doc}`} />
      ) : (
        <dl className="grid grid-cols-[minmax(5rem,7rem)_1fr] content-start gap-x-3 overflow-auto px-2 py-1">
          {Object.entries(data).map(([k, v]) => (
            <div key={k} className="group contents">
              <dt className="flex h-7 items-center truncate text-muted-foreground">{k}</dt>
              <dd className="flex h-7 min-w-0 items-center gap-1 border-b border-border/50">
                <span className="truncate">{k === "state" ? <StatusChip state={String(v)} /> : v === undefined ? "—" : typeof v === "object" ? JSON.stringify(v) : String(v)}</span>
                {v !== undefined ? (
                  <button
                    type="button"
                    className="invisible ml-auto min-h-6 rounded px-1 text-muted-foreground group-hover:visible hover:bg-hover focus-visible:visible"
                    onClick={() => void navigator.clipboard?.writeText(typeof v === "object" ? JSON.stringify(v) : String(v))}
                    aria-label={`Copy ${k}`}
                  >
                    Copy
                  </button>
                ) : null}
              </dd>
            </div>
          ))}
        </dl>
      )}
    </div>
  );
}
