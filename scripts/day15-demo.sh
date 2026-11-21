#!/usr/bin/env bash
# Демо дня 15: устаревание и конфликты памяти. Чат идёт на opencode, сжатие и
# экстрактор памяти — на локальную qwen2.5:14b (Ollama), поэтому старые
# значения не нужно никуда отправлять, а лимит провайдера не мешает прогону.
set -euo pipefail
cd "$(dirname "$0")/.."

DATA=${DATA:-/tmp/day15-demo}
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}
SUMMARY_MODEL=${SUMMARY_MODEL:-qwen2.5:14b}
SCRIPT_DELAY=${SCRIPT_DELAY:-20s}

# shellcheck disable=SC1090
set -a && . ~/.env && set +a
export LLM_SUMMARY_BASE_URL=${LLM_SUMMARY_BASE_URL:-http://localhost:11434/v1}
export LLM_SUMMARY_API_KEY=${LLM_SUMMARY_API_KEY:-ollama}
export LLM_SUMMARY_MODEL=${LLM_SUMMARY_MODEL:-$SUMMARY_MODEL}
export LLM_INVARIANT_MODEL=${LLM_INVARIANT_MODEL:-$SUMMARY_MODEL}

rm -rf "$DATA"
mkdir -p "$DATA"

echo "демо дня 15: данные $DATA, чат $MODEL, сжатие и память $SUMMARY_MODEL"
./agent -data "$DATA" -session demo \
  -model "$MODEL" -summary-model "$SUMMARY_MODEL" \
  -no-reasoning -skip-profile \
  -script testdata/day15-demo.txt -script-delay "$SCRIPT_DELAY"
