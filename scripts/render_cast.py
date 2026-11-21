#!/usr/bin/env python3
"""Рендер asciinema-записи в mp4: каст → кадры → видео.

Зачем свой рендерер: agg/terminalizer в этом окружении недоступны, а прошлые
скрипты жили в /tmp и были потеряны вместе с кастом. Здесь всё, что нужно для
пересборки видео: эмуляция экрана (без ANSI — в headless-записи цветов нет),
сжатие пауз «модель думает», титульная карточка и карточки с результатами.

    python3 scripts/render_cast.py --cast /tmp/day11-demo.cast \
        --out video/память-день-11.mp4
"""

import argparse
import json
import re
import subprocess
import sys

from PIL import Image, ImageDraw, ImageFont

BG = (16, 16, 20)
FG = (222, 222, 222)
DIM = (130, 130, 140)
ACCENT = (120, 190, 255)

FONT_PATH = "/System/Library/Fonts/Menlo.ttc"
FONT_SIZE = 15
LINE_H = 19
PAD = 10

SPINNER = re.compile(r"\A\r[ \t]*[\\/|-]?[ \t]*\Z")


def font(size=FONT_SIZE, bold=False):
    return ImageFont.truetype(FONT_PATH, size, index=1 if bold else 0)


class Screen:
    """Минимальный терминал: печать, \\r, \\n, \\b, перенос строк и скролл."""

    def __init__(self, cols, rows):
        self.cols = cols
        self.rows = rows
        self.grid = [[" "] * cols for _ in range(rows)]
        self.row = 0
        self.col = 0
        self.status = ""  # строка спиннера, в сетку не попадает

    def _newline(self):
        self.row += 1
        if self.row >= self.rows:
            self.grid.pop(0)
            self.grid.append([" "] * self.cols)
            self.row = self.rows - 1

    def _put(self, ch):
        if self.col >= self.cols:
            self.col = 0
            self._newline()
        self.grid[self.row][self.col] = ch
        self.col += 1

    def feed(self, data):
        # Спиннер — отдельная строка, которая тут же стирается: в кадр её не
        # тащим, иначе она попадает в снимки и рвёт плавность.
        if SPINNER.match(data):
            self.status = data.replace("\r", "").rstrip()
            return
        i = 0
        while i < len(data):
            ch = data[i]
            if ch == "\r":
                self.col = 0
                if self.status:
                    self.status = ""
            elif ch == "\n":
                # Перевод строки без возврата каретки встречается в
                # синтезированных кастах (run_to_cast.py пишет простой "\n").
                # Настоящий терминал в режиме ONLCR сам возвращает курсор к
                # левому краю; без этого сброса каждая следующая строка
                # начиналась бы с колонки, где закончилась предыдущая, — текст
                # уезжал вправо и обрывался на краю экрана.
                self.col = 0
                self._newline()
            elif ch == "\b":
                self.col = max(0, self.col - 1)
            elif ch == "\t":
                # Табуляция — не печатаемый символ: в сетке он превращается в
                # квадрат (у моноширинного шрифта нет такого глифа). Двигаем
                # курсор до следующего таб-стопа в 4 колонки, как в редакторе.
                self.col = min(self.cols - 1, (self.col // 4 + 1) * 4)
            elif ch == "\x1b":
                # ANSI в headless-записи не встречается, но на всякий случай
                # пропускаем последовательность целиком.
                m = re.match(r"\x1b\[[0-9;]*[A-Za-z]", data[i:])
                i += len(m.group(0)) if m else 1
                continue
            else:
                self._put(ch)
            i += 1

    def snapshot(self):
        # status в сравнение не входит: спиннер меняется каждые 130 мс, и если
        # считать его частью кадра, ожидание модели растянет видео на минуты.
        return ("\n".join("".join(r).rstrip() for r in self.grid), self.row)


def snapshots(path):
    """Снимки экрана: только там, где меняется содержимое, а не спиннер."""
    events = []
    with open(path, encoding="utf-8") as f:
        header = json.loads(f.readline())
        for line in f:
            line = line.strip()
            if not line:
                continue
            t, kind, data = json.loads(line)
            if kind == "o":
                events.append((t, data))

    scr = Screen(header["width"], header["height"])
    out = []
    prev = None
    for t, data in events:
        scr.feed(data)
        snap = scr.snapshot()
        if snap != prev:
            out.append((t, (snap[0], snap[1], scr.status)))
            prev = snap
    return header, out


def video_times(snaps, fast=0.05, cap=0.45, pause=1.8, read=0.14, read_max=3.0, settle=2.4):
    """Сжимает паузы: поток текста идёт почти как в жизни, ожидание модели —
    один короткий бит. Плюс время на чтение: чем больше строк появилось,
    тем дольше кадр висит (иначе /memory пролетает за один кадр)."""
    times = []
    prev_t = 0.0
    prev_text = ""
    prev_burst = 0
    for i, (t, snap) in enumerate(snaps):
        text = snap[0]
        if i == 0:
            times.append(0.0)
            prev_t, prev_text = t, text
            continue
        dt = t - prev_t
        step = pause if dt > 2.0 else max(fast, min(dt, cap))
        # Новые строки, а не разница количества: экран прокручивается, и
        # «длина текста» всегда равна высоте терминала.
        old = set(prev_text.splitlines())
        added = sum(1 for l in text.splitlines() if l and l not in old)
        if added > 0:
            step += min(read_max, read * added)
        # Таблица памяти или /context печатается не одним кадром, а десятком
        # мелких. Копим строки, пока терминал не замолчал: если перед новой
        # командой на экране осел крупный блок — держим его отдельно.
        burst = added if dt > 0.5 else prev_burst + added
        last = [l for l in text.splitlines() if l.strip()]
        if prev_burst >= 6 and last and last[-1].startswith("agent> "):
            step += settle
        times.append(times[-1] + step)
        prev_t, prev_text, prev_burst = t, text, burst
    return times


def draw_screen(draw, snap, f, fdim):
    text, cursor_row, status = snap
    y = PAD
    for line in text.split("\n"):
        if line:
            draw.text((PAD, y), line, font=f, fill=FG)
        y += LINE_H
    if status:
        draw.text((PAD, PAD + cursor_row * LINE_H), status, font=f, fill=DIM)


def card(lines, size=(1348, 982)):
    """Титульная карточка и карточки результатов: (текст, размер, цвет, отступ).

    Крупные строки центрируются каждая сама по себе, мелкие — одним блоком с
    общим левым краем: иначе таблица, набранная пробелами, выглядит рваной.
    """
    img = Image.new("RGB", size, BG)
    draw = ImageDraw.Draw(img)
    fonts = [font(sz, bold=sz >= 26) for _, sz, _, _ in lines]
    total = sum(sz + gap for _, sz, _, gap in lines)
    y = (size[1] - total) / 2
    body = [draw.textlength(t, font=f) for (t, sz, _, _), f in zip(lines, fonts)
            if t and sz < 26]
    body_x = (size[0] - max(body, default=0)) / 2
    for (text, sz, color, gap), f in zip(lines, fonts):
        if sz >= 26:
            x = (size[0] - draw.textlength(text, font=f)) / 2
        else:
            x = body_x
        draw.text((x, y), text, font=f, fill=color)
        y += sz + gap
    return img


def card_frames(seconds, fps=8):
    return int(seconds * fps)


CARD_COLORS = {"accent": ACCENT, "fg": FG, "dim": DIM}


def load_cards(path):
    """Читает карточки результатов: [{seconds, lines: [[text, size, color, gap]]}].

    Цвета — имена accent/fg/dim. Встроенные карточки дня 11 остаются по
    умолчанию: пересборка старого видео не зависит от внешнего файла, а новый
    день передаёт свой --cards.
    """
    with open(path, encoding="utf-8") as f:
        raw = json.load(f)
    out = []
    for c in raw:
        lines = []
        for text, size, color, gap in c["lines"]:
            lines.append((text, int(size), CARD_COLORS[color], int(gap)))
        out.append((float(c.get("seconds", 6)), lines))
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--cast", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--fps", type=int, default=8)
    ap.add_argument("--title", default="День 11 · Модель памяти")
    ap.add_argument("--subtitle",
                    default="слои, которые попадают в промпт по разным правилам")
    ap.add_argument("--no-cards", action="store_true")
    ap.add_argument("--cards", default="", help="JSON с карточками результатов вместо стандартных дня 11")
    args = ap.parse_args()

    header, snaps = snapshots(args.cast)
    times = video_times(snaps)
    width = header["width"] * 9 + 2 * PAD
    height = header["height"] * LINE_H + 2 * PAD
    width += width % 2
    height += height % 2

    f = font()
    fdim = font()

    proc = subprocess.Popen(
        ["ffmpeg", "-v", "error", "-y",
         "-f", "rawvideo", "-pix_fmt", "rgb24", "-s", f"{width}x{height}",
         "-r", str(args.fps), "-i", "-",
         "-c:v", "libx264", "-preset", "medium", "-crf", "20",
         "-pix_fmt", "yuv420p", "-an", "-movflags", "+faststart",
         args.out],
        stdin=subprocess.PIPE)

    def emit(img):
        proc.stdin.write(img.tobytes())

    fps = args.fps
    if not args.no_cards:
        t = card([(args.title, 34, ACCENT, 22), (args.subtitle, 18, DIM, 0)],
                 size=(width, height))
        for _ in range(card_frames(4, fps)):
            emit(t)

    cur = Image.new("RGB", (width, height), BG)
    for i, (t_v, (t, snap)) in enumerate(zip(times, snaps)):
        next_t = times[i + 1] if i + 1 < len(times) else t_v + 1.5
        cur = Image.new("RGB", (width, height), BG)
        draw = ImageDraw.Draw(cur)
        draw_screen(draw, snap, f, fdim)
        n = max(1, round((next_t - t_v) * fps))
        for _ in range(n):
            emit(cur)

    if not args.no_cards:
        if args.cards:
            for seconds, lines in load_cards(args.cards):
                c = card(lines, size=(width, height))
                for _ in range(card_frames(seconds, fps)):
                    emit(c)
        else:
            abl = card([
                ("Проверка: 8 вопросов в каждом состоянии памяти", 26, ACCENT, 26),
                ("всё на месте            8/8", 22, FG, 10),
                ("без knowledge           6/8", 22, FG, 10),
                ("без decisions           6/8", 22, FG, 10),
                ("без working             8/8", 22, FG, 10),
                ("без short               8/8", 22, FG, 10),
                ("", 10, FG, 0),
                ("вопросы, которые ломаются, — про адрес и про решение", 18, DIM, 0),
            ], size=(width, height))
            for _ in range(card_frames(6, fps)):
                emit(abl)
            tok = card([
                ("Что уходит в модель: ~3208 из 6000 токенов", 26, ACCENT, 26),
                ("short (окно + конспект)    ~2681", 22, FG, 10),
                ("tools                       ~388", 22, FG, 10),
                ("system                      ~256", 22, FG, 10),
                ("working                     ~180", 22, FG, 10),
                ("decisions                    ~47", 22, FG, 10),
                ("profile                      ~43", 22, FG, 10),
                ("knowledge                    ~26   ← только релевантные вопросу", 22, FG, 0),
            ], size=(width, height))
            for _ in range(card_frames(6, fps)):
                emit(tok)

    proc.stdin.close()
    proc.wait()
    print(f"готово: {args.out} ({width}x{height}, {fps} fps)", file=sys.stderr)


if __name__ == "__main__":
    main()
