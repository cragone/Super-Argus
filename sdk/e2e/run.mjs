// End-to-end proof that an application logging through an Argus SDK is not
// affected when Argus goes down. For one SDK it:
//
//   1. builds and starts the real argus server on a temp data directory,
//   2. starts a sample app that logs ~2,000 lines/s through the SDK,
//   3. hard-kills the server mid-run, leaves it down, then restarts it on the
//      same data (so the WAL is recovered),
//   4. compares what the app logged / believes was sent / dropped with what
//      Argus actually stored, and how long the app's log calls took in each phase.
//
//   node sdk/e2e/run.mjs go|node [--duration 28] [--down-at 6] [--up-at 16]
//
// Needs Go on PATH; the node variant also needs `npm install` in sdk/node.
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, mkdirSync, existsSync } from 'node:fs';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { createInterface } from 'node:readline';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const root = resolve(here, '../..');
const exe = process.platform === 'win32' ? '.exe' : '';

const args = process.argv.slice(2);
const sdk = args[0];
if (sdk !== 'go' && sdk !== 'node') {
  console.error('usage: node sdk/e2e/run.mjs go|node [--duration S] [--down-at S] [--up-at S]');
  process.exit(2);
}
const opt = (name, def) => {
  const i = args.indexOf(`--${name}`);
  return i >= 0 ? Number(args[i + 1]) : def;
};
const duration = opt('duration', 28);
const downAt = opt('down-at', 6);
const upAt = opt('up-at', 16);
const rate = opt('rate', 2000);
const MAX_CALL_US = 100_000; // "no app impact": no single log call may take longer than 100 ms

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const work = mkdtempSync(join(tmpdir(), 'argus-e2e-'));

function freePort() {
  return new Promise((res) => {
    const s = createServer().listen(0, '127.0.0.1', () => {
      const { port } = s.address();
      s.close(() => res(port));
    });
  });
}

