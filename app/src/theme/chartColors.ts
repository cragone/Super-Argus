import { useColorScheme } from 'react-native';

/**
 * Chart colors: the validated reference palette from the dataviz skill.
 * Categorical slots are assigned in fixed order and never cycled; the dark
 * column is the same eight hues stepped for the dark surface.
 */
export interface ChartColors {
  surface: string;
  ink: string; // primary text
  inkSecondary: string;
  inkMuted: string; // axis labels
  grid: string; // hairline gridlines
  baseline: string;
  border: string;
  categorical: string[]; // 8 slots
  sequential: string; // one-hue ranked bars
  deEmphasis: string; // context marks next to an accent
}

const LIGHT: ChartColors = {
  surface: '#fcfcfb',
  ink: '#0b0b0b',
  inkSecondary: '#52514e',
  inkMuted: '#898781',
  grid: '#e1e0d9',
  baseline: '#c3c2b7',
  border: 'rgba(11,11,11,0.10)',
  categorical: ['#2a78d6', '#eb6834', '#1baf7a', '#eda100', '#e87ba4', '#008300', '#6250d6', '#e34948'],
  sequential: '#2a78d6',
  deEmphasis: '#c3c2b7',
};

const DARK: ChartColors = {
  surface: '#1a1a19',
  ink: '#f0efec',
  inkSecondary: '#c3c2b7',
  inkMuted: '#898781',
  grid: '#2c2c2a',
  baseline: '#383835',
  border: 'rgba(255,255,255,0.10)',
  categorical: ['#3987e5', '#d95926', '#199e70', '#c98500', '#d55181', '#008300', '#9085e9', '#e66767'],
  sequential: '#3987e5',
  deEmphasis: '#4a4a46',
};

export const chartPalettes = { light: LIGHT, dark: DARK };

export function useChartColors(): ChartColors {
  return useColorScheme() === 'dark' ? DARK : LIGHT;
}

/**
 * Keeps a series on the same color between refreshes: a key keeps its slot,
 * new keys take the lowest free slot, and a changing series count never repaints survivors.
 */
export class ColorAssigner {
  private slots = new Map<string, number>();

  slot(key: string, used: Set<number> = new Set(this.slots.values())): number {
    let s = this.slots.get(key);
    if (s === undefined) {
      s = 0;
      while (used.has(s)) s++;
      this.slots.set(key, s);
    }
    return s % 8;
  }

  /** Slots for a list of keys, in order. */
  assign(keys: string[]): number[] {
    // forget keys that left, so their slot can be reused
    const present = new Set(keys);
    for (const k of [...this.slots.keys()]) if (!present.has(k)) this.slots.delete(k);
    return keys.map((k) => this.slot(k));
  }
}
