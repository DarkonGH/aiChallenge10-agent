#!/usr/bin/env bash
# День 18: пользователь просит агента запустить проверку курса по расписанию.
# Модель вызывает локальный инструмент run_forex_schedule, тот — MCP forex,
# а числовой отчёт и текстовая сводка строятся по всем замерам с начала.
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT=${ROOT:-/tmp/day18-chat}
DATA="$ROOT/data"
STORE="$ROOT/forex.jsonl"
LOG=${LOG:-/tmp/day18-chat.log}
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}
SUMMARY_MODEL=${SUMMARY_MODEL:-qwen2.5:14b}
SUMMARY_BASE_URL=${SUMMARY_BASE_URL:-http://localhost:11434/v1}

set -a
[ -f "$HOME/.env" ] && . "$HOME/.env"
set +a
# Для демонстрации сводка должна идти в локальную Ollama, а не в облако
# из унаследованного LLM_SUMMARY_BASE_URL.
export LLM_SUMMARY_BASE_URL="$SUMMARY_BASE_URL"
export LLM_SUMMARY_API_KEY=${LLM_SUMMARY_API_KEY:-ollama}
export LLM_SUMMARY_MODEL="$SUMMARY_MODEL"
export LLM_INVARIANT_MODEL="$SUMMARY_MODEL"

rm -rf "$ROOT"
mkdir -p "$DATA" bin

GOTOOLCHAIN=local go build -o bin/mcp-forex ./cmd/mcp-forex
GOTOOLCHAIN=local go build -o agent .

FOREX_STORE="$STORE" ./agent \
  -data "$DATA" \
  -session demo \
  -model "$MODEL" \
  -summary-model "$SUMMARY_MODEL" \
  -mcp-config testdata/day18-mcp.json \
  -mcp-approval=allow \
  -no-memory \
  -skip-profile \
  -no-reasoning \
  -script testdata/day18-chat.txt \
  -script-delay 2s \
  2>&1 | tee "$LOG"

python3 scripts/check_day18_chat.py "$STORE" "$LOG" "$DATA"
