import {
  OTHER_KEY,
  foldSeries,
  formatBytes,
  formatTick,
  formatValue,
  lastValue,
  nearestIndex,
  niceAxis,
  seriesKey,
  seriesLabel,
  timeTicks,
  toChartSeries,
  unionTimes,
  valueAt,
  type ChartSeries,
} from '@/lib/chart';
import { ColorAssigner } from '@/theme/chartColors';

const mk = (key: string, pts: [number, number][]): ChartSeries => ({ key, label: key, points: pts });

describe('series helpers', () => {
  it('labels series', () => {
    expect(seriesLabel({})).toBe('total');
    expect(seriesLabel({ level: 'error' })).toBe('error');
    expect(seriesLabel({ level: '' })).toBe('(empty)');
    expect(seriesLabel({ service: 'api', env: 'prod' })).toBe('env=prod, service=api');
  });

  it('keys are order independent', () => {
    expect(seriesKey({ a: '1', b: '2' })).toBe(seriesKey({ b: '2', a: '1' }));
    expect(seriesKey({ a: '1' })).not.toBe(seriesKey({ a: '2' }));
  });

  it('converts server series', () => {
    const [s] = toChartSeries([{ labels: { level: 'error' }, values: [[1000, 2], [2000, 3]] }]);
    expect(s.label).toBe('error');
    expect(s.points).toEqual([[1000, 2], [2000, 3]]);
    expect(lastValue(s)).toBe(3);
    expect(lastValue({ key: 'k', label: 'k', points: [] })).toBe(0);
  });

  it('finds values and nearest positions', () => {
    const s = mk('a', [[10, 1], [20, 2], [30, 3]]);
    expect(valueAt(s, 20)).toBe(2);
    expect(valueAt(s, 25)).toBeNull();
    expect(unionTimes([s, mk('b', [[15, 1], [30, 1]])])).toEqual([10, 15, 20, 30]);
    const xs = [10, 20, 30, 40];
    expect(nearestIndex(xs, 0)).toBe(0);
    expect(nearestIndex(xs, 24)).toBe(1);
    expect(nearestIndex(xs, 26)).toBe(2);
    expect(nearestIndex(xs, 99)).toBe(3);
    expect(nearestIndex([], 5)).toBe(-1);
  });
});

describe('foldSeries', () => {
  const many = Array.from({ length: 12 }, (_, i) => mk(`s${i}`, [[1, i + 1], [2, i + 1]]));

  it('leaves 8 or fewer series alone', () => {
    expect(foldSeries(many.slice(0, 8))).toHaveLength(8);
  });

  it('keeps the 7 biggest in their original order and folds the rest into a summed Other', () => {
    const out = foldSeries(many);
    expect(out).toHaveLength(8);
    const other = out[out.length - 1];
    expect(other.key).toBe(OTHER_KEY);
    expect(other.label).toBe('Other (5)');
    // folded: s0..s4 = 1+2+3+4+5 = 15 at each timestamp
    expect(other.points).toEqual([[1, 15], [2, 15]]);
    expect(out.slice(0, 7).map((s) => s.key)).toEqual(['s5', 's6', 's7', 's8', 's9', 's10', 's11']);
  });

  it('sums series with different timestamps as zeros where missing', () => {
    const s = [...many.slice(0, 7), mk('x', [[1, 0.5]]), mk('y', [[2, 0.25]])];
    const other = foldSeries(s, 8).pop()!;
    expect(other.points).toEqual([[1, 0.5 + 0], [2, 0.25]].map(([a, b]) => [a, b]));
  });
});

describe('axes and formatting', () => {
  it('builds clean axes from zero', () => {
    expect(niceAxis(87)).toEqual({ ticks: [0, 20, 40, 60, 80, 100], max: 100 });
    expect(niceAxis(10)).toEqual({ ticks: [0, 2, 4, 6, 8, 10], max: 10 });
    expect(niceAxis(0.37).ticks[0]).toBe(0);
    expect(niceAxis(0.37).max).toBeGreaterThanOrEqual(0.37);
    expect(niceAxis(0)).toEqual({ ticks: [0, 1], max: 1 });
    expect(niceAxis(NaN).max).toBe(1);
  });

  it('formats values compactly', () => {
    expect(formatValue(0)).toBe('0');
    expect(formatValue(0.004)).toBe('0.004');
    expect(formatValue(0.5)).toBe('0.5');
    expect(formatValue(12.34)).toBe('12.3');
    expect(formatValue(1284)).toBe('1,284');
    expect(formatValue(12900)).toBe('12.9K');
    expect(formatValue(4_200_000)).toBe('4.2M');
    expect(formatValue(3.1e9)).toBe('3.1B');
    expect(formatValue(NaN)).toBe('-');
  });

  it('formats bytes', () => {
    expect(formatBytes(512)).toBe('512 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatBytes(5 * 1024 * 1024)).toBe('5 MB');
  });

  it('builds time ticks and labels', () => {
    expect(timeTicks(0, 100, 4)).toEqual([0, 25, 50, 75, 100]);
    expect(timeTicks(5, 5)).toEqual([5]);
    const ms = new Date(2026, 2, 1, 13, 5).getTime();
    expect(formatTick(ms, 3600_000)).toBe('13:05');
    expect(formatTick(ms, 7 * 24 * 3600_000)).toBe('03-01 13:05');
  });
});

describe('ColorAssigner', () => {
  it('assigns the lowest free slot in order and never cycles within 8', () => {
    const c = new ColorAssigner();
    expect(c.assign(['a', 'b', 'c'])).toEqual([0, 1, 2]);
  });

  it('keeps survivors on their color when the series set changes', () => {
    const c = new ColorAssigner();
    c.assign(['a', 'b', 'c']);
    expect(c.assign(['a', 'c'])).toEqual([0, 2]); // b left; a and c keep their colors
    expect(c.assign(['a', 'c', 'd'])).toEqual([0, 2, 1]); // d reuses the freed slot
    expect(c.assign(['d', 'a', 'c'])).toEqual([1, 0, 2]); // order in the list does not matter
  });
});
