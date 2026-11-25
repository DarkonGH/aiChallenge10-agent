#!/usr/bin/env bash
# День 17: свой MCP-сервер вокруг API ЦБ РФ + локальный calc.
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT=${ROOT:-/tmp/day17-demo}
DATA="$ROOT/data"
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}
SUMMARY_MODEL=${SUMMARY_MODEL:-qwen2.5:14b}
SCRIPT_DELAY=${SCRIPT_DELAY:-2s}

set -a
[ -f "$HOME/.env" ] && . "$HOME/.env"
set +a

export LLM_SUMMARY_BASE_URL=${LLM_SUMMARY_BASE_URL:-http://localhost:11434/v1}
export LLM_SUMMARY_API_KEY=${LLM_SUMMARY_API_KEY:-ollama}
export LLM_SUMMARY_MODEL=${LLM_SUMMARY_MODEL:-$SUMMARY_MODEL}
export LLM_INVARIANT_MODEL=${LLM_INVARIANT_MODEL:-$SUMMARY_MODEL}

rm -rf "$ROOT"
mkdir -p "$DATA" bin

go build -o bin/mcp-cbr ./cmd/mcp-cbr
go build -o agent .

./agent -data "$DATA" -session demo \
  -model "$MODEL" -summary-model "$SUMMARY_MODEL" \
  -no-reasoning -skip-profile -no-memory \
  -mcp-approval=allow -mcp-guard=heuristic \
  -dump-request "$ROOT/requests.ndjson" \
  -script testdata/day17-demo.txt -script-delay "$SCRIPT_DELAY" \
  2>&1 | tee /tmp/day17-demo.log

python3 scripts/check_day17.py "$DATA" "$ROOT/requests.ndjson"
