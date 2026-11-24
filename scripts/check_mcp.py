#!/usr/bin/env python3
"""Проверяет демо дня 16: MCP + локальный инструмент в одном запросе.

    python3 scripts/check_mcp.py /tmp/day16-fs/sessions /tmp/day16-fs/requests.ndjson

Что проверяется:
  * агент вызвал MCP-инструмент mcp_fs_read_file;
  * агент вызвал локальный calc;
  * calc вернул 25 000, а read_file — строки 12000/4000/9000;
  * в теле запроса к модели есть обе схемы: mcp_fs_read_file и calc;
  * write_file не зарегистрирован;
  * финальный ответ называет итог 25 000.
"""

import glob
import json
import os
import sys


def load_messages(sessions_dir):
    paths = glob.glob(os.path.join(sessions_dir, "*", "session.jsonl"))
    if not paths:
        raise SystemExit(f"нет session.jsonl в {sessions_dir}")
    messages = []
    for path in paths:
        for line in open(path, encoding="utf-8"):
            line = line.strip()
            if not line:
                continue
            ev = json.loads(line)
            if ev.get("type") == "message":
                messages.append(ev["message"])
    return messages


def load_requests(path):
    out = []
    for line in open(path, encoding="utf-8"):
        line = line.strip()
        if line:
            out.append(json.loads(line))
    return out


def tool_names(body):
    return [t.get("function", {}).get("name", "") for t in body.get("tools", [])]


def main():
    if len(sys.argv) != 3:
        raise SystemExit("использование: check_mcp.py <каталог сессий> <requests.ndjson>")
    sessions_dir, requests_path = sys.argv[1], sys.argv[2]

    messages = load_messages(sessions_dir)
    requests = load_requests(requests_path)
    calls = []
    results = []
    for msg in messages:
        for call in msg.get("tool_calls") or []:
            fn = call.get("function", {})
            calls.append((fn.get("name", ""), fn.get("arguments", "")))
        if msg.get("role") == "tool":
            results.append((msg.get("name", ""), msg.get("content", "")))
    final_answer = ""
    for msg in messages:
        if msg.get("role") == "assistant" and (msg.get("content") or "").strip():
            final_answer = msg["content"]

    ok = True

    def check(condition, text):
        nonlocal ok
        print(("  ok   " if condition else "  FAIL ") + text)
        ok = ok and condition

    call_names = [name for name, _ in calls]
    check("mcp_fs_read_file" in call_names, "MCP fs: вызван mcp_fs_read_file")
    check("calc" in call_names, "локальный: вызван calc")

    read_args = [args for name, args in calls if name == "mcp_fs_read_file"]
    check(read_args and "expenses.csv" in read_args[-1], "read_file получил expenses.csv")

    read_results = [text for name, text in results if name == "mcp_fs_read_file"]
    joined_read = "\n".join(read_results)
    check(all(x in joined_read for x in ("12000", "4000", "9000")),
          "read_file вернул строки 12000/4000/9000")

    calc_results = [text.strip() for name, text in results if name == "calc"]
    check(any(x in ("25000", "25 000") for x in calc_results), "calc вернул 25000")

    schema_bodies = []
    for body in requests:
        names = tool_names(body)
        if "mcp_fs_read_file" in names and "calc" in names:
            schema_bodies.append(body)
    check(bool(schema_bodies), "в теле запроса есть и mcp_fs_read_file, и calc")
    if schema_bodies:
        body = schema_bodies[0]
        names = tool_names(body)
        check("mcp_fs_write_file" not in names, "write_file не зарегистрирован")
        check("mcp_web_fetch" in names, "web fetch виден в списке инструментов")
        print("  запрос: модель %s, инструментов %d, последнее сообщение: %s" %
              (body.get("model", "?"), len(names), body["messages"][-1].get("role", "?")))

    check("25" in final_answer and ("000" in final_answer or " 000" in final_answer),
          "финальный ответ называет итог 25 000")

    print("ИТОГ:", "ok" if ok else "FAIL")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
