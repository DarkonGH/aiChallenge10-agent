#!/usr/bin/env python3
"""Проверка демо дня 19: модель сама строит MCP-цепочку.

Оркестратора run_pipeline больше нет. Модель должна сама вызвать
search -> summarize -> save_to_file, передать items и summary и
остановиться на пустом результате или ошибке.
"""

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
            event = json.loads(line)
            message = event.get("message") or {}
            role = message.get("role")
            if role == "user":
                current = {"user": message.get("content") or "",
                           "calls": [], "results": []}
                turns.append(current)
            elif role == "assistant" and current is not None:
                for call in message.get("tool_calls") or []:
                    fn = call.get("function") or {}
                    current["calls"].append({
                        "id": call.get("id"),
                        "name": fn.get("name") or "",
                        "args": parse_args(fn.get("arguments")),
                    })
            elif role == "tool" and current is not None:
                current["results"].append({
                    "name": message.get("name") or "",
                    "content": message.get("content") or "",
                })
    return turns


def parse_args(raw):
    if isinstance(raw, dict):
        return raw
    try:
        value = json.loads(raw or "{}")
    except json.JSONDecodeError:
        return {}
    return value if isinstance(value, dict) else {}


def extract_json(content):
    start = content.find("{")
    end = content.rfind("}")
    if start < 0 or end <= start:
        return None
    try:
        value = json.loads(content[start:end + 1])
    except json.JSONDecodeError:
        return None
    return value if isinstance(value, dict) else None


def names(turn):
    return [call["name"] for call in turn["calls"]]


def first_index(turn, tool):
    for i, call in enumerate(turn["calls"]):
        if call["name"] == tool:
            return i
    return -1


def results(turn, tool):
    out = []
    for result in turn["results"]:
        if result["name"] == tool:
            value = extract_json(result["content"])
            if value is not None:
                out.append(value)
    return out


def error_text(turn, tool):
    for result in turn["results"]:
        if result["name"] == tool:
            return result["content"]
    return ""


