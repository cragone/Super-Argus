import { TailConnection, type TailDeps, type TailEntry, type TailResponse, type TailStatus } from '@/lib/tailConnection';
import { appendCapped } from '@/hooks/useTail';

const enc = new TextEncoder();

/** A response whose body yields the given chunks, then ends (or hangs when `hang` is set). */
function sse(chunks: string[], opts: { hang?: boolean } = {}): TailResponse {
  let i = 0;
  let release: () => void = () => {};
  const hung = new Promise<void>((r) => (release = r));
  return {
    status: 200,
    ok: true,
    json: async () => ({}),
    body: {
      getReader: () => ({
        async read() {
          if (i < chunks.length) return { done: false, value: enc.encode(chunks[i++]) };
          if (opts.hang) await hung;
          return { done: true };
        },
        cancel() {
          release();
        },
      }),
    },
  };
}

const status = (code: number, error?: { code: string; message: string }): TailResponse => ({
  status: code,
  ok: code < 300,
  body: null,
  json: async () => ({ error }),
});

const entry = (line: string): string =>
  `event: entry\ndata: ${JSON.stringify({ ts: '1', stream: '1', labels: { service: 'api' }, line })}\n\n`;

function harness(responses: TailResponse[], tokens: { current: string | null; refreshTo: string | null } = { current: 't1', refreshTo: 't2' }) {
  const calls: { url: string; auth: string }[] = [];
  const entries: TailEntry[] = [];
  const statuses: TailStatus[] = [];
  const errors: { message: string; fatal: boolean }[] = [];
  const dropped: number[] = [];
  const sleeps: number[] = [];
  let refreshes = 0;
  const deps: TailDeps = {
    url: () => 'http://x/api/v1/tail?query=q',
    getToken: () => tokens.current,
    refresh: async () => {
      refreshes++;
      tokens.current = tokens.refreshTo;
      return tokens.current;
    },
    fetch: async (url, init) => {
      calls.push({ url, auth: init.headers.Authorization });
      const next = responses.shift();
      if (!next) throw new Error('no more responses');
      return next;
    },
    // Record the requested delay but yield a macrotask, so a retry loop cannot starve the test's timers.
    sleep: (ms) => {
      sleeps.push(ms);
      return new Promise((r) => setTimeout(r, 0));
    },
  };
  const conn = new TailConnection(deps, {
    onStatus: (s) => statuses.push(s),
    onEntry: (e) => entries.push(e),
    onDropped: (n) => dropped.push(n),
    onError: (message, fatal) => errors.push({ message, fatal }),
  });
  return { conn, calls, entries, statuses, errors, dropped, sleeps, refreshes: () => refreshes };
}

const settle = () => new Promise((r) => setTimeout(r, 120)); // generous: the loop runs on macrotasks, so load can delay it

