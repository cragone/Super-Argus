import { fireEvent, render, screen } from '@testing-library/react-native';

import { LogRow } from '@/components/LogRow';

const entry = {
  ts: '1772366400123456789',
  stream: '1',
  line: '{"level":"error","status":503}',
  fields: { level: 'error', status: '503' },
};

describe('LogRow', () => {
  it('shows a collapsed line and toggles on press', async () => {
    const onToggle = jest.fn();
    await render(<LogRow rowKey="k1" entry={entry} labels={{ service: 'api' }} expanded={false} onToggle={onToggle} />);
    expect(screen.getByText(entry.line)).toBeTruthy();
    expect(screen.queryByText(/service="api"/)).toBeNull();
    await fireEvent.press(screen.getByRole('button'));
    expect(onToggle).toHaveBeenCalledWith('k1');
  });

  it('pretty-prints JSON and shows labels and fields when expanded', async () => {
    await render(<LogRow rowKey="k1" entry={entry} labels={{ service: 'api', env: 'prod' }} expanded onToggle={() => {}} />);
    expect(screen.getByText(/"status": 503/)).toBeTruthy();
    expect(screen.getByText(/service="api"/)).toBeTruthy();
  });

  it('shows non-JSON lines as they are', async () => {
    await render(
      <LogRow rowKey="k2" entry={{ ts: '1772366400000000000', stream: '1', line: 'plain text line' }} expanded onToggle={() => {}} />,
    );
    expect(screen.getByText('plain text line')).toBeTruthy();
  });
});
