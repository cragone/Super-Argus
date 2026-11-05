import { useMemo, useState } from 'react';
import { ScrollView, TextInput, View, type TextInputSelectionChangeEvent } from 'react-native';

import { applySuggestion, filterSuggestions, getContext } from '@/lib/autocomplete';
import { useLabelNames, useLabelValues } from '@/hooks/useLogs';
import { mono, useTheme } from '@/theme';

import { Chip } from './ui';

/** ArgusQL input with label and label-value autocomplete inside the stream selector. */
export function QueryInput({
  value,
  onChange,
  onSubmit,
}: {
  value: string;
  onChange: (v: string) => void;
  onSubmit: () => void;
}) {
  const t = useTheme();
  const [cursor, setCursor] = useState(value.length);

  const ctx = useMemo(() => getContext(value, cursor), [value, cursor]);
  const names = useLabelNames();
  const values = useLabelValues(ctx?.kind === 'value' ? ctx.label : null);

  const suggestions = useMemo(() => {
    if (!ctx) return [];
    const pool = ctx.kind === 'label' ? names.data : values.data;
    return filterSuggestions(pool ?? [], ctx.prefix);
  }, [ctx, names.data, values.data]);

  const pick = (s: string) => {
    if (!ctx) return;
    const r = applySuggestion(value, cursor, ctx, s);
    onChange(r.text);
    setCursor(r.cursor);
  };

  return (
    <View>
      <TextInput
        accessibilityLabel="Query"
        value={value}
        onChangeText={onChange}
        onSelectionChange={(e: TextInputSelectionChangeEvent) =>
          setCursor(e.nativeEvent.selection.end)
        }
        onSubmitEditing={onSubmit}
        placeholder={'{service="api"} |= "error"'}
        placeholderTextColor={t.textDim}
        autoCapitalize="none"
        autoCorrect={false}
        spellCheck={false}
        multiline
        style={[
          mono,
          {
            minHeight: 44,
            maxHeight: 120,
            color: t.text,
            backgroundColor: t.surface,
            borderColor: t.border,
            borderWidth: 1,
            borderRadius: 8,
            paddingHorizontal: 10,
            paddingVertical: 8,
            fontSize: 14,
          },
        ]}
      />
      {suggestions.length > 0 && (
        <ScrollView horizontal keyboardShouldPersistTaps="always" showsHorizontalScrollIndicator={false} style={{ marginTop: 6 }}>
          {suggestions.map((s) => (
            <Chip key={s} label={s} accessibilityLabel={`Insert ${s}`} onPress={() => pick(s)} />
          ))}
        </ScrollView>
      )}
    </View>
  );
}
