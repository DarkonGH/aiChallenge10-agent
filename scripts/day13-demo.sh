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
model=${MODEL:-opencode-go/deepseek-v4.1-flash}
# Провайдер считает запросы в скользящем окне 60 с (на практике ~3 запроса),
# а один ход — это два вызова: ответ и экстрактор памяти. Пауза между ходами
# держит прогон в лимите; всплески добивает повтором на 429 в клиенте.
delay=${SCRIPT_DELAY:-45s}

rm -rf "$data"
mkdir -p "$data"

exec ./agent -data "$data" -session demo \
  -model "$model" -summary-model "$model" -no-reasoning -script testdata/day13-demo.txt \
  -script-delay "$delay"
