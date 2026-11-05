/**
 * Runs at most `max` tasks at once, the rest wait in order. A dashboard fires one
 * query per panel; without this, a board with more panels than the server's
 * per-user concurrency cap would show errors for the overflow.
 */
export function createLimiter(max: number) {
  let active = 0;
  const waiting: (() => void)[] = [];

  const next = () => {
    active--;
    waiting.shift()?.();
  };

  return async function run<T>(task: () => Promise<T>): Promise<T> {
    if (active >= max) await new Promise<void>((resolve) => waiting.push(resolve));
    active++;
    try {
      return await task();
    } finally {
      next();
    }
  };
}

interface HasStatus {
  status?: number;
}

/**
 * Retries a task when the server says "too many concurrent queries" (429),
 * waiting a little longer each time. Any other error is thrown straight away.
 */
export async function retryOn429<T>(task: () => Promise<T>, opts: { tries?: number; delayMs?: number; sleep?: (ms: number) => Promise<void> } = {}): Promise<T> {
  const tries = opts.tries ?? 4;
  const delay = opts.delayMs ?? 400;
  const sleep = opts.sleep ?? ((ms: number) => new Promise<void>((r) => setTimeout(r, ms)));
  for (let attempt = 1; ; attempt++) {
    try {
      return await task();
    } catch (e) {
      if ((e as HasStatus)?.status !== 429 || attempt >= tries) throw e;
      await sleep(delay * attempt);
    }
  }
}
