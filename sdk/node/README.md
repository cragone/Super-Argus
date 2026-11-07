# Argus Node SDK

Ships structured logs from Node.js (18+) to an Argus server. No runtime dependencies (global `fetch`, `node:zlib`). Written in TypeScript; types are included.

```ts
import { ArgusLogger } from 'argus-logger';

const log = new ArgusLogger({
  url: 'http://argus:8080',
  apiKey: process.env.ARGUS_KEY!,
  labels: { service: 'api', env: 'prod' }, // low cardinality only
});

log.info('order placed', { orderId: 42 });
log.error('payment failed', { err: new Error('card declined') }); // errors serialise

process.on('SIGTERM', async () => {
  await log.close(); // flush what is queued (default wait: 5s)
  process.exit(0);
});
```

Each line is one JSON object, `{"time","level","msg",...fields}`, so Argus queries can use `| json | level="error"`. `time`, `level` and `msg` always win over fields with the same name. `log.send(line)` queues a raw line instead.

The package is not published yet: build it with `npm install && npm run build` in `sdk/node` and depend on the folder.

## It cannot hurt your application

- **`log()` never throws and never blocks.** It pushes onto a bounded queue; when the queue is full, or a field cannot be serialised (circular structure, a throwing `toJSON`), the line is dropped and counted in `log.stats().dropped`.
- **Memory is bounded** by `maxQueue` (default 10,000) lines of at most `maxLineBytes` (default 64 KiB, longer lines are truncated).
- **Failures are retried, then dropped.** Network errors, 5xx and 429 are retried up to `maxRetries` (default 3) with exponential backoff from `backoffMs` (250), honouring `Retry-After` but never waiting more than `maxRetryWaitMs` (30 s). Other 4xx are not retried. Requests time out after `timeoutMs` (10 s).
- **It never keeps your process alive.** Its timers are `unref()`'d. Lines still queued when the process exits are lost unless you `await log.close()` first.
- **One request at a time**, so a long outage does not pile up connections.

Delivery is at-least-once: if Argus stores a batch but the response is lost, the retry stores it again.

## API

| | |
|---|---|
| `new ArgusLogger(options)` | `url` and `apiKey` required; also `labels`, `batchSize` (200), `flushIntervalMs` (1000), `maxQueue`, `maxRetries`, `backoffMs`, `maxRetryWaitMs`, `maxLineBytes`, `timeoutMs`, `level` (`'info'`), `onError`, `fetch` |
| `debug/info/warn/error(msg, fields?)`, `log(level, msg, fields?)` | queue a structured line; levels below `options.level` are skipped; an unknown level counts as `info` |
| `send(line, ts?)` | queue a raw line |
| `flush()` | resolves once everything queued so far is delivered or dropped |
| `close(timeoutMs = 5000)` | flush, stop, refuse further lines; always resolves |
| `stats()` | `{ sent, dropped, batches, queued }` |

`onError` is called for drops after failed delivery; the default writes to `console.error` at most once every 30 seconds.

## Test it yourself

`npm test` includes a test that logs 100,000 lines while the server refuses connections, then black-holes them, then recovers. From the repo root, `node sdk/e2e/run.mjs node` does the same against the real `argus` binary, including a hard kill and a restart.
