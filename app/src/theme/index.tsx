import { useColorScheme } from 'react-native';

export interface Palette {
  bg: string;
  surface: string;
  surfaceAlt: string;
  border: string;
  text: string;
  textDim: string;
  accent: string;
  accentText: string;
  danger: string;
  levels: { error: string; warn: string; info: string; debug: string };
}

export const light: Palette = {
  bg: '#f6f7f9',
  surface: '#ffffff',
  surfaceAlt: '#eef0f4',
  border: '#d9dde5',
  text: '#14171f',
  textDim: '#5d6678',
  accent: '#2f5bea',
  accentText: '#ffffff',
  danger: '#c62828',
  levels: { error: '#d32f2f', warn: '#ed8f03', info: '#2f7d4f', debug: '#8a93a6' },
};

export const dark: Palette = {
  bg: '#0e1117',
  surface: '#161b24',
  surfaceAlt: '#1f2632',
  border: '#2a3342',
  text: '#e6e9f0',
  textDim: '#93a0b5',
  accent: '#6d8dff',
  accentText: '#0b1020',
  danger: '#ff6b6b',
  levels: { error: '#ff6b6b', warn: '#ffb454', info: '#5fd18b', debug: '#7b8699' },
};

/** Palette for the current system appearance. */
export function useTheme(): Palette {
  return useColorScheme() === 'dark' ? dark : light;
}

export const mono = { fontFamily: 'Menlo, Consolas, "Roboto Mono", monospace' } as const;
