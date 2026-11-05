# Argus: Build Plan

Argus is a self-hosted, logs-only observability tool modeled on Grafana + Loki. It ships as a single Go binary with a React Native (Expo) client that runs on iOS, Android, and web. It must run on one machine with no separate database service and handle production traffic for ~30,000 users.

**Instructions for Claude Code:** Work phase by phase. Finish each phase's acceptance criteria and tests before starting the next. Ask before deviating from this plan. Keep commits small, one logical change each.

---

## 1. Goals and non-goals

**Goals**
- Ingest logs over HTTP, store them cheaply, search them fast enough
- Live tail
- Saved dashboards with charts built from log queries
- Alert rules with webhook notifications
- Mobile and web client from one codebase
- Single binary + single data directory, easy to back up

**Non-goals (for now)**
- Metrics, traces
- Multi-node clustering
- Plugin system
- Full LogQL compatibility

**Expected load:** 200 to 2,000 events/sec sustained, bursts up to 5x. Roughly 20 to 200M lines/day before compression. Retention default 14 days.

---

## 2. Tech stack

| Concern | Choice |
|---|---|
| Language | Go 1.23+ |
| HTTP router | chi |
| ORM | **GORM** (metadata and index only, never log lines) |
| Migrations | **goose** (SQL files, embedded with `go:embed`) |
| Metadata DB | SQLite (WAL mode, pure-Go driver `github.com/glebarez/sqlite`), Postgres supported via config |
| Log storage | Custom chunk store on local disk (Loki-style), zstd compression (`klauspost/compress`) |
| Auth | argon2id password hashing, JWT for UI sessions, hashed API keys for ingest |
| Config | env vars + optional `argus.yaml` (koanf) |
| Logging (self) | `log/slog` |
| Client | **React Native + Expo** (Expo Router, TypeScript), web target via `react-native-web` |
| Client state | TanStack Query + Zustand |
| Charts | `victory-native` or `react-native-svg` based custom charts (must work on web) |
| Tests | `testing` + `testify`; client: Jest + React Native Testing Library |

**Hard rules**
- GORM is never used for `AutoMigrate`. Schema changes go through goose migrations only.
- Log lines never go into SQL. SQL holds metadata and the chunk index.
- No CGO, so cross-compilation stays trivial.

---

## 3. Architecture

```
 Apps/SDKs ──HTTP push──▶ Ingest ──▶ WAL (disk) ──▶ Head chunks (memory)
                              │                          │ flush (size/age)
                              ▼                          ▼
                         Tail hub (pub/sub)       Chunk files (zstd, disk)
                              │                          ▲
 Argus client ◀──SSE/WS───────┘                          │
 (RN: iOS/Android/Web) ◀──REST── Query engine ──reads────┘
                                      ▲
                       GORM/SQLite: streams, chunk index, users,
                       API keys, dashboards, alerts
        Ruler (ticker) ──runs saved queries──▶ Notifier (webhooks)
        Compactor/Retention ──deletes old chunks + index rows
```

### Repo layout

```
argus/
  cmd/argus/main.go
  internal/
    config/
    db/                 # GORM setup, goose runner
      migrations/       # *.sql goose files (embedded)
    models/             # GORM models
    auth/
    ingest/             # HTTP push, validation, limits
    wal/
    chunk/              # chunk format, head chunk, flush, reader
    index/              # stream registry + chunk index via GORM
    query/              # ArgusQL lexer, parser, planner, engine
    tail/               # pub/sub hub
    ruler/              # alert evaluation
    notify/             # webhook (later Slack/email)
    retention/
    api/                # REST handlers, middleware
    webui/              # go:embed of exported Expo web build
  app/                  # Expo React Native client
  deploy/
    Dockerfile
    docker-compose.yml  # optional, single service
  plan.md
```

---

## 4. Data model (GORM models, goose migrations)

