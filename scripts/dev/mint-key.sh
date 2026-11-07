#!/usr/bin/env bash
# Mints a testapp ingest API key against a running Argus server, or reuses
# one minted by a previous run. Writes testapp/.env.generated, which
# scripts/dev/run-testapp.sh sources before starting testapp.
#
# If data/ ever gets wiped (fresh DB, old keys gone), delete
# testapp/.env.generated to force a fresh key on the next run.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"

GEN_FILE="testapp/.env.generated"
ARGUS_URL="${ARGUS_URL:-http://localhost:8080}"

if [ -s "$GEN_FILE" ] && grep -q '^ARGUS_API_KEY=' "$GEN_FILE"; then
  echo "mint-key: reusing existing key in $GEN_FILE"
  exit 0
fi

set -a
# shellcheck disable=SC1091
[ -f .env ] && source .env
set +a

EMAIL="${ARGUS_ADMIN_EMAIL:?ARGUS_ADMIN_EMAIL not set (check .env)}"
PASSWORD="${ARGUS_ADMIN_PASSWORD:?ARGUS_ADMIN_PASSWORD not set (check .env)}"

echo "mint-key: logging in as $EMAIL"
LOGIN_RESP="$(curl -sf -X POST "$ARGUS_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")"

ACCESS_TOKEN="$(printf '%s' "$LOGIN_RESP" | jq -r '.access_token // empty')"
if [ -z "$ACCESS_TOKEN" ]; then
  echo "mint-key: login failed: $LOGIN_RESP" >&2
  exit 1
fi

echo "mint-key: creating ingest API key"
KEY_RESP="$(curl -sf -X POST "$ARGUS_URL/api/v1/api-keys" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"name":"testapp-dev"}')"

API_KEY="$(printf '%s' "$KEY_RESP" | jq -r '.key // empty')"
if [ -z "$API_KEY" ]; then
  echo "mint-key: key creation failed: $KEY_RESP" >&2
  exit 1
fi

cat > "$GEN_FILE" <<EOF
ARGUS_URL=$ARGUS_URL
ARGUS_API_KEY=$API_KEY
EOF

echo "mint-key: wrote $GEN_FILE"
