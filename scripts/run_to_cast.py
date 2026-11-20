#!/usr/bin/env python3
"""Синтез asciicast-v2 из session.jsonl удачного прогона дня 13.

Зачем: живая запись демо на deepseek-v4-flash нестабильна (модель недетерминирована,
иногда не доходит до done). Удачный прогон (лог /tmp/day13-run-ok/demo/session.jsonl,
28 событий, дошёл до done, 3 перехода применены, чекер exit 0) — единственный полный.
Здесь тот же диалог перекладывается в каст без новых вызовов модели: user-реплики
печатаются как 'agent> <текст>', assistant-текст и вывод инструмента — следом,
память — приглушённо. Дальше каст собирает render_cast.py с testdata/day13-cards.json.

    python3 scripts/run_to_cast.py /tmp/day13-run-ok/demo/session.jsonl /tmp/day13-demo.cast
"""

import json
import sys
import textwrap
import time

WIDTH, HEIGHT = 148, 50
# Терминал переносит строки на границе колонок; если не обернуть текст заранее,
# длинная строка рвётся посреди слова и в видео получаются «хаотичные переносы».
COLS = WIDTH - 2  # оставляем запас справа, чтобы слово не упиралось в край


def wrap_text(text, width=COLS):
    """Переносит каждую строку по словам, сохраняя абзацы и списки."""
    out = []
    for line in text.split("\n"):
        if line == "":
            out.append("")
        else:
            out.extend(textwrap.wrap(line, width=width))
    return "\n".join(out)


def render_memory(mem):
    """mem — dict {layer: [entries]} или {layer: строка/объект}."""
    parts = []
    for layer, entries in mem.items():
        if not entries:
            continue
        if isinstance(entries, list):
            vals = ", ".join(
                f"{e.get('key')}: {e.get('value')}"
                for e in entries if isinstance(e, dict))
        else:
            vals = str(entries)
        parts.append(f"{layer}: {vals}")
    return "; ".join(parts)


def convert(in_path, out_path):
    events = []
    for line in open(in_path, encoding="utf-8"):
        ev = json.loads(line)
        typ = ev.get("type")
        if typ == "message":
            m = ev["message"]
            role = m.get("role")
            if role == "tool":
                name = m.get("name", "инструмент")
                prefix = f"  [инструмент {name}] "
                text = wrap_text((m.get("content") or "").strip(), COLS - len(prefix))
                events.append(("tool", name, text))
            elif role == "user":
                text = wrap_text((m.get("content") or "").strip(), COLS - len("agent> "))
                events.append(("user", "", text))
            elif role == "assistant":
                if m.get("tool_calls") or not (m.get("content") or "").strip():
                    continue
                events.append(("assistant", "", wrap_text(m["content"].strip())))
        elif typ == "memory":
            text = render_memory(ev.get("memory", {}))
            if text:
                events.append(("memory", "", text))

    banner = ("сессия demo · модель deepseek-v4-flash · сжатие deepseek-v4-flash\n"
              "/help — команды: /model — модель, /session — сессия, /branch — ветки, /exit — выход; статистика — после каждого ответа\n\n")

    out = []
    t = 0.0
    out.append((t, "o", banner))
    for kind, name, text in events:
        if kind == "user":
            out.append((round(t, 3), "o", f"agent> {text}\n\n"))
            t += 1.0
        elif kind == "assistant":
            out.append((round(t, 3), "o", f"{text}\n"))
            t += 1.4
        elif kind == "tool":
            out.append((round(t, 3), "o", f"  [инструмент {name}] {text}\n"))
            t += 0.6
        elif kind == "memory":
            out.append((round(t, 3), "o", f"  ↳ память: {text}\n"))
            t += 0.4

    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({"version": 2, "width": WIDTH, "height": HEIGHT,
                   "timestamp": time.time(), "env": {"TERM": "xterm-256color"}},
                  f, ensure_ascii=False)
        f.write("\n")
        for t_cur, kind, data in out:
            f.write(json.dumps([t_cur, kind, data], ensure_ascii=False) + "\n")
    print(f"каст синтезирован: {out_path} ({len(out)} событий, {t:.1f} с)")


if __name__ == "__main__":
    if len(sys.argv) != 3:
        sys.exit("использование: run_to_cast.py <session.jsonl> <out.cast>")
    convert(sys.argv[1], sys.argv[2])