async function main() {
  const port = await freePort();
  const base = `http://127.0.0.1:${port}`;
  const bin = join(work, `argus${exe}`);
  console.log(`building argus server...`);
  execFileSync('go', ['build', '-o', bin, './cmd/argus'], { cwd: root, stdio: 'inherit' });

  const dataDir = join(work, 'data');
  mkdirSync(dataDir);
  const env = {
    ...process.env, ARGUS_DATA_DIR: dataDir, ARGUS_HTTP_ADDR: `127.0.0.1:${port}`, ARGUS_JWT_SECRET: 'e2e-secret',
    ARGUS_ADMIN_EMAIL: 'admin@e2e.test', ARGUS_ADMIN_PASSWORD: 'e2e-password-123',
  };
  let server;
  const startServer = async () => {
    server = spawn(bin, [], { env, stdio: 'ignore' });
    for (let i = 0; i < 100; i++) {
      try {
        if ((await fetch(`${base}/readyz`)).ok) return;
      } catch { /* not listening yet */ }
      await sleep(100);
    }
    throw new Error('argus did not become ready');
  };

  await startServer();
  const login = await (await fetch(`${base}/api/v1/auth/login`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email: env.ARGUS_ADMIN_EMAIL, password: env.ARGUS_ADMIN_PASSWORD }),
  })).json();
  const bearer = { Authorization: `Bearer ${login.access_token}`, 'Content-Type': 'application/json' };
  const { key } = await (await fetch(`${base}/api/v1/api-keys`, { method: 'POST', headers: bearer, body: JSON.stringify({ name: `e2e-${sdk}` }) })).json();
  if (!key) throw new Error('could not create an API key');

  // Build and start the sample app for the chosen SDK.
  const label = `e2e-${sdk}`;
  let cmd, cmdArgs, cwd;
  if (sdk === 'go') {
    cwd = join(root, 'sdk/go');
    const sample = join(work, `sample${exe}`);
    console.log('building Go sample app...');
    execFileSync('go', ['build', '-o', sample, './examples/sample'], { cwd, stdio: 'inherit' });
    [cmd, cmdArgs] = [sample, []];
  } else {
    cwd = join(root, 'sdk/node');
    if (!existsSync(join(cwd, 'node_modules'))) throw new Error('run `npm install` in sdk/node first');
    console.log('building Node sample app...');
    execFileSync(process.execPath, [join(cwd, 'node_modules/typescript/bin/tsc'), '-p', 'tsconfig.json'], { cwd, stdio: 'inherit' });
    [cmd, cmdArgs] = [process.execPath, ['dist/examples/sample-app.js']];
  }
  const records = [];
  const app = spawn(cmd, cmdArgs, {
    cwd, stdio: ['ignore', 'pipe', 'inherit'],
    env: { ...process.env, ARGUS_URL: base, ARGUS_KEY: key, SERVICE_LABEL: label, DURATION_S: String(duration), RATE: String(rate) },
  });
  createInterface({ input: app.stdout }).on('line', (l) => {
    try { records.push(JSON.parse(l)); } catch { /* ignore stray output */ }
  });
  const appExit = new Promise((res) => app.on('exit', res));
  const t0 = Date.now();
  console.log(`sample app (${sdk}) running for ${duration}s at ~${rate} lines/s`);

  await sleep(downAt * 1000);
  server.kill('SIGKILL'); // a crash, not a clean shutdown
  const killedAt = Date.now();
  console.log(`[${((killedAt - t0) / 1000).toFixed(1)}s] argus killed`);
  await sleep(Math.max(0, (upAt - downAt) * 1000));
  await startServer();
  const upAtMs = Date.now();
  console.log(`[${((upAtMs - t0) / 1000).toFixed(1)}s] argus restarted on the same data`);

  await appExit;
  await sleep(1500); // let the last batches land
  const final = records.find((r) => r.final);
  if (!final) throw new Error('the sample app did not finish cleanly');

  // What does Argus actually hold for this service?
  // Metric ranges are aligned down to the step, so end an hour ahead (the aligned end is then
  // at or after "now") and use a 2h window so the last point counts every line stored.
  const end = Date.now() + 3600_000;
  const q = new URLSearchParams({
    query: `sum(count_over_time({service="${label}"}[2h]))`, start: String((end - 3 * 3600_000) * 1e6), end: String(end * 1e6), step: '3600s',
  });
  const metricRes = await fetch(`${base}/api/v1/query/metric?${q}`, { headers: bearer });
  const metric = await metricRes.json();
  if (!metricRes.ok || !metric.series?.length) console.log('stored-count query returned:', metricRes.status, JSON.stringify(metric).slice(0, 400));
  const pts = metric.series?.[0]?.values ?? [];
  const stored = pts.length ? Math.max(...pts.map((p) => p[1])) : 0;

  const phase = (from, to) => {
    const rs = records.filter((r) => !r.final && r.t_ms > from && r.t_ms <= to);
    return {
      seconds: rs.length, calls: rs.reduce((a, r) => a + r.calls, 0),
      p99_us: Math.max(0, ...rs.map((r) => r.p99_us)), max_us: Math.max(0, ...rs.map((r) => r.max_us)),
    };
  };
  const phases = { 'argus up': phase(0, killedAt), 'argus DOWN': phase(killedAt, upAtMs), 'argus recovered': phase(upAtMs, Infinity) };

  console.log(`\n=== ${sdk} SDK: log-call latency by phase (worst second in each) ===`);
  console.log('phase'.padEnd(18), 'seconds'.padStart(8), 'calls'.padStart(9), 'worst p99'.padStart(11), 'worst max'.padStart(11));
  for (const [name, p] of Object.entries(phases)) {
    console.log(name.padEnd(18), String(p.seconds).padStart(8), String(p.calls).padStart(9), `${(p.p99_us / 1000).toFixed(2)}ms`.padStart(11), `${(p.max_us / 1000).toFixed(2)}ms`.padStart(11));
  }
  console.log(`\nlines the app logged:            ${final.logged}`);
  console.log(`acknowledged by Argus (sent):    ${final.sent}`);
  console.log(`dropped by the SDK:              ${final.dropped}`);
  console.log(`stored in Argus:                 ${stored}`);

  const worst = Math.max(...Object.values(phases).map((p) => p.max_us));
  const checks = [
    [`no log call took longer than ${MAX_CALL_US / 1000}ms (worst ${(worst / 1000).toFixed(2)}ms)`, worst < MAX_CALL_US],
    ['every logged line is accounted for (sent + dropped = logged)', final.sent + final.dropped === final.logged],
    ['some lines were really dropped during the outage (the test exercised it)', final.dropped > 0],
    ['logs flowed again after the restart', final.sent > 0 && phases['argus recovered'].calls > 0 && stored > 0],
    ['nothing Argus acknowledged was lost across the crash (stored >= sent)', stored >= final.sent],
  ];
  console.log('');
  let ok = true;
  for (const [what, pass] of checks) {
    console.log(`${pass ? 'PASS' : 'FAIL'}  ${what}`);
    ok &&= pass;
  }
  server.kill();
  return ok;
}

let ok = false;
try {
  ok = await main();
} catch (e) {
  console.error(e);
}
process.exit(ok ? 0 : 1);
