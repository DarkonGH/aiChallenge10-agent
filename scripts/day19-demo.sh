#!/usr/bin/env bash
# День 19: четыре сценария вокруг forex + save_to_file.
# 1) успешная цепочка, 2) ошибка первого шага, 3) деление на ноль,
# 4) ошибка сохранения в ~/secret. Запись stdout идёт в /tmp/day19-demo.log.
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT=${ROOT:-/tmp/day19-demo}
DATA="$ROOT/data"
WORK="$ROOT/work"
REPORTS="$WORK/reports"
LOG=${LOG:-/tmp/day19-demo.log}
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}

set -a
[ -f "$HOME/.env" ] && . "$HOME/.env"
set +a

rm -rf "$ROOT"
mkdir -p "$DATA" "$REPORTS" bin

GOTOOLCHAIN=local go build -o goagent .
GOTOOLCHAIN=local go build -o bin/mcp-forex ./cmd/mcp-forex
GOTOOLCHAIN=local go build -o bin/mcp-pipeline ./cmd/mcp-pipeline

FOREX_STORE="$ROOT/forex.jsonl" \
PIPELINE_SEARCH_ROOT="$WORK" \
PIPELINE_OUT_DIR="$REPORTS" \
./goagent \
  -workdir "$WORK" \
  -data "$DATA" \
  -session demo \
  -model "$MODEL" \
  -mcp-config testdata/day19-mcp.json \
  -mcp-approval=allow \
  -invariant-check keywords \
  -no-memory \
  -skip-profile \
  -no-reasoning \
  -max-tool-chars 6000 \
  -max-prompt-tokens 16000 \
  -max-steps 20 \
  -script testdata/day19-chat.txt \
  -script-delay 2s \
  2>&1 | tee "$LOG"

python3 scripts/check_day19.py "$REPORTS" "$LOG" "$DATA" "$WORK"
