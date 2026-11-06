import type { AlertRule } from '@/api/client';
import type { Palette } from '@/theme';

export type AlertState = AlertRule['state']['state'];
export type Comparator = AlertRule['comparator'];

export const COMPARATORS: { id: Comparator; symbol: string }[] = [
  { id: 'gt', symbol: '>' },
  { id: 'gte', symbol: '≥' },
  { id: 'lt', symbol: '<' },
  { id: 'lte', symbol: '≤' },
  { id: 'eq', symbol: '=' },
];

export const SEVERITIES = ['info', 'warning', 'critical'] as const;

export function comparatorSymbol(c: Comparator): string {
  return COMPARATORS.find((x) => x.id === c)?.symbol ?? c;
}

/** "value > 10 for 2m", the condition in one line. */
export function describeCondition(r: Pick<AlertRule, 'comparator' | 'threshold' | 'for_duration'>): string {
  const base = `value ${comparatorSymbol(r.comparator)} ${r.threshold}`;
  const f = r.for_duration;
  return f && f !== '0s' ? `${base} for ${f}` : base;
}

export function stateColor(state: AlertState | 'resolved', t: Palette): string {
  switch (state) {
    case 'firing':
      return t.danger;
    case 'pending':
      return t.levels.warn;
    case 'resolved':
      return t.levels.info;
    default:
      return t.textDim;
  }
}

export function formatValue(v: number): string {
  return Number.isInteger(v) ? String(v) : v.toFixed(2);
}

/** Matches the server's duration syntax (15s, 5m, 1.5h, 2d); "0s" and empty mean none. */
export function validDuration(s: string, allowZero: boolean): boolean {
  const v = s.trim();
  if (v === '') return allowZero;
  if (/^0+(\.0+)?(ms|s|m|h|d|w)?$/.test(v)) return allowZero;
  return /^\d+(\.\d+)?(ms|s|m|h|d|w)$/.test(v);
}
