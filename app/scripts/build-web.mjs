// Exports the web build and copies it into internal/webui/dist so `go build` embeds it.
import { execSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const appDir = resolve(here, '..');
const out = join(appDir, '.web-export');
const dest = resolve(appDir, '..', 'internal', 'webui', 'dist');

rmSync(out, { recursive: true, force: true });
execSync(`npx expo export --platform web --output-dir "${out}"`, { cwd: appDir, stdio: 'inherit' });

mkdirSync(dest, { recursive: true });
for (const name of readdirSync(dest)) {
  if (name !== '.gitkeep') rmSync(join(dest, name), { recursive: true, force: true });
}
cpSync(out, dest, { recursive: true });
rmSync(out, { recursive: true, force: true });
// Use the owl favicon from assets/ instead of the exporter's generated .ico.
cpSync(join(appDir, 'assets', 'favicon.png'), join(dest, 'favicon.png'));
const indexPath = join(dest, 'index.html');
writeFileSync(indexPath, readFileSync(indexPath, 'utf8').replace(/<link rel="icon"[^>]*>/, '<link rel="icon" type="image/png" href="/favicon.png"/>'));
if (!existsSync(join(dest, 'index.html'))) throw new Error('export produced no index.html');
console.log(`web UI written to ${dest}`);
