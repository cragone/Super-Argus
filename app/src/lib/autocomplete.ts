export type AutocompleteContext =
  | { kind: 'label'; prefix: string; start: number }
  | { kind: 'value'; label: string; prefix: string; start: number };

/**
 * Works out what the user is typing inside the stream selector {...}:
 * a label name, or the value inside the quotes of label="...".
 * `start` is the index in the text where the typed prefix begins.
 */
export function getContext(text: string, cursor: number): AutocompleteContext | null {
  const before = text.slice(0, cursor);
  const open = before.lastIndexOf('{');
  if (open < 0 || before.indexOf('}', open) >= 0) return null;

  // Start of the current matcher: after the last comma that is not inside a string.
  let segStart = open + 1;
  let inStr = false;
  for (let i = open + 1; i < before.length; i++) {
    const c = before[i];
    if (c === '\\' && inStr) i++;
    else if (c === '"') inStr = !inStr;
    else if (c === ',' && !inStr) segStart = i + 1;
  }
  const seg = before.slice(segStart);

  const label = /^(\s*)([A-Za-z_][A-Za-z0-9_]*)?$/.exec(seg);
  if (label) {
    const prefix = label[2] ?? '';
    return { kind: 'label', prefix, start: cursor - prefix.length };
  }
  const value = /^\s*([A-Za-z_][A-Za-z0-9_]*)\s*(?:=~|!~|!=|=)\s*"((?:[^"\\]|\\.)*)$/.exec(seg);
  if (value) {
    return { kind: 'value', label: value[1], prefix: value[2], start: cursor - value[2].length };
  }
  return null;
}

export function filterSuggestions(all: string[], prefix: string, max = 20): string[] {
  const p = prefix.toLowerCase();
  const starts = all.filter((s) => s.toLowerCase().startsWith(p));
  const contains = p ? all.filter((s) => !s.toLowerCase().startsWith(p) && s.toLowerCase().includes(p)) : [];
  return [...starts, ...contains].filter((s) => s !== prefix).slice(0, max);
}

/** Inserts a suggestion, returning the new text and cursor position. */
export function applySuggestion(
  text: string,
  cursor: number,
  ctx: AutocompleteContext,
  suggestion: string,
): { text: string; cursor: number } {
  const after = text.slice(cursor);
  const closing = after.startsWith('"') ? '' : '"';
  if (ctx.kind === 'label') {
    const insert = `${suggestion}="`;
    return { text: text.slice(0, ctx.start) + insert + closing + after, cursor: ctx.start + insert.length };
  }
  const escaped = suggestion.replace(/\\/g, '\\\\').replace(/"/g, '\\"');
  return { text: text.slice(0, ctx.start) + escaped + closing + after, cursor: ctx.start + escaped.length + 1 };
}
