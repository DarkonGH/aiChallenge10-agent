#!/usr/bin/env python3
"""Синтез asciicast из настоящего stdout-лога консоли.

Зачем: run_to_cast.py собирает каст из session.jsonl, но в лог сессии попадают
только сообщения и события памяти. Всё, что агент печатает сам, — футер со
статистикой, «↳ память: …; всего N», строки «↳ task_transition({…}) → …»,
таблицы /memory — в session.jsonl отсутствует. Поэтому видео, собранное из
session.jsonl, неизбежно отличается от консоли: пропадает футер, теряется
«всего N», инструменты показываются в другом формате.

Этот скрипт берёт ровно то, что видел пользователь в терминале, и переносит
строки в каст. Ничего не синтезируется и не додумывается: если строки нет в
логе, её не будет и в видео, а если есть — она будет дословно.

    ./goagent … -script testdata/day15-demo.txt 2>&1 | tee /tmp/day15-demo.log
    python3 scripts/log_to_cast.py /tmp/day15-demo.log /tmp/day15-demo.cast

Дальше каст собирает render_cast.py с карточками из testdata/dayNN-cards.json.
"""

import argparse
import json
import textwrap
import time

WIDTH, HEIGHT = 148, 50
COLS = WIDTH - 2  # запас справа, чтобы слово не упиралось в край


def wrap_line(line, width=COLS):
    """Переносит строку по словам, сохраняя ведущий отступ.

    Терминал переносит по границе колонок, поэтому длинные строки таблиц и
    trace-выводов надо обернуть заранее: иначе в кадре получаются обрывы
    посреди слова. Отступ продолжения повторяет отступ строки, чтобы
    «  ↳ память: …» и «   - ключ: …» оставались читаемыми.
    """
    if not line.strip():
        return [""]
    indent = line[: len(line) - len(line.lstrip())]
    return textwrap.wrap(line, width=width, subsequent_indent=indent,
                         break_long_words=False, break_on_hyphens=False)


def convert(in_path, out_path, line_delay=0.06, prompt_delay=0.9, blank_delay=0.15):
    raw = open(in_path, encoding="utf-8").read().splitlines()

    # Хвост shell-обёртки: «EXIT=0» печатает не агент, а скрипт запуска.
    while raw and (raw[-1].startswith("EXIT=") or not raw[-1].strip()):
        raw.pop()

    out = []
    t = 0.0
    last = ""

    for line in raw:
        line = line.replace("\t", "    ").replace("₽", "руб.")
        for piece in wrap_line(line):
            lead = ""
            # Приглашение в консоли отделено от предыдущего вывода пустой
            # строкой; в логе её нет, потому что prompt печатается сразу после
            # последней строки ответа.
            if piece.startswith("goagent> ") and last and last != "\n":
                lead = "\n"
            out.append((round(t, 3), "o", f"{lead}{piece}\n"))
            last = f"{lead}{piece}\n"
            if piece.startswith("goagent> "):
                t += prompt_delay  # пользователь набирает команду
            elif piece == "":
                t += blank_delay
            else:
                t += line_delay    # строки вывода идут потоком

    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({"version": 2, "width": WIDTH, "height": HEIGHT,
                   "timestamp": time.time(), "env": {"TERM": "xterm-256color"}},
                  f, ensure_ascii=False)
        f.write("\n")
        for t_cur, kind, data in out:
            f.write(json.dumps([t_cur, kind, data], ensure_ascii=False) + "\n")
    print(f"каст из лога: {out_path} ({len(out)} событий, {t:.1f} с)")


if __name__ == "__main__":
    ap = argparse.ArgumentParser(description="синтез asciicast из stdout-лога")
    ap.add_argument("log", help="stdout-лог консольного прогона")
    ap.add_argument("out", help="куда записать .cast")
    ap.add_argument("--line-delay", type=float, default=0.06,
                    help="пауза между строками вывода, секунды")
    ap.add_argument("--prompt-delay", type=float, default=0.9,
                    help="пауза после приглашения goagent>, секунды")
    ap.add_argument("--blank-delay", type=float, default=0.15,
                    help="пауза на пустой строке, секунды")
    args = ap.parse_args()
    convert(args.log, args.out, args.line_delay, args.prompt_delay, args.blank_delay)
