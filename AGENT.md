# goagent — карта проекта для разработки

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
go build -o goagent .       # или просто ./run.sh — соберёт и запустит

# запуск: нужен OpenAI-совместимый эндпоинт
export LLM_BASE_URL="https://..."
export OPENAI_API_KEY="..."
# необязательно: заголовки, которых требует шлюз (opencode просит сессию)
export LLM_EXTRA_HEADERS="x-opencode-session: goagent-cli"
./run.sh                  # интерактивный чат (run.sh подхватит ~/.env)
./run.sh -script testdata/checks-day9.txt   # прогон сценария без человека
```

Модель по умолчанию задаётся в `internal/config` (`ModelSet`, `DefaultModel`);
переопределяется `LLM_MODEL` / `LLM_SUMMARY_MODEL` или флагами `-model`,
`-summary-model`. Все реальные прогоны — на `opencode-go/deepseek-v4.1-flash`
(шлюз opencode требует префикс провайдера в id; без него — `402`). Ставки
хранятся раздельно — вход, выход и чтение из кеша (`RateInPerM`,
`RateOutPerM`, `RateCachePerM`); стоимость считается по фактическому
`usage`, поэтому кешированный префикс промпта стоит в 50 раз дешевле и виден
в футере как `кеш N/M`.

Локальные модели (эксперименты): Ollama отдаёт OpenAI-совместимый эндпоинт,
агент подключается так:

```bash
LLM_BASE_URL=http://localhost:11434/v1 OPENAI_API_KEY=ollama \
  ./goagent -model qwen2.5:14b -summary-model qwen2.5:14b -max-tokens 4096
```

Сжатие можно увести на локальную модель, оставив чат на платном эндпоинте:
`LLM_SUMMARY_BASE_URL` (плюс `LLM_SUMMARY_API_KEY`, `LLM_SUMMARY_EXTRA_HEADERS`)
задаёт отдельный клиент только для конспекта, фактов и экстрактора памяти.
Экстрактор вызывается каждый ход, поэтому локальная модель экономит и деньги,
и лимиты провайдера; запасная попытка при её сбое по-прежнему идёт на
чат-модель.

```bash
# чат — opencode, сжатие — локальный Ollama
export LLM_SUMMARY_BASE_URL=http://localhost:11434/v1
./goagent -summary-model qwen2.5:14b
```

Замечание: у Ollama дефолтный контекст 32768; на машине с 24 ГБ памятью
модель heavy q8_0 из-за этого уезжает на CPU и таймаутит — если нужен локальный
сервер, стартуйте с `OLLAMA_CONTEXT_LENGTH=8192`.

## Структура

```
main.go               CLI: флаги, REPL, команды, printContext, офлайн-ветки
cmd/mcp-cbr/          MCP-сервер курсов ЦБ РФ (день 17)
cmd/mcp-forex/        MCP-сервер Coinbase spot и JSONL-журнала (день 18)
cmd/mcp-pipeline/     MCP-сервер search → summarize → save_to_file (день 19)
cmd/mcp-fetch/        локальный HTTP-fetch MCP (день 17)
memory_cmd.go         /memory, -memory-report, -import-memory
profile_cmd.go        /profile: именованные профили, переключение, правка
internal/agent/       цикл агента: Chat, стриминг, вызовы инструментов
internal/compact/     стратегии контекста (summary/window/facts) + экстрактор
                      памяти (memory_extract.go: memorySystemPrompt, ImportFacts)
internal/memory/      модель памяти: Store, слои, Relevant, рендер в промпт,
                      время записи и замена по ключу (день 15)
internal/history/     event-sourced session.jsonl: события message/compact/
                      evict/facts/memory/fork, реплей при загрузке
internal/llm/         HTTP-клиент OpenAI-совместимого API, SSE-стрим, reasoning
internal/mcp/         MCP-клиент: mcp.json, stdio-транспорт, tools/list и
                      tools/call, allow-фильтр, path/url-политика, апрув
internal/workspace/   sandbox модели: ~/goagent-work, Resolve, секреты, пути
internal/forex/       Coinbase spot, append-only JSONL-журнал и отчёт
                      (день 18)
internal/scheduler/   фоновый цикл fetch → save → read_period → отчёт,
                      а также повторение разрешённого инструмента (день 18)
internal/pipeline/    цепочка search → summarize → save_to_file и
                      предсказуемая остановка на пустом/ошибке (день 19)
