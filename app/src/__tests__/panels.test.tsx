import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen } from '@testing-library/react-native';
import type { ReactElement } from 'react';

import { BarList, barItems } from '@/components/charts/BarList';
import { Legend } from '@/components/charts/Legend';
import { StatTile, statValue } from '@/components/charts/StatTile';
import { linePaths } from '@/components/charts/TimeseriesChart';
import { PanelForm } from '@/components/PanelForm';
import { panelDraft, rangeKey } from '@/hooks/useDashboards';
import type { ChartSeries } from '@/lib/chart';

/** PanelForm embeds the query box, which reads label data through react-query. */
const withClient = (ui: ReactElement) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>
);

const mk = (key: string, pts: [number, number][]): ChartSeries => ({ key, label: key, points: pts });

describe('linePaths', () => {
  const x = (t: number) => t / 10;
  const y = (v: number) => 100 - v;

  it('draws one path for continuous points', () => {
    expect(linePaths([[0, 1], [10, 2], [20, 3]], x, y, 10)).toEqual(['M 0.0 99.0 L 1.0 98.0 L 2.0 97.0']);
  });

  it('breaks the line where steps are missing (topk gaps)', () => {
    const paths = linePaths([[0, 1], [10, 2], [50, 3], [60, 4]], x, y, 10);
    expect(paths).toHaveLength(2);
    expect(paths[1].startsWith('M 5.0')).toBe(true);
  });

  it('handles empty input', () => {
    expect(linePaths([], x, y, 10)).toEqual([]);
  });
});

describe('top-N and stat helpers', () => {
  it('ranks newest values largest first and drops zeros', () => {
    const items = barItems([mk('a', [[1, 5], [2, 7]]), mk('b', [[1, 50], [2, 3]]), mk('c', [[1, 9], [2, 0]]), mk('d', [[1, 1], [2, 7]])]);
    expect(items.map((i) => [i.label, i.value])).toEqual([['a', 7], ['d', 7], ['b', 3]]);
  });

  it('caps the list', () => {
    const many = Array.from({ length: 20 }, (_, i) => mk(`s${i}`, [[1, i + 1]]));
    expect(barItems(many, 5)).toHaveLength(5);
    expect(barItems(many, 5)[0].label).toBe('s19');
  });

  it('sums the newest value of every series for a stat', () => {
    expect(statValue([])).toBeNull();
    expect(statValue([mk('a', [[1, 2], [2, 3]]), mk('b', [[1, 1], [2, 4]])])).toBe(7);
  });
});

describe('chart components', () => {
  it('renders ranked bars with accessible values', async () => {
    await render(<BarList items={[{ key: 'a', label: '/v1/items', value: 18 }, { key: 'b', label: '/v1/users', value: 5 }]} />);
    expect(screen.getByLabelText('/v1/items: 18')).toBeTruthy();
    expect(screen.getByLabelText('/v1/users: 5')).toBeTruthy();
  });

  it('shows a legend only for two or more series', async () => {
    const one = await render(<Legend series={[mk('a', [])]} slots={[0]} />);
    expect(screen.queryByText('a')).toBeNull();
    await one.unmount();
    await render(<Legend series={[mk('a', []), mk('b', [])]} slots={[0, 1]} />);
    expect(screen.getByText('a')).toBeTruthy();
    expect(screen.getByText('b')).toBeTruthy();
  });

  it('shows the stat value, or a dash without data', async () => {
    const r = await render(<StatTile series={[mk('a', [[1, 12900]])]} />);
    expect(screen.getByLabelText('Value 12.9K')).toBeTruthy();
    await r.unmount();
    await render(<StatTile series={[]} />);
    expect(screen.getByLabelText('Value no data')).toBeTruthy();
  });
});

describe('PanelForm', () => {
  it('requires a title and a query', async () => {
    const onSubmit = jest.fn().mockResolvedValue(undefined);
    await render(withClient(<PanelForm submitLabel="Add panel" onSubmit={onSubmit} onCancel={() => {}} />));
    await fireEvent.press(screen.getByLabelText('Add panel'));
    expect(screen.getByText('Give the panel a title')).toBeTruthy();
    await fireEvent.changeText(screen.getByLabelText('Panel title'), 'Errors');
    await fireEvent.press(screen.getByLabelText('Add panel'));
    expect(screen.getByText('Enter a query')).toBeTruthy();
    expect(onSubmit).not.toHaveBeenCalled();
  });

  it('submits trimmed values and keeps options; logs panels drop the step', async () => {
    const onSubmit = jest.fn().mockResolvedValue(undefined);
    await render(
      withClient(
        <PanelForm initial={{ title: ' T ', type: 'logs', query: ' {a="b"} ', step: '30s', options: { width: 'full' }, position: 3 }} submitLabel="Save" onSubmit={onSubmit} onCancel={() => {}} />,
      ),
    );
    await fireEvent.press(screen.getByLabelText('Save'));
    expect(onSubmit).toHaveBeenCalledWith({ title: 'T', type: 'logs', query: '{a="b"}', step: '', options: { width: 'full' }, position: 3 });
  });

  it('shows the server error and stays open', async () => {
    const onSubmit = jest.fn().mockRejectedValue(new Error('syntax error at position 0'));
    await render(withClient(<PanelForm initial={{ title: 'T', type: 'timeseries', query: '{a="b"}' }} submitLabel="Save" onSubmit={onSubmit} onCancel={() => {}} />));
    await fireEvent.press(screen.getByLabelText('Save'));
    expect(await screen.findByText('syntax error at position 0')).toBeTruthy();
  });
});

describe('dashboard helpers', () => {
  it('keys time ranges and builds a draft from a panel', () => {
    expect(rangeKey({ kind: 'preset', id: '1h' })).toBe('1h');
    expect(rangeKey({ kind: 'custom', start: 'a', end: 'b' })).toBe('a|b');
    expect(panelDraft({ id: 'p', dashboard_id: 'd', title: 'T', type: 'stat', query: 'q', step: '', options: { width: 'full' }, position: 2 })).toEqual({
      title: 'T', type: 'stat', query: 'q', step: '', options: { width: 'full' }, position: 2,
    });
  });
});
