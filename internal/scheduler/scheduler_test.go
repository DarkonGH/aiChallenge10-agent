package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DarkonGH/aiChallenge10-agent/internal/forex"
)

type fakeCaller struct {
	mu    sync.Mutex
	ticks []forex.Tick
	calls []string
	times []time.Time
}

func (f *fakeCaller) CallTrusted(_ context.Context, server, tool string, raw json.RawMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, server+"/"+tool)
	f.times = append(f.times, time.Now())
	switch tool {
	case "fetch_fx":
		var args map[string]string
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", err
		}
		rate := 82.0 + float64(len(f.ticks))*0.1
		tick := forex.Tick{
			Base:      args["base"],
			Quote:     args["quote"],
			Rate:      rate,
			FetchedAt: time.Now().UTC().Add(time.Duration(len(f.ticks)) * time.Millisecond).Format(time.RFC3339Nano),
			Source:    "fake",
		}
		f.ticks = append(f.ticks, tick)
		body, _ := json.Marshal(tick)
		return string(body), nil
	case "save_tick":
		var tick forex.Tick
		if err := json.Unmarshal(raw, &tick); err != nil {
			return "", err
		}
		// fetch_fx already appended the tick; save only counts.
		return fmt.Sprintf(`{"saved":true,"total":%d}`, len(f.ticks)), nil
	case "read_period":
		var args map[string]string
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", err
		}
		from, _ := time.Parse(time.RFC3339Nano, args["from"])
		var out []forex.Tick
		for _, tick := range f.ticks {
			at, _ := time.Parse(time.RFC3339Nano, tick.FetchedAt)
			if !at.Before(from) {
				out = append(out, tick)
			}
		}
		body, _ := json.Marshal(map[string]any{"count": len(out), "ticks": out})
		return string(body), nil
	default:
		return "", fmt.Errorf("unexpected tool %s", tool)
	}
}

func TestRunCollectsAndReportsAllRuns(t *testing.T) {
	caller := &fakeCaller{}
	var logs []string
	cfg := Config{
		Server:      "forex",
		Base:        "USD",
		Quote:       "RUB",
		Interval:    time.Millisecond,
		Runs:        4,
		ReportEvery: 2,
		Log:         func(s string) { logs = append(logs, s) },
	}
	if err := Run(context.Background(), caller, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(caller.ticks) != 4 {
		t.Fatalf("ticks = %d, want 4", len(caller.ticks))
	}
	if len(caller.calls) != 10 {
		t.Fatalf("calls = %d, want 10 (4 fetch + 4 save + 2 read)", len(caller.calls))
	}
	joined := strings.Join(logs, "\n")
	for _, want := range []string{
		"[запуск 1/4]",
		"[запуск 4/4]",
		"[отчёт после 2] USD/RUB: count=2",
		"[отчёт после 4] USD/RUB: count=4",
		"[итог] успешных запусков: 4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("в логе нет %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "count=1") {
		t.Fatalf("отчёт построен по одному замеру:\n%s", joined)
	}
}

func TestRunReportsFinalPartialPeriod(t *testing.T) {
	caller := &fakeCaller{}
	var logs []string
	err := Run(context.Background(), caller, Config{
		Server:      "forex",
		Base:        "USD",
		Quote:       "RUB",
		Interval:    time.Millisecond,
		Runs:        3,
		ReportEvery: 5,
		Log:         func(s string) { logs = append(logs, s) },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	joined := strings.Join(logs, "\n")
	if !strings.Contains(joined, "[отчёт после 3] USD/RUB: count=3") {
		t.Fatalf("нет финального отчёта:\n%s", joined)
	}
}

func TestRunAddsSummaryToReport(t *testing.T) {
	caller := &fakeCaller{}
	var mu sync.Mutex
	var logs []string
	cfg := Config{
		Server:      "forex",
		Base:        "USD",
		Quote:       "RUB",
		Interval:    time.Millisecond,
		Runs:        2,
		ReportEvery: 2,
		Log: func(s string) {
			mu.Lock()
			logs = append(logs, s)
			mu.Unlock()
		},
		Summarize: func(_ context.Context, rep forex.Report) (string, error) {
			if rep.Count != 2 {
				t.Errorf("summary report count = %d, want 2", rep.Count)
			}
			return "курс стабилен", nil
		},
	}
	if err := Run(context.Background(), caller, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	joined := strings.Join(logs, "\n")
	mu.Unlock()
	for _, want := range []string{
		"[отчёт после 2] USD/RUB: count=2",
		"[сводка] курс стабилен",
		"[итог] успешных запусков: 2",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("в логе нет %q:\n%s", want, joined)
		}
	}
}

func TestRunDurationUsesFixedStart(t *testing.T) {
	caller := &fakeCaller{}
	var logs []string
	err := Run(context.Background(), caller, Config{
		Server:       "forex",
		Base:         "USD",
		Quote:        "RUB",
		Interval:     5 * time.Millisecond,
		Duration:     25 * time.Millisecond,
		SummaryEvery: 50 * time.Millisecond,
		Runs:         5,
		Log:          func(s string) { logs = append(logs, s) },
		Summarize:    func(context.Context, forex.Report) (string, error) { return "", nil },
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(caller.ticks) != 5 {
		t.Fatalf("ticks = %d, want 5: пауза должна считаться от общего старта", len(caller.ticks))
	}
	if !strings.Contains(strings.Join(logs, "\n"), "[запуск 5/5]") {
		t.Fatalf("нет последнего запуска 5/5:\n%s", strings.Join(logs, "\n"))
	}
}

func TestRunValidatesConfig(t *testing.T) {
	if err := Run(context.Background(), &fakeCaller{}, Config{}); err == nil {
		t.Fatal("ожидалась ошибка для пустого интервала")
	}
}

func TestRunReturnsLastError(t *testing.T) {
	caller := &failingCaller{}
	logs := []string{}
	err := Run(context.Background(), caller, Config{
		Server:       "forex",
		Base:         "USD",
		Quote:        "RUB",
		Interval:     time.Millisecond,
		Duration:     5 * time.Millisecond,
		SummaryEvery: 10 * time.Millisecond,
		Log:          func(s string) { logs = append(logs, s) },
	})
	if err == nil {
		t.Fatal("expected error when every run fails")
	}
	if !strings.Contains(err.Error(), "последняя ошибка") || !strings.Contains(err.Error(), "client is closing") {
		t.Fatalf("err = %v, want last transport error", err)
	}
	if len(logs) == 0 {
		t.Fatal("expected per-run log")
	}
}

type failingCaller struct{}

func (f *failingCaller) CallTrusted(context.Context, string, string, json.RawMessage) (string, error) {
	return "", errors.New("client is closing: EOF")
}

func TestRunDelaysFirstCollection(t *testing.T) {
	caller := &fakeCaller{}
	start := time.Now()
	err := Run(context.Background(), caller, Config{
		Server:      "forex",
		Base:        "USD",
		Quote:       "RUB",
		Interval:    10 * time.Millisecond,
		Delay:       40 * time.Millisecond,
		Runs:        1,
		ReportEvery: 1,
		Log:         func(string) {},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(caller.ticks) != 1 {
		t.Fatalf("ticks = %d, want 1", len(caller.ticks))
	}
	if elapsed := time.Since(start); elapsed < 35*time.Millisecond {
		t.Fatalf("первый замер не ждал задержку: %s", elapsed)
	}
}
