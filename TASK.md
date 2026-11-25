# День 17. Свой MCP-сервер

## Задание

Реализовать собственный MCP-сервер вокруг внешнего API, зарегистрировать
инструмент с описанием входных параметров, подключить его к агенту и получить
результат в диалоге.

## Что сделано

- `cmd/mcp-cbr` — отдельный MCP-сервер на официальном Go SDK
  `github.com/modelcontextprotocol/go-sdk`.
- `internal/cbr` — клиент ЦБ РФ: запрос `XML_daily.asp`, разбор XML,
  декодирование windows-1251, проверка валюты и даты.
- `mcp.json` — третий сервер `rates`:
  ```json
  {
    "name": "rates",
    "command": "./bin/mcp-cbr",
    "args": [],
    "required": false,
    "allow": ["currency_rate"],
    "approval": {"currency_rate": "ask"},
    "descriptions": {
      "currency_rate": "Курс валюты ЦБ РФ на дату. Аргументы: currency (USD, EUR, CNY, ...), date (ДД.ММ.ГГГГ или пусто — сегодня)."
    }
  }
  ```
- `scripts/day17-demo.sh` — сборка сервера и агента, прогон демо и checker.
- `scripts/check_day17.py` — независимая проверка вызовов, аргументов,
  результата и наличия схемы в дампе запроса.
- `testdata/day17-demo.txt` — сценарий диалога.
- `testdata/day17-cards.json` — шесть карточек для видео.

## Инструмент

Имя: `currency_rate`.

Вход:

```json
{
  "currency": "USD",
  "date": "20.11.2026"
}
```

Выход:

```json
{
  "date": "20.11.2026",
  "currency": "USD",
  "nominal": 1,
  "rate": 81.929,
  "unit_rate": 81.929,
  "source": "ЦБ РФ"
}
```

`currency` — трёхбуквенный код, `date` — `ДД.ММ.ГГГГ` или пусто (сегодня).
Сервер ходит только на `https://www.cbr.ru/scripts/XML_daily.asp`;
пользовательский URL не принимается.

## Демо

Сценарий:

```text
/mcp
/tools
Узнай через MCP-инструмент currency_rate курс доллара ЦБ РФ на 20.11.2026
и посчитай локальным calc, сколько долларов можно купить на 350 000 рублей.
Курс бери только из MCP, не считай в уме.
```

Ход:

```text
mcp_rates_currency_rate({"currency":"USD","date":"20.11.2026"})
→ 81,9290 ₽ за 1 USD
calc({"expr":"350000/81.929"}) → 4271.991602485078
```

Ответ: **4 271,99 USD** (около 4 272 долларов).

`/mcp` показывает три сервера: `fs` (3 инструмента), `web` (1),
`rates` (1). В `/tools` десять строк, включая `mcp_rates_currency_rate`.
В схеме запроса к модели восемь инструментов: `now`, `calc`, `find_riddle`
и пять MCP-инструментов; `task_transition` и `check_solution` зарегистрированы,
но не видны без активной задачи и инвариантов.

## Проверки

```bash
gofmt -l .
GOTOOLCHAIN=local go vet ./...
GOTOOLCHAIN=local go test -count=1 ./...
go build -o agent .
go build -o bin/mcp-cbr ./cmd/mcp-cbr
python3 scripts/check_day17.py /tmp/day17-demo/data /tmp/day17-demo/requests.ndjson
```

Результат: `gofmt` пусто, `vet` чисто, все Go-пакеты `ok`, сборка агента и
сервера успешна. Checker: все 12 проверок `ok`, `ИТОГ: ok`.

## Видео

`video/свой-mcp-цб-день-17.mp4`

- 68 секунд, 1352x970, 8 fps, h264, без аудио;
- в начале `/mcp` и `/tools`;
- вызов собственного `mcp_rates_currency_rate`, локальный `calc`, ответ;
- шесть карточек: сервер, инструмент, API, MCP + calc, апрув/границы,
  зачем свой MCP.

Каст собран из настоящего stdout-лога:

```bash
python3 scripts/log_to_cast.py /tmp/day17-demo.log /tmp/day17-demo.cast \
  --line-delay 0.5 --prompt-delay 1.6 --blank-delay 0.3
python3 scripts/render_cast.py --cast /tmp/day17-demo.cast \
  --out video/свой-mcp-цб-день-17.mp4 \
  --title "День 17 · Свой MCP-сервер" \
  --subtitle "курс ЦБ РФ через MCP + локальный calc" \
  --cards testdata/day17-cards.json --hold 4
```

## Ограничения

- ЦБ РФ — внешний сервис; при недоступности сети демо падает честно,
  без подстановки выдуманного курса.
- Курс на выходные и праздники может отсутствовать; сервер возвращает ошибку
  ЦБ РФ.
- Отдельный `mcp-fetch` для личных экспериментов не входит в отчёт и видео.
