import { useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { copyText, formatCell, TABLE_ROW_LIMIT, toCsv, type Table } from "./table";

export type ChartFrameProps = {
  title: string;
  /** Hide the visible title when the panel already names the chart (it stays the accessible name). */
  hideTitle?: boolean;
  summary: string;
  /** Built lazily: only when the table view opens or CSV is copied. */
  table: () => Table;
  toolbar?: ReactNode;
  legend?: ReactNode;
  /** Polite live-region text (the keyboard cursor's readout). */
  announce?: string;
  /** Fixed plot height in px; by default the plot fills the space left in its parent. */
  plotHeight?: number;
  className?: string;
  children: (describedBy: string) => ReactNode;
};

/** The chrome every chart shares: title, Table / Copy CSV, legend, the plot or its table, summary, live region. */
export function ChartFrame({ title, hideTitle, summary, table, toolbar, legend, announce, plotHeight, className, children }: ChartFrameProps) {
  const summaryId = useId();
  const [view, setView] = useState<"chart" | "table">("chart");
  const [status, setStatus] = useState("");
  const ref = useRef<HTMLElement>(null);

  const copy = async () => {
    const ok = await copyText(toCsv(table()), ref.current);
    setStatus(ok ? "Copied the chart's data as CSV" : "Copy failed: the clipboard is not available");
  };

  return (
    <figure ref={ref} className={cn("cadence-chart m-0 flex min-h-0 min-w-0 flex-col gap-1", !plotHeight && "h-full", className)} data-chart-view={view}>
      <div className="flex min-h-6 items-center gap-1 text-xs">
        <figcaption className={cn("min-w-0 flex-1 truncate font-medium text-foreground", hideTitle && "sr-only")}>{title}</figcaption>
        {hideTitle && <span className="flex-1" />}
        {toolbar}
        <Button size="xs" variant="ghost" aria-pressed={view === "table"} onClick={() => setView(view === "table" ? "chart" : "table")}>
          {view === "table" ? "Chart" : "Table"}
        </Button>
        <Button size="xs" variant="ghost" onClick={() => void copy()}>
          Copy CSV
        </Button>
      </div>
      {legend}
      <div className={cn("relative min-h-0", plotHeight ? "flex-none" : "flex-1", view === "table" && "hidden")} style={plotHeight ? { height: plotHeight } : undefined}>
        {children(summaryId)}
      </div>
      {view === "table" && <DataTable table={table} summary={summary} />}
      <p id={summaryId} className="sr-only">
        {summary}
      </p>
      <div role="status" aria-live="polite" className="sr-only">
        {status || announce}
      </div>
    </figure>
  );
}

function DataTable({ table, summary }: { table: () => Table; summary: string }) {
  const t = useMemo(() => table(), [table]);
  const rows = t.rows.slice(0, TABLE_ROW_LIMIT);
  return (
    <div className="min-h-0 flex-1 overflow-auto rounded-sm border border-border" data-testid="chart-table">
      <table className="w-full border-collapse text-xs tabular-nums">
        <caption className="px-2 py-1 text-left text-muted-foreground">
          {summary}
          {t.rows.length > rows.length && ` Showing ${rows.length} of ${t.rows.length} rows; Copy CSV copies all of them.`}
        </caption>
        <thead className="sticky top-0 bg-tool">
          <tr>
            {t.columns.map((c) => (
              <th key={c} scope="col" className="border-b border-border px-2 py-1 text-left font-medium">
                {c}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i} className="hover:bg-muted">
              {r.map((v, j) =>
                j === 0 ? (
                  <th key={j} scope="row" className="px-2 py-0.5 text-left font-normal">
                    {formatCell(v)}
                  </th>
                ) : (
                  <td key={j} className="px-2 py-0.5 text-right">
                    {formatCell(v)}
                  </td>
                ),
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
