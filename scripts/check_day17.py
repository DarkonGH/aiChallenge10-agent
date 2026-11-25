#!/usr/bin/env python3
"""Проверка демо дня 17: свой MCP-инструмент currency_rate + локальный calc."""

import glob
import json
import os
import re
import sys


def load_messages(sessions_dir):
    messages = []
    for path in sorted(glob.glob(os.path.join(sessions_dir, "*/session.jsonl"))):
        with open(path, encoding="utf-8") as f:
            for line in f:
                ev = json.loads(line)
                if ev.get("type") == "message":
                    messages.append(ev["message"])
    return messages


def load_requests(path):
    with open(path, encoding="utf-8") as f:
        return [json.loads(line) for line in f if line.strip()]


def tool_names(body):
    return [t.get("function", {}).get("name") for t in body.get("tools", [])]


def main():
    if len(sys.argv) != 3:
        print("usage: check_day17.py <sessions-dir> <requests.ndjson>")
        return 2
    sessions_dir, requests_path = sys.argv[1], sys.argv[2]

    messages = load_messages(sessions_dir)
    requests = load_requests(requests_path)

    calls = []
    results = []
    final_answer = ""
    for m in messages:
        role = m.get("role")
        if role == "assistant":
            for tc in m.get("tool_calls") or []:
                fn = tc.get("function") or {}
                calls.append((fn.get("name", ""), fn.get("arguments", "")))
            text = (m.get("content") or "").strip()
            if text:
                final_answer = text
        elif role == "tool":
            results.append((m.get("name", ""), m.get("content") or ""))

    ok = True

    def check(cond, text):
        nonlocal ok
        print(("ok   " if cond else "FAIL ") + text)
        ok = ok and bool(cond)

    rate_calls = [c for c in calls if c[0] == "mcp_rates_currency_rate"]
    calc_calls = [c for c in calls if c[0] == "calc"]

    check(rate_calls, "вызван mcp_rates_currency_rate")
    if rate_calls:
        args = rate_calls[0][1]
        check("USD" in args, "валюта USD в аргументах MCP")
        check("20.11.2026" in args, "дата 20.11.2026 в аргументах MCP")

    check(calc_calls, "вызван локальный calc")
    if calc_calls:
        expr = calc_calls[-1][1]
        check("350000" in expr, "calc использует сумму 350000")
        check("81.929" in expr or "81,929" in expr, "calc использует курс 81.929")

    joined_results = "\n".join(content for _, content in results)
    check("81.929" in joined_results or "81,929" in joined_results,
          "результат MCP содержит курс 81.929")
    check("4271" in joined_results or "4 271" in joined_results,
          "результат calc содержит 4271.99")

    check("4 272" in final_answer or "4272" in final_answer,
          "финальный ответ содержит ≈4 272 доллара")
    check("81,929" in final_answer or "81.929" in final_answer,
          "финальный ответ содержит курс 81,929")

    request_tools = [set(tool_names(r)) for r in requests]
    check(any("mcp_rates_currency_rate" in t for t in request_tools),
          "схема mcp_rates_currency_rate есть в теле запроса")
    check(any("calc" in t for t in request_tools),
          "схема calc есть в теле запроса")

    print("ИТОГ: ok" if ok else "ИТОГ: FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
