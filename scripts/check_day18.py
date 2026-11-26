#!/usr/bin/env python3
"""Проверка демо дня 18: несколько запусков планировщика и отчёт по всем."""

import json
import re
import sys
from datetime import datetime


def load_ticks(path):
    ticks = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if line:
                ticks.append(json.loads(line))
    return ticks


def parse_reports(log_text):
    pattern = re.compile(
        r"\[отчёт после (\d+)\] USD/RUB: count=(\d+) "
        r"min=([0-9.]+) max=([0-9.]+) avg=([0-9.]+) delta=([+-][0-9.]+)"
    )
    reports = {}
    for match in pattern.finditer(log_text):
        after = int(match.group(1))
        reports[after] = {
            "count": int(match.group(2)),
            "min": float(match.group(3)),
            "max": float(match.group(4)),
            "avg": float(match.group(5)),
            "delta": float(match.group(6)),
        }
    return reports


def expect_report(reports, after, ticks, check):
    report = reports.get(after)
    check(report is not None, f"есть отчёт после {after} запусков")
    if report is None:
        return
    rates = [tick["rate"] for tick in ticks]
    check(report["count"] == len(ticks),
          f"отчёт после {after}: count={report['count']} соответствует {len(ticks)} замерам")
    check(abs(report["min"] - min(rates)) <= 0.0002,
          f"отчёт после {after}: min={report['min']:.4f} соответствует {min(rates):.4f}")
    check(abs(report["max"] - max(rates)) <= 0.0002,
          f"отчёт после {after}: max={report['max']:.4f} соответствует {max(rates):.4f}")
    check(abs(report["avg"] - sum(rates) / len(rates)) <= 0.0002,
          f"отчёт после {after}: avg={report['avg']:.4f} соответствует {sum(rates) / len(rates):.4f}")


def main():
    if len(sys.argv) != 3:
        print("usage: check_day18.py <forex.jsonl> <demo.log>")
        return 2
    store_path, log_path = sys.argv[1], sys.argv[2]
    ticks = load_ticks(store_path)
    log_text = open(log_path, encoding="utf-8").read()
    reports = parse_reports(log_text)
    ok = True

    def check(cond, text):
        nonlocal ok
        print(("ok   " if cond else "FAIL ") + text)
        ok = ok and bool(cond)

    check(len(ticks) == 6, f"в журнале 6 накопленных замеров (найдено {len(ticks)})")
    check(all(t.get("base") == "USD" and t.get("quote") == "RUB" for t in ticks),
          "все замеры — USD/RUB")
    check(all(float(t.get("rate", 0)) > 0 for t in ticks),
          "у всех замеров положительный курс")
    times = [t.get("fetched_at", "") for t in ticks]
    check(len(set(times)) == len(times), "у замеров разные метки времени")
    try:
        parsed = [datetime.fromisoformat(t.replace("Z", "+00:00")) for t in times]
        check(parsed == sorted(parsed), "замеры лежат в хронологическом порядке")
    except ValueError:
        check(False, "метки времени разбираются как RFC3339")

    check(re.search(r"интервал \S+, запусков 6, отчёт каждые 3", log_text) is not None,
          "в логе есть параметры расписания")
    check("[запуск 1/6]" in log_text and "[запуск 6/6]" in log_text,
          "в логе есть первый и последний запуски")
    check("всего 6" in log_text, "save_tick накапливает записи")

    expect_report(reports, 3, ticks[:3], check)
    expect_report(reports, 6, ticks, check)
    check(reports.get(6, {}).get("count", 0) != 1,
          "итоговый отчёт построен по всем замерам, а не по последнему")
    check("[итог] успешных запусков: 6, записей в журнале: 6" in log_text,
          "итоговая строка подтверждает 6 успешных запусков")

    print("ИТОГ: ok" if ok else "ИТОГ: FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
