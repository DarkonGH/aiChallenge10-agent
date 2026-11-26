// Package scheduler запускает фоновые сборы по расписанию.
//
// Планировщик не использует чат-модель: он напрямую вызывает MCP-инструменты
// через CallTrusted. Это позволяет гонять его как обычный фоновый процесс,
// без диалога и без API-ключа модели.
package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DarkonGH/aiChallenge10-agent/internal/forex"
)

// Caller — минимальная часть mcp.Manager, нужная планировщику.
type Caller interface {
	CallTrusted(ctx context.Context, server, tool string, raw json.RawMessage) (string, error)
}

// Config описывает расписание.
type Config struct {
	Server       string        // MCP-сервер, обычно "forex"
	Base         string        // базовая валюта, обычно USD
	Quote        string        // котируемая валюта, обычно RUB
	Interval     time.Duration // пауза между запусками
	Runs         int           // 0 — бесконечно; игнорируется, если задан Duration
	Duration     time.Duration // 0 — работать по Runs; иначе остановиться по времени
	ReportEvery  int           // отчёт после каждых N успешных запусков (режим Runs)
	SummaryEvery time.Duration // отчёт по времени (режим Duration); 0 — не использовать
	Log          func(string)  // куда печатать; nil — fmt.Println

	// Summarize добавляет текстовую сводку к числовому отчёту. Цифры уже
	// посчитаны кодом; модель получает только готовые агрегаты и не должна
	// пересчитывать их заново. Ошибка сводки не отменяет отчёт.
	Summarize func(ctx context.Context, rep forex.Report) (string, error)
	// OnReport получает каждый собранный отчёт. Нужен фоновому инструменту,
	// чтобы вернуть модели последний отчёт, не читая журнал повторно.
	OnReport func(success int, rep forex.Report)
}

// Run выполняет сборы до исчерпания Runs или отмены контекста.
func Run(ctx context.Context, caller Caller, cfg Config) error {
	if caller == nil {
		return fmt.Errorf("планировщик: нет MCP-клиента")
	}
	if cfg.Interval <= 0 {
		return fmt.Errorf("планировщик: интервал должен быть больше нуля")
	}
	if cfg.Duration < 0 {
		return fmt.Errorf("планировщик: длительность не может быть отрицательной")
	}
	if cfg.Duration == 0 && cfg.ReportEvery <= 0 {
		return fmt.Errorf("планировщик: ReportEvery должен быть больше нуля")
	}
	if cfg.Duration > 0 && cfg.SummaryEvery <= 0 {
		return fmt.Errorf("планировщик: SummaryEvery должен быть больше нуля")
	}
	if cfg.Duration == 0 && cfg.Runs < 0 {
		return fmt.Errorf("планировщик: Runs не может быть отрицательным")
	}
	if cfg.Server == "" {
		cfg.Server = "forex"
	}
	if cfg.Base == "" {
		cfg.Base = "USD"
	}
	if cfg.Quote == "" {
		cfg.Quote = "RUB"
	}
	logf := cfg.Log
	if logf == nil {
		logf = func(s string) { fmt.Println(s) }
	}

	started := time.Now().UTC()
	lastReport := started
	if cfg.Duration > 0 {
		logf(fmt.Sprintf("[планировщик] интервал %s, длительность %s, сводка каждые %s, пара %s/%s",
			cfg.Interval, cfg.Duration, cfg.SummaryEvery, cfg.Base, cfg.Quote))
	} else {
		logf(fmt.Sprintf("[планировщик] интервал %s, запусков %s, отчёт каждые %d, пара %s/%s",
			cfg.Interval, runLimit(cfg.Runs), cfg.ReportEvery, cfg.Base, cfg.Quote))
	}

	// В режиме Duration число запусков считается заранее: 600 с / 10 с = 60.
	// Паузу отсчитываем от общего старта, а не от конца предыдущего вызова.
	// Иначе накладные расходы fetch/save (доли секунды) накапливаются, и
	// шестьдесят первый старт выходит за границу — в логе получается 59/60.
	durationRuns := 0
	if cfg.Duration > 0 {
		durationRuns = int(cfg.Duration / cfg.Interval)
		if durationRuns < 1 {
			durationRuns = 1
		}
	}

	attempts, success, reported, total := 0, 0, 0, 0
	var lastErr error
	var summaryWG sync.WaitGroup
	for {
		if cfg.Duration > 0 {
			if attempts >= durationRuns {
				break
			}
		} else if cfg.Runs > 0 && attempts >= cfg.Runs {
			break
		}
		if attempts > 0 {
			if cfg.Duration > 0 {
				next := started.Add(time.Duration(attempts) * cfg.Interval)
				if err := waitUntil(ctx, next); err != nil {
					return err
				}
				if time.Now().After(started.Add(cfg.Duration)) {
					break
				}
			} else if err := wait(ctx, cfg.Interval); err != nil {
				return err
			}
		}
		attempts++

		tick, err := collect(ctx, caller, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
			logf(fmt.Sprintf("[запуск %s] ошибка: %v", progress(attempts, cfg.Runs), err))
			continue
		}
		total, err = save(ctx, caller, cfg, tick)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			lastErr = err
			logf(fmt.Sprintf("[запуск %s] ошибка сохранения: %v", progress(attempts, cfg.Runs), err))
			continue
		}
		success++
		logf(fmt.Sprintf("[запуск %s] %s/%s %.4f · сохранено, всего %d",
			progress(attempts, cfg.Runs), tick.Base, tick.Quote, tick.Rate, total))

		if cfg.Duration > 0 {
			if time.Since(lastReport) >= cfg.SummaryEvery {
				if err := report(ctx, caller, cfg, started, success, logf, &summaryWG); err != nil {
					return err
				}
				lastReport = time.Now()
				reported = success
			}
		} else if success%cfg.ReportEvery == 0 {
			if err := report(ctx, caller, cfg, started, success, logf, &summaryWG); err != nil {
				return err
			}
			reported = success
		}
	}

	if success == 0 {
		// Модель получает не только «ни одного запуска», но и последнюю
		// причину: иначе она идёт вызывать fetch_fx вручную и видит тот же EOF.
		if lastErr != nil {
			return fmt.Errorf("планировщик: ни одного успешного запуска; последняя ошибка: %w", lastErr)
		}
		return fmt.Errorf("планировщик: ни одного успешного запуска")
	}
	if success != reported {
		if err := report(ctx, caller, cfg, started, success, logf, &summaryWG); err != nil {
			return err
		}
	}
	// Сводки идут параллельно сборам; перед итоговой строкой дожидаемся их,
	// иначе последняя сводка может появиться уже после завершения команды.
	summaryWG.Wait()
	logf(fmt.Sprintf("[итог] успешных запусков: %d, записей в журнале: %d", success, total))
	return nil
}

