#!/usr/bin/env bash
# Generates sustained load against a running testapp. Disabled by default in
# process-compose.yaml; enable it from the process-compose TUI.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT/testapp"

exec go run ./cmd/traffic -duration 10m -rps 10
