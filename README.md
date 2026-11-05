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

## Querying (Phase 3)

All query routes need `Authorization: Bearer <access token>` (any role).

- `GET /api/v1/query/range?query=&start=&end=&limit=&direction=`
  - `start` (inclusive) and `end` (exclusive) are required: RFC3339 or unix nanoseconds.
  - `direction` is `backward` (newest first, default) or `forward`. `limit` defaults to 1,000 and is capped at 5,000.
  - Returns `{"entries":[{"ts","stream","line","fields"}],"streams":{id:{labels}},"stats":{...}}`. `ts` and stream ids are strings because they exceed JavaScript's safe integer range. `fields` is present when the query has a `| json` or `| logfmt` stage.
- `GET /api/v1/labels` and `GET /api/v1/labels/{name}/values` (optional `start`/`end`) feed autocomplete.

### ArgusQL
```
{service="api", env=~"prod|staging"} |= "timeout" != "healthcheck" | json | level="error" | status>=500
```
- Selector matchers: `=` `!=` `=~` `!~`. At least one must not match an empty value (`{service=~".*"}` is rejected). Regexes are RE2 and anchored.
- Line filters: `|=` contains, `!=` does not contain, `|~` regex, `!~` negated regex.
- Parsers: `| json` (nested keys are joined with `_`, so `req.id` becomes `req_id`) and `| logfmt`.
- Field filters after a parser (or on stream labels): `=` `!=` `=~` `!~` against strings or numbers, and `>` `>=` `<` `<=` against numbers. A missing field behaves like `""`; numeric comparisons on missing or non-numeric fields never match.
- In double-quoted strings write `"\\d+"` to get a backslash, or use a backtick string: `` `\d+` ``.
- Metric queries (`count_over_time`, `sum by`, `topk`) arrive in Phase 6.

### Query limits
Required time range; at most `ARGUS_QUERY_MAX_RANGE` (default `168h`); `ARGUS_QUERY_TIMEOUT` (default `30s`); at most `ARGUS_QUERY_MAX_BYTES_MB` (default 4096) of raw log data scanned; `ARGUS_QUERY_CONCURRENCY` (default 4) simultaneous queries per user. Errors: `400 bad_query`, `422 query_limit`, `429 too_many_queries`, `504 query_timeout`.

### How a query runs
Streams are found through the indexed `stream_labels` table, then chunks overlapping the range are selected from the `chunks` table. Chunk files are scanned in parallel (one worker per CPU), skipping blocks outside the range, and merged with the not-yet-flushed head chunks. Results are bounded top-N, and in `backward` mode chunks are visited newest-first and skipped once they cannot improve a full result. Results are totally ordered (time, stream, line), so they are deterministic.

On a 12-core dev machine with 10M lines (1.25 GB raw, 100 streams, 1,000 chunks) and a 1h window: selector plus `|=` answers in 40-70 ms; a worst-case full scan of every line (`|= "no-such-text"` across all streams) takes ~0.6 s. Reproduce with `ARGUS_QUERY_LINES=10000000 go test ./internal/query -run LargeDataset -v`.

Golden files live in `internal/query/testdata`; refresh them with `go test ./internal/query -update` and review the diff.
