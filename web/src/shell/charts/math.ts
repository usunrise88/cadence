// Pure helpers for time series: smoothing, alignment and min/max envelopes. The browser only zooms, smooths and
// switches scales (R53); every number shown comes from the API.

export type Num = number | null;

function finite(v: unknown): v is number {
  return typeof v === "number" && Number.isFinite(v);
}

/**
 * Exponential moving average with debiasing, as TensorBoard's smoothing slider: `weight` in [0, 1), 0 = raw.
 * Gaps (null, NaN, ±Infinity) stay gaps and do not reset the average.
 */
export function ema(ys: ArrayLike<Num>, weight: number): Num[] {
  const w = Math.min(Math.max(weight, 0), 0.999);
  const out: Num[] = new Array<Num>(ys.length);
  let last = 0;
  let n = 0;
  for (let i = 0; i < ys.length; i++) {
    const y = ys[i];
    if (!finite(y)) {
      out[i] = null;
      continue;
    }
    last = last * w + (1 - w) * y;
    n++;
    const debias = w === 0 ? 1 : 1 - w ** n;
    out[i] = last / debias;
  }
  return out;
}

/** Values a log scale can show: non-positive and non-finite values become gaps. */
export function logSafe(ys: ArrayLike<Num>): { values: Num[]; skipped: number } {
  const values: Num[] = new Array<Num>(ys.length);
  let skipped = 0;
  for (let i = 0; i < ys.length; i++) {
    const y = ys[i];
    if (finite(y) && y > 0) values[i] = y;
    else {
      if (finite(y)) skipped++;
      values[i] = null;
    }
  }
  return { values, skipped };
}

/** Points of one series with a finite x, sorted by x (stable; a repeated x keeps the last value). */
export function sortedPoints(xs: ArrayLike<number> | undefined, ys: ArrayLike<Num>): { x: number[]; y: Num[] } {
  if (!xs) return { x: [], y: [] };
  const n = Math.min(xs.length, ys.length);
  let sorted = true;
  for (let i = 1; i < n; i++) {
    if ((xs[i] as number) < (xs[i - 1] as number)) {
      sorted = false;
      break;
    }
  }
  const idx = Array.from({ length: n }, (_, i) => i).filter((i) => finite(xs[i]));
  if (!sorted) idx.sort((a, b) => (xs[a] as number) - (xs[b] as number) || a - b);
  const x: number[] = [];
  const y: Num[] = [];
  for (const i of idx) {
    const xi = xs[i] as number;
    const yi = ys[i];
    const v = finite(yi) ? yi : null;
    if (x.length && x[x.length - 1] === xi) y[y.length - 1] = v;
    else {
      x.push(xi);
      y.push(v);
    }
  }
  return { x, y };
}

/** Union of the x values of several sorted series, with each series' values placed on it (null where absent). */
export function align(series: { x: number[]; ys: Num[][] }[]): { x: number[]; columns: Num[][] } {
  const all = new Set<number>();
  for (const s of series) for (const v of s.x) all.add(v);
  const x = [...all].sort((a, b) => a - b);
  const pos = new Map<number, number>();
  x.forEach((v, i) => pos.set(v, i));
  const columns: Num[][] = [];
  for (const s of series) {
    for (const ys of s.ys) {
      const col: Num[] = new Array<Num>(x.length).fill(null);
      s.x.forEach((v, i) => {
        col[pos.get(v) as number] = ys[i] ?? null;
      });
      columns.push(col);
    }
  }
  return { x, columns };
}

export type Envelope = { min: Num[]; max: Num[]; mean: Num[]; last: Num[] };

/** Bucket centres of `n` equal-width buckets over [lo, hi]. */
export function bucketCentres(lo: number, hi: number, n: number): number[] {
  if (n <= 0) return [];
  if (hi <= lo) return [lo];
  const w = (hi - lo) / n;
  return Array.from({ length: n }, (_, i) => lo + w * (i + 0.5));
}

/**
 * Min/max envelope of a sorted series over `n` equal-width x buckets spanning [lo, hi]: per bucket the minimum,
 * maximum, mean and last value (null for empty buckets). Several series binned with the same lo/hi/n share one x
 * axis without further alignment.
 */
export function envelope(x: ArrayLike<number>, ys: ArrayLike<Num>, lo: number, hi: number, n: number): Envelope {
  const buckets = hi <= lo ? 1 : Math.max(1, n);
  const min: Num[] = new Array<Num>(buckets).fill(null);
  const max: Num[] = new Array<Num>(buckets).fill(null);
  const sum = new Array<number>(buckets).fill(0);
  const count = new Array<number>(buckets).fill(0);
  const last: Num[] = new Array<Num>(buckets).fill(null);
  const w = (hi - lo) / buckets;
  for (let i = 0; i < x.length; i++) {
    const y = ys[i];
    const xi = x[i] as number;
    if (!finite(y) || !finite(xi) || xi < lo || xi > hi) continue;
    const b = w > 0 ? Math.min(buckets - 1, Math.floor((xi - lo) / w)) : 0;
    const mn = min[b];
    const mx = max[b];
    min[b] = mn == null || y < mn ? y : mn;
    max[b] = mx == null || y > mx ? y : mx;
    sum[b] = (sum[b] ?? 0) + y;
    count[b] = (count[b] ?? 0) + 1;
    last[b] = y;
  }
  const mean: Num[] = count.map((c, i) => (c ? (sum[i] ?? 0) / c : null));
  return { min, max, mean, last };
}

/** Index of the x value nearest to `v` in a sorted array (binary search); -1 when empty. */
export function nearestIndex(xs: ArrayLike<number>, v: number): number {
  const n = xs.length;
  if (!n) return -1;
  let lo = 0;
  let hi = n - 1;
  while (hi - lo > 1) {
    const mid = (lo + hi) >> 1;
    if ((xs[mid] as number) <= v) lo = mid;
    else hi = mid;
  }
  return Math.abs((xs[hi] as number) - v) < Math.abs((xs[lo] as number) - v) ? hi : lo;
}

/** Index of the nearest non-null value at or around `i` in one column (for the keyboard cursor over gaps). */
export function nearestDefined(col: ArrayLike<Num>, i: number): number {
  for (let d = 0; d < col.length; d++) {
    if (i - d >= 0 && col[i - d] != null) return i - d;
    if (i + d < col.length && col[i + d] != null) return i + d;
  }
  return -1;
}
