/**
 * A stand-in application that logs through the Argus Node SDK at a steady rate
 * and reports, once a second, how long its log calls took. sdk/e2e/run.mjs uses
 * it to show that Argus going down does not affect an app.
 *
 *   ARGUS_URL=http://localhost:8080 ARGUS_KEY=... DURATION_S=20 RATE=2000 npm run sample
 */
import { ArgusLogger } from '../src/index.js';

const url = process.env.ARGUS_URL;
const apiKey = process.env.ARGUS_KEY;
if (!url || !apiKey) {
  console.error('set ARGUS_URL and ARGUS_KEY');
  process.exit(2);
}
const intEnv = (k: string, def: number): number => {
  const n = Number(process.env[k]);
  return Number.isFinite(n) && n > 0 ? n : def;
};

const log = new ArgusLogger({
  url, apiKey, labels: { service: process.env.SERVICE_LABEL ?? 'sample', sdk: 'node' },
  flushIntervalMs: 200, backoffMs: 200, maxRetries: 2, timeoutMs: 3000,
  onError: () => undefined, // the harness reads the counters; keep stdout clean
});

const durationMs = intEnv('DURATION_S', 20) * 1000;
const perTick = Math.floor(intEnv('RATE', 2000) / 100);
let logged = 0;
let window: number[] = [];

function emit(final: boolean): void {
  window.sort((a, b) => a - b);
  const n = window.length;
  const st = log.stats();
  console.log(JSON.stringify({
    t_ms: Date.now(), calls: n,
    p99_us: n ? Math.round((window[Math.floor(n * 0.99)] ?? 0) * 1000) : 0,
    max_us: n ? Math.round((window[n - 1] ?? 0) * 1000) : 0,
    logged, sent: st.sent, dropped: st.dropped, ...(final ? { final: true } : {}),
  }));
  window = [];
}

const start = Date.now();
const tick = setInterval(() => {
  for (let i = 0; i < perTick; i++) {
    const t0 = performance.now();
    log.info('request handled', { n: logged, path: `/api/v1/things/${logged % 97}` });
    window.push(performance.now() - t0);
    logged++;
  }
}, 10);
const report = setInterval(() => emit(false), 1000);

setTimeout(async () => {
  clearInterval(tick);
  clearInterval(report);
  await log.close(15_000);
  emit(true);
}, durationMs);

void start;
