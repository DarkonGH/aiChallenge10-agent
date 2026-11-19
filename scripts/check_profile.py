#!/usr/bin/env python3
"""Проверка персонализации по журналу сессии: соблюдён ли профиль.

Меряются три оси, и все три — измеримые, а не «на глаз»:

  длина      — символов в ответе (порог задан в testdata/day12-profiles.json)
  код        — есть ли блок ``` в ответе
  обращение  — формы «ты» или «вы»

У каждой оси три исхода:

  ✓  предпочтение соблюдено;
  ✗  нарушено — ответ прямо противоречит профилю (есть «вы» в профиле «на ты»,
     есть код там, где он запрещён, длина выше лимита);
  —  не проявлено — проверить нечего: например, ответ не обращается к
     пользователю ни на «ты», ни на «вы». Это не нарушение, но и не
     соблюдение, поэтому такие ответы не идут в зачёт и печатаются отдельно.

Пороги зафиксированы в файле спецификации до прогона, а не подбираются после:
иначе под любой ответ можно подогнать «предпочтение соблюдено».

    # один вопрос, три профиля
    scripts/check_profile.py --data /tmp/day12-profiles \\
        --profiles кратко,подробно,смешанный

    # десять вопросов подряд в одном профиле
    scripts/check_profile.py --data /tmp/day12-stability --profiles кратко --stability

Код возврата 0, если нарушений нет, и 1, если есть хотя бы одно.
"""

import argparse
import json
import os
import re
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
SPEC_PATH = os.path.join(HERE, "..", "testdata", "day12-profiles.json")

TY = re.compile(r"(?i)\b(ты|тебе|тебя|твой|твоя|твои|твоего|твоему)\b")
VY = re.compile(r"(?i)\b(вы|вам|вас|ваш|ваша|ваши|вашего|вашему)\b")

OK, FAIL, NA = "ok", "fail", "na"
MARKS = {OK: "✓", FAIL: "✗", NA: "—"}


def norm(s):
    """Схлопывает пробелы: длина не должна зависеть от переносов строк."""
    return re.sub(r"\s+", " ", s).strip()


def read_answers(path):
    """Все непустые ответы ассистента по порядку. Ходы с вызовом инструмента
    приходят с пустым content и пропускаются."""
    answers = []
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            ev = json.loads(line)
            if ev.get("type") != "message":
                continue
            m = ev.get("message") or {}
            if m.get("role") != "assistant":
                continue
            text = (m.get("content") or "").strip()
            if text:
                answers.append(text)
    return answers


def check(answer, spec):
    """Возвращает список (ось, статус, пояснение)."""
    text = norm(answer)
    n = len(text)
    rows = []

    if "макс_символов" in spec:
        limit = spec["макс_символов"]
        rows.append(("длина", OK if n <= limit else FAIL, f"{n} ≤ {limit}"))
    else:
        limit = spec["мин_символов"]
        rows.append(("длина", OK if n >= limit else FAIL, f"{n} ≥ {limit}"))

    has_code = "```" in answer
    rows.append(("код", OK if has_code == spec["код"] else FAIL, "есть" if has_code else "нет"))

    ty, vy = bool(TY.search(text)), bool(VY.search(text))
    if spec["обращение"] == "ты":
        status = FAIL if vy else (OK if ty else NA)
    else:
        status = FAIL if ty else (OK if vy else NA)
    rows.append(("обращение", status, f"ты={str(ty).lower()}, вы={str(vy).lower()}"))

    return rows


def counts(rows):
    checked = sum(1 for _, st, _ in rows if st != NA)
    good = sum(1 for _, st, _ in rows if st == OK)
    bad = sum(1 for _, st, _ in rows if st == FAIL)
    return good, checked, bad


def cell(status, note):
    return f"{note} {MARKS[status]}"


def log_path(data, profile):
    """Журнал прогона одного профиля: <data>/<профиль>/run/session.jsonl.

    Раскладка фиксирована скриптами profile_experiment.sh и
    profile_stability.sh: у каждого профиля свой каталог данных, чтобы сессии
    и файлы памяти не смешивались.
    """
    return os.path.join(data, profile, "run", "session.jsonl")


