export type LogLevel = 'error' | 'warn' | 'info' | 'debug';

const ALIASES: Record<string, LogLevel> = {
  error: 'error', err: 'error', fatal: 'error', panic: 'error', critical: 'error', crit: 'error', severe: 'error',
  warn: 'warn', warning: 'warn',
  info: 'info', information: 'info', notice: 'info',
  debug: 'debug', trace: 'debug', verbose: 'debug',
};

/** Level from a parsed level/severity field, else from the first level-looking token in the line. */
export function detectLevel(line: string, fields?: Record<string, string>): LogLevel | null {
  const f = fields?.level ?? fields?.severity ?? fields?.lvl;
  if (f && ALIASES[f.toLowerCase()]) return ALIASES[f.toLowerCase()];
  const m = /\b(error|err|fatal|panic|critical|warn(?:ing)?|info|debug|trace)\b/i.exec(line.slice(0, 200));
  return m ? ALIASES[m[1].toLowerCase()] : null;
}

/** Pretty-printed JSON for lines that are JSON objects or arrays, else null. */
export function prettyJson(line: string): string | null {
  const t = line.trim();
  if (!(t.startsWith('{') || t.startsWith('['))) return null;
  try {
    return JSON.stringify(JSON.parse(t), null, 2);
  } catch {
    return null;
  }
}
