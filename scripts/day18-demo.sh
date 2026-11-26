#!/usr/bin/env bash
# День 18: планировщик собирает USD/RUB через MCP и строит отчёт по всем замерам.
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT=${ROOT:-/tmp/day18-demo}
STORE="$ROOT/forex.jsonl"
LOG=/tmp/day18-demo.log
INTERVAL=${INTERVAL:-10s}
RUNS=${RUNS:-6}
REPORT_EVERY=${REPORT_EVERY:-3}

rm -rf "$ROOT"
mkdir -p "$ROOT" bin

GOTOOLCHAIN=local go build -o bin/mcp-forex ./cmd/mcp-forex
GOTOOLCHAIN=local go build -o agent .

FOREX_STORE="$STORE" ./agent \
  -mcp-config testdata/day18-mcp.json \
  -mcp-approval=allow \
  -schedule-interval "$INTERVAL" \
  -schedule-runs "$RUNS" \
  -schedule-report-every "$REPORT_EVERY" \
  -schedule-pair USD/RUB \
  2>&1 | tee "$LOG"

python3 scripts/check_day18.py "$STORE" "$LOG"