describe('TailConnection', () => {
  it('connects with the bearer token, reports live and delivers entries and drops', async () => {
    const h = harness([sse(['event: ready\ndata: {}\n\n', entry('one'), entry('two'), 'event: dropped\ndata: {"count":7}\n\n'], { hang: true })]);
    h.conn.start();
    await settle();
    expect(h.calls).toEqual([{ url: 'http://x/api/v1/tail?query=q', auth: 'Bearer t1' }]);
    expect(h.statuses).toEqual(['connecting', 'live']);
    expect(h.entries.map((e) => e.line)).toEqual(['one', 'two']);
    expect(h.dropped).toEqual([7]);
    h.conn.stop();
    expect(h.statuses.at(-1)).toBe('stopped');
  });

  it('survives events split mid-chunk', async () => {
    const e = entry('split');
    const h = harness([sse(['event: ready\ndata: {}\n\n', e.slice(0, 20), e.slice(20)], { hang: true })]);
    h.conn.start();
    await settle();
    expect(h.entries.map((x) => x.line)).toEqual(['split']);
    h.conn.stop();
  });

  it('reconnects with exponential backoff after the stream ends, resetting once live again', async () => {
    const h = harness([sse([]), sse([]), sse(['event: ready\ndata: {}\n\n']), sse([], { hang: true })]);
    h.conn.start();
    await settle();
    expect(h.calls).toHaveLength(4);
    expect(h.sleeps.slice(0, 3)).toEqual([500, 1000, 500]); // backoff doubled, then reset by `ready`
    expect(h.statuses).toContain('reconnecting');
    h.conn.stop();
  });

  it('retries after a network error and reports it without giving up', async () => {
    const h = harness([sse(['event: ready\ndata: {}\n\n'], { hang: false })]);
    // second fetch throws (no more responses)
    h.conn.start();
    await settle();
    expect(h.errors.some((e) => !e.fatal && e.message.startsWith('Connection lost'))).toBe(true);
    expect(h.calls.length).toBeGreaterThan(1);
    h.conn.stop();
  });

  it('refreshes the token on 401 and retries once', async () => {
    const h = harness([status(401), sse(['event: ready\ndata: {}\n\n'], { hang: true })]);
    h.conn.start();
    await settle();
    expect(h.calls.map((c) => c.auth)).toEqual(['Bearer t1', 'Bearer t2']);
    expect(h.refreshes()).toBe(1);
    expect(h.statuses).toContain('live');
    h.conn.stop();
  });

  it('stops with a fatal error when the session cannot be refreshed', async () => {
    const h = harness([status(401)], { current: 't1', refreshTo: null });
    h.conn.start();
    await settle();
    expect(h.errors).toEqual([{ message: expect.stringContaining('Sign in again'), fatal: true }]);
    expect(h.statuses.at(-1)).toBe('error');
    expect(h.calls).toHaveLength(1);
  });

  it('stops on a rejected query and shows the server message', async () => {
    const h = harness([status(400, { code: 'bad_query', message: 'selector must contain a matcher' })]);
    h.conn.start();
    await settle();
    expect(h.errors).toEqual([{ message: 'selector must contain a matcher', fatal: true }]);
    expect(h.calls).toHaveLength(1);
  });

  it('keeps retrying (slowly) when the per-user tail cap is hit', async () => {
    const h = harness([status(429, { code: 'too_many_tails', message: 'too many live tail connections' }), sse(['event: ready\ndata: {}\n\n'], { hang: true })]);
    h.conn.start();
    await settle();
    expect(h.errors[0]).toEqual({ message: 'too many live tail connections', fatal: false });
    expect(h.sleeps[0]).toBe(5000);
    expect(h.statuses).toContain('live');
    h.conn.stop();
  });

  it('refreshes immediately and reconnects when the server reports the token expired', async () => {
    const h = harness([sse(['event: ready\ndata: {}\n\n', 'event: expired\ndata: {}\n\n']), sse(['event: ready\ndata: {}\n\n'], { hang: true })]);
    h.conn.start();
    await settle();
    expect(h.calls.map((c) => c.auth)).toEqual(['Bearer t1', 'Bearer t2']);
    expect(h.sleeps).toEqual([]); // no delay for an expiry
    h.conn.stop();
  });

  it('stop() prevents any further connection attempts', async () => {
    const h = harness([sse([]), sse([])]);
    h.conn.start();
    h.conn.stop();
    await settle();
    expect(h.calls.length).toBeLessThanOrEqual(1);
    expect(h.statuses.at(-1)).toBe('stopped');
  });
});

describe('appendCapped', () => {
  it('keeps only the newest entries', () => {
    expect(appendCapped([1, 2, 3], [4, 5], 4)).toEqual([2, 3, 4, 5]);
    expect(appendCapped([1], [], 4)).toEqual([1]);
    expect(appendCapped([], [1, 2, 3, 4, 5, 6], 4)).toEqual([3, 4, 5, 6]);
  });
});
