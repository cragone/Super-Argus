import { FlashList, type FlashListRef } from '@shopify/flash-list';
import { useCallback, useEffect, useRef, useState } from 'react';
import { Text, View } from 'react-native';

import { LogRow } from '@/components/LogRow';
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

/**
 * Live stream for one service query. Starts on mount and whenever `query`
 * changes; the tail is stopped on unmount, which frees the per-user tail slot
 * when the parent switches to History.
 */
export function ServiceLive({ query }: { query: string }) {
  const t = useTheme();
  const tail = useTail();
  const { start, stop } = tail;
  const [autoScroll, setAutoScroll] = useState(true);
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const list = useRef<FlashListRef<TailLine>>(null);

  useEffect(() => {
    start(query);
    return stop;
    // start/stop identities may change per render; restart only when the query changes
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);

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

  const running = tail.status === 'connecting' || tail.status === 'live' || tail.status === 'reconnecting';
  const dot = tail.status === 'live' ? t.levels.info : tail.status === 'error' ? t.danger : t.levels.warn;

  return (
    <View style={{ flex: 1 }}>
      <View style={{ padding: 12, gap: 8, borderBottomWidth: 1, borderBottomColor: t.border }}>
        <View style={{ flexDirection: 'row', gap: 8 }}>
          {running ? (
            <Button title="Stop" variant="ghost" onPress={tail.stop} style={{ flex: 1 }} />
          ) : (
            <Button title="Resume stream" onPress={() => tail.start(query)} style={{ flex: 1 }} />
          )}
          <Button title={tail.paused ? 'Unpause' : 'Pause'} variant="ghost" disabled={!running} onPress={() => tail.setPaused(!tail.paused)} />
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
        {tail.error && <ErrorText>{tail.error}</ErrorText>}
        {tail.droppedLines > 0 && (
          <Text accessibilityLabel="Dropped lines" style={{ color: t.levels.warn, fontSize: 13 }}>
            Too fast to keep up: {tail.droppedLines.toLocaleString()} lines were skipped. Add a filter to see everything.
          </Text>
        )}
      </View>
      {tail.lines.length === 0 ? (
        <Text style={{ color: t.textDim, padding: 16 }}>
          {running ? 'Waiting for new lines from this service…' : 'Stream stopped.'} Older lines are under History.
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
