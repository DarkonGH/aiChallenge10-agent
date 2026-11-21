#!/usr/bin/env python3
"""Проверка дня 15 по журналу сессии и файлам памяти.

Считает не «на глаз», а по событиям: каждое изменение памяти лежит в
session.jsonl с парой old → new, поэтому видно, что именно заменилось.
Устаревшим считается ответ, где старое значение названо как актуальное:
упоминание «было 300 000» или «парковка отменена» — не устаревание.

    python3 scripts/check_memory_conflicts.py /tmp/day15-demo
"""

import json
import sys
from datetime import datetime
from pathlib import Path

# Вопросы, после которых проверяется актуальное значение. Маркеры ищутся в
# нормализованном тексте реплики пользователя.
CHECKS = [
    {
        "key": "срок",
        "question": "какой сейчас срок",
        "new": "20 декабря",
        "old": "15 декабря",
    },
    {
        "key": "парковка",
        "question": "нужна ли нам парковка",
        "new": None,
        "old": "20 машин",
        "removed": True,
    },
    {
        "key": "вариант",
        "question": "какой вариант теперь основной",
        "new": "готовый офис",
        "old": "коворкинг",
    },
    {
        "key": "бюджет",
        "question": "какой бюджет сейчас",
        "new": "350 000",
        "old": "300 000",
    },
]
FINAL_QUESTION = "назови актуальные вводные"

# Старое значение в таком предложении — история, а не текущее состояние.
ALLOW = (
    "было",
    "вместо",
    "увеличен с",
    "увеличили с",
    "сдвинул",
    "отмен",
    "отпад",
    "фильтр",
    "убрать",
    "убира",
    "снима",
    "больше не",
    "не рассматрив",
    "не нужн",
    "не требу",
    "прежн",
    "раньше",
    "ранее",
    "до ",
    "с ",
)


def norm(s):
    s = (s or "").lower().replace("ё", "е").replace("\u00a0", " ")
    return " ".join(s.split())


def sentences(text):
    text = (text or "").replace("\n", " ")
    out, cur = [], []
    for ch in text:
        cur.append(ch)
        if ch in ".!?;":
            s = "".join(cur).strip()
            if s:
                out.append(s)
            cur = []
    s = "".join(cur).strip()
    if s:
        out.append(s)
    return out


def stale_mention(text, old):
    """True, если old назван как актуальное, а не как история/отмена."""
    if not old:
        return False
    o = norm(old)
    for s in sentences(text):
        ns = norm(s)
        if o not in ns:
            continue
        if any(m in ns for m in ALLOW):
            continue
        return True
    return False


def apply_changes(state, changes):
    for c in changes or []:
        key = norm(c.get("key"))
        kind = c.get("kind")
        if kind in ("add", "update"):
            state[key] = c.get("new")
        elif kind in ("remove", "evict"):
            state.pop(key, None)


def cleanup_time(events):
    """Когда рабочая память в последний раз опустела и было ли ей что терять.

    Смотрим только события памяти с изменениями: если экстрактор очистил
    working раньше перехода задачи в done, это не «очистка после завершения»,
    а потеря временных фактов по ходу диалога.
    """
    state = set()
    had_working = False
    empty_at = ""
    for ev in events:
        if ev.get("type") != "memory":
            continue
        ch = ev.get("changes") or []
        if not ch:
            continue
        at = ch[0].get("at") or ""
        for c in ch:
            if c.get("layer") != "working":
                continue
            key = norm(c.get("key"))
            if c.get("kind") in ("add", "update"):
                state.add(key)
            elif c.get("kind") in ("remove", "evict"):
                state.discard(key)
        if state:
            had_working = True
            empty_at = ""
        elif had_working and not empty_at:
            empty_at = at
    return empty_at


def read_json(path, default):
    try:
        return json.loads(Path(path).read_text(encoding="utf-8"))
    except FileNotFoundError:
        return default
    except json.JSONDecodeError as e:
        print(f"не читается {path}: {e}")
        return default


def store_values(data, session):
    """Словарь ключ → значение по всем слоям — то, что реально уходит в промпт."""
    base = Path(data) / session / "memory"
    values = {}
    working = read_json(base / "working.json", []) or []
    for e in working:
        values[norm(e.get("key"))] = e.get("value")
    long = read_json(base / "long.json", {}) or {}
    for layer in ("decisions", "knowledge"):
        for e in long.get(layer, []) or []:
            values[norm(e.get("key"))] = e.get("value")
    return values


