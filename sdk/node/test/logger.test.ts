import assert from 'node:assert/strict';
import { createServer, type IncomingMessage, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { after, describe, it } from 'node:test';
import { gunzipSync } from 'node:zlib';

import { ArgusLogger, type ArgusOptions } from '../src/index.js';

interface Entry {
  ts: string;
  labels: Record<string, string>;
  line: string;
}

/** A fake Argus that records pushes. `status` scripts failures by call number. */
class FakeArgus {
  entries: Entry[] = [];
  keys: string[] = [];
  encodings: string[] = [];
  calls = 0;
  status: (call: number) => { code: number; retryAfter?: number } = () => ({ code: 200 });
  readonly server: Server;
  url = '';

  constructor() {
    this.server = createServer((req, res) => void this.handle(req, res));
  }

  async start(): Promise<this> {
    await new Promise<void>((r) => this.server.listen(0, '127.0.0.1', r));
    this.url = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}/`;
    return this;
  }

  private async handle(req: IncomingMessage, res: import('node:http').ServerResponse): Promise<void> {
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    const call = ++this.calls;
    const s = this.status(call);
    if (s.code !== 200) {
      if (s.retryAfter) res.setHeader('Retry-After', String(s.retryAfter));
      res.writeHead(s.code).end();
      return;
    }
    this.keys.push(String(req.headers['x-argus-key']));
    this.encodings.push(String(req.headers['content-encoding']));
    const lines = gunzipSync(Buffer.concat(chunks)).toString().split('\n').filter(Boolean);
    const batch = lines.map((l) => JSON.parse(l) as Entry);
    this.entries.push(...batch);
    res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ accepted: batch.length, rejected: 0 }));
  }

  lines(): string[] {
    return this.entries.map((e) => e.line);
  }

  stop(): Promise<void> {
    this.server.closeAllConnections();
    return new Promise((r) => this.server.close(() => r()));
  }
}

const servers: FakeArgus[] = [];
after(async () => {
  await Promise.all(servers.map((s) => s.stop()));
});

async function fake(): Promise<FakeArgus> {
  const f = await new FakeArgus().start();
  servers.push(f);
  return f;
}

function opts(f: FakeArgus, extra: Partial<ArgusOptions> = {}): ArgusOptions {
  return {
    url: f.url, apiKey: 'argus_test', labels: { service: 'node-test' }, batchSize: 3,
    flushIntervalMs: 20, backoffMs: 1, onError: () => undefined, ...extra,
  };
}

async function eventually(cond: () => boolean, ms = 3000): Promise<void> {
  const end = Date.now() + ms;
  while (!cond()) {
    if (Date.now() > end) assert.fail('condition never became true');
    await new Promise((r) => setTimeout(r, 5));
  }
}

describe('shipping', () => {
  it('sends gzipped NDJSON with the key and labels, honouring the batch size', async () => {
    const f = await fake();
    const log = new ArgusLogger(opts(f));
    for (const l of ['a', 'b', 'c', 'd']) log.send(l);
    await log.close();
    assert.deepEqual(f.lines(), ['a', 'b', 'c', 'd']);
    assert.equal(f.keys[0], 'argus_test');
    assert.equal(f.encodings[0], 'gzip');
    assert.deepEqual(f.entries[0]?.labels, { service: 'node-test' });
    assert.ok(!Number.isNaN(Date.parse(f.entries[0]?.ts ?? '')));
    assert.deepEqual(log.stats(), { sent: 4, dropped: 0, batches: 2, queued: 0 });
  });

  it('flushes a partial batch on the timer', async () => {
    const f = await fake();
    const log = new ArgusLogger(opts(f));
    log.send('lonely');
    await eventually(() => f.lines().length === 1);
    await log.close();
  });

  it('writes structured lines with a level, message and fields; reserved keys win; errors serialise', async () => {
    const f = await fake();
    const log = new ArgusLogger(opts(f, { level: 'info' }));
    log.debug('below the level');
    log.error('boom', { status: 500, level: 'spoofed', err: new Error('bad thing'), big: 10n });
    await log.close();
    assert.equal(f.lines().length, 1, 'debug is filtered');
    const m = JSON.parse(f.lines()[0] ?? '{}') as Record<string, unknown>;
    assert.equal(m.level, 'error');
    assert.equal(m.msg, 'boom');
    assert.equal(m.status, 500);
    assert.equal(m.big, '10');
    assert.equal((m.err as { message: string }).message, 'bad thing');
    assert.ok(typeof m.time === 'string');
  });

  it('truncates long lines and copies the labels object', async () => {
    const f = await fake();
    const labels = { service: 'a' };
    const log = new ArgusLogger(opts(f, { maxLineBytes: 16, labels }));
    labels.service = 'mutated';
    log.send('0123456789ABCDEFGHIJ');
    await log.close();
    assert.equal(f.lines()[0], '0123456789ABCDEF...[truncated]');
    assert.deepEqual(f.entries[0]?.labels, { service: 'a' });
  });
});

describe('failure handling', () => {
  it('retries transient failures and delivers once', async () => {
    const f = await fake();
    f.status = (call) => (call <= 2 ? { code: 503 } : { code: 200 });
    const log = new ArgusLogger(opts(f));
    log.send('survives');
    await log.close();
    assert.deepEqual(f.lines(), ['survives']);
    assert.equal(f.calls, 3);
    assert.equal(log.stats().dropped, 0);
  });

  it('retries 429 but not a bad key', async () => {
    const f = await fake();
    f.status = (call) => (call === 1 ? { code: 429 } : { code: 200 });
    const log = new ArgusLogger(opts(f));
    log.send('x');
    await log.close();
    assert.deepEqual(f.lines(), ['x']);

    const bad = await fake();
    bad.status = () => ({ code: 401 });
    const errors: string[] = [];
    const log2 = new ArgusLogger(opts(bad, { onError: (m) => errors.push(m) }));
    log2.send('y');
    await log2.close();
    assert.equal(bad.calls, 1, '401 is not retried');
    assert.equal(log2.stats().dropped, 1);
    assert.equal(errors.length, 1);
  });

  it('gives up after maxRetries', async () => {
    const f = await fake();
    f.status = () => ({ code: 500 });
    const log = new ArgusLogger(opts(f, { maxRetries: 2 }));
    log.send('doomed');
    await log.close();
    assert.equal(f.calls, 3, 'first try plus 2 retries');
    assert.equal(log.stats().dropped, 1);
  });

  it('honours Retry-After but caps it', async () => {
    const f = await fake();
    f.status = (call) => (call === 1 ? { code: 429, retryAfter: 30 } : { code: 200 });
    const log = new ArgusLogger(opts(f, { maxRetryWaitMs: 150 }));
    const start = Date.now();
    log.send('slow down');
    await eventually(() => f.lines().length === 1, 10_000);
    const took = Date.now() - start;
    await log.close();
    assert.ok(took >= 120, `waited out the (capped) Retry-After instead of hammering: ${took}ms`);
    assert.ok(took < 5000, `the cap stopped a 30s Retry-After from stalling delivery: ${took}ms`);
  });

  it('never throws from log calls, even with hostile input', async () => {
    const f = await fake();
    const log = new ArgusLogger(opts(f));
    const circular: Record<string, unknown> = {};
    circular.self = circular;
    assert.doesNotThrow(() => log.info('circular', circular));
    assert.doesNotThrow(() => log.info('throwing', { nested: { toJSON() { throw new Error('nope'); } } }));
    assert.doesNotThrow(() => log.log('bogus' as never, 'unknown level'));
    log.info('fine');
    await log.close();
    const levels = f.lines().map((l) => (JSON.parse(l) as { level: string }).level);
    assert.deepEqual(levels, ['info', 'info'], 'the unknown level is treated as info; the two unserialisable lines are dropped');
    assert.equal(log.stats().dropped, 2);
  });

  it('close() flushes, then refuses new lines, and does not hang on a dead server', async () => {
    const f = await fake();
    const log = new ArgusLogger(opts(f));
    log.send('before');
    await log.close();
    log.send('after');
    assert.deepEqual(f.lines(), ['before']);
    assert.equal(log.stats().dropped, 1);

    const dead = new ArgusLogger({ url: 'http://127.0.0.1:1', apiKey: 'k', backoffMs: 1, maxRetries: 1, onError: () => undefined });
    dead.send('nobody home');
    const start = Date.now();
    await dead.close(2000);
    assert.ok(Date.now() - start < 3000, 'close is bounded');
  });
});

describe('server downtime', () => {
  // Simulates Argus refusing connections, then black-holing them (requests hang
  // until the client times out), then coming back.
  it('does not affect the application and recovers afterwards', async () => {
    const f = await fake();
    let mode: 'up' | 'refused' | 'blackhole' = 'refused';
    const flaky: typeof fetch = (input, init) => {
      if (mode === 'refused') return Promise.reject(new TypeError('fetch failed: ECONNREFUSED'));
      if (mode === 'blackhole') {
        return new Promise((_, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new DOMException('timed out', 'AbortError')));
        });
      }
      return fetch(input, init);
    };
    let errors = 0;
    const log = new ArgusLogger(opts(f, {
      batchSize: 100, flushIntervalMs: 10, maxQueue: 1000, maxRetries: 1, timeoutMs: 50,
      fetch: flaky, onError: () => { errors++; },
    }));

    global.gc?.();
    const heapBefore = process.memoryUsage().heapUsed;
    let worst = 0;
    // Log in slices with a pause between them, like a busy app that keeps yielding
    // to the event loop, so the shipper really attempts (and fails) deliveries.
    const burst = async (n: number, tag: string): Promise<void> => {
      for (let i = 0; i < n; i++) {
        const t0 = performance.now();
        log.info('request handled', { tag, i });
        worst = Math.max(worst, performance.now() - t0);
        if (i % 2000 === 1999) await new Promise((r) => setTimeout(r, 5));
      }
    };

    await burst(50_000, 'refused');
    mode = 'blackhole';
    await burst(50_000, 'blackhole');
    assert.ok(worst < 50, `a log call blocked for ${worst.toFixed(1)}ms`);
    assert.ok(log.stats().queued <= 1000, 'the queue is bounded');
    assert.ok(log.stats().dropped > 50_000, 'excess lines are dropped and counted, not buffered');
    const growthMB = (process.memoryUsage().heapUsed - heapBefore) / 1e6;
    assert.ok(growthMB < 40, `memory grew ${growthMB.toFixed(1)}MB`);

    mode = 'up';
    await eventually(() => {
      log.info('request handled', { tag: 'recovered' });
      return f.lines().some((l) => l.includes('"tag":"recovered"'));
    }, 10_000);
    await log.close();
    assert.ok(errors > 0, 'drops were reported through onError');
  });
});
