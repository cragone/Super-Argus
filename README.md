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

## Client app (Phase 4)

`app/` is one Expo (React Native) codebase for iOS, Android and web: Expo Router, TypeScript, TanStack Query, Zustand, FlashList. Screens so far: sign-in, Explore (label browser, ArgusQL input with label and value autocomplete, time range picker, virtualized results with expandable lines and JSON pretty-printing) and Settings. Dark and light themes follow the system.

```
cd app
npm install
npm run web          # dev server in the browser
npm run ios          # or: npm run android (needs a simulator/emulator or Expo Go)
npm test             # Jest + React Native Testing Library
npm run typecheck
```

**Pointing the app at a server.** On iOS and Android the sign-in screen asks for the server address and remembers it. The web build served by Argus itself uses the same origin. For `npm run web` against a server on another port, create `app/.env.local` with `EXPO_PUBLIC_ARGUS_URL=http://localhost:8080` and start Argus with `ARGUS_CORS_ORIGINS=http://localhost:8081`.

**Sessions.** Native apps keep the refresh token in the OS keystore (`expo-secure-store`). The web client never touches a refresh token from JavaScript: the server sets it as an HttpOnly, SameSite=Strict cookie (`Secure` behind HTTPS), and access tokens live only in memory.

**Typed API client.** The server publishes its OpenAPI document at `/api/v1/openapi.yaml` (source: `internal/api/openapi/openapi.yaml`). A Go test fails if the router and the document disagree. Regenerate the TypeScript types with `npm run gen:api` after changing it. We write the spec first and verify it against the router rather than using `oapi-codegen` or `swaggo`; this avoids restructuring the handlers or annotating them, and the parity test keeps it honest.

**Embedding the web UI in the binary.**
```
cd app && npm run build:web     # exports into internal/webui/dist
cd .. && go build ./cmd/argus   # the binary now serves the UI at /
```
Without the build step the binary still compiles and `/` explains how to build the UI. `dist/` is git-ignored.

## Live tail (Phase 5)

`GET /api/v1/tail?query=...` streams matching lines as they are ingested, as server-sent events. It takes the same ArgusQL as `/query/range` (a stream selector is required; line filters, `| json` / `| logfmt` and field filters all work) but no time range. Send the access token as `Authorization: Bearer ...` (browsers' `EventSource` cannot, so clients use `fetch` streaming).

Events: `ready`, `entry` (`{ts, stream, labels, line, fields}`), `dropped` (`{"count":N}`), `expired`. Lines starting with `:` are 15-second heartbeats.

- **Filtering is server-side**, so a narrow query costs the client nothing for lines it does not match.
- **A slow client never slows ingest.** Each tail has a bounded buffer; when it is full, batches are dropped for that tail only and the client is told with a `dropped` event (the app shows "N lines were skipped"). A client that stops reading entirely is disconnected after a 10 s write timeout.
- **Caps:** `ARGUS_TAIL_PER_USER` (default 5) and `ARGUS_TAIL_MAX` (default 500) open tails; over the cap returns `429`.
- **Token lifetime:** a tail ends with an `expired` event when the access token expires. The app refreshes the token and reconnects on its own.
- **Measured:** a pushed line reaches the Go test client in under 10 ms, and a Chrome tab running the web app shows it in about 90-100 ms (it renders in 100 ms batches). Ingesting 16,000 lines with a stalled tail attached took 0.9 s.

In the app, the **Live Tail** tab has start/stop, pause/resume (new lines are held and counted while paused), auto-scroll, clear, level coloring, and keeps the newest 2,000 lines in memory. It reconnects with exponential backoff if the connection drops.

SSE is the only transport. If it proves unreliable on a particular mobile network or proxy, a WebSocket endpoint is the planned fallback (not built).

## Metric queries and dashboards (Phase 6)

### Metric queries
`GET /api/v1/query/metric?query=&start=&end=&step=` evaluates a range aggregation at every step:

```
count_over_time({service="api"} |= "error" [5m])
rate({env="prod"} | json | status>=500 [1m])
bytes_over_time({service="api"}[1h])
sum by (level) (count_over_time({env="prod"} | json [1m]))
topk(10, sum by (route) (count_over_time({service="api"} | json | status>=500 [1h])))
```

- The value at time *t* covers the lines in `(t - window, t]`. `rate` is lines per second; `bytes_over_time` sums line sizes.
- Without `sum`, there is one series per stream (labelled with the stream labels). `sum by (a, b)` groups by those labels; they may be stream labels or fields extracted by `| json` / `| logfmt`. `sum(...)` alone folds everything into one series.
- `topk(n, ...)` keeps the n largest series **at each step**, so a series can appear and disappear (the chart breaks the line there).
- `step` is a duration (`30s`, `5m`) or seconds; by default about 250 points. `start` and `end` are aligned down to the step, and **identical requests are cached for one step**, so dashboards refreshing faster than their step do not rescan.
- Durations accept `ms`, `s`, `m`, `h`, `d`, `w`.
- Response: `{"series":[{"labels":{},"values":[[unix_ms, value],...]}],"start_ms","end_ms","step_ms","stats"}`.
- Limits: `ARGUS_QUERY_METRIC_MAX_RANGE` (default `744h`), at most 2,000 steps and 500 series per query, plus the scan-bytes budget, timeout and per-user concurrency from the log query limits. Over a limit returns `422`.

### Dashboards, panels, saved queries
REST resources under `/api/v1` (viewers read, editors and admins write): `/dashboards`, `/dashboards/{id}` (includes panels), `/dashboards/{id}/panels[/{panelId}]`, `/saved-queries`. A panel has a `type` (`timeseries`, `top_n`, `stat`, `logs`), a `query` (metric for the first three, log for `logs`; validated on save), an optional `step`, free-form `options` (the app uses `width` and `height`) and a `position`. A dashboard holds up to 100 panels.

### In the app
- **Dashboards tab:** list, create, open. A dashboard has one time range and one auto-refresh interval (off, 15s, 30s, 1m, 5m) shared by every panel, in a single row above the panels. Charts keep their previous frame (dimmed) while refreshing.
- **Panels:** time series (line per series, 2 px lines, hairline grid, legend for 2+ series, crosshair tooltip that lists every series, **Table** toggle with every value, more than 8 series fold into "Other"), top-N ranked bars (newest value, largest first, one hue), stat (headline number plus sparkline) and logs (newest 50 lines).
- **Edit mode** (editors and admins): add, edit and delete panels; move up/down; toggle half/full width; cycle height. We use buttons instead of drag-to-resize, which works the same on touch and mouse.
- **Explore:** *Save query*, *Add to dashboard*, and a row of saved queries to load.
- Chart colors are the validated reference palette from the dataviz guidance (fixed slot order, a series keeps its color when others appear or disappear). `node validate_palette.js` passes for both modes; light-mode contrast is low for three hues, which the legend and table view cover.

Seed data for a demo: push an hour of traffic, then open the Dashboards tab.
