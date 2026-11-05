import { useMemo, useState } from 'react';
import { Platform, Text, View, type LayoutChangeEvent } from 'react-native';
import Svg, { Circle, Line, Path, Text as SvgText } from 'react-native-svg';

import {
  formatFullTime,
  formatTick,
  formatValue,
  nearestIndex,
  niceAxis,
  peak,
  timeTicks,
  unionTimes,
  valueAt,
  type ChartSeries,
} from '@/lib/chart';
import { useChartColors } from '@/theme/chartColors';

import { HoverSurface } from './HoverSurface';

const M = { left: 44, right: 14, top: 10, bottom: 22 };
// SVG text does not inherit the app font on web; use the system sans (native keeps its default).
const FONT = Platform.select({ web: 'system-ui, -apple-system, "Segoe UI", sans-serif', default: undefined });

export interface TimeseriesChartProps {
  series: ChartSeries[];
  /** Categorical color slot per series (fixed order, see ColorAssigner). */
  slots: number[];
  startMs: number;
  endMs: number;
  stepMs: number;
  height?: number;
  /** Value formatter for axis and tooltip (defaults to compact numbers). */
  format?: (v: number) => string;
}

/** Builds line paths, breaking the line wherever steps are missing (topk gaps). */
export function linePaths(
  points: [number, number][],
  x: (t: number) => number,
  y: (v: number) => number,
  stepMs: number,
): string[] {
  const paths: string[] = [];
  let cur = '';
  let prevT = -Infinity;
  for (const [t, v] of points) {
    const seg = `${x(t).toFixed(1)} ${y(v).toFixed(1)}`;
    if (!cur || t - prevT > stepMs * 1.5) {
      if (cur) paths.push(cur);
      cur = `M ${seg}`;
    } else {
      cur += ` L ${seg}`;
    }
    prevT = t;
  }
  if (cur) paths.push(cur);
  return paths;
}

/**
 * Line chart on one zero-based axis. 2px lines, hairline grid, one tooltip listing every
 * series at the hovered time, end dots with a surface ring, area wash for a single series.
 */
export function TimeseriesChart({ series, slots, startMs, endMs, stepMs, height = 200, format = formatValue }: TimeseriesChartProps) {
  const c = useChartColors();
  const [width, setWidth] = useState(0);
  const [hoverX, setHoverX] = useState<number | null>(null);

  const plotW = Math.max(0, width - M.left - M.right);
  const plotH = height - M.top - M.bottom;
  const axis = useMemo(() => niceAxis(Math.max(0, ...series.map(peak))), [series]);
  const times = useMemo(() => unionTimes(series), [series]);

  const x = (t: number) => M.left + (endMs > startMs ? ((t - startMs) / (endMs - startMs)) * plotW : 0);
  const y = (v: number) => M.top + plotH - (v / axis.max) * plotH;

  const hoverIdx = hoverX === null || times.length === 0 ? -1 : nearestIndex(times.map(x), hoverX + 0);
  const hoverT = hoverIdx >= 0 ? times[hoverIdx] : null;

  const onLayout = (e: LayoutChangeEvent) => setWidth(Math.floor(e.nativeEvent.layout.width));
  const single = series.length === 1;

  const rows =
    hoverT === null
      ? []
      : series
          .map((s, i) => ({ s, i, v: valueAt(s, hoverT) }))
          .filter((r) => r.v !== null)
          .sort((a, b) => (b.v as number) - (a.v as number))
          .slice(0, 8);

  const tipRight = hoverT !== null && x(hoverT) < M.left + plotW / 2;

  return (
    <View onLayout={onLayout} style={{ width: '100%', height }} accessible accessibilityLabel={`Line chart with ${series.length} series`}>
      {width > 0 && (
        <>
          <Svg width={width} height={height}>
            {axis.ticks.map((tv) => (
              <Line key={`g${tv}`} x1={M.left} x2={M.left + plotW} y1={y(tv)} y2={y(tv)} stroke={tv === 0 ? c.baseline : c.grid} strokeWidth={1} />
            ))}
            {axis.ticks.map((tv) => (
              <SvgText key={`l${tv}`} fontFamily={FONT} x={M.left - 6} y={y(tv) + 4} fontSize={11} fill={c.inkMuted} textAnchor="end">
                {format(tv)}
              </SvgText>
            ))}
            {timeTicks(startMs, endMs, plotW > 420 ? 5 : 3).map((t, i, arr) => (
              <SvgText
                fontFamily={FONT}
                key={`x${t}`}
                x={x(t)}
                y={height - 6}
                fontSize={11}
                fill={c.inkMuted}
                textAnchor={i === 0 ? 'start' : i === arr.length - 1 ? 'end' : 'middle'}>
                {formatTick(t, endMs - startMs)}
              </SvgText>
            ))}

            {single && series[0].points.length > 1 && (
              <Path
                d={`${linePaths(series[0].points, x, y, stepMs).join(' ')} L ${x(series[0].points[series[0].points.length - 1][0]).toFixed(1)} ${y(0)} L ${x(series[0].points[0][0]).toFixed(1)} ${y(0)} Z`}
                fill={c.categorical[slots[0] % 8]}
                fillOpacity={0.1}
              />
            )}
            {series.map((s, i) =>
              linePaths(s.points, x, y, stepMs).map((d, k) => (
                <Path key={`${s.key}-${k}`} d={d} stroke={c.categorical[slots[i] % 8]} strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" fill="none" />
              )),
            )}
            {series.map((s, i) => {
              const last = s.points[s.points.length - 1];
              if (!last) return null;
              return <Circle key={`e${s.key}`} cx={x(last[0])} cy={y(last[1])} r={4} fill={c.categorical[slots[i] % 8]} stroke={c.surface} strokeWidth={2} />;
            })}

            {hoverT !== null && (
              <>
                <Line x1={x(hoverT)} x2={x(hoverT)} y1={M.top} y2={M.top + plotH} stroke={c.inkMuted} strokeWidth={1} />
                {rows.map((r) => (
                  <Circle key={`h${r.s.key}`} cx={x(hoverT)} cy={y(r.v as number)} r={4} fill={c.categorical[slots[r.i] % 8]} stroke={c.surface} strokeWidth={2} />
                ))}
              </>
            )}
          </Svg>

          <HoverSurface width={width} height={plotH + M.top} onMove={setHoverX} />

          {hoverT !== null && rows.length > 0 && (
            <View
              pointerEvents="none"
              style={{
                position: 'absolute',
                top: M.top,
                ...(tipRight ? { left: x(hoverT) + 12 } : { right: width - x(hoverT) + 12 }),
                backgroundColor: c.surface,
                borderColor: c.border,
                borderWidth: 1,
                borderRadius: 8,
                paddingVertical: 6,
                paddingHorizontal: 10,
                gap: 3,
                maxWidth: 260,
              }}>
              <Text style={{ color: c.inkSecondary, fontSize: 11 }}>{formatFullTime(hoverT)}</Text>
              {rows.map((r) => (
                <View key={r.s.key} style={{ flexDirection: 'row', alignItems: 'center', gap: 6 }}>
                  <View style={{ width: 10, height: 2, backgroundColor: c.categorical[slots[r.i] % 8] }} />
                  <Text style={{ color: c.ink, fontSize: 12, fontWeight: '700' }}>{format(r.v as number)}</Text>
                  <Text numberOfLines={1} style={{ color: c.inkSecondary, fontSize: 11, flexShrink: 1 }}>
                    {r.s.label}
                  </Text>
                </View>
              ))}
            </View>
          )}
        </>
      )}
    </View>
  );
}
