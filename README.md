# Super-Agrus
Open Source Logging tool

## Running

```
cp .env.example .env   # set ARGUS_JWT_SECRET (and ARGUS_ADMIN_EMAIL / ARGUS_ADMIN_PASSWORD for first boot)
go run ./cmd/argus
```

`.env` is picked up from the working directory or any parent. Real environment variables win over `.env`.

## Ingest (Phase 2)

`POST /api/v1/ingest/push` with header `X-Argus-Key: <api key>` (mint one via `POST /api/v1/api-keys` as admin).
Body is NDJSON, optionally gzip (`Content-Encoding: gzip`), max 5 MB:

```
{"ts":"2026-03-01T12:00:00Z","labels":{"service":"api","env":"prod"},"line":"GET /x 200"}
```

- `ts` is RFC3339 or epoch nanoseconds; if omitted the server time is used. Older than 24h or more than 5 min ahead is rejected.
- Default labels for the batch can be set with `X-Argus-Labels: service=api,env=prod`; per-line labels override them.
- Response: `{"accepted":N,"rejected":M,"errors":[...]}`. `200` when anything was accepted, `400` when everything was rejected, `429` (with `Retry-After`) when the key's rate limit is hit, `413` for oversized bodies/batches.

**Do not put user IDs, request IDs or session IDs in labels.** Every distinct label set is a stream, and each API key may hold at most `ARGUS_MAX_STREAMS_PER_KEY` (default 10,000) of them. Put high-cardinality values in the log line instead.

### Limits
15 labels per stream, 256 bytes per label value, 64 KB per line (longer lines are truncated with a `...[truncated]` marker), per-key rate limits `ARGUS_RATE_LINES_PER_SEC` (default 10,000; a key's own `rate_limit` overrides it) and `ARGUS_RATE_BYTES_PER_SEC` (default 10 MB), with bursts up to 5x.

### Storage layout and durability
```
data/argus.db                                   SQLite metadata: users, keys, streams, chunk index
data/wal/wal-XXXXXXXX.log                       write-ahead log segments
data/chunks/yyyy/mm/dd/<stream_id>/<id>.chk     zstd-compressed chunk files
```
Every accepted line is appended to the WAL before the request is acknowledged. The WAL is fsynced every `ARGUS_WAL_SYNC_MS` (default 200; `0` = fsync on every request). After a crash (`kill -9`, power loss) at most that window of acknowledged lines can be lost; the rest is replayed into chunk files on the next start. A crash right after chunks were written but before old WAL segments were dropped can duplicate lines on replay (at-least-once).

Head chunks flush to disk at ~1 MB or ~1 h of age, and on graceful shutdown. Chunks older than `ARGUS_RETENTION_DAYS` (default 14) are deleted hourly, along with streams that have no chunks left.

### Load testing
```
ARGUS_LOAD_SECONDS=600 ARGUS_LOAD_RATE=2000 go test ./internal/ingest -run TestLoad -timeout 20m -v
```
