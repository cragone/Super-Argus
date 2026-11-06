import { serviceQuery } from '@/lib/serviceQuery';

describe('serviceQuery', () => {
  it('pins the service with no filter', () => {
    expect(serviceQuery('api', '')).toBe('{service="api"}');
    expect(serviceQuery('api', '   ')).toBe('{service="api"}');
  });

  it('turns plain text into a contains filter and escapes it', () => {
    expect(serviceQuery('api', 'timeout')).toBe('{service="api"} |= "timeout"');
    expect(serviceQuery('api', 'say "hi"\\')).toBe('{service="api"} |= "say \\"hi\\"\\\\"');
  });

  it('passes pipelines through untouched', () => {
    expect(serviceQuery('api', '| json | level="error"')).toBe('{service="api"} | json | level="error"');
  });
});
