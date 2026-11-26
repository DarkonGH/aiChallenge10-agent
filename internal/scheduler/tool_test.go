package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeToolCaller struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeToolCaller) CallTool(_ context.Context, tool string, raw json.RawMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, tool+" "+string(raw))
	if f.err != nil {
		return "", f.err
	}
	return `{"value":42}`, nil
}

func TestRunToolCollectsResultsAndReports(t *testing.T) {
	caller := &fakeToolCaller{}
	var logs []string
	results, err := RunTool(context.Background(), caller, ToolConfig{
		Tool:        "calc",
		Args:        json.RawMessage(`{"expr":"6*7"}`),
		Interval:    5 * time.Millisecond,
		Duration:    30 * time.Millisecond,
		ReportEvery: 10 * time.Millisecond,
		Log:         func(s string) { logs = append(logs, s) },
	})
	if err != nil {
		t.Fatalf("RunTool: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("ожидалось несколько запусков, получено %d", len(results))
	}
	if results[0].Run != 1 || results[0].Output == "" {
		t.Fatalf("некорректный первый результат: %+v", results[0])
	}
	if len(caller.calls) != len(results) {
		t.Fatalf("вызовов %d, результатов %d", len(caller.calls), len(results))
	}
	if !strings.Contains(caller.calls[0], `{"expr":"6*7"}`) {
		t.Fatalf("аргументы не переданы: %q", caller.calls[0])
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"[планировщик]", "[запуск 1]", "[отчёт после", "[итог]"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("в логе нет %q:\n%s", want, joined)
		}
	}
}

func TestRunToolReturnsLastError(t *testing.T) {
	caller := &fakeToolCaller{err: errors.New("boom")}
	_, err := RunTool(context.Background(), caller, ToolConfig{
		Tool:        "calc",
		Args:        json.RawMessage(`{"expr":"1+1"}`),
		Interval:    5 * time.Millisecond,
		Duration:    20 * time.Millisecond,
		ReportEvery: 10 * time.Millisecond,
		Log:         func(string) {},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	if !strings.Contains(err.Error(), "последняя ошибка: boom") {
		t.Fatalf("нет последней ошибки: %v", err)
	}
}

func TestRunToolRejectsEmptyTool(t *testing.T) {
	_, err := RunTool(context.Background(), &fakeToolCaller{}, ToolConfig{
		Interval:    time.Millisecond,
		Duration:    time.Millisecond,
		ReportEvery: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "не задан инструмент") {
		t.Fatalf("ожидалась ошибка про инструмент, получено %v", err)
	}
}
