import { useEffect, useRef } from 'react';
import { Image, Platform, StyleSheet, View } from 'react-native';

// Where the pupil sits in owl-eye.jpg (643x360), as a fraction of the image.
// The picture is positioned so this point lands in the middle of the screen.
const PUPIL_X = 0.49;
const PUPIL_Y = 0.435;
const RAMP = ' .\'`^",:;-~+=*abOX#%@';
const FONT = 'ui-monospace, "Cascadia Mono", Menlo, Consolas, "DejaVu Sans Mono", monospace';

function draw(canvas: HTMLCanvasElement, img: HTMLImageElement, w: number, h: number) {
  const dpr = Math.min(window.devicePixelRatio || 1, 2);
  canvas.width = Math.round(w * dpr);
  canvas.height = Math.round(h * dpr);
  canvas.style.width = `${w}px`;
  canvas.style.height = `${h}px`;
  const ctx = canvas.getContext('2d');
  if (!ctx) return;
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);

  // Cell size scales with the screen so phones and big monitors both get a readable grid.
  const fontSize = Math.max(8, Math.min(14, Math.round(Math.min(w, h) / 55)));
  ctx.font = `${fontSize}px ${FONT}`;
  const cw = ctx.measureText('M').width || fontSize * 0.6;
  const ch = Math.round(fontSize * 1.05);
  const cols = Math.ceil(w / cw);
  const rows = Math.ceil(h / ch);

  // Scale so the image still covers the screen with the pupil exactly in the centre.
  const iw = img.naturalWidth;
  const ih = img.naturalHeight;
  const px = PUPIL_X * iw;
  const py = PUPIL_Y * ih;
  const s = Math.max(w / 2 / px, w / 2 / (iw - px), h / 2 / py, h / 2 / (ih - py));

  // Average the picture down to one pixel per character cell.
  const small = document.createElement('canvas');
  small.width = cols;
  small.height = rows;
  const sctx = small.getContext('2d', { willReadFrequently: true });
  if (!sctx) return;
  sctx.imageSmoothingQuality = 'high';
  const sx = s / cw;
  const sy = s / ch;
  sctx.setTransform(sx, 0, 0, sy, cols / 2 - px * sx, rows / 2 - py * sy);
  sctx.drawImage(img, 0, 0);
  const data = sctx.getImageData(0, 0, cols, rows).data;

  ctx.fillStyle = '#07060a';
  ctx.fillRect(0, 0, w, h);
  ctx.textBaseline = 'top';
  for (let r = 0; r < rows; r++) {
    for (let c = 0; c < cols; c++) {
      const i = (r * cols + c) * 4;
      const R = data[i] ?? 0;
      const G = data[i + 1] ?? 0;
      const B = data[i + 2] ?? 0;
      const luma = (0.2126 * R + 0.7152 * G + 0.0722 * B) / 255;
      if (luma < 0.04) continue; // pure shadow (and the pupil) stays empty
      const lift = 1.35; // brighten so the colour survives thin glyphs
      ctx.fillStyle = `rgb(${Math.min(255, R * lift)},${Math.min(255, G * lift)},${Math.min(255, B * lift)})`;
      ctx.fillText(RAMP[Math.min(RAMP.length - 1, Math.floor(Math.sqrt(luma) * RAMP.length))], c * cw, r * ch);
    }
  }
}

/** Full-screen colour ASCII rendering of the owl eye, web only. Sits behind its siblings. */
export function AsciiOwl() {
  const host = useRef<unknown>(null);

  useEffect(() => {
    if (Platform.OS !== 'web') return;
    const el = host.current as unknown as HTMLElement | null;
    if (!el) return;
    const canvas = document.createElement('canvas');
    el.appendChild(canvas);
    const img = new window.Image();
    let timer: ReturnType<typeof setTimeout> | undefined;
    const render = () => {
      if (img.complete && img.naturalWidth) draw(canvas, img, el.clientWidth, el.clientHeight);
    };
    const onResize = () => {
      clearTimeout(timer);
      timer = setTimeout(render, 80);
    };
    img.onload = render;
    img.src = Image.resolveAssetSource(require('../../assets/owl-eye.jpg'))?.uri ?? '';
    window.addEventListener('resize', onResize);
    return () => {
      clearTimeout(timer);
      window.removeEventListener('resize', onResize);
      canvas.remove();
    };
  }, []);

  if (Platform.OS !== 'web') return null;
  return <View ref={host as never} pointerEvents="none" style={[StyleSheet.absoluteFill, { backgroundColor: '#07060a' }]} />;
}
