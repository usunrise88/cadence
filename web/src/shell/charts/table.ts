// Table view and CSV copy (R53 accessibility: every chart has a table view with CSV copy).

export type Cell = string | number | null;
export type Table = { columns: string[]; rows: Cell[][] };

/** Rows rendered in the table view; Copy CSV always copies every row. */
export const TABLE_ROW_LIMIT = 500;

function csvCell(v: Cell): string {
  if (v == null) return "";
  const s = typeof v === "number" ? (Number.isFinite(v) ? String(v) : "") : v;
  return /[",\r\n]/.test(s) || /^\s|\s$/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}

/** RFC 4180 CSV with CRLF line ends; numbers at full precision, gaps empty. */
export function toCsv(t: Table): string {
  return [t.columns, ...t.rows].map((r) => r.map(csvCell).join(",")).join("\r\n") + "\r\n";
}

const nf = new Map<string, Intl.NumberFormat>();

/** Compact, locale-aware number formatting: up to 4 significant digits, or fixed decimals when given. */
export function formatNumber(v: number, decimals?: number): string {
  if (!Number.isFinite(v)) return "—";
  const abs = Math.abs(v);
  const key = decimals != null ? `d${decimals}` : abs !== 0 && (abs < 1e-3 || abs >= 1e7) ? "e" : "s";
  let f = nf.get(key);
  if (!f) {
    f =
      key === "e"
        ? new Intl.NumberFormat(undefined, { notation: "scientific", maximumSignificantDigits: 3 })
        : key === "s"
          ? new Intl.NumberFormat(undefined, { maximumSignificantDigits: 4 })
          : new Intl.NumberFormat(undefined, { maximumFractionDigits: decimals, minimumFractionDigits: 0 });
    nf.set(key, f);
  }
  return f.format(v);
}

export function formatCell(v: Cell): string {
  if (v == null) return "";
  return typeof v === "number" ? formatNumber(v) : v;
}

/** Copies text with the clipboard of the window that owns `el` (popouts have their own). */
export async function copyText(text: string, el: Element | null): Promise<boolean> {
  const nav = el?.ownerDocument.defaultView?.navigator ?? (typeof navigator !== "undefined" ? navigator : undefined);
  try {
    await nav?.clipboard.writeText(text);
    return !!nav?.clipboard;
  } catch {
    return false;
  }
}