func collect(ctx context.Context, caller Caller, cfg Config) (forex.Tick, error) {
	args, err := json.Marshal(map[string]string{"base": cfg.Base, "quote": cfg.Quote})
	if err != nil {
		return forex.Tick{}, err
	}
	out, err := caller.CallTrusted(ctx, cfg.Server, "fetch_fx", args)
	if err != nil {
		return forex.Tick{}, fmt.Errorf("fetch_fx: %w", err)
	}
	var tick forex.Tick
	if err := json.Unmarshal([]byte(out), &tick); err != nil {
		return forex.Tick{}, fmt.Errorf("fetch_fx: разбор ответа: %w", err)
	}
	if tick.Rate <= 0 || tick.Base == "" || tick.Quote == "" {
		return forex.Tick{}, fmt.Errorf("fetch_fx: пустой или некорректный ответ %q", out)
	}
	return tick, nil
}

func save(ctx context.Context, caller Caller, cfg Config, tick forex.Tick) (int, error) {
	args, err := json.Marshal(tick)
	if err != nil {
		return 0, err
	}
	out, err := caller.CallTrusted(ctx, cfg.Server, "save_tick", args)
	if err != nil {
		return 0, fmt.Errorf("save_tick: %w", err)
	}
	var result struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return 0, fmt.Errorf("save_tick: разбор ответа: %w", err)
	}
	return result.Total, nil
}

func report(ctx context.Context, caller Caller, cfg Config, from time.Time, success int, logf func(string), summaryWG *sync.WaitGroup) error {
	args, err := json.Marshal(map[string]string{
		"from":  from.Format(time.RFC3339Nano),
		"quote": cfg.Quote,
	})
	if err != nil {
		return err
	}
	out, err := caller.CallTrusted(ctx, cfg.Server, "read_period", args)
	if err != nil {
		return fmt.Errorf("read_period: %w", err)
	}
	var result struct {
		Count int          `json:"count"`
		Ticks []forex.Tick `json:"ticks"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return fmt.Errorf("read_period: разбор ответа: %w", err)
	}
	rep, err := forex.BuildReport(result.Ticks)
	if err != nil {
		return fmt.Errorf("отчёт: %w", err)
	}
	logf(formatReport(success, rep))
	if cfg.OnReport != nil {
		cfg.OnReport(success, rep)
	}
	if cfg.Summarize != nil {
		// Сводка — отдельный вызов модели, он может занять секунды. Нельзя
		// останавливать из-за него сбор курса: следующие запуски должны идти
		// строго по интервалу. Поэтому сводка считается параллельно.
		summaryWG.Add(1)
		go func(rep forex.Report) {
			defer summaryWG.Done()
			text, err := cfg.Summarize(ctx, rep)
			switch {
			case err != nil:
				logf("[сводка] недоступна: " + err.Error())
			case strings.TrimSpace(text) != "":
				logf("[сводка] " + oneLine(text))
			}
		}(rep)
	}
	return nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func formatReport(success int, rep forex.Report) string {
	first := formatTickTime(rep.First)
	last := formatTickTime(rep.Last)
	return fmt.Sprintf("[отчёт после %d] %s/%s: count=%d min=%.4f max=%.4f avg=%.4f delta=%+.4f; %s → %s",
		success, rep.Base, rep.Quote, rep.Count, rep.Min, rep.Max, rep.Avg, rep.Delta, first, last)
}

func formatTickTime(tick forex.Tick) string {
	at, err := time.Parse(time.RFC3339Nano, tick.FetchedAt)
	if err != nil {
		return tick.FetchedAt
	}
	return at.Local().Format("15:04:05")
}

func progress(attempt, runs int) string {
	if runs > 0 {
		return fmt.Sprintf("%d/%d", attempt, runs)
	}
	return fmt.Sprintf("%d", attempt)
}

func runLimit(runs int) string {
	if runs == 0 {
		return "∞"
	}
	return fmt.Sprintf("%d", runs)
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// waitUntil ждёт абсолютный момент. Если он уже прошёл, запуск выполняется
// сразу: лучше выдержать общую длительность, чем накапливать отставание.
func waitUntil(ctx context.Context, at time.Time) error {
	if d := time.Until(at); d > 0 {
		return wait(ctx, d)
	}
	return nil
}
