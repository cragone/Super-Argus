import { SSEParser } from './sse';

export interface TailEntry {
  /** Unix nanoseconds as a string. */
  ts: string;
  stream: string;
  labels: Record<string, string>;
  line: string;
  fields?: Record<string, string>;
}

export type TailStatus = 'connecting' | 'live' | 'reconnecting' | 'stopped' | 'error';

interface Reader {
  read(): Promise<{ done: boolean; value?: Uint8Array }>;
  cancel(): Promise<void> | void;
}

export interface TailResponse {
  status: number;
  ok: boolean;
  body?: { getReader(): Reader } | null;
  json(): Promise<unknown>;
}

export interface TailDeps {
  url(): string;
  getToken(): string | null;
  /** Fresh access token, or null when the session is over. */
  refresh(): Promise<string | null>;
  fetch(url: string, init: { headers: Record<string, string>; signal?: AbortSignal }): Promise<TailResponse>;
  sleep(ms: number): Promise<void>;
  decode?(bytes: Uint8Array): string;
}

export interface TailHandlers {
  onStatus(s: TailStatus): void;
  onEntry(e: TailEntry): void;
  /** The server skipped this many lines because the client was too slow. */
  onDropped(count: number): void;
  /** A message to show the user. `fatal` means the connection will not be retried. */
  onError(message: string, fatal: boolean): void;
}

const MIN_BACKOFF = 500;
const MAX_BACKOFF = 10_000;
const TOO_MANY_TAILS_WAIT = 5_000;

/**
 * One live-tail subscription over server-sent events. Reconnects with
 * exponential backoff, refreshes the access token on 401 and when the server
 * says it expired, and gives up only on errors a retry cannot fix.
 */
export class TailConnection {
  private stopped = true;
  private abort: AbortController | null = null;
  private reader: Reader | null = null;

  constructor(
    private readonly deps: TailDeps,
    private readonly h: TailHandlers,
  ) {}

  start() {
    if (!this.stopped) return;
    this.stopped = false;
    void this.run();
  }

  stop() {
    if (this.stopped) return;
    this.stopped = true;
    this.abort?.abort();
    try {
      void Promise.resolve(this.reader?.cancel()).catch(() => {});
    } catch {
      /* already closed */
    }
    this.h.onStatus('stopped');
  }

  private async run() {
    let backoff = MIN_BACKOFF;
    let first = true;
    let refreshFirst = false;

    while (!this.stopped) {
      this.h.onStatus(first ? 'connecting' : 'reconnecting');
      first = false;
      let wait = backoff;
      try {
        let token = refreshFirst ? await this.deps.refresh() : this.deps.getToken();
        refreshFirst = false;
        if (!token && !(token = await this.deps.refresh())) return this.fatal('Your session has ended. Sign in again.');

        this.abort = new AbortController();
        const open = (t: string) =>
          this.deps.fetch(this.deps.url(), {
            headers: { Authorization: `Bearer ${t}`, Accept: 'text/event-stream' },
            signal: this.abort?.signal,
          });
        let res = await open(token);
        if (res.status === 401) {
          const fresh = await this.deps.refresh();
          if (!fresh) return this.fatal('Your session has ended. Sign in again.');
          res = await open(fresh);
        }
        if (this.stopped) return;

        if (res.status === 400) return this.fatal(await message(res, 'Invalid query'));
        if (res.status === 429) {
          this.h.onError(await message(res, 'Too many live tail connections'), false);
          wait = TOO_MANY_TAILS_WAIT;
        } else if (!res.ok || !res.body) {
          throw new Error(`HTTP ${res.status}`);
        } else {
          const outcome = await this.consume(res.body.getReader(), () => {
            // A connection that got as far as `ready` was healthy: retry quickly if it later drops.
            backoff = MIN_BACKOFF;
            wait = MIN_BACKOFF;
          });
          if (outcome === 'expired') {
            refreshFirst = true;
            wait = 0;
          }
        }
      } catch (e) {
        if (this.stopped) return;
        this.h.onError(`Connection lost: ${e instanceof Error ? e.message : 'network error'}`, false);
      }
      if (this.stopped) return;
      if (wait > 0) {
        await this.deps.sleep(wait);
        backoff = Math.min(backoff * 2, MAX_BACKOFF);
      }
    }
  }

  private async consume(reader: Reader, onReady: () => void): Promise<'ended' | 'expired'> {
    this.reader = reader;
    const parser = new SSEParser();
    const decode = this.deps.decode ?? defaultDecode();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done || this.stopped) return 'ended';
        for (const ev of parser.push(decode(value as Uint8Array))) {
          switch (ev.event) {
            case 'ready':
              onReady();
              this.h.onStatus('live');
              break;
            case 'entry':
              try {
                this.h.onEntry(JSON.parse(ev.data) as TailEntry);
              } catch {
                /* skip a malformed event rather than dropping the stream */
              }
              break;
            case 'dropped':
              try {
                this.h.onDropped((JSON.parse(ev.data) as { count: number }).count);
              } catch {
                /* ignore */
              }
              break;
            case 'expired':
              return 'expired';
          }
        }
      }
    } finally {
      this.reader = null;
      try {
        void Promise.resolve(reader.cancel()).catch(() => {});
      } catch {
        /* already closed */
      }
    }
  }

  private fatal(msg: string) {
    this.stopped = true;
    this.h.onError(msg, true);
    this.h.onStatus('error');
  }
}

async function message(res: TailResponse, fallback: string): Promise<string> {
  try {
    const body = (await res.json()) as { error?: { message?: string } };
    return body.error?.message ?? fallback;
  } catch {
    return fallback;
  }
}

function defaultDecode(): (b: Uint8Array) => string {
  const d = new TextDecoder();
  return (b) => d.decode(b, { stream: true });
}
