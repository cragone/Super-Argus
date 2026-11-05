import { addMatcher } from '@/lib/selector';

describe('addMatcher', () => {
  it('creates a selector from nothing', () => {
    expect(addMatcher('', 'service', 'api')).toBe('{service="api"}');
    expect(addMatcher('  ', 'service', 'api')).toBe('{service="api"}');
  });

  it('keeps existing pipeline text when there is no selector yet', () => {
    expect(addMatcher('|= "err"', 'service', 'api')).toBe('{service="api"} |= "err"');
  });

  it('fills an empty selector', () => {
    expect(addMatcher('{}', 'env', 'prod')).toBe('{env="prod"}');
  });

  it('appends to an existing selector and keeps the rest', () => {
    expect(addMatcher('{service="api"} |= "x"', 'env', 'prod')).toBe('{service="api", env="prod"} |= "x"');
    expect(addMatcher('{service="api",}', 'env', 'prod')).toBe('{service="api", env="prod"}');
  });

  it('replaces the value of an existing equality matcher for that label', () => {
    expect(addMatcher('{service="api", env="dev"}', 'env', 'prod')).toBe('{service="api", env="prod"}');
  });

  it('does not replace regex matchers, it adds alongside', () => {
    expect(addMatcher('{env=~"a|b"}', 'env', 'prod')).toBe('{env=~"a|b", env="prod"}');
  });

  it('escapes the value', () => {
    expect(addMatcher('', 'msg', 'say "hi"')).toBe('{msg="say \\"hi\\""}');
  });
});
