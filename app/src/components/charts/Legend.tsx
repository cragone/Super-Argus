import { Text, View } from 'react-native';

import type { ChartSeries } from '@/lib/chart';
import { useChartColors } from '@/theme/chartColors';

/** Identity channel for 2+ series: line key + name in text ink (never the series color). */
export function Legend({ series, slots }: { series: ChartSeries[]; slots: number[] }) {
  const c = useChartColors();
  if (series.length < 2) return null; // a single series is named by the panel title
  return (
    <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 12, paddingTop: 6 }} accessibilityRole="list">
      {series.map((s, i) => (
        <View key={s.key} style={{ flexDirection: 'row', alignItems: 'center', gap: 6, maxWidth: 220 }} accessibilityRole="text">
          <View style={{ width: 14, height: 2, borderRadius: 1, backgroundColor: c.categorical[slots[i] % 8] }} />
          <Text numberOfLines={1} style={{ color: c.inkSecondary, fontSize: 12, flexShrink: 1 }}>
            {s.label}
          </Text>
        </View>
      ))}
    </View>
  );
}
