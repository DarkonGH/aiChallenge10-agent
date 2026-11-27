#!/usr/bin/env bash
# Устойчивость персонализации: десять вопросов подряд в одном профиле.
# Проверяем, на каком ответе предпочтения (длина, код, обращение) перестают
# соблюдаться.
#
#   scripts/profile_stability.sh [каталог]
#   MODEL=qwen2.5:14b scripts/profile_stability.sh
set -euo pipefail
cd "$(dirname "$0")/.."

data=${1:-/tmp/day12-stability}
model=${MODEL:-glm-5.3-flash}
root="$data/кратко"

rm -rf "$data"
mkdir -p "$root/profiles/кратко"
cp testdata/day12-profiles/кратко/profile.json "$root/profiles/кратко/profile.json"
{ grep -v '^#' testdata/day12-stability.txt | sed '/^[[:space:]]*$/d'; echo '/exit'; } > "$root/script.txt"

./goagent -data "$root" -session run -profile кратко \
  -model "$model" -summary-model "$model" -no-reasoning -skip-profile -script "$root/script.txt" \
  > "$root/run.log" 2>&1

python3 scripts/check_profile.py --data "$data" --profiles кратко --stability
