import { formatTime, nsToDate, resolveRange } from '@/lib/time';

describe('resolveRange', () => {
  const now = Date.UTC(2026, 2, 1, 12, 0, 0);

  it('resolves presets to nanosecond strings, ending just after now', () => {
    const r = resolveRange({ kind: 'preset', id: '1h' }, now);
    expect(r).toEqual({ start: `${now - 3_600_000}000000`, end: `${now + 1000}000000` });
  });

  it('rejects unknown presets', () => {
    expect(resolveRange({ kind: 'preset', id: 'nope' }, now)).toEqual({ error: 'Unknown time range' });
  });

  it('parses custom RFC3339 ranges', () => {
    const r = resolveRange({ kind: 'custom', start: '2026-03-01T12:00:00Z', end: '2026-03-01T13:00:00Z' });
    expect(r).toEqual({ start: '1772366400000000000', end: '1772370000000000000' });
  });

  it('validates custom input', () => {
    expect(resolveRange({ kind: 'custom', start: 'yesterday', end: '2026-03-01T13:00:00Z' })).toEqual({
      error: expect.stringContaining('Start'),
    });
    expect(resolveRange({ kind: 'custom', start: '2026-03-01T12:00:00Z', end: 'x' })).toEqual({
      error: expect.stringContaining('End'),
    });
    expect(resolveRange({ kind: 'custom', start: '2026-03-01T13:00:00Z', end: '2026-03-01T12:00:00Z' })).toEqual({
      error: 'End must be after start',
    });
  });
});

describe('timestamps', () => {
  it('converts nanosecond strings without float rounding', () => {
    expect(nsToDate('1772366400123456789').getTime()).toBe(1772366400123);
    expect(nsToDate('5').getTime()).toBe(0);
  });

  it('formats HH:mm:ss.SSS in local time', () => {
    expect(formatTime('1772366400123456789')).toMatch(/^\d{2}:\d{2}:\d{2}\.123$/);
  });
});
