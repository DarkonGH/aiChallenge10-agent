#!/usr/bin/env bash
# Демо дня 16: MCP-сервер filesystem + локальный calc в одном запросе.
# Общий mcp.json берётся из корня; ${MCP_FS_ROOT} сужает filesystem-сервер
# до демо-каталога, чтобы агент не мог читать остальной диск.
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT=${ROOT:-/tmp/day16-fs}
DATA=${DATA:-$ROOT/sessions}
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}
SUMMARY_MODEL=${SUMMARY_MODEL:-qwen2.5:14b}
SCRIPT_DELAY=${SCRIPT_DELAY:-12s}
GUARD=${GUARD:-heuristic}
ACTION_GUARD=${ACTION_GUARD:-off}
GUARD_MODEL=${GUARD_MODEL:-}

# shellcheck disable=SC1090
set -a && . ~/.env && set +a
export LLM_SUMMARY_BASE_URL=${LLM_SUMMARY_BASE_URL:-http://localhost:11434/v1}
export LLM_SUMMARY_API_KEY=${LLM_SUMMARY_API_KEY:-ollama}
export LLM_SUMMARY_MODEL=${LLM_SUMMARY_MODEL:-$SUMMARY_MODEL}
export LLM_INVARIANT_MODEL=${LLM_INVARIANT_MODEL:-$SUMMARY_MODEL}
export MCP_FS_ROOT=$ROOT/data

rm -rf "$ROOT"
mkdir -p "$MCP_FS_ROOT" "$DATA"

cat > "$MCP_FS_ROOT/expenses.csv" <<'CSV'
month,item,amount
2026-09,аренда коворкинга,12000
2026-10,интернет и связь,4000
2026-11,мебель и оборудование,9000
CSV

go build -o agent .

echo "демо дня 16: MCP filesystem + локальный calc; данные $MCP_FS_ROOT"
./agent -data "$DATA" -session demo \
  -model "$MODEL" -summary-model "$SUMMARY_MODEL" \
  -no-reasoning -skip-profile -no-memory \
  -mcp-approval=allow \
  -mcp-guard="$GUARD" -mcp-guard-model="$GUARD_MODEL" \
  -mcp-action-guard="$ACTION_GUARD" \
  -dump-request "$ROOT/requests.ndjson" \
  -script testdata/day16-demo.txt -script-delay "$SCRIPT_DELAY" \
  2>&1 | tee /tmp/day16-demo.log

python3 scripts/check_mcp.py "$DATA" "$ROOT/requests.ndjson"
