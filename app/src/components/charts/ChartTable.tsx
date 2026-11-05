import { ScrollView, Text, View } from 'react-native';

import { formatFullTime, formatValue, unionTimes, valueAt, type ChartSeries } from '@/lib/chart';
import { useChartColors } from '@/theme/chartColors';

/** The table view of a chart: every value is reachable without hover. Newest rows first. */
export function ChartTable({ series, rows = 30, format = formatValue }: { series: ChartSeries[]; rows?: number; format?: (v: number) => string }) {
  const c = useChartColors();
  const times = unionTimes(series).slice(-rows).reverse();
  const cols = series.slice(0, 8);
  return (
    <ScrollView horizontal showsHorizontalScrollIndicator>
      <View accessibilityRole="summary" accessibilityLabel="Data table">
        <View style={{ flexDirection: 'row', borderBottomWidth: 1, borderBottomColor: c.grid }}>
          <Text style={{ width: 150, color: c.inkSecondary, fontSize: 11, paddingVertical: 4 }}>Time</Text>
          {cols.map((s) => (
            <Text key={s.key} numberOfLines={1} style={{ width: 90, color: c.inkSecondary, fontSize: 11, paddingVertical: 4, textAlign: 'right' }}>
              {s.label}
            </Text>
          ))}
        </View>
        {times.map((t) => (
          <View key={t} style={{ flexDirection: 'row', borderBottomWidth: 1, borderBottomColor: c.grid }}>
            <Text style={{ width: 150, color: c.inkSecondary, fontSize: 11, paddingVertical: 3 }}>{formatFullTime(t)}</Text>
            {cols.map((s) => {
              const v = valueAt(s, t);
              return (
                <Text key={s.key} style={{ width: 90, color: c.ink, fontSize: 11, paddingVertical: 3, textAlign: 'right', fontVariant: ['tabular-nums'] }}>
                  {v === null ? '' : format(v)}
                </Text>
              );
            })}
          </View>
        ))}
      </View>
    </ScrollView>
  );
}
