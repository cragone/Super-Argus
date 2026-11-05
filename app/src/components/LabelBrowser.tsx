import { useState } from 'react';
import { ScrollView, Text, View } from 'react-native';

import { useLabelNames, useLabelValues } from '@/hooks/useLogs';
import { useTheme } from '@/theme';

import { Chip } from './ui';

/** Browse label names, then their values; picking a value adds it to the selector. */
export function LabelBrowser({ onPick }: { onPick: (name: string, value: string) => void }) {
  const t = useTheme();
  const [open, setOpen] = useState<string | null>(null);
  const names = useLabelNames();
  const values = useLabelValues(open);

  if (names.isLoading) return <Text style={{ color: t.textDim }}>Loading labels…</Text>;
  if (names.isError) return <Text style={{ color: t.danger }}>Could not load labels.</Text>;
  if (!names.data?.length) return <Text style={{ color: t.textDim }}>No labels yet. Push some logs first.</Text>;

  return (
    <View style={{ gap: 8 }}>
      <ScrollView horizontal showsHorizontalScrollIndicator={false}>
        {names.data.map((n) => (
          <Chip key={n} label={n} selected={open === n} onPress={() => setOpen(open === n ? null : n)} />
        ))}
      </ScrollView>
      {open && (
        <ScrollView horizontal showsHorizontalScrollIndicator={false} accessibilityLabel={`Values of ${open}`}>
          {values.isLoading && <Text style={{ color: t.textDim }}>Loading…</Text>}
          {values.data?.length === 0 && <Text style={{ color: t.textDim }}>No values</Text>}
          {values.data?.map((v) => (
            <Chip key={v} label={v} accessibilityLabel={`Add ${open}=${v}`} onPress={() => onPick(open, v)} />
          ))}
        </ScrollView>
      )}
    </View>
  );
}
