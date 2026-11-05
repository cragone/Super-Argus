/** A labelled line: points are [unix ms, value]. */
export interface ChartSeries {
  key: string;
  label: string;
  points: [number, number][];
}

/** Server series shape (see /query/metric). */
export interface RawSeries {
  labels: Record<string, string>;
  values: number[][];
}

/** "service=api, env=prod" or "total" for an unlabelled series. */
export function seriesLabel(labels: Record<string, string>): string {
  const keys = Object.keys(labels).sort();
  if (keys.length === 0) return 'total';
  // A single label reads best as just its value (the grouping label is in the panel query).
  if (keys.length === 1) return labels[keys[0]] || '(empty)';
  return keys.map((k) => `${k}=${labels[k]}`).join(', ');
}

/** Stable key identifying a series across refreshes (used to keep its color). */
export function seriesKey(labels: Record<string, string>): string {
  return Object.keys(labels)
    .sort()
    .map((k) => `${k}=${labels[k]}`)
    .join('\u0000');
}

export function toChartSeries(raw: RawSeries[]): ChartSeries[] {
  return raw.map((s) => ({
    key: seriesKey(s.labels),
    label: seriesLabel(s.labels),
    points: s.values.map((v) => [v[0], v[1]] as [number, number]),
  }));
}

export function peak(s: ChartSeries): number {
  let m = 0;
  for (const [, v] of s.points) if (v > m) m = v;
  return m;
}

/** The newest value of a series (0 when empty). */
export function lastValue(s: ChartSeries): number {
  return s.points.length ? s.points[s.points.length - 1][1] : 0;
}

export const OTHER_KEY = '\u0000other';

/**
 * Keeps the `max - 1` series with the highest peak and folds the rest into a
 * summed "Other" series, so a chart never needs more categorical colors than exist.
 * Missing points count as zero when summing.
 */
export function foldSeries(series: ChartSeries[], max = 8): ChartSeries[] {
  if (series.length <= max) return series;
  const ranked = [...series].sort((a, b) => peak(b) - peak(a) || (a.key < b.key ? -1 : 1));
  const keep = ranked.slice(0, max - 1);
  const rest = ranked.slice(max - 1);
  const sums = new Map<number, number>();
  for (const s of rest) for (const [t, v] of s.points) sums.set(t, (sums.get(t) ?? 0) + v);
  const other: ChartSeries = {
    key: OTHER_KEY,
    label: `Other (${rest.length})`,
    points: [...sums.entries()].sort((a, b) => a[0] - b[0]),
  };
  // Keep the original order for the survivors so colors do not shuffle between refreshes.
  const kept = new Set(keep.map((s) => s.key));
  return [...series.filter((s) => kept.has(s.key)), other];
}

/** Rounds up to a "nice" number (1, 2, 5 x 10^n). */
function niceNum(x: number, round: boolean): number {
  const exp = Math.floor(Math.log10(x));
  const f = x / Math.pow(10, exp);
  let nf: number;
  if (round) nf = f < 1.5 ? 1 : f < 3 ? 2 : f < 7 ? 5 : 10;
  else nf = f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10;
  return nf * Math.pow(10, exp);
}

/** Axis from 0 to a clean maximum with clean tick steps. */
export function niceAxis(max: number, count = 4): { ticks: number[]; max: number } {
  if (!isFinite(max) || max <= 0) return { ticks: [0, 1], max: 1 };
  const step = niceNum(max / count, true);
  const top = Math.ceil(max / step) * step;
  const ticks: number[] = [];
  for (let v = 0; v <= top + step / 2; v += step) ticks.push(Number(v.toPrecision(12)));
  return { ticks, max: top };
}

/**
 * Compact value for axes, tooltips and stat tiles: full below 10,000
 * (1,284), compact above (12.9K, 4.2M). Small values keep up to 2 decimals.
 */
export function formatValue(v: number): string {
  if (!isFinite(v)) return '-';
  const a = Math.abs(v);
  if (a >= 1e9) return trim(v / 1e9) + 'B';
  if (a >= 1e6) return trim(v / 1e6) + 'M';
  if (a >= 1e4) return trim(v / 1e3) + 'K';
  if (a >= 100) return Math.round(v).toLocaleString('en-US');
  if (a >= 10) return trim(v, 1);
  if (a === 0) return '0';
  return trim(v, a < 0.01 ? 3 : 2);
}

function trim(n: number, digits = 1): string {
  return String(Number(n.toFixed(digits)));
}

export function formatBytes(v: number): string {
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let i = 0;
  let n = Math.abs(v);
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${trim(v < 0 ? -n : n, n >= 100 ? 0 : 1)} ${units[i]}`;
}

/** Evenly spaced tick times across [start, end]. */
export function timeTicks(startMs: number, endMs: number, count = 4): number[] {
  if (endMs <= startMs) return [startMs];
  const out: number[] = [];
  for (let i = 0; i <= count; i++) out.push(startMs + ((endMs - startMs) * i) / count);
  return out;
}

const pad = (n: number) => String(n).padStart(2, '0');

/** HH:mm for spans up to a day and a half, else MM-dd HH:mm. */
export function formatTick(ms: number, spanMs: number): string {
  const d = new Date(ms);
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return spanMs <= 36 * 3600_000 ? hm : `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}`;
}

export function formatFullTime(ms: number): string {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
}

/** Index in sorted `xs` nearest to `x`. */
export function nearestIndex(xs: number[], x: number): number {
  if (xs.length === 0) return -1;
  let lo = 0;
  let hi = xs.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (xs[mid] < x) lo = mid + 1;
    else hi = mid;
  }
  if (lo > 0 && Math.abs(xs[lo - 1] - x) <= Math.abs(xs[lo] - x)) return lo - 1;
  return lo;
}

/** Sorted union of all timestamps. */
export function unionTimes(series: ChartSeries[]): number[] {
  const s = new Set<number>();
  for (const x of series) for (const [t] of x.points) s.add(t);
  return [...s].sort((a, b) => a - b);
}

export function valueAt(s: ChartSeries, t: number): number | null {
  // series are sorted by time; binary search
  let lo = 0;
  let hi = s.points.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    const pt = s.points[mid][0];
    if (pt === t) return s.points[mid][1];
    if (pt < t) lo = mid + 1;
    else hi = mid - 1;
  }
  return null;
}
