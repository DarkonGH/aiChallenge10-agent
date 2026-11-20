#!/usr/bin/env python3
"""Проверка дня 13: конечный автомат задачи по журналу сессии.

Читает session.jsonl и task.json каталога сессии и проверяет:
  - цепочка применённых переходов task_transition в журнале совпадает с
    историей в task.json (журнал и файл не разошлись);
  - каждый применённый переход разрешён таблицей, включая старт в planning;
  - каждый отклонённый вызов отклонён именно кодом (ошибка инструмента с
    внятной причиной: запрещён, пауза, план не утверждён), а не сбоем.

Таблица переходов продублирована здесь намеренно: чекер сверяет поведение
кода с заданием, а не с самим собой.

    python3 scripts/check_task.py <каталог-сессии>
"""

import json
import os
import sys

STAGES = ["planning", "execution", "validation", "done"]

TRANSITIONS = {
    "planning": ["execution"],
    "execution": ["validation"],
    "validation": ["execution", "done"],
    "done": [],
}

# Фразы, которыми код отказывает в переходе. Отказ без одной из них — не
# контролируемый отказ автомата, а сбой.
REJECT_MARKERS = ["запрещён", "на паузе", "план не утверждён", "не начата", "завершена"]


def load_journal(session_dir):
    calls, results = [], {}
    path = os.path.join(session_dir, "session.jsonl")
    with open(path, encoding="utf-8") as f:
        for line in f:
            ev = json.loads(line)
            if ev.get("type") != "message":
                continue
            m = ev["message"]
            if m.get("role") == "assistant":
                for tc in m.get("tool_calls") or []:
                    fn = tc.get("function", {})
                    if fn.get("name") == "task_transition":
                        calls.append((tc.get("id"), fn.get("arguments", "{}")))
            elif m.get("role") == "tool" and m.get("name") == "task_transition":
                results[m.get("tool_call_id")] = m.get("content", "")
    return calls, results


def load_state(session_dir):
    path = os.path.join(session_dir, "task.json")
    if not os.path.exists(path):
        return None
    with open(path, encoding="utf-8") as f:
        text = f.read().strip()
    if not text:
        return None
    return json.loads(text)


def main():
    if len(sys.argv) != 2:
        sys.exit("использование: check_task.py <каталог-сессии>")
    session_dir = sys.argv[1]
    state = load_state(session_dir)
    calls, results = load_journal(session_dir)

    problems = []
    applied = rejected = 0
    applied_seq = []  # (from, to) применённых в журнале

    stage = None  # этап по цепочке журнала
    if state and state.get("history"):
        # Старт задачи — всегда первая запись истории; журнал её не содержит,
        # поэтому цепочка журнала начинается уже из planning.
        first = state["history"][0]
        if first.get("from") or first.get("to") != "planning":
            problems.append(f"первая запись истории не старт в planning: {first}")
        else:
            stage = "planning"

    for cid, raw_args in calls:
        try:
            args = json.loads(raw_args)
        except json.JSONDecodeError:
            problems.append(f"аргументы не JSON: {raw_args!r}")
            continue
        to = str(args.get("to", "")).strip().lower()
        result = results.get(cid, "")
        is_reject = result.startswith("ошибка инструмента")

        if is_reject:
            rejected += 1
            if not any(mark in result for mark in REJECT_MARKERS):
                problems.append(f"отказ не похож на отказ кода: {result[:140]}")
            elif to in STAGES and stage is not None and to in TRANSITIONS.get(stage, []):
                # Отказ легального по таблице перехода возможен только из-за
                # гейта плана или паузы — обе фразы уже проверены маркерами.
                if "план не утверждён" not in result and "на паузе" not in result:
                    problems.append(
                        f"отклонён легальный переход {stage} → {to} без причины: {result[:140]}")
            print(f"  отклонён (код): {stage or '—'} → {to}: {result.split('ошибка инструмента task_transition: ')[-1][:90]}")
        else:
            applied += 1
            legal = stage is not None and to in TRANSITIONS.get(stage, [])
            if not legal:
                problems.append(f"применён запрещённый переход {stage or '—'} → {to}")
            applied_seq.append((stage, to))
            print(f"  применён: {stage} → {to}")
            stage = to

    # Сверка с task.json: история файла должна повторять цепочку журнала.
    if state:
        file_seq = [(tr.get("from") or None, tr.get("to"))
                    for tr in (state.get("history") or [])[1:]]
        if file_seq != applied_seq:
            problems.append(f"журнал и task.json разошлись:\n    журнал: {applied_seq}\n    файл:   {file_seq}")
        final = state.get("stage")
        if applied_seq and final != applied_seq[-1][1]:
            problems.append(f"финальный этап файла {final!r}, по журналу {applied_seq[-1][1]!r}")
        print(f"\nфинальный этап: {final or '—'}; переходов применено {applied}, отклонено {rejected}")
    else:
        print(f"\nзадача не создана; переходов применено {applied}, отклонено {rejected}")

    if problems:
        print("ПРОВАЛ:")
        for p in problems:
            print(f"  ✗ {p}")
        sys.exit(1)
    print("ОК: автомат задачи прошёл проверку по журналу")


if __name__ == "__main__":
    main()
