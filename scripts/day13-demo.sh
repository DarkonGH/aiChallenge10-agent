#!/usr/bin/env bash
# Подготовка данных и запуск демо дня 13 (для записи в каст).
#
# Автомат задачи: planning → execution → validation → done, переходы
# ассистента проверяет код. Сценарий показывает: прямой запрет нарушить
# порядок, гейт утверждённого плана, паузу и продолжение с того же места.
#
#   scripts/day13-demo.sh [каталог данных]
set -euo pipefail
cd "$(dirname "$0")/.."

data=${1:-/tmp/day13-video}
model=${MODEL:-glm-5.3-flash}

rm -rf "$data"
mkdir -p "$data"

exec ./agent -data "$data" -session demo \
  -model "$model" -summary-model "$model" -no-reasoning -script testdata/day13-demo.txt
