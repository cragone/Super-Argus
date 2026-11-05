import { Text, View } from 'react-native';

import { formatValue, lastValue, type ChartSeries } from '@/lib/chart';
import { useChartColors } from '@/theme/chartColors';

export interface BarItem {
  key: string;
  label: string;
  value: number;
}

/** Top-N items from series: the newest value of each, largest first. */
export function barItems(series: ChartSeries[], max = 10): BarItem[] {
  return series
    .map((s) => ({ key: s.key, label: s.label, value: lastValue(s) }))
    .filter((i) => i.value > 0)
    .sort((a, b) => b.value - a.value || (a.label < b.label ? -1 : 1))
    .slice(0, max);
}

/**
 * Ranked horizontal bars, largest first, one hue: bars share one baseline, so close
 * values stay comparable. 16px thick, square at the baseline, 4px rounded at the tip, value at the tip.
 */
export function BarList({ items, format = formatValue }: { items: BarItem[]; format?: (v: number) => string }) {
  const c = useChartColors();
  const max = Math.max(...items.map((i) => i.value), 0);
  return (
    <View style={{ gap: 10 }} accessibilityRole="list">
      {items.map((it) => (
        <View key={it.key} accessibilityRole="text" accessibilityLabel={`${it.label}: ${format(it.value)}`}>
          <Text numberOfLines={1} style={{ color: c.inkSecondary, fontSize: 12, marginBottom: 3 }}>
            {it.label}
          </Text>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: 8 }}>
            <View style={{ flex: 1, height: 16, justifyContent: 'center' }}>
              <View
                style={{
                  width: `${max > 0 ? Math.max(2, (it.value / max) * 100) : 0}%`,
                  height: 16,
                  backgroundColor: c.sequential,
                  borderTopRightRadius: 4,
                  borderBottomRightRadius: 4,
                }}
              />
            </View>
            <Text style={{ color: c.ink, fontSize: 12, fontWeight: '700', minWidth: 44, textAlign: 'right' }}>{format(it.value)}</Text>
          </View>
        </View>
      ))}
    </View>
  );
}
