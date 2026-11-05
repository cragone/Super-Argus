import { FlashList, type FlashListRef } from '@shopify/flash-list';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Text, View } from 'react-native';

import { LogRow } from '@/components/LogRow';
import { QueryInput } from '@/components/QueryInput';
import { Button, Chip, ErrorText } from '@/components/ui';
import { MAX_TAIL_LINES, useTail, type TailLine } from '@/hooks/useTail';
import { useTheme } from '@/theme';

const STATUS_TEXT = {
  stopped: 'Not connected',
  connecting: 'Connecting…',
  live: 'Live',
  reconnecting: 'Reconnecting…',
  error: 'Stopped',
} as const;

export default function LiveTailScreen() {
  const t = useTheme();
  const [query, setQuery] = useState('');
  const [autoScroll, setAutoScroll] = useState(true);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [formError, setFormError] = useState<string | null>(null);
  const tail = useTail();
  const list = useRef<FlashListRef<TailLine>>(null);

  const running = tail.status === 'connecting' || tail.status === 'live' || tail.status === 'reconnecting';

  const start = useCallback(() => {
    if (!query.trim()) {
      setFormError('Enter a query with a stream selector, e.g. {service="api"}');
      return;
    }
    setFormError(null);
    setExpanded(new Set());
    tail.start(query.trim());
  }, [query, tail]);

  const toggle = useCallback((key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (!next.delete(key)) next.add(key);
      return next;
    });
  }, []);

  useEffect(() => {
    if (autoScroll && !tail.paused && tail.lines.length > 0) list.current?.scrollToEnd({ animated: false });
  }, [tail.lines, autoScroll, tail.paused]);

  const dot = tail.status === 'live' ? t.levels.info : tail.status === 'error' ? t.danger : t.levels.warn;

  return (
    <View style={{ flex: 1, backgroundColor: t.bg }}>
      <View style={{ padding: 12, gap: 10, borderBottomWidth: 1, borderBottomColor: t.border, backgroundColor: t.surface }}>
        <QueryInput value={query} onChange={setQuery} onSubmit={start} />
        <View style={{ flexDirection: 'row', gap: 8 }}>
          {running ? (
            <Button title="Stop" variant="ghost" onPress={tail.stop} style={{ flex: 1 }} />
          ) : (
            <Button title="Start tail" onPress={start} style={{ flex: 1 }} />
          )}
          <Button
            title={tail.paused ? 'Resume' : 'Pause'}
            variant="ghost"
            disabled={!running}
            onPress={() => tail.setPaused(!tail.paused)}
          />
          <Button title="Clear" variant="ghost" onPress={tail.clear} />
        </View>
        <View style={{ flexDirection: 'row', alignItems: 'center' }}>
          <Chip label={autoScroll ? 'Auto-scroll on' : 'Auto-scroll off'} selected={autoScroll} onPress={() => setAutoScroll((v) => !v)} />
          <View style={{ width: 9, height: 9, borderRadius: 5, backgroundColor: dot, marginRight: 6 }} />
          <Text accessibilityLabel="Tail status" style={{ color: t.textDim, fontSize: 13 }}>
            {STATUS_TEXT[tail.status]}
            {tail.paused && tail.buffered > 0 ? ` · paused, ${tail.buffered} new` : tail.paused ? ' · paused' : ''}
          </Text>
        </View>
        {formError && <ErrorText>{formError}</ErrorText>}
        {tail.error && <ErrorText>{tail.error}</ErrorText>}
        {tail.droppedLines > 0 && (
          <Text accessibilityLabel="Dropped lines" style={{ color: t.levels.warn, fontSize: 13 }}>
            Too fast to keep up: {tail.droppedLines.toLocaleString()} lines were skipped. Narrow the query to see everything.
          </Text>
        )}
      </View>

      {tail.lines.length === 0 ? (
        <Text style={{ color: t.textDim, padding: 16 }}>
          {running ? 'Waiting for new lines…' : 'Write a query and start the tail to watch new lines arrive.'}
        </Text>
      ) : (
        <FlashList
          ref={list}
          data={tail.lines}
          extraData={expanded}
          keyExtractor={(l) => String(l.id)}
          renderItem={({ item }) => (
            <LogRow rowKey={String(item.id)} entry={item} labels={item.labels} expanded={expanded.has(String(item.id))} onToggle={toggle} />
          )}
        />
      )}
      <Text style={{ color: t.textDim, fontSize: 11, padding: 6, textAlign: 'center' }}>
        {tail.lines.length.toLocaleString()} / {MAX_TAIL_LINES.toLocaleString()} lines kept in memory
      </Text>
    </View>
  );
}
