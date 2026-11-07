/**
 * Argus logger for Node.js.
 *
 * Safe to put in a request path:
 * - logging never blocks and never throws: when the in-memory queue is full the
 *   line is dropped and counted, so a slow or dead Argus cannot slow the app down;
 * - memory is bounded by `maxQueue` lines of at most `maxLineBytes` each;
 * - lines are batched and gzipped; failed sends are retried with exponential
 *   backoff (honouring Retry-After) and then dropped, never held forever;
 * - its timer is unref'd, so it never keeps the process alive;
 * - no runtime dependencies (Node 18+: global fetch, node:zlib).
 */
import { gzip } from 'node:zlib';
import { promisify } from 'node:util';

const gzipAsync = promisify(gzip);

export type Level = 'debug' | 'info' | 'warn' | 'error';

export interface ArgusOptions {
  /** Argus base URL, e.g. http://localhost:8080 */
  url: string;
  /** Ingest API key (create one as admin via POST /api/v1/api-keys). */
  apiKey: string;
  /** Labels identifying the stream. Keep them low-cardinality: never user, request or session IDs. */
  labels?: Record<string, string>;
  /** Lines per request (default 200). */
  batchSize?: number;
  /** Flush a partial batch after this long (default 1000). */
  flushIntervalMs?: number;
  /** Lines buffered before new ones are dropped (default 10000). */
  maxQueue?: number;
  /** Retries per batch for 429/5xx/network errors (default 3). */
  maxRetries?: number;
  /** First retry delay, doubled each time (default 250). */
  backoffMs?: number;
  /** Cap on any single wait, including a server's Retry-After (default 30000). */
  maxRetryWaitMs?: number;
  /** Longer lines are truncated before queueing (default 65536). */
  maxLineBytes?: number;
  /** Per-request timeout (default 10000). */
  timeoutMs?: number;
  /** Called when lines are dropped after failed delivery. Default: console.error, at most once per 30s. */
  onError?: (message: string) => void;
  /** Minimum level for the level helpers (default 'info'). */
  level?: Level;
  /** Override fetch (tests, custom agents). */
  fetch?: typeof fetch;
}

export interface Stats {
  /** Lines Argus accepted. */
  sent: number;
  /** Lines discarded: queue full, closed, or delivery kept failing. */
  dropped: number;
  /** Successful requests. */
  batches: number;
  /** Lines waiting to be sent. */
  queued: number;
}

interface Entry {
  ts: string;
  labels: Record<string, string>;
  line: string;
}

const LEVELS: Record<Level, number> = { debug: 10, info: 20, warn: 30, error: 40 };

function errorReplacer(_key: string, value: unknown): unknown {
  if (value instanceof Error) return { name: value.name, message: value.message, stack: value.stack };
  if (typeof value === 'bigint') return value.toString();
  return value;
}

function throttled(everyMs: number, fn: (m: string) => void): (m: string) => void {
  let last = 0;
  return (m) => {
    const now = Date.now();
    if (now - last >= everyMs) {
      last = now;
      fn(m);
    }
  };
}

export class ArgusLogger {
  private readonly o: Required<Omit<ArgusOptions, 'fetch' | 'labels'>> & { labels: Record<string, string>; fetch: typeof fetch };
  private readonly url: string;
  private queue: Entry[] = [];
  private timer: NodeJS.Timeout | undefined;
  private pumping: Promise<void> | undefined;
  private closed = false;
  private closing = false;
  private sentCount = 0;
  private droppedCount = 0;
  private batchCount = 0;

  constructor(options: ArgusOptions) {
    if (!options.url || !options.apiKey) throw new TypeError('ArgusLogger needs url and apiKey');
    this.o = {
      url: options.url,
      apiKey: options.apiKey,
      labels: { ...(options.labels ?? {}) }, // the caller may keep mutating its object
      batchSize: Math.max(1, options.batchSize ?? 200),
      flushIntervalMs: Math.max(1, options.flushIntervalMs ?? 1000),
      maxQueue: Math.max(1, options.maxQueue ?? 10_000),
      maxRetries: Math.max(0, options.maxRetries ?? 3),
      backoffMs: Math.max(1, options.backoffMs ?? 250),
      maxRetryWaitMs: Math.max(1, options.maxRetryWaitMs ?? 30_000),
      maxLineBytes: Math.max(16, options.maxLineBytes ?? 64 * 1024),
      timeoutMs: Math.max(1, options.timeoutMs ?? 10_000),
      onError: options.onError ?? throttled(30_000, (m) => console.error(m)),
      level: options.level ?? 'info',
      fetch: options.fetch ?? globalThis.fetch.bind(globalThis),
    };
    this.url = options.url.replace(/\/+$/, '') + '/api/v1/ingest/push';
    this.timer = setInterval(() => this.kick(), this.o.flushIntervalMs);
    this.timer.unref();
  }

  debug(message: string, fields?: Record<string, unknown>): void {
    this.log('debug', message, fields);
  }
  info(message: string, fields?: Record<string, unknown>): void {
    this.log('info', message, fields);
  }
  warn(message: string, fields?: Record<string, unknown>): void {
    this.log('warn', message, fields);
  }
  error(message: string, fields?: Record<string, unknown>): void {
    this.log('error', message, fields);
  }

  /** Queue one structured line: {"time","level","msg",...fields}. Never throws, never blocks. */
  log(level: Level, message: string, fields?: Record<string, unknown>): void {
    try {
      const rank = LEVELS[level] ?? LEVELS.info; // an unknown level counts as info rather than skipping the filter
      if (rank < LEVELS[this.o.level]) return;
      const now = new Date();
      const base = { time: now.toISOString(), level: level in LEVELS ? level : 'info', msg: message };
      // Reserved keys win over caller fields so the line always parses the same way.
      this.send(JSON.stringify({ ...fields, ...base }, errorReplacer), now);
    } catch {
      this.droppedCount++; // e.g. a throwing toJSON; the host must never see it
    }
  }

