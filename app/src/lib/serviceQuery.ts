import { addMatcher } from './selector';

/** Escape a string for use inside a double-quoted LogQL line filter. */
function quote(s: string): string {
  return `"${s.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;
}

/**
 * The LogQL query for one service. Plain filter text becomes a "contains" line
 * filter; text starting with `|` is appended as-is so pipelines still work,
 * e.g. `| json | level="error"`.
 */
export function serviceQuery(service: string, filter: string): string {
  const selector = addMatcher('', 'service', service);
  const f = filter.trim();
  if (!f) return selector;
  return f.startsWith('|') ? `${selector} ${f}` : `${selector} |= ${quote(f)}`;
}
