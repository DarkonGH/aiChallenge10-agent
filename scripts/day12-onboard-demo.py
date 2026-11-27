#!/usr/bin/env python3
"""Запись демо дня 12: пустой профиль → онбординг из диалога → профиль влияет
на ответ → правка предпочтения прямо в диалоге.

Зачем отдельный скрипт: онбординг работает только в интерактивном TTY, а
asciinema не умеет сам вести диалог с программой. Здесь агент запускается в
pty, ввод подаётся по ожидаемым промптам, а всё, что печатает агент, пишется
в asciicast-v2 — дальше его собирает render_cast.py.

    python3 scripts/day12-onboard-demo.py /tmp/day12-onboard.cast
"""

import fcntl
import json
import os
import pty
import select
import shutil
import struct
import subprocess
import sys
import termios
import time

WIDTH, HEIGHT = 148, 50
MODEL = os.environ.get("MODEL", "glm-5.3-flash")
DATA = os.environ.get("DAY12_ONBOARD_DATA", "/tmp/day12-onboard")

# (что ждём, что печатаем, сколько подождать после Enter)
STEPS = [
    ("Как к вам обращаться", "Меня зовут Денис, я разработчик"),
    ("Как отвечать", "Отвечай кратко, без кода и без длинных списков"),
    ("Чего не делать", "Не используй таблицы"),
]

# Диалог после онбординга: тот же вопрос до и после правки предпочтения.
QUESTION = "Как в Go прочитать файл целиком?"
UPDATE = "теперь можно с примером кода"


def now() -> float:
    return time.monotonic()


def main() -> None:
    if len(sys.argv) != 2:
        sys.exit("использование: day12-onboard-demo.py <выход.cast>")
    out_path = sys.argv[1]
    shutil.rmtree(DATA, ignore_errors=True)
    os.makedirs(DATA, exist_ok=True)

    master, slave = pty.openpty()
    fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack("HHHH", HEIGHT, WIDTH, 0, 0))
    proc = subprocess.Popen(
        ["./goagent", "-data", DATA, "-session", "demo", "-profile", "demo",
         "-model", MODEL, "-summary-model", MODEL, "-no-reasoning"],
        stdin=slave, stdout=slave, stderr=slave,
        start_new_session=True, close_fds=True)
    os.close(slave)

    events: list[tuple[float, str]] = []
    buf = ""
    start = now()

    def record(chunk: str) -> None:
        nonlocal buf
        buf += chunk
        events.append((round(now() - start, 3), "o", chunk))

    dead = False  # агент закрыл pty: дальше читать нечего

    def pump(seconds: float) -> None:
        # Внимание к EOF: после закрытия slave-конца pty master вечно
        # «readable», и без проверки eof здесь возникает бесконечный цикл
        # (осушение никогда не прервётся — ошибка, на которой простояли ~20
        # минут). Поэтому EOF и OSError просто завершают осушение.
        nonlocal dead
        end = now() + seconds
        while now() < end and not dead:
            r, _, _ = select.select([master], [], [], max(0.0, end - now()))
            if not r:
                continue
            parts = []
            # Эхо ввода редактор пишет по символу (перерисовка на каждый
            # символ); без склейки каждая перерисовка стала бы отдельным
            # событием каста = отдельным кадром в видео, и набор выглядел
            # медленным. Забираем всё, что доедет за следующие 5 мс, одним
            # событием.
            while True:
                try:
                    chunk = os.read(master, 65536)
                except OSError:
                    dead = True
                    break
                if not chunk:
                    dead = True
                    break
                parts.append(chunk.decode("utf-8", "replace"))
                r, _, _ = select.select([master], [], [], 0.005)
                if not r:
                    break
            data = "".join(parts)
            if data:
                record(data)

    def wait_for(needle: str, timeout: float = 180.0) -> None:
        end = now() + timeout
        while now() < end:
            if needle in buf:
                return
            if dead:
                tail = buf[-800:].replace("\n", "\\n")
                sys.exit(f"агент завершился до {needle!r}; хвост вывода: {tail}")
            pump(0.05)
        tail = buf[-800:].replace("\n", "\\n")
        sys.exit(f"не дождались {needle!r}; хвост вывода: {tail}")

    def type_line(text: str) -> None:
        # Строка целиком одним write: редактор ввода перерисовывает строку на
        # каждый символ, и при посимвольном/порционном вводе каждая перерисовка
        # доезжает отдельным куском = отдельный кадр (при 8 fps набор выглядел
        # как ~8 символов/с). Один burst эхо-события слипает в 1–2 кадра.
        os.write(master, text.encode() + b"\r")
        pump(0.35)

    def wait_for_after(pos: int, needle: str, timeout: float = 180.0) -> None:
        end = now() + timeout
        while now() < end:
            if buf.find(needle, pos) != -1:
                return
            if dead:
                tail = buf[pos:][-800:].replace("\n", "\\n")
                sys.exit(f"агент завершился до {needle!r}; хвост вывода: {tail}")
            pump(0.05)
        tail = buf[pos:][-800:].replace("\n", "\\n")
        sys.exit(f"не дождались {needle!r}; хвост вывода: {tail}")

    def ask(text: str) -> None:
        # Ждём хвост предыдущего ответа относительно позиции ввода: строка
        # статистики печатается только после завершения хода. Команды чата
        # отвечают сразу, без футера.
        pos = len(buf)
        type_line(text)
        print(f"… {text}", flush=True)
        if not text.startswith("/"):
            wait_for_after(pos, "· промпт ~")

    # Онбординг: три вопроса, профиль собирается из диалога.
    for i, (needle, answer) in enumerate(STEPS, 1):
        wait_for(needle)
        print(f"… онбординг {i}/3", flush=True)
        type_line(answer)
    wait_for("профиль пополнен из диалога")
    wait_for("goagent> ")

    # Вопрос в исходном профиле: кратко, без кода.
    ask(QUESTION)
    # Правка предпочтения обычной репликой и тот же вопрос ещё раз.
    ask(UPDATE)
    ask(QUESTION)

    # Показать профиль, который к этому моменту собрался, и то, что он
    # действительно уходит в системный промпт каждого запроса.
    ask("/profile")
    ask("/context")

    # Выйти корректно, чтобы сессия сохранилась.
    type_line("/exit")
    pump(1.0)
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()

    with open(out_path, "w", encoding="utf-8") as f:
        json.dump({"version": 2, "width": WIDTH, "height": HEIGHT,
                   "timestamp": time.time(), "env": {"TERM": "xterm-256color"}},
                  f, ensure_ascii=False)
        f.write("\n")
        for t, kind, data in events:
            f.write(json.dumps([t, kind, data], ensure_ascii=False) + "\n")
    print(f"каст записан: {out_path} ({len(events)} событий, {events[-1][0]:.1f} с)")


if __name__ == "__main__":
    main()
