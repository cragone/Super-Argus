import { detectLevel, prettyJson } from '@/lib/logLevel';

describe('detectLevel', () => {
  it('prefers a parsed level field', () => {
    expect(detectLevel('anything', { level: 'ERROR' })).toBe('error');
    expect(detectLevel('anything', { severity: 'warning' })).toBe('warn');
  });

  it('falls back to the line text', () => {
    expect(detectLevel('2026 level=error msg=boom')).toBe('error');
    expect(detectLevel('[WARN] disk almost full')).toBe('warn');
    expect(detectLevel('INFO started')).toBe('info');
    expect(detectLevel('trace id=1')).toBe('debug');
    expect(detectLevel('nothing to see')).toBeNull();
  });

  it('does not match levels inside other words', () => {
    expect(detectLevel('information_schema query')).toBeNull();
    expect(detectLevel('terrorist')).toBeNull();
  });

  it('ignores an unknown level field value', () => {
    expect(detectLevel('level=error', { level: 'weird' })).toBe('error');
  });
});

describe('prettyJson', () => {
  it('pretty-prints objects and arrays', () => {
    expect(prettyJson('{"a":1,"b":[2]}')).toBe('{\n  "a": 1,\n  "b": [\n    2\n  ]\n}');
    expect(prettyJson('  [1]  ')).toBe('[\n  1\n]');
  });

  it('returns null for non-JSON', () => {
    expect(prettyJson('plain')).toBeNull();
    expect(prettyJson('{broken')).toBeNull();
    expect(prettyJson('')).toBeNull();
  });
});
