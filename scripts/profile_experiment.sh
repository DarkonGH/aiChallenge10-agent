#!/usr/bin/env bash
# Матрица персонализации: один и тот же вопрос — трём профилям с
# противоположными предпочтениями. У каждого профиля свой каталог данных,
# поэтому сессии, память и журналы не пересекаются.
#
#   scripts/profile_experiment.sh [каталог]
#   MODEL=qwen2.5:14b scripts/profile_experiment.sh
set -euo pipefail
cd "$(dirname "$0")/.."

data=${1:-/tmp/day12-profiles}
model=${MODEL:-glm-5.3-flash}
question=$(grep -v '^#' testdata/day12-question.txt | sed '/^[[:space:]]*$/d')

rm -rf "$data"
for name in кратко подробно смешанный; do
  root="$data/$name"
  mkdir -p "$root/profiles/$name"
  cp "testdata/day12-profiles/$name/profile.json" "$root/profiles/$name/profile.json"
  printf '%s\n/exit\n' "$question" > "$root/script.txt"
  ./goagent -data "$root" -session run -profile "$name" \
    -model "$model" -summary-model "$model" -no-reasoning -skip-profile -script "$root/script.txt" \
    > "$root/run.log" 2>&1
done

python3 scripts/check_profile.py --data "$data" --profiles кратко,подробно,смешанный
