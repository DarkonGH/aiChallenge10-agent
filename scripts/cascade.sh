#!/usr/bin/env bash
# Каскадная проверка памяти: один диалог, после каждой очистки слоя — те же
# восемь контрольных вопросов. В отличие от матрицы (пять независимых копий
# сессии) здесь слои тают по очереди, как в задании, поэтому видно, что
# ломается на каждом шаге.
#
# Окно — 2 сообщения: иначе ответы предыдущего шага остаются в промпте и
# «очищенный» слой продолжает отвечать эхом из диалога.
#
#   scripts/cascade.sh [каталог-данных]
#   MODEL=qwen2.5:14b scripts/cascade.sh
set -euo pipefail
cd "$(dirname "$0")/.."

data=${1:-/tmp/day11-cascade}
model=${MODEL:-glm-5.3-flash}
script=/tmp/day11-cascade.txt

questions=$(grep -v '^#' testdata/day11-questions.txt | sed '/^[[:space:]]*$/d')

{
  cat testdata/day11-memory.txt
  echo "/memory"
  echo "$questions"
  for layer in knowledge decisions working profile; do
    echo "/memory clear $layer"
    echo "$questions"
  done
  echo "/exit"
} > "$script"

rm -rf "$data"
./goagent -data "$data" -session cascade -strategy window -window 2 \
  -model "$model" -summary-model "$model" -no-reasoning -script "$script"

python3 scripts/check_memory.py --log "$data/cascade/session.jsonl"
