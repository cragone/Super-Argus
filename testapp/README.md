# Test app

A small local **gin + GORM** service that ships its logs to Argus, plus a traffic generator, so you can watch a real application's logs in Argus: Explore, Live Tail and a ready-made dashboard.

It is its own Go module (`testapp/go.mod`), so gin and GORM stay out of Argus's dependencies. `go build ./...` and `go test ./...` at the repo root do not include it; run them inside `testapp/`.

```
testapp/
  main.go                  the service (gin routes, GORM + SQLite, slog -> console + Argus)
  internal/app/            routes, request-log middleware, panic recovery, GORM -> slog adapter
  internal/argus/          log shipper (batched, gzip, retry, never blocks) and slog handler
  cmd/traffic/             traffic generator + "Test app" dashboard setup
```

## Run it

You need three things running: Argus, the test app, and (optionally) the traffic generator.

**1. Start Argus** (from the repo root; see the main README for `.env`):

```
go run ./cmd/argus
```

**2. Create an ingest key** (once). Sign in as admin and mint a key; it is shown only once:

```bash
TOKEN=$(curl -s localhost:8080/api/v1/auth/login -d '{"email":"admin@example.com","password":"YOUR_PASSWORD"}' | jq -r .access_token)
curl -s localhost:8080/api/v1/api-keys -H "Authorization: Bearer $TOKEN" -d '{"name":"testapp"}' | jq -r .key
```

(PowerShell: `Invoke-RestMethod` the same two URLs, or create the key from the Argus app's API via any HTTP client.)

**3. Start the test app** with that key:

```bash
cd testapp
ARGUS_URL=http://localhost:8080 ARGUS_API_KEY=argus_xxxxxxxx go run .
```

```powershell
cd testapp
$env:ARGUS_API_KEY = "argus_xxxxxxxx"; go run .
```

Without `ARGUS_API_KEY` the app still runs and prints its logs to the console; nothing is sent.

| Variable | Default | Purpose |
|---|---|---|
| `ARGUS_URL` | `http://localhost:8080` | Argus base URL |
| `ARGUS_API_KEY` | none | ingest key; unset = console only |
| `TESTAPP_ADDR` | `:9090` | listen address |
| `TESTAPP_DB` | `testapp.db` | SQLite file |
| `TESTAPP_ENV` | `local` | value of the `env` label |

**4. Make some noise.** Either call routes by hand or run the generator:

```bash
go run ./cmd/traffic -duration 2m -rps 15      # a realistic mix; prints a summary at the end
go run ./cmd/traffic -dashboard -argus http://localhost:8080 -email admin@example.com -password YOUR_PASSWORD
```

The second command creates a **Test app** dashboard (six panels: requests by status, server errors by route, busiest routes, 5xx stat, log volume by level, errors and warnings). It is safe to run twice. Then open the Argus app: Dashboards tab, Test app.

## Routes

| Route | What it does | Typical log |
|---|---|---|
| `GET /items?q=` | list items (GORM `LIKE`) | 200, one SQL line |
| `POST /items` `{name,qty}` | create | 201, `item created`; 400/422 on bad input (warn) |
| `GET /items/:id` | fetch one | 200, or 404 (warn) |
| `DELETE /items/:id` | delete | 204, or 404 (warn) |
| `POST /seed?n=` | insert up to 500 random items | 201, `seeded items` |
| `GET /slow?ms=` | sleeps (default 800, max 5000) | 200; `slow handler` warning at 500 ms or more |
| `GET /flaky?p=` | fails with probability p (default 0.3) | 502 + `upstream dependency failed` (error) |
| `GET /error` | always fails | 500 + `unexpected failure` (error) |
| `GET /panic` | panics | 500 + `panic recovered` with stack trace (error); the server keeps running |
| `GET /noise?n=` | writes n extra lines at debug/info/warn/error | 200 |
| `GET /healthz`, `GET /` | health and route list | 200 |

## What lands in Argus

Stream labels: `{service="testapp", env="local"}`. Everything else is in the JSON line, because request ids and item ids must never be labels.

```json
{"time":"...","level":"warn","msg":"request","request_id":"3a768b2a809ff616","method":"GET","route":"/items/:id","path":"/items/424242","status":404,"latency_ms":0,"bytes":29,"client_ip":"127.0.0.1"}
{"time":"...","level":"info","msg":"query","component":"gorm","sql":"SELECT * FROM `items` WHERE id = \"170\" ...","rows":1,"duration_ms":0}
```

- One `request` line per request: 5xx are `error`, 4xx are `warn`. `route` is the route template (`/items/:id`), so it is safe to group by; the raw URL is in `path`.
- Every GORM query is a line with its SQL, rows and duration; queries over 200 ms are `slow query` warnings, failures are `query failed`.
- Send `X-Request-Id` yourself and it is echoed back and logged, which makes one request easy to find.

Queries to try in **Explore** (set the range to the last 15 minutes):

```
{service="testapp"} | json | level="error"
{service="testapp"} | json | msg="request" | status>=500
{service="testapp"} | json | msg="request" | route="/items/:id" | status=404
{service="testapp"} | json | component="gorm" | duration_ms>50
{service="testapp"} |= "panic recovered"
sum by (route) (rate({service="testapp"} | json | msg="request" [1m]))
topk(3, sum by (route) (count_over_time({service="testapp"} | json | status>=500 [5m])))
```

Open **Live Tail** with `{service="testapp"} | json | level=~"error|warn"`, then run the traffic generator and watch lines arrive.

## How the shipper behaves

`internal/argus` is the part worth copying into a real service:

- **Never blocks the request path.** Logging puts a line on a bounded queue (10,000). If Argus is slow or down and the queue fills, lines are dropped and counted, not waited on.
- **Batches** (200 lines or 1 s), **gzips**, and sends NDJSON to `/api/v1/ingest/push`.
- **Retries** 429s, 5xx and network errors 3 times with exponential backoff, then drops the batch. A bad key (4xx) is dropped immediately, because retrying cannot help.
- **Flushes on shutdown** (Ctrl-C), and prints `sent` and `dropped` counts.

To see the "Argus is down" behavior, stop Argus while running the traffic generator: request latency in the test app does not change, and the shipper logs `argus: dropping N lines ...` to stderr.

## Tests

```
cd testapp && go test -race ./...
```

The route tests use a temp SQLite file; the shipper tests use `httptest` servers (batching, gzip, labels, retries, full queue, shutdown flush). The traffic tests check the route mix and the dashboard setup against a fake Argus.

Editors: because `testapp/` is a separate module, gopls may say it is "not included in your workspace". Add a local `go.work` (it is git-ignored) with `go work init . ./testapp`, or open `testapp/` on its own.
