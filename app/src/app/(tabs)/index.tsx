import { FlashList } from '@shopify/flash-list';
import { useCallback, useMemo, useState } from 'react';
import { Text, View } from 'react-native';

import { errorMessage } from '@/api/client';
import { LabelBrowser } from '@/components/LabelBrowser';
import { LogRow } from '@/components/LogRow';
import { QueryInput } from '@/components/QueryInput';
import { SaveBar } from '@/components/SaveBar';
import { TimeRangePicker } from '@/components/TimeRangePicker';
import { Button, Chip, ErrorText } from '@/components/ui';
import { useRangeQuery, type SubmittedQuery } from '@/hooks/useLogs';
import { addMatcher } from '@/lib/selector';
import { resolveRange, type TimeRange } from '@/lib/time';
import { useTheme } from '@/theme';

const LIMIT = 1000;

export default function ExploreScreen() {
  const t = useTheme();
  const [query, setQuery] = useState('');
  const [range, setRange] = useState<TimeRange>({ kind: 'preset', id: '1h' });
  const [direction, setDirection] = useState<'backward' | 'forward'>('backward');
  const [showLabels, setShowLabels] = useState(true);
  const [submitted, setSubmitted] = useState<SubmittedQuery | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());

  const result = useRangeQuery(submitted);

  const run = useCallback(() => {
    if (!query.trim()) {
      setFormError('Enter a query, e.g. {service="api"}');
      return;
    }
    const r = resolveRange(range);
    if ('error' in r) {
      setFormError(r.error);
      return;
    }
    setFormError(null);
    setExpanded(new Set());
    setSubmitted((prev) => ({ query: query.trim(), start: r.start, end: r.end, direction, limit: LIMIT, nonce: (prev?.nonce ?? 0) + 1 }));
  }, [query, range, direction]);

  const toggle = useCallback((key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  }, []);

  const rows = useMemo(() => result.data?.entries.map((e, i) => ({ key: `${e.ts}-${e.stream}-${i}`, entry: e })) ?? [], [result.data]);
  const stats = result.data?.stats;

  return (
    <View style={{ flex: 1, backgroundColor: t.bg }}>
      <View style={{ padding: 12, gap: 10, borderBottomWidth: 1, borderBottomColor: t.border, backgroundColor: t.surface }}>
        <QueryInput value={query} onChange={setQuery} onSubmit={run} />
        <View style={{ flexDirection: 'row', gap: 8 }}>
          <Button title="Run query" onPress={run} loading={result.isFetching} style={{ flex: 1 }} />
          <Button
            title={direction === 'backward' ? 'Newest first' : 'Oldest first'}
            variant="ghost"
            onPress={() => setDirection((d) => (d === 'backward' ? 'forward' : 'backward'))}
          />
        </View>
        <TimeRangePicker value={range} onChange={setRange} />
        <View style={{ flexDirection: 'row', alignItems: 'center' }}>
          <Chip label={showLabels ? 'Hide labels' : 'Browse labels'} onPress={() => setShowLabels((v) => !v)} />
        </View>
        <SaveBar
          query={query}
          range={range}
          onLoad={(q) => {
            setQuery(q.query);
            if (q.default_range) setRange({ kind: 'preset', id: q.default_range });
          }}
        />
        {showLabels && <LabelBrowser onPick={(n, v) => setQuery((q) => addMatcher(q, n, v))} />}
        {formError && <ErrorText>{formError}</ErrorText>}
      </View>

      {result.isError && (
        <View style={{ padding: 12 }}>
          <ErrorText>{errorMessage(result.error)}</ErrorText>
        </View>
      )}
      {stats && (
        <Text accessibilityLabel="Query stats" style={{ color: t.textDim, fontSize: 12, paddingHorizontal: 12, paddingVertical: 6 }}>
          {rows.length} {rows.length === 1 ? 'line' : 'lines'}
          {rows.length >= LIMIT ? ` (limit ${LIMIT})` : ''} · {stats.elapsed_ms} ms · scanned {stats.lines_scanned.toLocaleString()} lines
        </Text>
      )}

      <View style={{ flex: 1 }}>
        {!submitted ? (
          <Text style={{ color: t.textDim, padding: 16 }}>
            Write a query or pick labels above, then run it. Example: {'{service="api"} |= "error" | json'}
          </Text>
        ) : result.isFetching && !result.data ? (
          <Text style={{ color: t.textDim, padding: 16 }}>Running…</Text>
        ) : rows.length === 0 && !result.isError ? (
          <Text style={{ color: t.textDim, padding: 16 }}>No lines matched in this time range.</Text>
        ) : (
          <FlashList
            data={rows}
            extraData={expanded}
            keyExtractor={(r) => r.key}
            renderItem={({ item }) => (
              <LogRow
                rowKey={item.key}
                entry={item.entry}
                labels={result.data?.streams[item.entry.stream]}
                expanded={expanded.has(item.key)}
                onToggle={toggle}
              />
            )}
          />
        )}
      </View>
    </View>
  );
}
