#!/usr/bin/env bash
# Day-14 demo: project invariants are checked by code before the answer is
# released. The chat model is remote; the prose checker is a local Ollama model
# (the same endpoint as compaction), so the extra per-answer call costs nothing.
set -euo pipefail
cd "$(dirname "$0")/.."

DATA=${DATA:-/tmp/day14-demo}
MODEL=${MODEL:-opencode-go/deepseek-v4.1-flash}
SUMMARY_MODEL=${SUMMARY_MODEL:-qwen2.5:14b}
SCRIPT_DELAY=${SCRIPT_DELAY:-20s}

# shellcheck disable=SC1090
set -a; [ -f "$HOME/.env" ] && . "$HOME/.env"; set +a

# The local endpoint must win over ~/.env's summary settings: the checker model
# and the endpoint have to match, otherwise qwen2.5:14b is sent to opencode.
# A ${VAR:-default} would silently keep an LLM_SUMMARY_* value from ~/.env, so
# the demo uses its own names (SUMMARY_BASE_URL/SUMMARY_API_KEY) as the escape
# hatch and overwrites the generic variables unconditionally.
export LLM_SUMMARY_BASE_URL=${SUMMARY_BASE_URL:-http://localhost:11434/v1}
export LLM_SUMMARY_API_KEY=${SUMMARY_API_KEY:-ollama}
export LLM_SUMMARY_MODEL="$SUMMARY_MODEL"
export LLM_INVARIANT_MODEL="$SUMMARY_MODEL"

rm -rf "$DATA"
mkdir -p "$DATA"
cp testdata/day14-invariants.json "$DATA/invariants.json"

./goagent \
  -data "$DATA" \
  -session demo \
  -model "$MODEL" \
  -summary-model "$SUMMARY_MODEL" \
  -invariants "$DATA/invariants.json" \
  -invariant-check llm \
  -invariant-model "$SUMMARY_MODEL" \
  -no-reasoning \
  -skip-profile \
  -script testdata/day14-demo.txt \
  -script-delay "$SCRIPT_DELAY"
