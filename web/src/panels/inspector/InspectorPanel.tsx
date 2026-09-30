import { Copy, NavArrowRight, Pin, PinSolid } from "iconoir-react";
import { Button } from "@/components/ui/button";
import type { EntityData } from "@/shell/entity/manifest";
import { ActorBadge, EmptyState, PanelToolbar, StatusChip } from "@/shell/entity/primitives";
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
        <div className="min-h-0 overflow-auto px-2 py-1">
          <dl className="grid grid-cols-[minmax(5rem,7.5rem)_1fr] content-start gap-x-3">
            {fields(data).map(([k, v]) => (
              <div key={k} className="group contents">
                <dt className="flex min-h-7 items-center truncate border-b border-border/50 text-muted-foreground" title={k}>
                  {fieldLabel(k)}
                </dt>
                <dd className="flex min-h-7 min-w-0 items-center gap-1 border-b border-border/50 py-0.5">
                  <span className="min-w-0 flex-1 truncate">
                    <FieldValue name={k} value={v} toolCallId={data.toolCallId} />
                  </span>
                  {!empty(v) ? <CopyButton label={fieldLabel(k)} text={typeof v === "object" ? JSON.stringify(v, null, 2) : String(v)} /> : null}
                </dd>
              </div>
            ))}
          </dl>
          {raw(data).map(([k, v]) => (
            <details key={k} className="group mt-2 rounded border bg-tool px-2 py-1" data-slot="inspector-raw">
              <summary className="flex min-h-6 cursor-pointer items-center gap-1 text-muted-foreground select-none">
                <NavArrowRight aria-hidden className="size-3 transition-transform group-open:rotate-90" />
                {fieldLabel(k)} <span className="text-[11px]">(raw)</span>
              </summary>
              <pre className="mt-1 max-h-80 overflow-auto font-mono text-[11px] whitespace-pre-wrap">{JSON.stringify(v, null, 2)}</pre>
            </details>
          ))}
        </div>
      )}
    </div>
  );
}

// Fields read as labels and values people use: "Updated" and a local time, the actor's name, lists joined; nested
// records (the full API entity) fold into a raw block below.

const LABELS: Record<string, string> = { id: "ID", rev: "Revision", updatedAt: "Updated", createdAt: "Created", projectId: "Project ID", toolCallId: "Tool call" };
const ISO = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}/;

function fieldLabel(k: string): string {
  if (LABELS[k]) return LABELS[k];
  const words = k.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const empty = (v: unknown) => v === undefined || v === null || v === "" || (Array.isArray(v) && v.length === 0);
const isRecord = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
const isActor = (v: unknown) => isRecord(v) && typeof v.kind === "string" && typeof v.id === "string" && Object.keys(v).every((k) => ["kind", "id", "name", "sessionId"].includes(k));

function fields(data: EntityData): [string, unknown][] {
  return Object.entries(data).filter(([, v]) => !isRecord(v) || isActor(v));
}

function raw(data: EntityData): [string, unknown][] {
  return Object.entries(data).filter(([, v]) => isRecord(v) && !isActor(v));
}

function FieldValue({ name, value, toolCallId }: { name: string; value: unknown; toolCallId?: string }) {
  if (empty(value)) return <span className="text-muted-foreground">—</span>;
  if (name === "state") return <StatusChip state={String(value)} />;
  if (isActor(value)) return <ActorBadge actor={value as EntityData["actor"]} toolCallId={toolCallId} />;
  if (typeof value === "string" && ISO.test(value)) {
    const d = new Date(value);
    return <time dateTime={value} title={value}>{Number.isNaN(d.getTime()) ? value : d.toLocaleString()}</time>;
  }
  if (Array.isArray(value)) return <span title={JSON.stringify(value)}>{value.every((x) => typeof x !== "object") ? value.join(", ") : `${value.length} ${value.length === 1 ? "item" : "items"}`}</span>;
  return <span title={String(value)}>{String(value)}</span>;
}

function CopyButton({ label, text }: { label: string; text: string }) {
  return (
    <Button
      size="icon-xs"
      variant="ghost"
      className="invisible size-6 shrink-0 text-muted-foreground group-hover:visible focus-visible:visible"
      onClick={() => void navigator.clipboard?.writeText(text)}
      aria-label={`Copy ${label}`}
      title={`Copy ${label}`}
    >
      <Copy aria-hidden />
    </Button>
  );
}