def run_matrix(data, profiles, spec):
    width = max([len(p) for p in profiles] + [len("профиль")]) + 2
    header = "профиль".ljust(width) + "длина".ljust(22) + "код".ljust(14) + "обращение".ljust(26) + "итого"
    print(header)
    print("-" * len(header))
    bad_total = 0
    for name in profiles:
        log = log_path(data, name)
        if not os.path.exists(log):
            print(name.ljust(width) + "нет журнала: " + log)
            bad_total += 1
            continue
        answers = read_answers(log)
        if not answers:
            print(name.ljust(width) + "нет ответа ассистента")
            bad_total += 1
            continue
        rows = check(answers[-1], spec[name])
        good, checked, bad = counts(rows)
        bad_total += bad
        line = name.ljust(width)
        line += cell(rows[0][1], rows[0][2]).ljust(22)
        line += cell(rows[1][1], rows[1][2]).ljust(14)
        line += cell(rows[2][1], rows[2][2]).ljust(26)
        line += f"{good}/{checked}, нарушений {bad}"
        print(line)
        if bad:
            print(f"   ответ: {norm(answers[-1])[:200]}")
    print()
    print(f"профилей: {len(profiles)}, нарушений: {bad_total}")
    return bad_total == 0


def run_stability(data, profile, spec):
    log = log_path(data, profile)
    if not os.path.exists(log):
        print("нет журнала: " + log, file=sys.stderr)
        return False
    answers = read_answers(log)
    if not answers:
        print("нет ответов ассистента", file=sys.stderr)
        return False

    print(f"профиль «{profile}», ответов: {len(answers)}")
    header = "№".ljust(4) + "длина".ljust(22) + "код".ljust(14) + "обращение".ljust(26) + "итог"
    print(header)
    print("-" * len(header))
    first_fail = None
    first_by_axis = {}
    na_axes = {}
    for i, answer in enumerate(answers, 1):
        rows = check(answer, spec[profile])
        good, checked, bad = counts(rows)
        line = str(i).ljust(4)
        line += cell(rows[0][1], rows[0][2]).ljust(22)
        line += cell(rows[1][1], rows[1][2]).ljust(14)
        line += cell(rows[2][1], rows[2][2]).ljust(26)
        line += f"{good}/{checked}, нарушений {bad}"
        print(line)
        for axis, status, _ in rows:
            if status == FAIL:
                first_by_axis.setdefault(axis, i)
            elif status == NA:
                na_axes[axis] = na_axes.get(axis, 0) + 1
        if bad and first_fail is None:
            first_fail = i

    print()
    if first_fail is None:
        print(f"нарушений нет во всех {len(answers)} ответах")
    else:
        axes = ", ".join(f"{axis} — с №{n}" for axis, n in first_by_axis.items())
        print(f"первый ответ с нарушением: №{first_fail} ({axes})")
    if na_axes:
        parts = ", ".join(f"{axis} — {n}" for axis, n in na_axes.items())
        print(f"не проявлено (проверить нечего): {parts}")
    return first_fail is None


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--data", required=True, help="каталог данных агента")
    ap.add_argument("--profiles", required=True, help="имена профилей через запятую")
    ap.add_argument("--stability", action="store_true", help="режим десяти вопросов подряд")
    args = ap.parse_args()

    with open(SPEC_PATH, encoding="utf-8") as f:
        spec = json.load(f)

    profiles = [p.strip() for p in args.profiles.split(",") if p.strip()]
    unknown = [p for p in profiles if p not in spec]
    if unknown:
        print(f"нет спецификации для профилей: {', '.join(unknown)}", file=sys.stderr)
        return 2

    if args.stability:
        if len(profiles) != 1:
            print("--stability проверяет один профиль", file=sys.stderr)
            return 2
        ok = run_stability(args.data, profiles[0], spec)
    else:
        ok = run_matrix(args.data, profiles, spec)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