internal/tools/       реестр инструментов: now, calc, find_riddle (tasks.txt),
                      task_transition — запрос перехода задачи (день 13)
internal/task/        конечный автомат задачи: этапы, таблица переходов,
                      гейты плана и паузы, task.json сессии
internal/tokens/      оценка токенов и калибровка (EMA по факту ответа)
internal/config/      список моделей, лимиты, Truncate/OneLine
internal/readline/    строчный редактор с ANSI; Ctrl-C в ответе отменяет ход,
                      Ctrl-D на пустой строке — выход
testdata/             сценарии прогонов (checks-day9.txt, day11-*.txt,
                      day12-*: профили, вопрос, устойчивость, демо, карточки,
                      day13-*: сценарий и карточки автомата задачи,
                      day15-*: сценарий старения памяти, команды и карточки,
                      day16-*: сценарий MCP + локальный calc, карточки,
                      day17-*: MCP ЦБ РФ, демо и карточки,
                      day18-*: планировщик forex, MCP-конфиг, карточки,
                      day19-*: MCP-пайплайн, заметки, демо и карточки)
scripts/              render_cast.py — сборка демо-видео из asciinema-каста,
                      cascade.sh — каскадная проверка памяти,
                      check_memory.py — проверка ответов по журналу,
                      profile_experiment.sh / profile_stability.sh — матрица
                      персонализации и десять вопросов подряд,
                      check_profile.py — длина/код/обращение по журналу,
                      day12-demo.sh — подготовка данных для демо дня 12,
                      day12-onboard-demo.py — запись онбординга в pty
                      (пустой профиль → диалог → правка в диалоге),
                      day13-demo.sh — подготовка данных для демо дня 13,
                      check_task.py — автомат задачи по журналу и task.json,
                      day15-demo.sh — демо устаревания памяти,
                      check_memory_conflicts.py — замены, устаревшие ответы,
                      очистка working после done,
                      day16-demo.sh — MCP filesystem + локальный calc,
                      check_mcp.py — вызовы MCP/calc, схемы в дампе запроса,
                      day17-demo.sh — MCP ЦБ РФ + локальный calc,
                      check_day17.py — курс, calc, схемы в запросе,
                      day18-demo.sh — планировщик forex на 6 запусков,
                      check_day18.py — JSONL, накопление и отчёт по всем,
                      day18-chat.sh — чат-сценарий: 24 запуска и сводки,
                      check_day18_chat.py — аргументы, 24 запуска, отчёт,
                      day19-demo.sh — MCP-пайплайн и два сбоя,
                      check_day19.py — три шага, передача данных, остановки,
                      run_to_cast.py — детерминированная сборка каста из
                      session.jsonl (для видео, без вызовов модели),
                      log_to_cast.py — каст из настоящего stdout-лога консоли:
                      футер, «всего N», полные строки инструментов