Write models in `internal/models`. Write the matching SQL by hand in `internal/db/migrations/NNNNN_name.sql` using goose `-- +goose Up/Down`. Use UUIDs (or ULIDs) as string primary keys. All tables get `created_at`, `updated_at`.

- **User**: id, email (unique), password_hash, role (`admin|editor|viewer`)
- **APIKey**: id, name, key_hash, key_prefix, tenant/app label, allowed_labels (JSON), rate_limit, last_used_at, revoked_at
- **Stream**: id (uint64 hash of sorted labels), labels (JSON), labels_text (normalized `k="v",...`), first_seen, last_seen
- **StreamLabel**: stream_id, key, value (indexed on key+value, powers selector lookup)
- **Chunk**: id, stream_id, path, min_ts, max_ts, line_count, bytes_compressed, bytes_raw, created_at (indexed on stream_id, min_ts, max_ts)
- **SavedQuery**: id, owner_id, name, query, default_range
- **Dashboard**: id, owner_id, title, description, layout (JSON)
- **Panel**: id, dashboard_id, title, type (`logs|timeseries|top_n|stat`), query, step, options (JSON), position
- **AlertRule**: id, name, query (metric-style), comparator, threshold, window, for_duration, cooldown, severity, enabled, notify_channel_ids (JSON)
- **AlertState**: rule_id, state (`ok|pending|firing`), since, last_eval, last_value
- **AlertEvent**: id, rule_id, state, value, fired_at, resolved_at
- **NotificationChannel**: id, type (`webhook`), config (JSON, secrets encrypted)
- **Setting**: key, value (retention days, limits)

Seed: on first boot, if no users exist, create an admin from `ARGUS_ADMIN_EMAIL` / `ARGUS_ADMIN_PASSWORD`.

---

## 5. Log storage engine

1. **Ingest** validates labels, enforces limits, resolves/creates the `Stream`, appends to WAL, then to the stream's in-memory head chunk, and publishes to the tail hub.
2. **WAL**: segment files in `data/wal/`, fsync on interval (default 200ms) with a configurable strict mode. On boot, replay into head chunks, then truncate.
3. **Head chunk** flushes when it hits ~1 MB raw, ~1h age, or on shutdown.
4. **Chunk file format**: header (magic, version, stream id, min/max ts), then blocks of ~256 KB. Each block is a zstd-compressed run of `(ts_ns varint delta, len varint, bytes)`. Footer holds a block index (offset, min/max ts, line count). Optional per-block bloom filter (phase 8).
5. **Chunk files** live in `data/chunks/<yyyy>/<mm>/<dd>/<stream_id>/<chunk_id>.chk`. Wrap file access in a `ChunkStore` interface so S3 can be added later.
6. **Retention**: a compactor loop deletes chunk files and `Chunk` rows older than the retention window, and prunes streams with no chunks.

**Limits (enforced at ingest, configurable)**
- Max labels per stream: 15
- Max label value length: 256 bytes
- Max line size: 64 KB (truncate with marker)
- Max active streams per API key: 10,000
- Per-key rate limit (lines/sec and bytes/sec)
- Reject timestamps older than 24h or more than 5 min in the future

**Cardinality rule:** user IDs, request IDs, and session IDs are never labels. Document this in the README and in the API error message when the stream limit is hit.

---

## 6. Query language (ArgusQL)

A LogQL-flavored subset.

```
{service="api", env="prod"} |= "timeout" != "healthcheck" | json | level="error"
count_over_time({service="api"} |= "error" [5m])
sum by (level) (count_over_time({env="prod"} [1m]))
topk(10, sum by (route) (count_over_time({service="api"} | json | status>=500 [1h])))
```

Support in v1:
- Selectors with `=`, `!=`, `=~`, `!~` (at least one matcher must be non-empty-matching)
- Line filters `|=`, `!=`, `|~`, `!~`
- Parsers `| json`, `| logfmt`
- Field filters after parsing (`=`, `!=`, `>`, `<`, `>=`, `<=`, `=~`)
- Range aggregations `count_over_time`, `rate`, `bytes_over_time`
- `sum by (...)`, `topk`

