import { ScrollView, Text, TextInput, View } from 'react-native';

import { RANGE_PRESETS, type TimeRange } from '@/lib/time';
import { mono, useTheme } from '@/theme';

import { Chip } from './ui';

export function TimeRangePicker({ value, onChange }: { value: TimeRange; onChange: (r: TimeRange) => void }) {
  const t = useTheme();
  const input = {
    flex: 1,
    color: t.text,
    backgroundColor: t.surface,
    borderColor: t.border,
    borderWidth: 1,
    borderRadius: 8,
    paddingHorizontal: 8,
    paddingVertical: 6,
    fontSize: 13,
    ...mono,
  } as const;

  return (
    <View style={{ gap: 8 }}>
      <ScrollView horizontal showsHorizontalScrollIndicator={false}>
        {RANGE_PRESETS.map((p) => (
          <Chip
            key={p.id}
            label={`Last ${p.label}`}
            selected={value.kind === 'preset' && value.id === p.id}
            onPress={() => onChange({ kind: 'preset', id: p.id })}
          />
        ))}
        <Chip
          label="Custom"
          selected={value.kind === 'custom'}
          onPress={() => {
            if (value.kind === 'custom') return;
            const end = new Date();
            onChange({ kind: 'custom', start: new Date(end.getTime() - 3_600_000).toISOString(), end: end.toISOString() });
          }}
        />
      </ScrollView>
      {value.kind === 'custom' && (
        <View style={{ flexDirection: 'row', gap: 8, alignItems: 'center' }}>
          <Text style={{ color: t.textDim }}>From</Text>
          <TextInput
            accessibilityLabel="Start time"
            value={value.start}
            onChangeText={(start) => onChange({ ...value, start })}
            autoCapitalize="none"
            autoCorrect={false}
            style={input}
          />
          <Text style={{ color: t.textDim }}>to</Text>
          <TextInput
            accessibilityLabel="End time"
            value={value.end}
            onChangeText={(end) => onChange({ ...value, end })}
            autoCapitalize="none"
            autoCorrect={false}
            style={input}
          />
        </View>
      )}
    </View>
  );
}
