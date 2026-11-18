# agent.md — карта проекта для разработки

CLI-чат-агент на Go: цикл «запрос → выбор инструмента → результат → ответ»,
сжатие контекста, ветвление диалога и явная модель памяти. Это памятка для
человека и ИИ-агента, который работает в этом репозитории: как собирать, где
что лежит, какие инварианты нельзя ломать. Пользовательская документация —
в [README.md](README.md), отчёт по памяти — в [MEMORY.md](MEMORY.md).

## Сборка, тесты, запуск

```bash
gofmt -l .                # форматирование; вывод должен быть пустым
go vet ./...              # статический анализ
go test -count=1 ./...    # все пакеты должны быть зелёными
go build -o agent .       # или просто ./run.sh — соберёт и запустит

# запуск: нужен OpenAI-совместимый эндпоинт
export LLM_BASE_URL="https://..."
export OPENAI_API_KEY="..."
./run.sh                  # интерактивный чат (run.sh подхватит ~/.env)
./run.sh -script testdata/checks-day9.txt   # прогон сценария без человека
```

Модель по умолчанию задаётся в `internal/config` (`ModelSet`, `DefaultModel`);
переопределяется `LLM_MODEL` / `LLM_SUMMARY_MODEL` или флагами `-model`,
`-summary-model`. Все реальные прогоны — на `glm-5.3-flash`.

Локальные модели (эксперименты): Ollama отдаёт OpenAI-совместимый эндпоинт,
агент подключается так:

```bash
LLM_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama \
  ./agent -model qwen2.5:14b -summary-model qwen2.5:14b -max-tokens 4096
```

Замечание: у Ollama дефолтный контекст 32768; на машине с 24 ГБ памятью
модель heavy q8_0 из-за этого уезжает на CPU и таймаутит — если нужен локальный
сервер, стартуйте с `OLLAMA_CONTEXT_LENGTH=8192`.

## Структура

```
main.go               CLI: флаги, REPL, команды, printContext, офлайн-ветки
memory_cmd.go         /memory, -memory-report, -import-memory
internal/agent/       цикл агента: Chat, стриминг, вызовы инструментов
internal/compact/     стратегии контекста (summary/window/facts) + экстрактор
                      памяти (memory_extract.go: memorySystemPrompt, ImportFacts)
internal/memory/      модель памяти: Store, слои, Relevant, рендер в промпт
internal/history/     event-sourced session.jsonl: события message/compact/
                      evict/facts/memory/fork, реплей при загрузке
internal/llm/         HTTP-клиент OpenAI-совместимого API, SSE-стрим, reasoning
internal/tools/       реестр инструментов: now, calc, find_riddle (tasks.txt)
internal/tokens/      оценка токенов и калибровка (EMA по факту ответа)
internal/config/      список моделей, лимиты, Truncate/OneLine
internal/readline/    строчный редактор с ANSI
testdata/             сценарии прогонов (checks-day9.txt, day11-*.txt)
scripts/              render_cast.py — сборка демо-видео из asciinema-каста,
                      cascade.sh — каскадная проверка памяти,
                      check_memory.py — проверка ответов по журналу
video/                mp4-демо — в .gitignore, в репозиторий не попадает
MEMORY.md             отчёт дня 11: модель памяти, токены, ablation
ANALYSIS.md           отчёт дня 10: сравнение стратегий
```

## Модель памяти (коротко)

Слои, каждый со своим правилом попадания в промпт:

| слой | хранение | в промпт | лимит |
|---|---|---|---|
| short | `data/<сессия>/session.jsonl` | последние N сообщений (+конспект в summary) | `-window` |
| working | `data/<сессия>/memory/working.json` | всегда | 12 |
| profile | `data/shared/profile.json` (общий) | всегда | 8 |
| decisions | `data/<сессия>/memory/long.json` | всегда, с датой | 12 |
| knowledge | `data/<сессия>/memory/long.json` | только релевантные вопросу, до 6 | 20 |

Маршрутизация — моделью: один вызов экстрактора на ход (`Compactor.ExtractMemory`,
модель = `-summary-model`) возвращает дифф `{"working":…,"profile":…,…}`.
Никаких правил по ключевым словам — эвристика удалена: на реальной сессии
она отправляла 34/34 факта в один слой. Импорт фактов дня 10
(`-import-memory`) — тот же принцип: один вызов модели с тем же
`memorySystemPrompt`, лимиты слоёв названы в промпте.

## Инварианты

1. **session.jsonl — только добавление.** Всё состояние (окно, конспект,
   архив, память) восстанавливается реплеем событий. Не переписывайте
   существующие события, не меняйте формат задним числом.
2. **Файлы памяти — источник истины**, событие `memory` в журнале — аудит
   и реплей. Запись файлов только атомарная (`.tmp` + rename).
3. **Лимиты слоёв — в коде** (`memory.Caps`), не в промпте. Промпт может
   сообщать лимиты модели, но отсечение делает `capEntries`.
4. **Каждое изменение памяти** проходит: `Apply` → `Save` → `AppendMemory`
   (журнал). Ошибка экстрактора оставляет память прежней и не роняет ход.
5. **Идемпотентность реплея**: `history.OpenHistory` может читать лог,
   написанный любой прежней версией; старые события должны продолжать
   применяться.
6. **Без глобального состояния**: `Agent`/`Compactor`/`Store` — обычные
   структуры, конфигурация через сеттеры. Пакетные переменные — только
   константы уровня процесса (таймауты, `ModelSet`).
7. **Русский язык в интерфейсе и ошибках** — так заведено по всему коду;
   сообщения об ошибках для пользователя формулируются по-русски.

## Команды чата и флаги (шпаргалка)

`/help /exit /new /session /model /summary /strategy /facts /memory
/memory clear <слой> /memory add <слой> ключ=значение /branch /system
/tools /history /context /tokens /compact /recall`

Флаги офлайна (без сети): `-print-session <id|last>`, `-memory-report
<id|last>`, `-list-sessions`, `-list-models`, `-import-memory <сессия>
-session <новая>`.

## Соглашения по коммитам и артефактам

- **Агент сам не коммитит.** Я готовлю изменения и предлагаю: коммитить или
  нет, и называю готовый заголовок коммита (например, `DAY11: …`). Команду
  `git commit` выполняет пользователь; без его прямого указания я коммитов
  не создаю.
- Коммиты — короткая строка на русском, префикс дня для фич: `DAY11:
  реализация памяти и слоев`, рефакторинги — `refactor: …`.
- Демо-видео: имена по дням (`память-день-11.mp4`), без аудиодорожки и
  без метаданных поворота, 1352x970, 8 fps; `video/` в .gitignore.
  Сборка воспроизводимая: `asciinema rec --overwrite -f asciicast-v2
  --window-size 148x50 -c "./agent -data /tmp/day11-video -session demo
  -strategy window -window 2 -model glm-5.3-flash -summary-model glm-5.3-flash
  -no-reasoning -script testdata/day11-demo.txt" /tmp/day11-demo.cast`, затем
  `python3 scripts/render_cast.py --cast /tmp/day11-demo.cast --out video/…`.
- Прогоны сценариев лежат в `/tmp/day11*/` — в репозиторий попадают только
  сценарии (`testdata/`) и вывод, если он нужен отчёту.