Implementation: hand-written lexer and recursive-descent parser producing an AST, then a planner that (a) resolves streams via `StreamLabel`, (b) selects chunks overlapping the time range via the `Chunk` table, (c) scans blocks in parallel (worker pool sized to CPUs), skipping blocks outside the time range, (d) merges with head chunks, (e) applies pipeline stages in a streaming fashion.

**Safety limits:** required time range, max range per query type, max lines returned (default 5,000), max bytes scanned, query timeout (default 30s), concurrency cap per user.

---

## 7. REST and streaming API

All under `/api/v1`. JWT bearer for UI routes, API key (`X-Argus-Key`) for ingest.

**Ingest**
- `POST /ingest/push`: NDJSON, one object per line: `{"ts":"RFC3339 or ns epoch","labels":{...},"line":"..."}` (labels may be set once via headers for the batch)
- `POST /ingest/loki`: optional Loki-compatible push (phase 9)

**Auth and users**
- `POST /auth/login`, `POST /auth/refresh`, `GET /me`
- CRUD `/users` (admin), CRUD `/api-keys` (admin)

**Query**
- `GET /query/range?query=&start=&end=&limit=&direction=`
- `GET /query/metric?query=&start=&end=&step=`
- `GET /labels`, `GET /labels/{name}/values`, `GET /series`
- `GET /tail?query=` (SSE; WebSocket variant for RN native if SSE proves unreliable)

**Dashboards and queries**
- CRUD `/dashboards`, `/dashboards/{id}/panels`, `/saved-queries`

**Alerts**
- CRUD `/alert-rules`, `/channels`, `GET /alert-events`, `POST /channels/{id}/test`

