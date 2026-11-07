# Argus Go SDK

Ships `log/slog` records (or raw lines) to an Argus server. Standard library only.

```go
ship := argus.New(argus.Config{
    URL:    "http://argus:8080",
    APIKey: os.Getenv("ARGUS_KEY"),
    Labels: map[string]string{"service": "api", "env": "prod"}, // low cardinality only
})
defer ship.Close(context.Background()) // flushes what is queued

logger := slog.New(argus.Tee(slog.NewTextHandler(os.Stderr, nil), ship.Handler(slog.LevelInfo)))
logger.Info("order placed", "order_id", 42)
```

`argus.New` returns `nil` when the URL or key is empty, and a nil shipper is safe to use (everything becomes a no-op), so shipping can be switched off by configuration alone.

## It cannot hurt your application

- **Logging never blocks.** Lines go to a bounded in-memory queue. When it is full the line is dropped and counted (`ship.Stats().Dropped`), so a slow or dead Argus cannot slow a request down.
- **Memory is bounded** by `QueueSize` (default 10,000) lines of at most `MaxLineBytes` (default 64 KiB; longer lines are truncated).
- **Failures are retried, then dropped.** Network errors, 5xx and 429 are retried up to `MaxRetries` (default 3) with exponential backoff from `Backoff` (default 250 ms), honouring `Retry-After`, never waiting more than `MaxRetryWait` (default 30 s). Other 4xx (bad key, malformed) are not retried. Nothing is held forever.
- **Short, explicit timeouts** (5 s to connect, 10 s per request by default; pass `Client` to change them).
- **Quiet.** Drops are reported through `OnError`; the default logs at most one message every 30 seconds.

Delivery is at-least-once: if Argus stores a batch but the response is lost, the retry stores it again. Lines still queued when the process exits are lost unless you call `Close`.

## Config

| Field | Default | |
|---|---|---|
| `URL`, `APIKey` | required | create a key as admin with `POST /api/v1/api-keys` |
| `Labels` | none | copied at `New`; identify the stream |
| `BatchSize` | 200 | lines per request |
| `FlushEvery` | 1s | flush a partial batch after this long |
| `QueueSize` | 10,000 | lines buffered before dropping |
| `MaxRetries`, `Backoff`, `MaxRetryWait` | 3, 250ms, 30s | |
| `MaxLineBytes` | 65,536 | |
| `OnError` | throttled `log.Print` | called for drops |
| `Client` | 10s timeout, 5s dial | your own `*http.Client` |

## Test it yourself

`go test -race ./...` includes a test that runs the SDK against a server that refuses connections, then black-holes them, then recovers. From the repo root, `node sdk/e2e/run.mjs go` does the same against the real `argus` binary, including a hard kill and a restart.
