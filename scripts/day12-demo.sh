#!/usr/bin/env bash
# Подготовка данных и запуск демо дня 12 (для записи в каст).
#
# Профиль «кратко» начинается пустым и наполняется из диалога — это и есть
# «профиль пополняется сам». Профиль «подробно» заранее лежит на диске, чтобы
# показать переключение и тот же вопрос в другой персоне.
#
#   scripts/day12-demo.sh [каталог данных]
set -euo pipefail
cd "$(dirname "$0")/.."

data=${1:-/tmp/day12-video}
model=${MODEL:-glm-5.3-flash}

rm -rf "$data"
mkdir -p "$data/profiles/подробно"
cp testdata/day12-profiles/подробно/profile.json "$data/profiles/подробно/profile.json"

exec ./goagent -data "$data" -session demo -profile кратко \
  -model "$model" -summary-model "$model" -no-reasoning -skip-profile -script testdata/day12-demo.txt
