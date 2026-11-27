#!/usr/bin/env bash
# Собирает видео дня 20 из лога прогона.
set -euo pipefail
cd "$(dirname "$0")/.."

LOG=${LOG:-/tmp/day19-demo.log}
CAST=${CAST:-/tmp/day19-demo.cast}
OUT=${OUT:-video/четыре-сценария-день-19.mp4}

python3 scripts/log_to_cast.py "$LOG" "$CAST"
python3 scripts/render_cast.py \
  --cast "$CAST" \
  --out "$OUT" \
  --title "День 19 · Четыре сценария" \
  --subtitle "forex → расчёт → файл" \
  --cards testdata/day19-cards.json

ffprobe -v error -select_streams v:0 \
  -show_entries stream=codec_name,width,height,r_frame_rate \
  -show_entries format=duration \
  -of default=noprint_wrappers=1 "$OUT"
