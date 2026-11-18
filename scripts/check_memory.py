#!/usr/bin/env python3
"""Проверка памяти по журналу сессии: на какие контрольные вопросы агент
отвечает, а на какие — уже нет.

Читает session.jsonl, находит ответы на восемь контрольных вопросов из
testdata/day11-questions.txt и группирует их по шагам: шаг 0 — всё на месте,
дальше по шагу на каждую очистку слоя. Ответ считается верным, если в нём есть
все ключевые слова вопроса; честное «в данных нет» ключевых слов не содержит и
считается провалом — именно это и проверяется.

    scripts/check_memory.py --log /tmp/day11-cascade/cascade/session.jsonl
    scripts/check_memory.py --log ... --steps всё,−knowledge,−decisions

Код возврата 0, если все ответы на месте, и 1, если есть провалы.
"""

import argparse
import json
import re
import sys

# Вопрос → слова, которые обязаны быть в ответе. Регистр не важен.
CHECKS = [
    ("Какой срок переезда утвердили?", ["15 декабря"]),
    ("Какие ограничения по подрядчикам и выходным?", ["подрядчик", "будн"]),
    ("Как меня зовут?", ["дмитрий"]),
    ("Какая у меня роль и какой стиль ответов я просил?", ["тимлид", "коротк"]),
    ("Какое решение приняли по вариантам A и B?", ["вариант b", "коворкинг"]),
    ("Какую площадку выбрали?", ["пресня"]),
    ("Какой адрес у коворкинга?", ["пресненская"]),
    ("Какой телефон координатора Марины?", ["916"]),
]

DEFAULT_STEPS = ["всё на месте", "−knowledge", "−decisions", "−working", "−profile"]


def norm(s):
    """Схлопывает пробелы и типографику, чтобы сравнить вопрос с журналом."""
    s = s.replace("«", '"').replace("»", '"').replace("—", "-").replace("–", "-")
    s = re.sub(r"\s+", " ", s).strip().lower()
    return s


def read_messages(path):
    """Возвращает пары (вопрос, ответ) для контрольных вопросов по порядку."""
    by_question = {norm(q): i for i, (q, _) in enumerate(CHECKS)}
    answers = []  # (индекс вопроса, текст ответа)
    pending = None
    with open(path, encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            ev = json.loads(line)
            if ev.get("type") != "message" or not ev.get("message"):
                continue
            m = ev["message"]
            role, text = m.get("role"), (m.get("content") or "").strip()
            if role == "user":
                pending = by_question.get(norm(text))
            elif role == "assistant" and pending is not None and text:
                # Ответ с вызовом инструмента приходит с пустым content;
                # ждём следующее сообщение — это и будет ответ.
                answers.append((pending, text))
                pending = None
    return answers


def passed(answer, words):
    low = answer.lower()
    return all(w in low for w in words)


def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--log", required=True, help="путь к session.jsonl")
    ap.add_argument("--steps", default=",".join(DEFAULT_STEPS),
                    help="подписи шагов через запятую (по 8 вопросов на шаг)")
    ap.add_argument("--brief", action="store_true", help="только таблица, без разбора провалов")
    args = ap.parse_args()

    steps = [s for s in args.steps.split(",") if s]
    answers = read_messages(args.log)
    if not answers:
        print("в журнале нет ответов на контрольные вопросы", file=sys.stderr)
        return 2

    # Шаг — восемь подряд идущих ответов. Если вопрос пропущен, шаг всё равно
    # закрывается по счётчику, но об этом честно пишем.
    chunks = [answers[i:i + len(CHECKS)] for i in range(0, len(answers), len(CHECKS))]
    width = max([len(s) for s in steps] + [len("шаг")]) + 2
    header = "шаг".ljust(width) + "".join(f"{i + 1:>3}" for i in range(len(CHECKS))) + "   итого"
    print(header)
    print("-" * len(header))

    failures = []
    total_ok = 0
    for n, chunk in enumerate(chunks):
        label = steps[n] if n < len(steps) else f"шаг {n}"
        marks = []
        ok = 0
        for pos, (qi, answer) in enumerate(chunk):
            good = passed(answer, CHECKS[qi][1])
            marks.append("  ✓" if good else "  ✗")
            if good:
                ok += 1
            else:
                failures.append((label, CHECKS[qi][0], answer))
        if len(chunk) < len(CHECKS):
            marks += ["  ·"] * (len(CHECKS) - len(chunk))
        total_ok += ok
        print(label.ljust(width) + "".join(marks) + f"   {ok}/{len(chunk)}")

    if not args.brief:
        for label, question, answer in failures:
            snippet = re.sub(r"\s+", " ", answer)[:150]
            print(f"\n✗ {label}: {question}\n   {snippet}")

    print(f"\nвсего ответов: {len(answers)}, шагов: {len(chunks)}")
    return 0 if not failures else 1


if __name__ == "__main__":
    sys.exit(main())
