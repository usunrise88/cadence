import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { utterancesSearchOptions } from "@/api/gen/@tanstack/react-query.gen";
import type { DatasetSplitName, UtterancesSearchData } from "@/api/gen/types.gen";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { NativeSelect } from "@/components/ui/native-select";
import { openAudio } from "@/shell/audio";
import { textDirection } from "@/shell/evaluation/format";
import { errorMessage } from "@/shell/panel/commands";

// utterances.search for the data documents (phase 4 · stream R): the Dataset version and Source documents list their
// utterances with the same filters an agent's tool takes — transcript text, language, speaker, origin, duration and,
// within a dataset version, the split. A row plays in the Audio panel.

export type UtteranceSearchProps = {
  /** Only utterances of this dataset version (ver_…); enables the split filter. */
  dataset?: string;
  /** Only utterances of this source (src_… or its name). */
  source?: string;
  /** Rows per page (default 25). */
  limit?: number;
};

type Filters = { q: string; language: string; speaker: string; origin: string; split: "" | DatasetSplitName; minDuration: string; maxDuration: string };
const EMPTY: Filters = { q: "", language: "", speaker: "", origin: "", split: "", minDuration: "", maxDuration: "" };

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v);
  useEffect(() => {
    const t = setTimeout(() => setD(v), ms);
    return () => clearTimeout(t);
  }, [v, ms]);
  return d;
}

const num = (s: string) => (s.trim() === "" || !Number.isFinite(Number(s)) ? undefined : Number(s));

/** The utterances.search query of the filters (empty fields leave the filter out). */
export function searchQuery(f: Filters, scope: { dataset?: string; source?: string }, limit: number, after?: string): NonNullable<UtterancesSearchData["query"]> {
  const q: NonNullable<UtterancesSearchData["query"]> = { limit };
  if (scope.dataset) q.dataset = scope.dataset;
  if (scope.source) q.source = scope.source;
  if (f.q.trim()) q.q = f.q.trim();
  if (f.language.trim()) q.language = f.language.trim();
  if (f.speaker.trim()) q.speaker = f.speaker.trim();
  if (f.origin.trim()) q.origin = f.origin.trim();
  if (f.split && scope.dataset) q.split = f.split;
  const min = num(f.minDuration);
  const max = num(f.maxDuration);
  if (min !== undefined) q.minDuration = min;
  if (max !== undefined) q.maxDuration = max;
  if (after) q.after = after;
  return q;
}

export function UtteranceSearch({ dataset, source, limit = 25 }: UtteranceSearchProps) {
  const [filters, setFilters] = useState<Filters>(EMPTY);
  const [pages, setPages] = useState<string[]>([]); // cursors of the pages before the current one
  const [after, setAfter] = useState<string | undefined>(undefined);
  const debounced = useDebounced(filters, 250);
  const set = (k: keyof Filters) => (e: { target: { value: string } }) => {
    setFilters((f) => ({ ...f, [k]: e.target.value }));
    setPages([]);
    setAfter(undefined);
  };
  const q = useQuery({ ...utterancesSearchOptions({ query: searchQuery(debounced, { dataset, source }, limit, after) }), retry: false });
  const items = q.data?.items ?? [];
  const id = dataset ?? source ?? "all";
  return (
    <div className="flex flex-col gap-2" data-slot="utterance-search">
      <div className="grid grid-cols-2 gap-1.5 @md:grid-cols-4" role="group" aria-label="Filter utterances">
        <Input className="col-span-2 h-6 text-xs" placeholder="Transcript contains…" aria-label="Transcript contains" value={filters.q} onChange={set("q")} />
        <Input className="h-6 text-xs" placeholder="Language (he, sr-RS)" aria-label="Language" value={filters.language} onChange={set("language")} />
        <Input className="h-6 text-xs" placeholder="Origin (human, pseudo-label)" aria-label="Origin" value={filters.origin} onChange={set("origin")} />
        <Input className="h-6 text-xs" placeholder="Speaker" aria-label="Speaker" value={filters.speaker} onChange={set("speaker")} />
        <Input className="h-6 text-xs" inputMode="decimal" placeholder="Min s" aria-label="Shortest duration in seconds" value={filters.minDuration} onChange={set("minDuration")} />
        <Input className="h-6 text-xs" inputMode="decimal" placeholder="Max s" aria-label="Longest duration in seconds" value={filters.maxDuration} onChange={set("maxDuration")} />
        {dataset ? (
          <NativeSelect className="h-6 w-auto text-xs" aria-label="Split" value={filters.split} onChange={set("split")}>
            <option value="">Every split</option>
            <option value="train">train</option>
            <option value="validation">validation</option>
            <option value="test">test</option>
          </NativeSelect>
        ) : null}
      </div>
      {q.error ? (
        <p role="alert" className="text-xs text-destructive">
          {errorMessage(q.error)}
        </p>
      ) : null}
      {items.length ? (
        <table className="w-full table-fixed text-xs" aria-label="Utterances">
          <thead className="text-left text-muted-foreground">
            <tr>
              <th className="w-14 font-normal">Play</th>
              <th className="font-normal">Transcript</th>
              <th className="w-16 font-normal">Duration</th>
              <th className="w-16 font-normal">Lang</th>
              <th className="w-20 font-normal">{dataset ? "Split" : "Source"}</th>
              <th className="w-24 font-normal">Origin</th>
            </tr>
          </thead>
          <tbody>
            {items.map((u) => {
              const t = u.transcripts[0];
              return (
                <tr key={u.id} className="h-7 border-t align-middle" data-utterance={u.id}>
                  <td>
                    <Button size="xs" variant="ghost" className="min-h-6" aria-label={`Play ${u.id}`} onClick={() => openAudio({ utterance: u.id })}>
                      Play
                    </Button>
                  </td>
                  <td className="truncate" title={t?.text}>
                    <bdi dir={textDirection(u.language)}>{t?.text ?? "—"}</bdi>
                  </td>
                  <td className="tabular-nums">{u.duration.toFixed(1)} s</td>
                  <td>{u.language}</td>
                  <td className="truncate">{dataset ? (u.split ?? "—") : u.sourceName}</td>
                  <td className="truncate">{t?.origin ?? "—"}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      ) : (
        <p className="text-xs text-muted-foreground">{q.isFetching ? "Searching…" : "No utterances match."}</p>
      )}
      <div className="flex items-center gap-1 text-xs" aria-label={`Pages of ${id}`}>
        <Button
          size="xs"
          variant="outline"
          disabled={pages.length === 0}
          onClick={() => {
            const prev = pages.slice(0, -1);
            setAfter(pages.length > 1 ? pages[pages.length - 2] : undefined);
            setPages(prev);
          }}
        >
          Previous
        </Button>
        <Button
          size="xs"
          variant="outline"
          disabled={!q.data?.next}
          onClick={() => {
            if (!q.data?.next) return;
            setPages((p) => [...p, q.data!.next!]);
            setAfter(q.data.next);
          }}
        >
          Next
        </Button>
        <span className="text-muted-foreground">
          Page {pages.length + 1} · {items.length} shown
        </span>
      </div>
    </div>
  );
}
