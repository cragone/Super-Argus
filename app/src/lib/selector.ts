const MATCHER = /([A-Za-z_][A-Za-z0-9_]*)\s*(=~|!~|!=|=)\s*"((?:[^"\\]|\\.)*)"/g;

const quote = (v: string) => `"${v.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`;

/**
 * Adds name="value" to the stream selector of the query, replacing an existing
 * equality matcher for the same label. Creates a selector if there is none.
 */
export function addMatcher(query: string, name: string, value: string): string {
  const matcher = `${name}=${quote(value)}`;
  const open = query.indexOf('{');
  const close = open >= 0 ? query.indexOf('}', open) : -1;
  if (open < 0 || close < 0) {
    const rest = query.trim();
    return `{${matcher}}${rest ? ' ' + rest : ''}`;
  }
  const inner = query.slice(open + 1, close);
  let replaced = false;
  const next = inner.replace(MATCHER, (m, n: string, op: string) => {
    if (n === name && op === '=' && !replaced) {
      replaced = true;
      return matcher;
    }
    return m;
  });
  const body = replaced ? next : inner.trim() === '' ? matcher : `${inner.replace(/[,\s]+$/, '')}, ${matcher}`;
  return query.slice(0, open + 1) + body + query.slice(close);
}