def main():
    out_dir, log_path, data_dir = sys.argv[1], sys.argv[2], sys.argv[3]
    with open(log_path, encoding="utf-8") as f:
        log = f.read()
    turns = load_turns(data_dir)

    check(len(turns) == 3, f"в сессии три хода пользователя (найдено {len(turns)})")

    all_calls = [call["name"] for turn in turns for call in turn["calls"]]
    check("run_pipeline" not in all_calls,
          "локальный run_pipeline не вызывался")
    check("run_pipeline" not in log,
          "в логе нет run_pipeline")
    check(all_calls.count("mcp_pipeline_search") >= 3,
          "модель вызывала mcp_pipeline_search в каждом сценарии")

    if len(turns) == 3:
        happy, empty, failed = turns

        # --- Сценарий 1: обычная цепочка ---
        hn = names(happy)
        check("mcp_pipeline_search" in hn,
              "успешный сценарий: search вызван")
        check("mcp_pipeline_summarize" in hn,
              "успешный сценарий: summarize вызван")
        check("mcp_pipeline_save_to_file" in hn,
              "успешный сценарий: save_to_file вызван")
        check(0 <= first_index(happy, "mcp_pipeline_search")
              < first_index(happy, "mcp_pipeline_summarize")
              < first_index(happy, "mcp_pipeline_save_to_file"),
              "порядок вызовов: search → summarize → save_to_file")

        searches = [c["args"] for c in happy["calls"]
                    if c["name"] == "mcp_pipeline_search"]
        check(any("mcp" in str(a.get("query", "")).lower() for a in searches),
              "search искал MCP")

        summaries = [c["args"] for c in happy["calls"]
                     if c["name"] == "mcp_pipeline_summarize"]
        check(bool(summaries) and all(isinstance(a.get("items"), list)
                                      and len(a["items"]) > 0 for a in summaries),
              "summarize получил непустой items от search")

        saves = [c["args"] for c in happy["calls"]
                 if c["name"] == "mcp_pipeline_save_to_file"]
        check(bool(saves) and saves[0].get("path") == "mcp-summary.md",
              "save_to_file сохраняет в mcp-summary.md")

        search_results = results(happy, "mcp_pipeline_search")
        check(any(r.get("count", 0) >= 2 for r in search_results),
              "search нашёл хотя бы два документа")
        check(any(bool(r.get("items")) for r in search_results),
              "search вернул items")

        summary_results = results(happy, "mcp_pipeline_summarize")
        check(bool(summary_results) and bool(summary_results[0].get("summary")),
              "summarize вернул непустой summary")

        save_results = results(happy, "mcp_pipeline_save_to_file")
        check(bool(save_results) and save_results[0].get("saved") is True,
              "save_to_file подтвердил сохранение")

        if summaries and saves and summary_results:
            summary = summary_results[0].get("summary", "")
            content = str(saves[0].get("content", ""))
            bullets = [line[2:].strip() for line in summary.splitlines()
                       if line.startswith("- ")]
            connected = any(b[:30] in content for b in bullets)
            check(connected,
                  "save_to_file получил текст из summary, а не выдуманный")

        # --- Сценарий 2: пустой поиск ---
        en = names(empty)
        empty_searches = [c["args"] for c in empty["calls"]
                          if c["name"] == "mcp_pipeline_search"]
        check(bool(empty_searches) and empty_searches[0].get("empty") is True,
              "пустой сценарий: search с empty=true")
        check("mcp_pipeline_summarize" not in en,
              "пустой сценарий: summarize не вызывался")
        check("mcp_pipeline_save_to_file" not in en,
              "пустой сценарий: save_to_file не вызывался")
        empty_results = results(empty, "mcp_pipeline_search")
        check(bool(empty_results) and empty_results[0].get("count") == 0,
              "пустой сценарий: search вернул count=0")
        check(not empty_results or not empty_results[0].get("items"),
              "пустой сценарий: items пустой")

        # --- Сценарий 3: ошибка summarize ---
        fn = names(failed)
        check("mcp_pipeline_search" in fn,
              "ошибочный сценарий: search вызван")
        check("mcp_pipeline_summarize" in fn,
              "ошибочный сценарий: summarize вызван")
        check("mcp_pipeline_save_to_file" not in fn,
              "ошибочный сценарий: save_to_file не вызывался")
        check(0 <= first_index(failed, "mcp_pipeline_search")
              < first_index(failed, "mcp_pipeline_summarize"),
              "ошибочный сценарий: summarize вызван после search")
        failed_summaries = [c["args"] for c in failed["calls"]
                            if c["name"] == "mcp_pipeline_summarize"]
        check(bool(failed_summaries) and failed_summaries[0].get("fail") is True,
              "ошибочный сценарий: summarize с fail=true")
        failed_text = error_text(failed, "mcp_pipeline_summarize").lower()
        check("тестовый сбой" in failed_text or "ошибка инструмента" in failed_text,
              "ошибочный сценарий: агент получил ошибку summarize")

    # --- Файл и лог ---
    summary_path = os.path.join(out_dir, "mcp-summary.md")
    check(os.path.isfile(summary_path), "файл mcp-summary.md создан")
    if os.path.isfile(summary_path):
        with open(summary_path, encoding="utf-8") as f:
            body = f.read()
        check("Резюме" in body, "в файле есть резюме")
        check("MCP" in body, "в файле есть содержание про MCP")

    files = [p for p in glob.glob(f"{out_dir}/**/*", recursive=True)
             if os.path.isfile(p)]
    check(len(files) == 1,
          f"в каталоге reports ровно один файл (найдено {len(files)})")

    check(log.count("mcp_pipeline_search(") >= 3,
          "в логе видны вызовы search")
    check(log.count("mcp_pipeline_summarize(") >= 2,
          "в логе видны summarize в успешном и ошибочном сценариях")
    check(log.count("mcp_pipeline_save_to_file(") >= 1,
          "в логе виден save_to_file только в успешном сценарии")

    print(f"ИТОГ: {'ok' if failures == 0 else 'FAIL'} ({checks - failures}/{checks})")
    return 0 if failures == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
