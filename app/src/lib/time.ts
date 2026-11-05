export interface RangePreset {
  id: string;
  label: string;
  ms: number;
}

export const RANGE_PRESETS: RangePreset[] = [
  { id: '15m', label: '15m', ms: 15 * 60_000 },
  { id: '1h', label: '1h', ms: 60 * 60_000 },
  { id: '6h', label: '6h', ms: 6 * 60 * 60_000 },
  { id: '24h', label: '24h', ms: 24 * 60 * 60_000 },
  { id: '7d', label: '7d', ms: 7 * 24 * 60 * 60_000 },
];

export type TimeRange =
  | { kind: 'preset'; id: string }
  | { kind: 'custom'; start: string; end: string }; // RFC3339 text as typed

export interface ResolvedRange {
  /** Unix nanoseconds as decimal strings (they exceed JS safe integers). */
  start: string;
  end: string;
}

const msToNs = (ms: number) => `${Math.trunc(ms)}000000`;

/** Resolves a range to nanosecond strings, or an error message for invalid custom input. */
export function resolveRange(range: TimeRange, now = Date.now()): ResolvedRange | { error: string } {
  if (range.kind === 'preset') {
    const p = RANGE_PRESETS.find((r) => r.id === range.id);
    if (!p) return { error: 'Unknown time range' };
    // end is exclusive on the server, so look one second ahead to include just-arrived lines
    return { start: msToNs(now - p.ms), end: msToNs(now + 1000) };
  }
  const s = Date.parse(range.start);
  const e = Date.parse(range.end);
  if (Number.isNaN(s)) return { error: 'Start must be a date like 2026-03-01T12:00:00Z' };
  if (Number.isNaN(e)) return { error: 'End must be a date like 2026-03-01T13:00:00Z' };
  if (e <= s) return { error: 'End must be after start' };
  return { start: msToNs(s), end: msToNs(e) };
}

/** Date for a nanosecond timestamp string (millisecond precision). */
export function nsToDate(ns: string): Date {
  return new Date(ns.length > 6 ? Number(ns.slice(0, -6)) : 0);
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0');

/** HH:mm:ss.SSS in local time. */
export function formatTime(ns: string): string {
  const d = nsToDate(ns);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
}

/** yyyy-MM-dd HH:mm:ss.SSS in local time. */
export function formatDateTime(ns: string): string {
  const d = nsToDate(ns);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${formatTime(ns)}`;
}
