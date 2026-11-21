#!/usr/bin/env python3
"""Синтез asciicast-v2 из session.jsonl удачного прогона (день 13/14).

Зачем: живая запись демо на модели нестабильна (модель недетерминирована,
иногда не доходит до финала). Удачный прогон — единственный полный; здесь тот
же диалог перекладывается в каст без новых вызовов модели: user-реплики
печатаются как 'agent> <текст>', assistant-текст и вывод инструмента — следом,
память — приглушённо, отказ кода — строкой «⛔ код: …». Дальше каст собирает
render_cast.py с карточками из testdata/dayNN-cards.json.

    python3 scripts/run_to_cast.py /tmp/day14-run-ok/demo/session.jsonl /tmp/day14-demo.cast \
        --commands testdata/day14-commands.json

The session log has no command events: commands and their output are printed to
stdout but are not part of the dialogue. --commands adds those blocks back by
the text of the next user message, so the video shows an explicit invariant
change instead of a jump from refusal to agreement.
"""

import argparse
import json
import re
import textwrap
import time

NOTICE_PREFIX = "[проверка кода · инварианты] "

WIDTH, HEIGHT = 148, 50
# Терминал переносит строки на границе колонок; если не обернуть текст заранее,
# длинная строка рвётся посреди слова и в видео получаются «хаотичные переносы».
COLS = WIDTH - 2  # оставляем запас справа, чтобы слово не упиралось в край


def plain_text(text):
    """Приводит ответ модели к тому, что видно в терминале.

    Модель отвечает Markdown-разметкой, а TUI её не рендерит: в кадре
    оставались бы **жирные** звёздочки и обратные кавычки. Знак рубля Menlo
    не содержит и рисует квадратом, поэтому заменяем его на «руб.».
    """
    text = re.sub(r"\*\*(.+?)\*\*", r"\1", text)
    text = re.sub(r"(?m)^#{1,6}\s+", "", text)
    text = text.replace("`", "")
    text = text.replace("₽", "руб.")
    return text


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


def render_memory_event(ev):
    """Память дня 15: показываем «было → стало», а не только новые записи.

    В событии memory лежат два представления: memory — дифф для rebuilt
    store, changes — журнал по ключам. Для видео важен второй: он объясняет
    замену и делает устаревание видимым.
    """
    changes = ev.get("changes") or []
    if not changes:
        return render_memory(ev.get("memory", {}))
    parts = []
    for c in changes:
        layer = c.get("layer", "?")
        key = c.get("key", "?")
        kind = c.get("kind")
        if kind == "update":
            parts.append(f"{layer}: {key}: {c.get('old', '')} → {c.get('new', '')}")
        elif kind == "add":
            parts.append(f"{layer}: + {key}: {c.get('new', '')}")
        elif kind == "remove":
            parts.append(f"{layer}: − {key} (было: {c.get('old', '')})")
        elif kind == "stale":
            parts.append(f"{layer}: {key}: устаревшее значение отклонено")
        elif kind == "evict":
            parts.append(f"{layer}: вытеснено {key}")
    return "; ".join(parts)


def convert(in_path, out_path, commands_path=""):
    commands = []
    if commands_path:
        with open(commands_path, encoding="utf-8") as f:
            commands = json.load(f)
    pending = list(commands)

    events = []
    header = {}
    pending_memory = []

    def flush_memory():
        for text in pending_memory:
            events.append(("memory", "", text))
        pending_memory.clear()

    for line in open(in_path, encoding="utf-8"):
        ev = json.loads(line)
        typ = ev.get("type")
        if typ == "session" and not header:
            header = ev
        if typ == "message":
            m = ev["message"]
            role = m.get("role")
            if role == "tool":
                flush_memory()
                name = m.get("name", "инструмент")
                prefix = f"  [инструмент {name}] "
                text = wrap_text(plain_text((m.get("content") or "").strip()), COLS - len(prefix))
                events.append(("tool", name, text))
            elif role == "user":
                raw = (m.get("content") or "").strip()
                if raw.startswith(NOTICE_PREFIX):
                    flush_memory()
                    # The gate's rejection is part of the story: it shows that
                    # the draft never reached stdout but stayed in the journal.
                    text = wrap_text(plain_text(raw[len(NOTICE_PREFIX):]), COLS - len("  ⛔ код: "))
                    events.append(("notice", "", text))
                else:
                    for c in list(pending):
                        if c.get("before") and c["before"] in raw:
                            events.append(("command", "", plain_text(c.get("command", ""))))
                            for out_line in c.get("output", []):
                                events.append(("command_out", "", plain_text(out_line)))
                            pending.remove(c)
                    flush_memory()
                    text = wrap_text(plain_text(raw), COLS - len("agent> "))
                    events.append(("user", "", text))
            elif role == "assistant":
                flush_memory()
                if m.get("tool_calls") or not (m.get("content") or "").strip():
                    continue
                events.append(("assistant", "", wrap_text(plain_text(m["content"].strip()))))
        elif typ == "memory":
            text = render_memory_event(ev)
            if text:
                pending_memory.append(text)

    flush_memory()

    # Команды без якоря-реплики (например, финальный /task) добавляются в
    # конец: в session.jsonl для них нет user-сообщения, к которому их можно
    # привязать.
    for c in [x for x in pending if x.get("after_last")]:
        events.append(("command", "", plain_text(c.get("command", ""))))
        for out_line in c.get("output", []):
            events.append(("command_out", "", plain_text(out_line)))
        pending.remove(c)

    chat = header.get("chat_model") or "модель"
    summary = header.get("summary_model") or chat
    banner = (f"сессия demo · модель {chat} · сжатие и память {summary}\n"
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
        elif kind == "notice":
            out.append((round(t, 3), "o", f"  ⛔ код: {text}\n"))
            t += 0.8
        elif kind == "command":
            body = wrap_text(text, COLS - len("agent> "))
            out.append((round(t, 3), "o", f"agent> {body}\n\n"))
            t += 0.9
        elif kind == "command_out":
            body = wrap_text(text, COLS - 2)
            out.append((round(t, 3), "o", f"  {body}\n"))
            t += 0.5
        elif kind == "memory":
            # Память приходит из журнала отдельным событием и тоже содержит
            # «₽»; без plain_text шрифт рисует его квадратом. Обёртка нужна,
            # чтобы длинная строка не рвалась посреди слова на краю экрана.
            prefix = "  ↳ память: "
            body = wrap_text(plain_text(text), COLS - len(prefix))
            body = body.replace("\n", "\n  ")
            out.append((round(t, 3), "o", f"{prefix}{body}\n"))
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
    parser = argparse.ArgumentParser(description="синтез asciicast из session.jsonl")
    parser.add_argument("session", help="session.jsonl удачного прогона")
    parser.add_argument("out", help="куда записать .cast")
    parser.add_argument("--commands", default="",
                        help="JSON с командами и их выводом (не попадают в session.jsonl)")
    args = parser.parse_args()
    convert(args.session, args.out, args.commands)