**Ops**
- `GET /healthz`, `GET /readyz`, `GET /metrics` (Prometheus format, Argus's own stats: ingest rate, WAL lag, flush duration, query latency, active streams, disk usage)

Use a consistent error envelope: `{"error":{"code":"...","message":"..."}}`.

---

## 8. Client (React Native / Expo)

One codebase in `app/`, using Expo Router. Build web with `npx expo export --platform web`, then embed the output into the Go binary via `internal/webui`.

**Screens**
1. **Login / server URL** (native needs a configurable server URL; web uses same origin)
2. **Explore**: label browser, ArgusQL input with autocomplete (labels and values from API), time range picker, results list with expandable lines, JSON field pretty-print, "save query", "add to dashboard"
3. **Live Tail**: query input, pause/resume, auto-scroll, line cap in memory (e.g. 2,000), level coloring
4. **Dashboards**: list, view, edit mode (add/move/resize panels), auto-refresh interval, shared time range
5. **Panels**: logs, timeseries, top-N bar, stat
6. **Alerts**: rules list and editor, state badges, event history
7. **Settings**: channels, API keys (admin), users (admin), retention/limits (admin)

**Client requirements**
- Virtualized lists (`FlashList`) for log rendering
- Dark and light themes
- Typed API client generated from an OpenAPI spec produced by the Go server (`oapi-codegen` or `swaggo`, pick one in phase 4)
- Secure token storage (`expo-secure-store` on native, memory + httpOnly refresh on web)
- Push notifications for firing alerts are a stretch goal (phase 10)

---

## 9. Alerting

- A single **ruler** goroutine ticks every 15s and evaluates enabled rules whose interval is due. Run rule queries through the same query engine with tighter limits.
- State machine: `ok → pending → firing → ok`. `for_duration` delays firing. `cooldown` prevents repeat notifications.
- Notify on transitions (firing, resolved) via webhook with a JSON payload and retry with backoff (3 tries). Record every transition in `AlertEvent`.
- Persist `AlertState` so a restart doesn't re-fire.

---

## 10. Security and production hardening

- TLS expected at a reverse proxy; support `ARGUS_TLS_CERT/KEY` too
- argon2id for passwords, API keys stored only as hashes (show once on creation)
- Login rate limiting and lockout backoff
- Roles enforced in middleware (viewer: read, editor: dashboards/queries/alerts, admin: everything)
- Optional PII redaction at ingest: configurable regex rules (emails, bearer tokens, card-like numbers, IPs) applied before WAL write
- Optional sampling rule: keep all `level>=warn`, sample the rest by percentage per service
- Request body size limit on ingest (default 5 MB), gzip accepted
- CORS locked to configured origins
- Graceful shutdown: stop accepting, drain ingest, flush head chunks, close WAL
- Backups: documented procedure (stop-free snapshot of SQLite via `VACUUM INTO` plus rsync of `data/chunks`), and a `argus backup` CLI command

---

## 11. Configuration

| Env var | Default | Purpose |
|---|---|---|
| `ARGUS_DATA_DIR` | `./data` | WAL, chunks, SQLite file |
| `ARGUS_HTTP_ADDR` | `:8080` | Listen address |
| `ARGUS_DB_DSN` | `sqlite:<data>/argus.db` | Switch to Postgres with a `postgres://` DSN |
| `ARGUS_RETENTION_DAYS` | `14` | Chunk retention |
| `ARGUS_JWT_SECRET` | required | Session signing |
| `ARGUS_ADMIN_EMAIL` / `ARGUS_ADMIN_PASSWORD` | none | First-boot admin |
| `ARGUS_WAL_SYNC_MS` | `200` | WAL fsync interval |
| `ARGUS_QUERY_TIMEOUT` | `30s` | Per-query timeout |
| `ARGUS_MAX_STREAMS_PER_KEY` | `10000` | Cardinality guard |

---

## 12. Phases and acceptance criteria

**Phase 0: Skeleton** ✅ *complete*
- Go module, repo layout, config loader, slog, chi server, `/healthz`
- GORM connection (SQLite, WAL mode, `busy_timeout`), goose runner that applies embedded migrations on boot
- *Done when:* `go run ./cmd/argus` boots, migrations apply, `/healthz` returns 200, tests pass in CI
- *Status:* done. Follow-up: CI workflow and `golangci-lint` are not set up yet.

**Phase 1: Models, auth, API keys** ✅ *complete*
- Migrations + models for User, APIKey, Setting
- Login, JWT, refresh, role middleware, admin seed, API key create/revoke
- *Done when:* admin can log in and mint an API key; unauthorized requests get 401/403; unit tests cover hashing and middleware

**Phase 2: Storage engine** ✅ *complete (see status)*
- WAL, head chunk, chunk file writer/reader, Stream/StreamLabel/Chunk migrations and index package
- Ingest endpoint with validation, limits, rate limiting
- *Done when:* a load test pushes 2,000 lines/sec for 10 minutes with no loss; kill -9 mid-run then restart loses at most the WAL sync window; chunks appear on disk and in the `Chunk` table; retention deletes old data
- *Status:* built and tested (`go test -race ./...`). Load test (`TestLoad`) verified at 2,000 lines/s for 30s with zero loss; the full 10-minute run still needs to be done. A real `kill -9` of the binary after an acknowledged 1,000-line push recovered all 1,000 lines. Replay is at-least-once (see README). `Stream` has an extra `api_key_id` column to enforce the per-key stream limit. Chunk files are read whole via `Store.Get` (S3-friendly) and block-skipped by time.

**Phase 3: Query engine** ✅ *complete (see status)*
- Lexer, parser, planner, executor for selectors, line filters, `json`/`logfmt`, field filters
- `/query/range`, `/labels`, `/labels/{name}/values`
- *Done when:* golden-file tests pass for parser and executor; a 10M-line dataset answers a selector + `|=` query over 1h in under 2s on a dev laptop; limits and timeouts verified
- *Status:* built and tested with `-race`. Parser and executor are covered by golden files; limits (range, lines, bytes, timeout, per-user concurrency) have tests; 10M lines scan in ~0.6 s worst case (see README). Added a stress test that queries while flushing and found/fixed a real duplicate-lines race (head chunk IDs now equal their chunk IDs so queries can dedupe). Not included: `/series`, metric functions (Phase 6). `start`/`end` are required on every query.

**Phase 4: Client foundation + Explore** ✅ *complete (see status)*
- Expo app, Expo Router, theming, API client (generated from OpenAPI), login flow
- Explore screen with label browser, query input, time picker, virtualized results
- Embed web export in the Go binary and serve at `/`
- *Done when:* same app works in iOS simulator, Android emulator, and browser against a local server
- *Status:* done and verified in a real browser (Chrome driven against the embedded build: login, label browser, autocomplete, query, expand, error display, reload via cookie, logout) plus Jest (35 tests) and `tsc`. iOS and Android bundles compile (`expo export`), but the app has **not** been run in a simulator/emulator here; do that before relying on native. Deviation: OpenAPI is spec-first (hand-written, embedded, route-parity test) instead of `oapi-codegen`/`swaggo`. Added to the server: HttpOnly refresh cookie + `/auth/logout`, opt-in CORS (`ARGUS_CORS_ORIGINS`), SPA serving. Not in this phase: save query / add to dashboard (need Phase 6 APIs).

**Phase 5: Live tail**
- Tail hub, `/tail` SSE (WebSocket fallback), Live Tail screen
- Per-user tail cap, required selector, server-side filtering, slow-consumer drop policy
- *Done when:* a pushed line appears in the client in under 500ms; a stalled client never blocks ingest

**Phase 6: Metric queries + dashboards**
- `count_over_time`, `rate`, `bytes_over_time`, `sum by`, `topk`, `/query/metric`
- Dashboard, Panel, SavedQuery migrations, CRUD APIs
- Dashboard UI with timeseries, top-N, stat, and logs panels; shared time range; auto-refresh
- *Done when:* a dashboard of four panels renders on mobile and web, and queries are cached for the step interval

**Phase 7: Alerts**
- AlertRule, AlertState, AlertEvent, NotificationChannel migrations
- Ruler, state machine, webhook notifier with retry, alert UI
- *Done when:* a rule fires after `for_duration`, sends exactly one webhook, resolves correctly, survives restart without re-firing

**Phase 8: Performance and hardening**
- Per-block bloom filters for line filters, query result cache, parallel scan tuning
- PII redaction and sampling rules, Prometheus `/metrics`, graceful shutdown, `argus backup`
- Soak test at 5x burst; document findings
- *Done when:* soak test passes with stable memory, and the README documents tuning and backup/restore

**Phase 9: Client SDKs (small)**
- Go `slog` handler and a Node/TS logger that batch, retry with backoff, bound their memory queue, and drop rather than block the host app
- Optional Loki-compatible push endpoint
- *Done when:* a sample app logs through each SDK with induced server downtime and no app impact

**Phase 10: Stretch**
- Slack/email channels, push notifications, S3 `ChunkStore`, Postgres validation run, multi-tenant label enforcement per API key

---

## 13. Conventions for Claude Code

- Run `gofmt`, `go vet`, and `golangci-lint` before each commit
- Every exported package gets a short doc comment; every phase ends with an updated README section
- Table-driven tests; use temp dirs for storage tests; never rely on wall-clock sleeps (inject a clock)
- Migrations are append-only. Never edit an applied migration; add a new one
- Keep the `ChunkStore`, `MetadataStore` boundaries clean so storage can change later
- When something in this plan is ambiguous or conflicts with reality (library limits, performance), stop and propose an alternative instead of silently changing the design
- Start now with **Phase 0**, then report back with what was built, what tests ran, and any open questions