video/                mp4-демо — в .gitignore, в репозиторий не попадает
MEMORY.md             отчёт дня 11: модель памяти, токены, ablation
PROFILE.md            отчёт дня 12: профиль, персонализация, проверки
MCP.md                MCP: протокол, конфиг, апрув, политика, guard
TASK.md               отчёт дня 19: композиция MCP, пайплайн и остановки
ANALYSIS.md           отчёт дня 10: сравнение стратегий
```

## Модель памяти (коротко)

Слои, каждый со своим правилом попадания в промпт:

| слой | хранение | в промпт | лимит |
|---|---|---|---|
| short | `data/<сессия>/session.jsonl` | последние N сообщений (+конспект в summary) | `-window` |
| working | `data/<сессия>/memory/working.json` | всегда | 12 |
| profile | `data/shared/profile.json` или `data/profiles/<имя>/profile.json` (`-profile`) | всегда, последним блоком | 8 |
| decisions | `data/<сессия>/memory/long.json` | всегда, с датой | 12 |
| knowledge | `data/<сессия>/memory/long.json` | только релевантные вопросу, до 6 | 20 |

Маршрутизация — моделью: один вызов экстрактора на ход (`Compactor.ExtractMemory`,
модель = `-summary-model`) возвращает дифф `{"working":…,"profile":…,…}`.
Никаких правил по ключевым словам — эвристика удалена: на реальной сессии
она отправляла 34/34 факта в один слой. Импорт фактов дня 10
(`-import-memory`) — тот же принцип: один вызов модели с тем же
`memorySystemPrompt`, лимиты слоёв названы в промпте.

## Устаревание и конфликты (день 15)

`Entry.UpdatedAt` — RFC3339Nano-время последней записи; его ставит код
(`memory.Now`), модель его не присылает. Правило конфликта живёт в
`mergeEntryChanged`: ключ сравнивается без учёта регистра и ё/е, при совпадении
запись заменяется (не добавляется вторая), а если у входящей записи время
раньше сохранённого — она отбрасывается как `stale`. `Apply` возвращает
`[]Change` (`add/update/remove/evict/stale`) с парой `old → new`; тот же журнал
пишется событием `memory` в `session.jsonl`, поэтому `/memory history` и
`-print-session` показывают историю замен. `Render` добавляет к каждой записи
`[02.01 15:04]`, чтобы модель видела, какая версия новее. У `decisions`
`canonicalKey` снимает ведущую дату (`2026-11-21 · …`): экстрактор иногда
копирует её из `Render`, и без нормализации замена шла бы новым ключом.

Очистка после задачи — код, не промпт: `task_transition` и `/task state done`
при первом входе в `done` вызывают `Agent.TaskDone`, который делает
`ClearWithChanges(working)`,
сохраняет store и пишет removals в журнал. `decisions` и `knowledge` не
трогаются. `/memory prompt` печатает ровно `Compactor.MemoryBlock()` — блок,
который уходит в системный промпт, поэтому отсутствие старых значений можно
проверить напрямую, а не спрашивать модель.

## MCP (дни 16–19)

MCP — общая настройка агента, а не только демо. `mcp.json` ищется по
`-mcp-config` → `MCP_CONFIG` → `./mcp.json` → `~/.config/goagent/mcp.json` →
`<data>/mcp.json`; `-no-mcp` выключает. Серверы запускаются до первой сессии,
их инструменты регистрируются как `mcp_<server>_<tool>` и попадают в
`/tools`, `-script` и после `/new`/`/session`.

`internal/mcp/client.go` подключается через официальный
`github.com/modelcontextprotocol/go-sdk` (`CommandTransport`, stdio),
вызывает `tools/list` с курсором и `tools/call`. `allow` — белый список
инструментов; `write_file` у filesystem-сервера не регистрируется. Схема
`InputSchema` превращается в `map[string]any`, описание можно перекрыть
`descriptions`.

Апрув стоит в коде до `ToolRegistry.Execute`, а не в промпте: `Tool.Approval`
(`allow/ask/deny`) и `Agent.SetApprover`. `y` — один раз, `n` — отказ
(модель получает результат «отклонён» и продолжает), `a` — до конца сессии;
`/mcp approvals` и `/mcp reset` управляют списком. В TTY по умолчанию `ask`,
в `-script`/пайпе `auto` и `ask` работают как `deny`; `-mcp-approval=allow`
нужен для неинтерактивного демо.

`internal/mcp/policy.go` проверяет `path_under` (Abs/Clean/Rel/EvalSymlinks) и
`url_hosts` (http/https, домен/поддомен, блокировка localhost/private/
link-local). `-dump-request <path>` пишет NDJSON тел запросов к чат-модели;
в демо в одном теле видны и `calc`, и `mcp_fs_read_file`.

Ответ MCP не считается доверенным: `internal/mcp/guard.go` убирает ANSI,
управляющие и невидимые символы, ограничивает размер и оборачивает результат
в `<untrusted_tool_output source="mcp:server/tool">`. `-mcp-guard`:
`heuristic` (по умолчанию, без вызова модели, помечает типовые инъекции) или
`model` (отдельный вызов модели сжатия, JSON `safe/reason`; небезопасный
результат блокируется и до основной модели не доходит). `-mcp-guard-model`
переопределяет модель. `-mcp-action-guard=model` дополнительно проверяет
намерение перед апрувом; по умолчанию `off`, потому что это ещё один вызов на
каждый MCP-инструмент. Решения guard пишутся событием `guard` в
`session.jsonl`.

Свой MCP-сервер — обычный бинарник в `cmd/`: `mcp-cbr` (курсы ЦБ РФ),
`mcp-forex` (Coinbase spot + JSONL), `mcp-pipeline` (`search` → `summarize`
→ `save_to_file`) и `mcp-fetch` (локальный HTTP). Он запускается как
дочерний процесс по `mcp.json`, общается по stdio и не является частью
бинаря `goagent`. Планировщик вызывает такие серверы через
`Manager.CallTrusted`: аргументы проходят ту же политику, но guard не
применяется, потому что результат нужен коду, а не модели; это допустимо
только для доверенного сервера.

MCP-серверы запускаются в отдельной process group, чтобы `Ctrl-C` в агенте не
убивал их вместе с прерванным ходом. Если сессия всё же закрылась, `Manager`
перезапускает сервер по его конфигу и повторяет вызов; в ошибку попадает
`stderr` сервера.

## Планировщик и накопление (день 18)

Планировщик работает в двух режимах. В чате модель вызывает локальный
инструмент `run_forex_schedule`: пользователь формулирует расписание словами,
код проверяет аргументы и запускает блокирующий цикл. Второй режим — CLI без
чат-модели, для фоновых прогонов и старого демо. MCP-серверы поднимаются до
настройки LLM, затем `internal/scheduler` вызывает `forex/fetch_fx`,
`forex/save_tick` и по времени `forex/read_period`. У `read_period` есть
`summary=true` (агрегаты и `runs` без сырых тиков) и `limit`/`offset`; отдельный
`list_periods` возвращает только серии журнала.

Чат-сценарий:

```text
Запусти проверку USD/RUB каждые 5 секунд, сохраняй результаты,
каждую минуту выводи сводку, заверши через 2 минуты.
```

Модель вызывает `run_forex_schedule` с `interval_seconds`, `duration_seconds`,
`summary_every_seconds` и `pair`. Числа считает `internal/forex`, а текстовую
сводку — локальная модель через `LLM_SUMMARY_*`. Сводка идёт параллельно
сборам: медленный ответ qwen не сдвигает следующий замер. Ошибка сводки не
отменяет числовой отчёт.

Для одиночного отложенного действия есть `run_tool_once`: он принимает
только `tool`, `args` и `delay_seconds` и вызывает инструмент ровно один раз.
Его используем для «через 30 секунд собери отчёт» — без выдуманных
`interval_seconds`, `duration_seconds` и `report_every_seconds`.

Для повторяющихся вызовов без сохранения и сводки есть второй инструмент —
`run_tool_schedule`. Он принимает `tool`, `args`, `delay_seconds`,
`interval_seconds`, `duration_seconds` и `report_every_seconds`, но набор
инструментов закрыт кодом: `calc`, `mcp_rates_currency_rate` (ЦБ РФ),
`mcp_forex_fetch_fx` (Coinbase spot), `mcp_forex_read_period` (сырые тики
или `summary:true`) и `mcp_forex_list_periods` (серии журнала). Модель не
может подставить произвольный MCP-инструмент. `delay_seconds` откладывает
первый запуск. Результаты всех запусков возвращаются в JSON, а ход диалога
блокируется до конца расписания.

```json
run_tool_schedule({
  "tool": "calc",
  "args": {"expr": "2+2"},
  "interval_seconds": 2,
  "duration_seconds": 10
})
```

Один отложенный вызов:

```json
run_tool_once({
  "tool": "mcp_forex_read_period",
  "args": {"quote": "RUB", "summary": true},
  "delay_seconds": 30
})
```

CLI-режим:

```bash
FOREX_STORE=/tmp/day18-demo/forex.jsonl \
  ./goagent -mcp-config testdata/day18-mcp.json -mcp-approval=allow \
  -schedule-interval 10s -schedule-runs 6 -schedule-report-every 3 \
  -schedule-pair USD/RUB
