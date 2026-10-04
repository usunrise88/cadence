import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { NavArrowDown, NavArrowRight } from "iconoir-react";
import { guidelinesGetOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Markdown } from "@/shell/markdown";
import { errorMessage } from "@/shell/panel/commands";

// The batch's annotation guidelines inside the Annotate view (R27; phase 4 tail): the Markdown file at the commit the
// batch pinned (guidelines.get), which a reviewer invited to the batch may read — nothing else of the repository.
// Collapsed by default so the item stays in view; the toggle remembers nothing.

export function GuidelinesPane({ batchId, defaultOpen = false }: { batchId: string; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const q = useQuery({ ...guidelinesGetOptions({ path: { id: batchId } }), staleTime: Infinity, enabled: open });
  const id = `guidelines-${batchId}`;
  return (
    <section aria-label="Annotation guidelines" className="border-b text-xs" data-slot="guidelines">
      <button type="button" className="flex min-h-7 w-full items-center gap-1.5 px-3 text-left hover:bg-hover" aria-expanded={open} aria-controls={id} onClick={() => setOpen((o) => !o)}>
        {open ? <NavArrowDown aria-hidden className="size-3.5" /> : <NavArrowRight aria-hidden className="size-3.5" />}
        <span className="font-medium">Guidelines</span>
        {q.data ? (
          <span className="truncate text-muted-foreground">
            {q.data.path} at {q.data.commit.slice(0, 8)}
          </span>
        ) : null}
      </button>
      {open ? (
        <div id={id} className="max-h-72 overflow-auto px-3 pb-2">
          {q.data ? (
            <>
              <Markdown slot="guidelines-text">{q.data.text}</Markdown>
              {q.data.truncated ? <p className="text-muted-foreground">Shown up to 256 KiB of {Math.round(q.data.bytes / 1024)} KiB.</p> : null}
            </>
          ) : q.error ? (
            <p role="alert" className="text-destructive">
              {errorMessage(q.error)}
            </p>
          ) : (
            <p className="text-muted-foreground">Loading the guidelines…</p>
          )}
        </div>
      ) : null}
    </section>
  );
}
