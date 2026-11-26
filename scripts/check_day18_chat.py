#!/usr/bin/env python3
"""Проверка чат-сценария дня 18: инструмент, 24 запуска, 2 сводки и общий отчёт."""

import glob
import json
import re
import sys

ok = 0


def check(condition, text):
    global ok
    if condition:
        ok += 1
        print(f"ok   {text}")
    else:
        print(f"FAIL {text}")


def load_ticks(path):
    out = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                out.append(json.loads(line))
    return out


def load_session(data_dir):
    paths = glob.glob(f"{data_dir}/*/session.jsonl")
    if not paths:
        return []
    events = []
    with open(paths[0], encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                events.append(json.loads(line))
    return events


def main():
    store_path, log_path, data_dir = sys.argv[1], sys.argv[2], sys.argv[3]
    ticks = load_ticks(store_path)
    log = open(log_path, encoding="utf-8").read()
    events = load_session(data_dir)

    check(len(ticks) == 24, f"в JSONL 24 записи (найдено {len(ticks)})")
    check(all(t.get("base") == "USD" and t.get("quote") == "RUB" for t in ticks),
          "все записи — USD/RUB")
    check(all(float(t.get("rate", 0)) > 0 for t in ticks),
          "у всех записей положительный курс")
    stamps = [t.get("fetched_at") for t in ticks]
    check(len(set(stamps)) == len(stamps), "все метки времени различаются")

    calls = []
    for ev in events:
        message = ev.get("message") or {}
        for call in message.get("tool_calls") or []:
            calls.append(call)
    schedule_calls = [c for c in calls
                      if (c.get("function") or {}).get("name") == "run_forex_schedule"]
    check(len(schedule_calls) == 1, "модель вызвала run_forex_schedule ровно один раз")
    if schedule_calls:
        args = json.loads(schedule_calls[0]["function"]["arguments"])
        check(args.get("interval_seconds") == 5, "интервал из запроса — 5 секунд")
        check(args.get("duration_seconds") == 120, "длительность из запроса — 120 секунд")
        check(args.get("summary_every_seconds") == 60, "сводка из запроса — раз в 60 секунд")
        check(args.get("pair") == "USD/RUB", "пара из запроса — USD/RUB")

    check("[планировщик] интервал 5s, длительность 2m0s, сводка каждые 1m0s" in log,
          "планировщик принял параметры")

    runs = re.findall(r"\[запуск (\d+)/24\]", log)
    check(len(runs) == 24, f"24 запуска в логе (найдено {len(runs)})")
    check(runs and runs[0] == "1" and runs[-1] == "24",
          "запуски идут от 1/24 до 24/24")

    reports = re.findall(r"\[отчёт после (\d+)\] USD/RUB: count=(\d+)", log)
    check(len(reports) >= 2, f"минимум два отчёта (найдено {len(reports)})")
    check(bool(reports) and int(reports[-1][1]) == 24,
          f"финальный отчёт построен по всем 24 замерам (получено {reports[-1][1] if reports else '—'})")
    check(all(int(count) > 1 for _, count in reports),
          "ни один отчёт не построен по одному замеру")

    summaries = [line for line in log.splitlines() if line.strip().startswith("↳ [сводка] ")]
    check(len(summaries) >= 2, f"минимум две текстовые сводки (найдено {len(summaries)})")
    check(all(len(line.split("]", 1)[-1].strip()) > 20 for line in summaries),
          "сводки непустые")

    final = re.search(
        r"\[отчёт после 24\] USD/RUB: count=24 min=([0-9.]+) max=([0-9.]+) avg=([0-9.]+)",
        log,
    )
    check(final is not None, "есть финальный отчёт count=24 с min/max/avg")
    if final:
        rates = [float(t["rate"]) for t in ticks]
        want = (min(rates), max(rates), sum(rates) / len(rates))
        got = tuple(float(x) for x in final.groups())
        check(all(abs(a - b) <= 0.0002 for a, b in zip(got, want)),
              "min/max/avg финального отчёта совпадают с JSONL")

    check("[итог] успешных запусков: 24, записей в журнале: 24" in log,
          "итоговая строка: 24 успешных запуска и 24 записи")

    print(f"ИТОГ: {'ok' if ok == 20 else 'FAIL'} ({ok}/20)")
    return 0 if ok == 20 else 1


if __name__ == "__main__":
    sys.exit(main())
