import { View, type LayoutChangeEvent } from 'react-native';

export interface HoverSurfaceProps {
  width: number;
  height: number;
  /** x in px relative to the surface's left edge, or null when the pointer leaves. */
  onMove(x: number | null): void;
}

/**
 * Transparent layer that reports the pointer x position: press and drag on touch
 * devices. (The web build replaces this file with real mouse hover.)
 */
export function HoverSurface({ width, height, onMove }: HoverSurfaceProps) {
  return (
    <View
      style={{ position: 'absolute', left: 0, top: 0, width, height }}
      onLayout={(_: LayoutChangeEvent) => {}}
      onStartShouldSetResponder={() => true}
      onMoveShouldSetResponder={() => true}
      onResponderGrant={(e) => onMove(e.nativeEvent.locationX)}
      onResponderMove={(e) => onMove(e.nativeEvent.locationX)}
      onResponderRelease={() => onMove(null)}
      onResponderTerminate={() => onMove(null)}
    />
  );
}