  /** Queue a raw line (already formatted by you). Never throws, never blocks. */
  send(line: string, ts: Date = new Date()): void {
    try {
      if (this.closed || this.queue.length >= this.o.maxQueue) {
        this.droppedCount++;
        return;
      }
      if (line.length > this.o.maxLineBytes) line = line.slice(0, this.o.maxLineBytes) + '...[truncated]';
      this.queue.push({ ts: ts.toISOString(), labels: this.o.labels, line });
      if (this.queue.length >= this.o.batchSize) this.kick();
    } catch {
      this.droppedCount++;
    }
  }

  stats(): Stats {
    return { sent: this.sentCount, dropped: this.droppedCount, batches: this.batchCount, queued: this.queue.length };
  }

  /** Resolve once everything queued so far has been delivered or dropped. */
  async flush(): Promise<void> {
    while (this.queue.length > 0 || this.pumping) {
      this.kick();
      if (this.pumping) await this.pumping;
      else break;
    }
  }

  /**
   * Flush what is queued (waiting up to `timeoutMs`), stop the timer and refuse
   * further lines. Always resolves; lines still undelivered at the deadline are dropped.
   */
  async close(timeoutMs = 5000): Promise<void> {
    if (this.closed) return;
    this.closing = true; // a retry sleeping at shutdown gives up waiting
    let deadline: NodeJS.Timeout | undefined;
    await Promise.race([
      this.flush().catch(() => undefined),
      new Promise<void>((resolve) => {
        deadline = setTimeout(resolve, timeoutMs);
        deadline.unref();
      }),
    ]);
    if (deadline) clearTimeout(deadline);
    this.closed = true;
    if (this.timer) clearInterval(this.timer);
    this.timer = undefined;
    this.droppedCount += this.queue.length;
    this.queue = [];
  }

  // --- delivery ---

  private kick(): void {
    if (this.pumping || this.queue.length === 0) return;
    this.pumping = this.pump().finally(() => {
      this.pumping = undefined;
    });
  }

  private async pump(): Promise<void> {
    while (this.queue.length > 0) {
      const batch = this.queue.splice(0, this.o.batchSize);
      try {
        await this.deliver(batch);
      } catch (err) {
        this.droppedCount += batch.length; // defensive: deliver() handles its own failures
        this.o.onError(`argus: dropping ${batch.length} lines: ${String(err)}`);
      }
    }
  }

  private async deliver(batch: Entry[]): Promise<void> {
    let body: Uint8Array;
    try {
      body = await gzipAsync(batch.map((e) => JSON.stringify(e)).join('\n') + '\n');
    } catch {
      this.droppedCount += batch.length;
      return;
    }
    let delay = this.o.backoffMs;
    for (let attempt = 0; ; attempt++) {
      const r = await this.post(body);
      if (r.status === 200) {
        this.sentCount += r.accepted;
        const rejected = batch.length - r.accepted; // too old, bad labels, ...
        if (rejected > 0) {
          this.droppedCount += rejected;
          this.o.onError(`argus: ${rejected} lines rejected by the server`);
        }
        this.batchCount++;
        return;
      }
      if (r.status >= 400 && r.status < 500 && r.status !== 429) {
        this.droppedCount += batch.length; // our fault (bad key, malformed): retrying cannot help
        this.o.onError(`argus: dropping ${batch.length} lines: server answered ${r.status}`);
        return;
      }
      if (attempt >= this.o.maxRetries) {
        this.droppedCount += batch.length;
        this.o.onError(`argus: dropping ${batch.length} lines after ${attempt + 1} attempts (last: ${r.status || r.error})`);
        return;
      }
      const wait = Math.min(Math.max(delay, r.retryAfterMs), this.o.maxRetryWaitMs);
      if (!this.closing) await new Promise<void>((resolve) => {
        const t = setTimeout(resolve, wait);
        t.unref();
      });
      delay *= 2;
    }
  }

  private async post(body: Uint8Array): Promise<{ status: number; accepted: number; retryAfterMs: number; error?: string }> {
    const ac = new AbortController();
    const timer = setTimeout(() => ac.abort(), this.o.timeoutMs);
    timer.unref();
    try {
      const res = await this.o.fetch(this.url, {
        method: 'POST',
        headers: { 'X-Argus-Key': this.o.apiKey, 'Content-Type': 'application/x-ndjson', 'Content-Encoding': 'gzip' },
        body: body as unknown as BodyInit, // a gzip Buffer; the DOM lib types are stricter than undici
        signal: ac.signal,
      });
      const secs = Number(res.headers.get('retry-after'));
      const retryAfterMs = Number.isFinite(secs) && secs > 0 ? secs * 1000 : 0;
      let accepted = 0;
      if (res.status === 200) {
        try {
          const j = (await res.json()) as { accepted?: number };
          accepted = typeof j.accepted === 'number' ? j.accepted : 0;
        } catch {
          // an unreadable 200 still means the server took the batch; do not resend it
          accepted = 0;
        }
      } else {
        await res.arrayBuffer().catch(() => undefined); // release the connection
      }
      return { status: res.status, accepted, retryAfterMs };
    } catch (err) {
      return { status: 0, accepted: 0, retryAfterMs: 0, error: err instanceof Error ? err.message : String(err) };
    } finally {
      clearTimeout(timer);
    }
  }
}

/** Convenience factory. */
export function createLogger(options: ArgusOptions): ArgusLogger {
  return new ArgusLogger(options);
}
