import { useState, type MouseEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Pin, PinSolid } from "iconoir-react";
import { Streamdown } from "streamdown";
import { helpGetOptions, helpSearchOptions } from "@/api/gen/@tanstack/react-query.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { EmptyState, ExplainThisButton, PanelToolbar } from "@/shell/entity/primitives";
import { useHelp } from "@/shell/help/store";
import type { PanelProps } from "@/shell/panel";

// Help: the article for the focused panel, field or error; follows focus unless pinned (docs/spec/11-ui-panels.md).
// Articles are the markdown in docs/help, bundled in the binary and served by help.get.

export function HelpEmpty() {
  return <EmptyState step="review" title="Nothing to explain yet" hint="Focus a panel, or search help with ? in the palette." />;
}

const LINK = /(?:^|\/)(errors|panels|steps|shell|guides)\/([a-z0-9-]+)(?:\.md)?(?:#.*)?$/;

/** Maps a link inside an article to another article id, if it points at one. */
export function articleIdFromHref(href: string): string | undefined {
  const m = LINK.exec(href);
  return m ? `${m[1]}.${m[2]}` : undefined;
}

export function HelpPanel(_props: PanelProps) {
  const { article, pinned, focusedHelp, show, follow } = useHelp();
  const [q, setQ] = useState("");
  const id = article ?? focusedHelp ?? "shell.overview";
  const doc = useQuery({ ...helpGetOptions({ path: { id } }), retry: false });
  const hits = useQuery({ ...helpSearchOptions({ query: { q, limit: 10 } }), enabled: q.length > 1 });

  const onClick = (e: MouseEvent<HTMLDivElement>) => {
    const a = (e.target as HTMLElement).closest("a");
    const href = a?.getAttribute("href");
    const target = href ? articleIdFromHref(href) : undefined;
    if (target) {
      e.preventDefault();
      show(target);
    }
  };

  return (
    <div className="flex h-full min-h-0 flex-col">
      <PanelToolbar>
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search help" aria-label="Search help" />
        <Button
          size="icon-xs"
          variant="ghost"
          className="size-6"
          aria-pressed={pinned}
          aria-label={pinned ? "Follow the focused panel" : "Pin this article"}
          onClick={() => (pinned ? follow() : show(id))}
        >
          {pinned ? <PinSolid aria-hidden /> : <Pin aria-hidden />}
        </Button>
        {doc.data ? <ExplainThisButton entity={null} article={id} what={`the help article “${doc.data.title}” and how it applies to this project`} /> : null}
      </PanelToolbar>
      {q.length > 1 ? (
        <ul className="border-b text-xs" aria-label="Help search results">
          {(hits.data?.items ?? []).map((h) => (
            <li key={h.id}>
              <button
                type="button"
                className="flex min-h-6 w-full flex-col items-start px-2 py-1 text-left hover:bg-hover"
                onClick={() => {
                  setQ("");
                  show(h.id);
                }}
              >
                <span className="font-medium">{h.title}</span>
                {h.summary ? <span className="text-muted-foreground">{h.summary}</span> : null}
              </button>
            </li>
          ))}
          {hits.data && hits.data.items.length === 0 ? <li className="px-2 py-1 text-muted-foreground">No articles match.</li> : null}
        </ul>
      ) : null}
      <div className="min-h-0 flex-1 overflow-auto px-4 py-3" onClick={onClick} data-article={id}>
        {doc.data ? (
          <article className="cadence-prose">
            <p role="heading" aria-level={3} className="cadence-title">
              {doc.data.title}
            </p>
            {doc.data.summary ? <p className="cadence-lede">{doc.data.summary}</p> : null}
            <Streamdown controls={false}>{doc.data.body}</Streamdown>
          </article>
        ) : doc.isLoading ? (
          <p className="text-xs text-muted-foreground">Loading…</p>
        ) : (
          <EmptyState step="review" title={`No help article “${id}”`} hint="Every panel, step kind and error type has one; this one is missing." />
        )}
      </div>
    </div>
  );
}
