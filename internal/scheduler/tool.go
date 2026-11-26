package scheduler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ToolCaller — минимальная часть приложения, нужная универсальному
// планировщику. Он не знает, локальный это инструмент или MCP: маршрутизацию
// и проверку допустимых имён делает вызывающая сторона.
type ToolCaller interface {
	CallTool(ctx context.Context, tool string, raw json.RawMessage) (string, error)
}

// ToolConfig описывает повторяющийся вызов одного инструмента.
type ToolConfig struct {
	Tool        string
	Args        json.RawMessage
	Interval    time.Duration
	Duration    time.Duration
	ReportEvery time.Duration
	Log         func(string)
	OnResult    func(ToolResult)
}

// ToolResult — один успешный вызов инструмента.
type ToolResult struct {
	Run    int       `json:"run"`
	At     time.Time `json:"at"`
	Output string    `json:"output"`
}

// RunTool выполняет инструмент по расписанию и возвращает все успешные
// результаты. Ошибки отдельных запусков не останавливают цикл — так же, как
// в forex-планировщике; если не удался ни один запуск, возвращается последняя
// причина.
func RunTool(ctx context.Context, caller ToolCaller, cfg ToolConfig) ([]ToolResult, error) {
	if caller == nil {
		return nil, fmt.Errorf("планировщик: нет вызывающей стороны")
	}
	if strings.TrimSpace(cfg.Tool) == "" {
		return nil, fmt.Errorf("планировщик: не задан инструмент")
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("планировщик: интервал должен быть больше нуля")
	}
	if cfg.Duration <= 0 {
		return nil, fmt.Errorf("планировщик: длительность должна быть больше нуля")
	}
	if cfg.ReportEvery <= 0 {
		return nil, fmt.Errorf("планировщик: отчётный интервал должен быть больше нуля")
	}
	if len(cfg.Args) == 0 || string(cfg.Args) == "null" {
		cfg.Args = json.RawMessage("{}")
	}

	logf := cfg.Log
	if logf == nil {
		logf = func(s string) { fmt.Println(s) }
	}

	logf(fmt.Sprintf("[планировщик] инструмент %s, интервал %s, длительность %s, отчёт каждые %s",
		cfg.Tool, cfg.Interval, cfg.Duration, cfg.ReportEvery))

	started := time.Now().UTC()
	lastReport := started
	attempts, success := 0, 0
	var lastErr error
	results := make([]ToolResult, 0)

	// Как и в forex-планировщике, число запусков считаем от длительности, а
	// паузу — от общего старта. Иначе время, потраченное на сам инструмент,
	// сдвигает каждый следующий запуск и последний не успевает.
	limit := int(cfg.Duration / cfg.Interval)
	if limit < 1 {
		limit = 1
	}

	for {
		if attempts >= limit {
			break
		}
		if attempts > 0 {
			next := started.Add(time.Duration(attempts) * cfg.Interval)
			if err := waitUntil(ctx, next); err != nil {
				return results, err
			}
			if time.Now().After(started.Add(cfg.Duration)) {
				break
			}
		}
		attempts++

		out, err := caller.CallTool(ctx, cfg.Tool, cfg.Args)
		if err != nil {
			if ctx.Err() != nil {
				return results, ctx.Err()
			}
			lastErr = err
			logf(fmt.Sprintf("[запуск %d] ошибка: %v", attempts, err))
			continue
		}
		success++
		res := ToolResult{
			Run:    success,
			At:     time.Now().UTC(),
			Output: out,
		}
		results = append(results, res)
		if cfg.OnResult != nil {
			cfg.OnResult(res)
		}
		logf(fmt.Sprintf("[запуск %d] %s → %s", success, cfg.Tool, shortLine(out, 220)))

		if time.Since(lastReport) >= cfg.ReportEvery {
			logf(fmt.Sprintf("[отчёт после %d] %s: запусков %d, успешных %d, последний: %s",
				success, cfg.Tool, attempts, success, shortLine(out, 220)))
			lastReport = time.Now()
		}
	}

	if success == 0 {
		if lastErr != nil {
			return results, fmt.Errorf("планировщик: ни одного успешного запуска; последняя ошибка: %w", lastErr)
		}
		return results, fmt.Errorf("планировщик: ни одного успешного запуска")
	}

	last := results[len(results)-1]
	logf(fmt.Sprintf("[итог] %s: успешных запусков %d из %d, последний: %s",
		cfg.Tool, success, attempts, shortLine(last.Output, 220)))
	return results, nil
}

func shortLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