def main():
    if len(sys.argv) < 2:
        print("нужно: check_memory_conflicts.py <data-каталог>")
        return 2
    data = Path(sys.argv[1])
    session_file = data / "demo" / "session.jsonl"
    if not session_file.exists():
        # Допускаем путь прямо к session.jsonl.
        if (data / "session.jsonl").exists():
            session_file = data / "session.jsonl"
            data = session_file.parent.parent
        else:
            print(f"нет файла {session_file}")
            return 2
    session = session_file.parent.name

    events = [json.loads(l) for l in session_file.read_text(encoding="utf-8").splitlines() if l.strip()]

    changes = []
    turns = []  # (user_text, final_answer, state_after_turn)
    state = {}
    current = None
    for ev in events:
        typ = ev.get("type")
        if typ == "memory":
            ch = ev.get("changes") or []
            changes.extend(ch)
            apply_changes(state, ch)
            if current is not None:
                current["state"] = dict(state)
            continue
        elif typ != "message":
            continue
        m = ev["message"]
        role = m.get("role")
        if role == "user":
            current = {"user": m.get("content") or "", "answer": "", "state": dict(state)}
            turns.append(current)
        elif role == "assistant" and current is not None:
            if m.get("tool_calls") or not (m.get("content") or "").strip():
                continue
            current["answer"] = m.get("content") or ""
            current["state"] = dict(state)

    # 1. Журнал: какие ключи менялись и есть ли пара «было → стало».
    changed = {}
    for c in changes:
        key = norm(c.get("key"))
        if c.get("kind") in ("update", "remove", "stale", "evict"):
            changed.setdefault(key, []).append(c)
    expected = {norm(c["key"]) for c in CHECKS}
    journal_ok = True
    for key in expected:
        items = changed.get(key, [])
        with_old = [c for c in items if c.get("old")]
        if not with_old:
            journal_ok = False
            print(f"  журнал: ключ {key} не менялся или нет старого значения")
        else:
            for c in with_old:
                if c.get("kind") == "update" and not c.get("new"):
                    journal_ok = False
                    print(f"  журнал: замена {key} без нового значения")

    # 2. Ответы после каждого изменения: актуальное значение, без устаревшего.
    stale = 0
    checked = 0
    for turn in turns:
        user = norm(turn["user"])
        answer = turn["answer"]
        for spec in CHECKS:
            if spec["question"] not in user:
                continue
            checked += 1
            new, old = spec["new"], spec["old"]
            na = norm(answer)
            if spec.get("removed"):
                ok = any(x in na for x in ("не нужна", "не требуется", "больше не нужна", "отменена", "отменено"))
            else:
                ok = norm(new) in na
            if not ok:
                print(f"  НЕТ АКТУАЛЬНОГО: «{turn['user'][:60]}…» → {na[:120]}")
            if stale_mention(answer, old):
                stale += 1
                print(f"  УСТАРЕВШЕЕ: «{turn['user'][:60]}…» называет {old} как актуальное")
        if FINAL_QUESTION in user:
            checked += 1
            na = norm(answer)
            for spec in CHECKS:
                if spec.get("removed"):
                    ok = any(x in na for x in ("не нужна", "не требуется", "отмен"))
                else:
                    ok = norm(spec["new"]) in na
                if not ok:
                    print(f"  НЕТ В СВОДКЕ: {spec['key']} = {spec['new'] or 'не требуется'}")
                if stale_mention(answer, spec["old"]):
                    stale += 1
                    print(f"  УСТАРЕВШЕЕ В СВОДКЕ: {spec['old']}")

    # 3. Файлы памяти после завершения задачи.
    working = read_json(data / session / "memory" / "working.json", []) or []
    long = read_json(data / session / "memory" / "long.json", {}) or {}
    decisions = long.get("decisions", []) or []
    knowledge = long.get("knowledge", []) or []
    task = read_json(data / session / "task.json", {}) or {}
    stage = task.get("stage", "")
    done_at = ""
    for tr in task.get("history", []) or []:
        if tr.get("to") == "done":
            done_at = tr.get("at") or ""
    emptied_at = cleanup_time(events)
    cleanup_ok = False
    if stage == "done" and done_at and emptied_at:
        try:
            cleanup_ok = datetime.fromisoformat(emptied_at) >= datetime.fromisoformat(done_at)
        except ValueError:
            cleanup_ok = emptied_at >= done_at

    values = store_values(data, session)
    all_text = " ".join(norm(v) for v in values.values())
    leaks = [c["old"] for c in CHECKS if c["old"] and norm(c["old"]) in all_text]
    # В удалённых фактах старого значения быть не должно; у обновлённых его
    # тоже не должно быть в текущем store.
    leaks = [x for x in leaks if x]

    target_changed = 0
    for key in expected:
        if any(c.get("old") for c in changed.get(key, [])):
            target_changed += 1

    print()
    print(f"ходов с вопросом об актуальном значении: {checked}")
    print(f"изменений памяти в журнале: {len(changes)}")
    print(f"целевых ключей с заменой/удалением: {target_changed} из {len(expected)}; всего изменённых ключей: {len(changed)}")
    print(f"журнал «было → стало»: {'ok' if journal_ok else 'НЕПОЛНЫЙ'}")
    print(f"устаревших ответов: {stale}")
    print(f"задача: этап {stage or '—'}")
    print(f"working после завершения: {len(working)} записей")
    print(f"очистка working: {'после done' if cleanup_ok else 'НЕ ПОСЛЕ done'}"
          + (f" (done {done_at}, пусто {emptied_at})" if done_at or emptied_at else ""))
    print(f"долговременная память: decisions {len(decisions)}, knowledge {len(knowledge)}")
    print(f"старые значения в текущей памяти: {len(leaks)}" + (f" — {', '.join(leaks)}" if leaks else ""))

    ok = journal_ok and stale == 0 and not leaks and checked >= 4
    ok = ok and len(working) == 0 and cleanup_ok and (len(decisions) + len(knowledge)) > 0
    print("ИТОГ:", "ok" if ok else "ЕСТЬ ПРОБЛЕМЫ")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
