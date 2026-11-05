import { memo } from 'react';
import { Pressable, Text, View } from 'react-native';

import type { QueryEntry } from '@/api/client';
import { detectLevel, prettyJson } from '@/lib/logLevel';
import { formatDateTime, formatTime } from '@/lib/time';
import { mono, useTheme } from '@/theme';

export interface LogRowProps {
  entry: QueryEntry;
  labels?: Record<string, string>;
  expanded: boolean;
  onToggle: (key: string) => void;
  rowKey: string;
}

/** One log line: time, level color bar, truncated text. Tap to expand (pretty JSON, fields, stream labels). */
export const LogRow = memo(function LogRow({ entry, labels, expanded, onToggle, rowKey }: LogRowProps) {
  const t = useTheme();
  const level = detectLevel(entry.line, entry.fields);
  const color = level ? t.levels[level] : t.border;
  const json = expanded ? prettyJson(entry.line) : null;

  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={`Log line at ${formatTime(entry.ts)}`}
      accessibilityState={{ expanded }}
      onPress={() => onToggle(rowKey)}
      style={{ flexDirection: 'row', borderBottomWidth: 1, borderBottomColor: t.border, backgroundColor: t.surface }}>
      <View style={{ width: 4, backgroundColor: color }} />
      <View style={{ flex: 1, paddingVertical: 6, paddingHorizontal: 10 }}>
        <Text style={[mono, { fontSize: 11, color: t.textDim }]}>{expanded ? formatDateTime(entry.ts) : formatTime(entry.ts)}</Text>
        <Text selectable={expanded} numberOfLines={expanded ? undefined : 2} style={[mono, { fontSize: 12.5, color: t.text }]}>
          {json ?? entry.line}
        </Text>
        {expanded && labels && (
          <Text style={[mono, { fontSize: 11, color: t.textDim, marginTop: 6 }]}>
            {Object.entries(labels)
              .map(([k, v]) => `${k}="${v}"`)
              .join('  ')}
          </Text>
        )}
        {expanded && entry.fields && Object.keys(entry.fields).length > 0 && (
          <View style={{ marginTop: 6 }}>
            {Object.entries(entry.fields).map(([k, v]) => (
              <Text key={k} style={[mono, { fontSize: 11, color: t.textDim }]}>
                <Text style={{ color: t.accent }}>{k}</Text>={v}
              </Text>
            ))}
          </View>
        )}
      </View>
    </Pressable>
  );
});
