#!/usr/bin/env bash
# День 19: один MCP-сервер с тремя инструментами. Локального run_pipeline нет:
# модель сама строит цепочку search → summarize → save_to_file и передаёт данные.
set -euo pipefail
cd "$(dirname "$0")/.."

ROOT=${ROOT:-/tmp/day19-demo}
DATA="$ROOT/data"
WORK="$ROOT/work"
OUT="$WORK/reports"
NOTES="$WORK/notes"
LOG=${LOG:-/tmp/day19-demo.log}
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}

set -a
[ -f "$HOME/.env" ] && . "$HOME/.env"
set +a

rm -rf "$ROOT"
mkdir -p "$DATA" "$OUT" "$NOTES" bin
cp "$PWD"/testdata/day19-notes/*.md "$NOTES"/

GOTOOLCHAIN=local go build -o bin/mcp-pipeline ./cmd/mcp-pipeline
GOTOOLCHAIN=local go build -o goagent .

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
  -max-steps 16 \
  -script testdata/day19-chat.txt \
  -script-delay 2s \
  2>&1 | tee "$LOG"

python3 scripts/check_day19.py "$OUT" "$LOG" "$DATA"
