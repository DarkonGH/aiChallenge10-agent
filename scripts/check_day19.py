#!/usr/bin/env python3
"""Проверка четырёх сценариев дня 20: forex + save_to_file."""

import glob
import json
import os
import sys

checks = 0
failures = 0


def check(condition, text):
    global checks, failures
    checks += 1
    if condition:
        print(f"ok   {text}")
    else:
        failures += 1
        print(f"FAIL {text}")


def parse_args(raw):
    try:
        return json.loads(raw or "{}")
    except json.JSONDecodeError:
        return {}


def load_turns(data_dir):
    paths = glob.glob(f"{data_dir}/*/session.jsonl")
    if not paths:
        return []
    turns = []
    current = None
    with open(paths[0], encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            message = (json.loads(line).get("message") or {})
            role = message.get("role")
            if role == "user":
                current = {"calls": [], "results": [], "text": ""}
                turns.append(current)
            elif role == "assistant" and current is not None:
                text = message.get("content") or ""
                if text:
                    current["text"] += text + "\n"
                for call in message.get("tool_calls") or []:
                    fn = call.get("function") or {}
                    current["calls"].append({
                        "name": fn.get("name") or "",
                        "args": parse_args(fn.get("arguments")),
                    })
            elif role == "tool" and current is not None:
                current["results"].append({
                    "name": message.get("name") or "",
                    "content": message.get("content") or "",
                })
    return turns


def calls_named(turn, name):
    return [c for c in turn["calls"] if c["name"] == name]


def result_for(turn, name):
    for result in turn["results"]:
        if result["name"] == name:
            return result["content"]
    return ""


def main():
    reports, log_path, data_dir, work_dir = sys.argv[1:5]
    log = open(log_path, encoding="utf-8").read()
    turns = load_turns(data_dir)

    check(len(turns) == 4, f"в сессии четыре сценария (найдено {len(turns)})")
    check("run_pipeline" not in log, "локальный run_pipeline не вызывался")

    if len(turns) != 4:
        print(f"ИТОГ: FAIL ({checks - failures}/{checks})")
        return 1

    # 1. Успешная цепочка.
    first = turns[0]
    schedules = calls_named(first, "run_forex_schedule")
    saves = calls_named(first, "mcp_pipeline_save_to_file")
    check(bool(schedules), "сценарий 1: запущен run_forex_schedule")
    if schedules:
        args = schedules[0]["args"]
        check(str(args.get("pair", "")).upper() == "USD/RUB",
              "сценарий 1: пара USD/RUB")
        check(int(args.get("interval_seconds", 0)) == 3,
              "сценарий 1: шаг 3 секунды")
        check(int(args.get("duration_seconds", 0)) == 30,
              "сценарий 1: длительность 30 секунд")
        check(int(args.get("summary_every_seconds", 0) or 0) == 0,
              "сценарий 1: без текстовой сводки")
    check(bool(saves), "сценарий 1: вызван save_to_file")
    if saves:
        check(saves[0]["args"].get("path") == "usd_rub_avg.md",
              "сценарий 1: путь usd_rub_avg.md")
    avg_path = os.path.join(reports, "usd_rub_avg.md")
    check(os.path.isfile(avg_path), "сценарий 1: файл среднего создан")
    if os.path.isfile(avg_path):
        body = open(avg_path, encoding="utf-8").read()
        check("82." in body, "сценарий 1: в файле среднее USD/RUB")
        check("средн" in body.lower(), "сценарий 1: в файле есть слово «среднее»")

    # 2. Ошибка первого шага.
    second = turns[1]
    fetches = calls_named(second, "mcp_forex_fetch_fx")
    check(bool(fetches), "сценарий 2: вызван fetch_fx")
    if fetches:
        args = fetches[0]["args"]
        check(str(args.get("base", "")).upper() == "USD",
              "сценарий 2: base=USD")
        check(str(args.get("quote", "")).upper() == "ZZZ",
              "сценарий 2: quote=ZZZ")
    check(not calls_named(second, "calc"),
          "сценарий 2: calc не вызывался")
    check(not calls_named(second, "mcp_pipeline_save_to_file"),
          "сценарий 2: save_to_file не вызывался")
    fetch_error = result_for(second, "mcp_forex_fetch_fx").lower()
    check("404" in fetch_error or "not found" in fetch_error,
          "сценарий 2: fetch_fx вернул 404/not found")
    check("перв" in second["text"].lower() or "шаг 1" in second["text"].lower(),
          "сценарий 2: модель назвала остановку на первом шаге")

    # 3. Деление на ноль.
    third = turns[2]
    check(bool(calls_named(third, "run_forex_schedule")),
          "сценарий 3: серия запущена")
    calcs = calls_named(third, "calc")
    check(bool(calcs), "сценарий 3: вызван calc")
    exprs = [str(c["args"].get("expr", "")).replace(" ", "") for c in calcs]
    check(any("/0" in expr for expr in exprs),
          "сценарий 3: хотя бы одно выражение делит на ноль")
    calc_errors = [
        r["content"].lower()
        for r in third["results"]
        if r["name"] == "calc"
    ]
    check(any("деление на ноль" in text for text in calc_errors),
          "сценарий 3: calc вернул «деление на ноль»")
    check(not calls_named(third, "mcp_pipeline_save_to_file"),
          "сценарий 3: save_to_file не вызывался")
    check(not os.path.exists(os.path.join(reports, "zero.md")),
          "сценарий 3: файл zero.md не создан")

    # 4. Ошибка сохранения.
    fourth = turns[3]
    check(bool(calls_named(fourth, "run_forex_schedule")),
          "сценарий 4: серия запущена")
    saves = calls_named(fourth, "mcp_pipeline_save_to_file")
    check(bool(saves), "сценарий 4: вызван save_to_file")
    if saves:
        check(saves[0]["args"].get("path") == "~/secret/report.md",
              "сценарий 4: путь ~/secret/report.md передан без замены")
    save_error = result_for(fourth, "mcp_pipeline_save_to_file").lower()
    check("начинается с ~" in save_error or "~" in save_error,
          "сценарий 4: save_to_file отклонил путь с ~")
    check(not os.path.exists(os.path.expanduser("~/secret/report.md")),
          "сценарий 4: файл в ~/secret не создан")

    # Общие проверки.
    files = sorted(
        os.path.relpath(p, reports)
        for p in glob.glob(f"{reports}/**/*", recursive=True)
        if os.path.isfile(p)
    )
    check(files == ["usd_rub_avg.md"],
          f"в reports только успешный файл (найдено {files})")
    check("без промежуточных сводок" in log,
          "в логе нет промежуточных сводок")
    check("[сводка]" not in log,
          "локальная модель не писала сводок")
    check(log.count("mcp_forex_fetch_fx") >= 1,
          "в логе виден вызов fetch_fx")
    check("начинается с ~" in log,
          "в логе видна ошибка пути с ~")

    total = checks
    print(f"ИТОГ: {'ok' if failures == 0 else 'FAIL'} ({checks - failures}/{total})")
    return 0 if failures == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
