import { applySuggestion, filterSuggestions, getContext } from '@/lib/autocomplete';

const ctxAt = (text: string) => getContext(text, text.length);

describe('getContext', () => {
  it('is null outside a selector', () => {
    expect(ctxAt('')).toBeNull();
    expect(ctxAt('plain text')).toBeNull();
    expect(ctxAt('{service="api"} |= "er')).toBeNull();
  });

  it('suggests label names at the start of a matcher', () => {
    expect(ctxAt('{')).toEqual({ kind: 'label', prefix: '', start: 1 });
    expect(ctxAt('{ser')).toEqual({ kind: 'label', prefix: 'ser', start: 1 });
    expect(ctxAt('{service="api", en')).toEqual({ kind: 'label', prefix: 'en', start: 16 });
    expect(ctxAt('{service="api",')).toEqual({ kind: 'label', prefix: '', start: 15 });
  });

  it('suggests values inside quotes for every operator', () => {
    for (const op of ['=', '!=', '=~', '!~']) {
      const text = `{env${op}"pr`;
      expect(ctxAt(text)).toEqual({ kind: 'value', label: 'env', prefix: 'pr', start: text.length - 2 });
    }
    expect(ctxAt('{env="')).toMatchObject({ kind: 'value', label: 'env', prefix: '' });
  });

  it('ignores commas inside a quoted value', () => {
    expect(ctxAt('{msg="a, b')).toMatchObject({ kind: 'value', label: 'msg', prefix: 'a, b' });
  });

  it('is null after a completed matcher value or once the selector is closed', () => {
    expect(ctxAt('{env="prod"')).toBeNull();
    expect(ctxAt('{env="prod"}')).toBeNull();
  });

  it('uses the cursor, not the end of the text', () => {
    const text = '{service="api", e} |= "x"';
    expect(getContext(text, 17)).toEqual({ kind: 'label', prefix: 'e', start: 16 });
  });
});

describe('filterSuggestions', () => {
  it('ranks prefix matches before substring matches and drops the exact match', () => {
    expect(filterSuggestions(['service', 'env', 'observer', 'ser'], 'ser')).toEqual(['service', 'observer']);
    expect(filterSuggestions(['alpha', 'beta', 'gamma'], 'a')).toEqual(['alpha', 'beta', 'gamma']);
    expect(filterSuggestions(['b', 'a'], '')).toEqual(['b', 'a']);
    expect(filterSuggestions(Array.from({ length: 50 }, (_, i) => `v${i}`), '', 5)).toHaveLength(5);
  });
});

describe('applySuggestion', () => {
  it('completes a label name and opens the value quote', () => {
    const text = '{service="api", en}';
    const cursor = 18;
    const ctx = getContext(text, cursor)!;
    expect(applySuggestion(text, cursor, ctx, 'env')).toEqual({ text: '{service="api", env=""}', cursor: 21 });
  });

  it('completes a value and closes the quote once', () => {
    let text = '{env="pr';
    let ctx = getContext(text, text.length)!;
    expect(applySuggestion(text, text.length, ctx, 'prod')).toEqual({ text: '{env="prod"', cursor: 11 });

    text = '{env="pr"}';
    ctx = getContext(text, 8)!;
    expect(applySuggestion(text, 8, ctx, 'prod')).toEqual({ text: '{env="prod"}', cursor: 11 });
  });

  it('escapes quotes and backslashes in values', () => {
    const text = '{path="';
    const ctx = getContext(text, text.length)!;
    expect(applySuggestion(text, text.length, ctx, 'a"b\\c').text).toBe('{path="a\\"b\\\\c"');
  });
});
