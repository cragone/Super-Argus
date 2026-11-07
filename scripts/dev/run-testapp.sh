#!/usr/bin/env bash
# Starts testapp with the key minted by scripts/dev/mint-key.sh, if any.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT/testapp"

if [ -f .env.generated ]; then
  set -a
  # shellcheck disable=SC1091
  source .env.generated
  set +a
fi

exec go run .