```

`-schedule-runs 0` — работать до Ctrl-C; сигнал отменяет контекст. Ошибка
отдельного запуска логируется, цикл продолжается. `internal/forex/store.go`
пишет append-only JSONL (`FOREX_STORE` или
`~/.local/share/goagent/forex/forex.jsonl`), поэтому
накопление переживает перезапуск; `report.BuildReport` считает
min/max/avg/delta по **всем** замерам периода, а не по последнему. Демо и
проверка — `scripts/day18-chat.sh` / `scripts/check_day18_chat.py` и
`scripts/day18-demo.sh` / `scripts/check_day18.py`.

## Композиция MCP (день 19)

`cmd/mcp-pipeline` — один сервер с тремя инструментами: `search` рекурсивно
ищет по текстовым файлам workdir, `summarize` делает экстрактивное резюме по
`items`, а `save_to_file` пишет текст внутри `PIPELINE_OUT_DIR`
(`<workdir>/reports`). Локального `run_pipeline` больше нет: модель сама
строит цепочку и передаёт данные между вызовами.

Порядок для просьбы «найди, сделай резюме и сохрани»:

```json
mcp_pipeline_search({"query": "MCP", "path": "notes"})
mcp_pipeline_summarize({"items": [...], "max_sentences": 5})
mcp_pipeline_save_to_file({"path": "mcp-summary.md", "content": "..."})
```

`search` возвращает `items`; их массив целиком передаётся в `summarize`.
`summarize` возвращает `summary`; он (или текст на его основе) передаётся в
`content` для `save_to_file`. Ничего не придумывается: если `search` вернул
`count=0`, `summarize` не вызывается; если `summarize` вернул ошибку,
`save_to_file` не вызывается.

`path` у `search` — необязательный подкаталог workdir. Ответ содержит
`path`, `line`, `snippet` вокруг первого совпадения, `scanned`, `skipped` и
`truncated`; лимиты — 1 МиБ на файл, 2000 файлов, 20 МиБ суммарно. Секреты,
скрытые файлы и бинарники пропускаются.

Пустой `search` и ошибка любого шага возвращаются модели как данные/ошибка,
а не как повод продолжать. В системный промпт добавлены правила 6 и 7: при
пустом результате или ошибке не выдумывать данные и не вызывать следующий
шаг; для цепочки самому передавать `items` из `search` в `summarize`, а
`summary` — в `content` `save_to_file`.

Флаги `empty` у `search` и `fail` у `summarize`/`save_to_file` нужны только
для воспроизводимой проверки сбоев. Демо — `scripts/day19-demo.sh`, проверка
— `scripts/check_day19.py` (**34 проверки**), заметки —
`testdata/day19-notes`. В демо стоит `-max-tool-chars 6000`: при стандартных
4000 символах модель видит только часть `items` и делает лишние `search`,
чтобы восстановить данные.

## Рабочий каталог модели (день 19)

У модели один sandbox, по умолчанию `~/goagent-work`. Его выбирает
`-workdir`, затем `AGENT_WORK_DIR`, иначе домашний каталог. При старте
создаются `notes/` и `reports/`; `MCP_FS_ROOT`, `PIPELINE_SEARCH_ROOT` и
`PIPELINE_OUT_DIR` выставляются из него до запуска MCP-серверов.

`internal/workspace.Resolve` принимает только относительный путь, проверяет
`Abs`/`Rel` и `EvalSymlinks`, поэтому `..`, абсолютный путь и symlink за
пределы workdir отклоняются. `search` не следует по symlink-каталогам,
пропускает скрытые имена, секреты (`.env*`, `*.pem`, `*.key`, `*.p12`,
`id_rsa*`, `id_ed25519*`, `credentials*`, `secrets*`) и файлы с NUL-байтом.
Для `fs.read_file` в `mcp.json` добавлено `deny_names`; `list_directory`
может показать имя, но содержимое секрета прочитать нельзя.

## Персонализация (день 12)

Профиль — не слой сессии, а отдельный файл на пользователя: `-profile <имя>`
берёт `data/profiles/<имя>/profile.json`, без флага — прежний
`data/shared/profile.json`. В системном промпте блок профиля идёт **последним**
(`memory.promptOrder`): предпочтение — это инструкция, а не факт, и ближайший к
диалогу блок должен быть ей.

Предпочтения хранятся под стабильными ключами `стиль`, `формат`, `обращение`,
`ограничения` — их заполняет тот же экстрактор, что раскладывает память:
«пиши кратко», «без кода», «на ты» попадают в `profile`, случайный тон одной
реплики — нет. Переключение (`/profile use`) ничего не перезаписывает; правка —
`/profile set`, очистка — `/profile clear`. При интерактивном старте
печатается активный профиль (`printProfileLine`): общий профиль из прошлой
задачи не должен применяться молча. Если профиль пуст, `onboardProfile` задаёт
три вопроса (обращение и роль, стиль и формат, ограничения) и отдаёт ответы
тому же экстрактору — код ключи не придумывает. Существующий профиль сначала
показывается с вопросом «использовать? [Y/n]»; «n» очищает его до интервью.
`-skip-profile` отключает диалог для сценариев, замеров и записи видео; не-TTY
stdin и `-script` не спрашивают и без флага. Проверки — `scripts/profile_*.sh`,
цифры — в [PROFILE.md](PROFILE.md).

## Инварианты проекта (день 14)

Проектные ограничения хранятся в `<data>/invariants.json` — **отдельно от
диалога**: `/new`, `/session` и новый лог их не трогают. У каждого инварианта
есть `id`, `area`, текст и машинное правило `deny` / `allow` / `max`.
`/invariant` показывает набор, `/invariant add|change|remove` меняет его,
`/invariant history [id]` — журнал с парой «было → стало». Менять набор может
только пользователь: у модели нет такого действия.

Проверка двухслойная:

1. **`check_solution`** — ленивый инструмент, видимый только при непустом
   наборе. Модель обязана вызвать его перед ответом и перечислить id
   затронутых инвариантов и конкретные выборы (`area`, `value`). Код проверяет
   значения и требует, чтобы все затронутые области были объявлены; иначе
   ответ не выпускается. Это детерминированная гарантия.
2. **Локальная модель** — после `check_solution` свободный текст ответа
   просматривает модель с эндпоинта сжатия (`LLM_SUMMARY_*`, по умолчанию
   Ollama). Она возвращает `{violates,id,quote,reason}` и ловит смысловые
   нарушения, которые регулярка не видит. Если эндпоинт недоступен, остаётся
   консервативный поиск запрещённых терминов; отказ виден в trace.
   Флаги: `-invariant-check llm|keywords`, `-invariant-model <id>`,
   `-invariants <path>`.

Гейт в `Agent.Chat`: при активном наборе финальный текст не отдаётся, пока
`check_solution` не прошёл, а текстовая проверка не сказала «чисто». Нарушающий
черновик остаётся в `session.jsonl` (аудит), в диалог добавляется служебная
реплика `[проверка кода · инварианты] …`, и модель переписывает ответ; после
трёх отказов ход завершается ошибкой. В stdout нарушающий текст не попадает:
при активных инвариантах стриминг ответа буферизуется, reasoning продолжает
идти. Блок инвариантов стоит в системном промпте после памяти и перед задачей.

## Инварианты кода

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
/memory clear <слой> /memory add <слой> ключ=значение
/profile /profile list /profile use <имя> /profile set ключ=значение
/profile clear /branch /system /tools /history /context /tokens /compact
/recall /invariant [list | add | change | remove | history]
/mcp [status | approvals | reset]`

