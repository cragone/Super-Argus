import { createLimiter, retryOn429 } from '@/lib/limiter';

const tick = () => new Promise((r) => setTimeout(r, 0));

describe('createLimiter', () => {
  it('never runs more than max tasks at once and keeps FIFO order', async () => {
    const run = createLimiter(3);
    let active = 0;
    let peak = 0;
    const started: number[] = [];
    const releases: (() => void)[] = [];

    const results = Array.from({ length: 8 }, (_, i) =>
      run(async () => {
        started.push(i);
        active++;
        peak = Math.max(peak, active);
        await new Promise<void>((r) => releases.push(r));
        active--;
        return i;
      }),
    );

    await tick();
    expect(started).toEqual([0, 1, 2]); // only 3 in flight
    while (releases.length) {
      releases.shift()!();
      await tick();
    }
    expect(await Promise.all(results)).toEqual([0, 1, 2, 3, 4, 5, 6, 7]);
    expect(peak).toBe(3);
    expect(started).toEqual([0, 1, 2, 3, 4, 5, 6, 7]);
  });

  it('frees the slot when a task throws', async () => {
    const run = createLimiter(1);
    await expect(run(async () => { throw new Error('boom'); })).rejects.toThrow('boom');
    expect(await run(async () => 'ok')).toBe('ok');
  });
});

describe('retryOn429', () => {
  const tooMany = Object.assign(new Error('at most 4 concurrent queries per user'), { status: 429 });

  it('retries 429s with growing delays, then succeeds', async () => {
    const sleeps: number[] = [];
    let calls = 0;
    const out = await retryOn429(
      async () => {
        if (++calls < 3) throw tooMany;
        return 'done';
      },
      { delayMs: 100, sleep: async (ms) => { sleeps.push(ms); } },
    );
    expect(out).toBe('done');
    expect(sleeps).toEqual([100, 200]);
  });

  it('gives up after the configured tries and rethrows', async () => {
    let calls = 0;
    await expect(
      retryOn429(async () => { calls++; throw tooMany; }, { tries: 3, sleep: async () => {} }),
    ).rejects.toBe(tooMany);
    expect(calls).toBe(3);
  });

  it('does not retry other errors', async () => {
    let calls = 0;
    await expect(
      retryOn429(async () => { calls++; throw Object.assign(new Error('bad query'), { status: 400 }); }, { sleep: async () => {} }),
    ).rejects.toThrow('bad query');
    expect(calls).toBe(1);
  });
});
