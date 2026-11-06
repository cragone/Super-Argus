import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { fireEvent, render, screen, waitFor } from '@testing-library/react-native';
import type { ReactElement } from 'react';

import type { AlertRule, Channel } from '@/api/client';
import { AlertRuleForm } from '@/components/AlertRuleForm';
import { describeCondition, formatValue, stateColor, validDuration } from '@/lib/alerts';
import { dark, light } from '@/theme';

const withClient = (ui: ReactElement) => (
  <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{ui}</QueryClientProvider>
);

describe('alert helpers', () => {
  it('describes the condition, mentioning the hold time only when set', () => {
    expect(describeCondition({ comparator: 'gt', threshold: 10, for_duration: '2m' })).toBe('value > 10 for 2m');
    expect(describeCondition({ comparator: 'lte', threshold: 0, for_duration: '0s' })).toBe('value ≤ 0');
  });

  it('colors states distinctly', () => {
    const colors = (['ok', 'pending', 'firing', 'resolved'] as const).map((s) => stateColor(s, light));
    expect(new Set(colors).size).toBe(4);
    expect(stateColor('firing', dark)).toBe(dark.danger);
  });

  it('validates durations like the server', () => {
    expect(validDuration('5m', false)).toBe(true);
    expect(validDuration('1.5h', false)).toBe(true);
    expect(validDuration('0s', true)).toBe(true);
    expect(validDuration('0s', false)).toBe(false);
    expect(validDuration('', true)).toBe(true);
    expect(validDuration('', false)).toBe(false);
    expect(validDuration('soon', true)).toBe(false);
    expect(validDuration('5', true)).toBe(false);
  });

  it('formats values', () => {
    expect(formatValue(12)).toBe('12');
    expect(formatValue(0.456)).toBe('0.46');
  });
});

describe('AlertRuleForm', () => {
  const channels: Channel[] = [{ id: 'c1', name: 'ops', type: 'webhook', url_host: 'h.io', has_secret: false, created_at: '', updated_at: '' }];
  const initial: AlertRule = {
    id: 'r1', owner_id: 'u', name: 'errors', query: 'rate({a="b"}[1m])', comparator: 'gt', threshold: 5, window: '5m',
    for_duration: '2m', cooldown: '0s', severity: 'critical', enabled: true, notify_channel_ids: ['c1'],
    state: { state: 'ok', since: '', last_eval: null, last_value: 0, last_error: '' }, created_at: '', updated_at: '',
  };

  it('submits the edited rule', async () => {
    const onSubmit = jest.fn().mockResolvedValue(undefined);
    await render(withClient(<AlertRuleForm initial={initial} channels={channels} submitLabel="Save rule" onSubmit={onSubmit} onCancel={jest.fn()} />));
    await fireEvent.changeText(screen.getByLabelText('Threshold'), '9.5');
    await fireEvent.press(screen.getByLabelText('Comparator lte'));
    await fireEvent.press(screen.getByLabelText('Channel ops')); // deselect
    await fireEvent.press(screen.getByLabelText('Save rule'));
    await waitFor(() => expect(onSubmit).toHaveBeenCalled());
    expect(onSubmit).toHaveBeenCalledWith({
      name: 'errors', query: 'rate({a="b"}[1m])', comparator: 'lte', threshold: 9.5, window: '5m', for_duration: '2m',
      cooldown: '0s', severity: 'critical', enabled: true, notify_channel_ids: [],
    });
  });

  it('rejects bad input before calling the server', async () => {
    const onSubmit = jest.fn();
    await render(withClient(<AlertRuleForm initial={initial} channels={channels} submitLabel="Save rule" onSubmit={onSubmit} onCancel={jest.fn()} />));
    await fireEvent.changeText(screen.getByLabelText('Window'), 'soon');
    await fireEvent.press(screen.getByLabelText('Save rule'));
    expect(await screen.findByRole('alert')).toBeTruthy();
    expect(onSubmit).not.toHaveBeenCalled();
  });
});