Флаги офлайна (без сети): `-print-session <id|last>`, `-memory-report
<id|last>`, `-list-sessions`, `-list-models`, `-import-memory <сессия>
-session <новая>`. Профиль: `-profile <имя>`. Инварианты: `-invariants
<path>`, `-invariant-check llm|keywords`, `-invariant-model <id>`. MCP: `-mcp-config <path>`, `-no-mcp`,
`-mcp-approval auto|allow|ask|deny`, `-mcp-guard off|heuristic|model`,
`-mcp-guard-model <id>`, `-mcp-action-guard off|model`, `-dump-request <path>`.
Планировщик: `-schedule-interval <dur>`, `-schedule-runs N`,
`-schedule-report-every N`, `-schedule-pair BASE/QUOTE` (0 запусков — до Ctrl-C).
`-no-memory` — не обновлять слоистую память (история и `/memory` остаются).

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
  --window-size 148x50 -c "./goagent -data /tmp/day11-video -session demo
  -strategy window -window 2 -model glm-5.3-flash -summary-model glm-5.3-flash
  -no-reasoning -script testdata/day11-demo.txt" /tmp/day11-demo.cast`, затем
  `python3 scripts/render_cast.py --cast /tmp/day11-demo.cast --out video/…`.
  День 12: `asciinema rec --overwrite -f asciicast-v2 --window-size 148x50
  -c "scripts/day12-demo.sh /tmp/day12-video" /tmp/day12-demo.cast`, затем
  `python3 scripts/render_cast.py --cast /tmp/day12-demo.cast --out
  video/персонализация-день-12.mp4 --cards testdata/day12-cards.json`.
  Онбординг-вариант: `python3 scripts/day12-onboard-demo.py /tmp/day12-onboard.cast`
  (сам ведёт диалог в pty), затем `render_cast.py` с тем же `--cards`.
  Карточки результатов по умолчанию — дня 11; новый день передаёт свой
  `--cards` (JSON: `[{seconds, lines: [[текст, размер, accent|fg|dim, отступ]]}]`).
- Прогоны сценариев лежат в `/tmp/day11*/` — в репозиторий попадают только
  сценарии (`testdata/`) и вывод, если он нужен отчёту.
