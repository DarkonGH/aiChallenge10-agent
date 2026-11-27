package forex

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// DefaultRunGap — пауза, после которой начинается новый запуск планировщика.
// Журнал может содержать несколько независимых серий: между ними обычно
// минуты или часы, а внутри серии — секунды.
const DefaultRunGap = 60 * time.Second

// Run описывает одну непрерывную серию замеров.
type Run struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Count int    `json:"count"`
}

// Report — сводка по всем замерам периода, а не по последнему.
type Report struct {
	Count int     `json:"count"`
	Base  string  `json:"base"`
	Quote string  `json:"quote"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Avg   float64 `json:"avg"`
	Delta float64 `json:"delta"`
	First Tick    `json:"first"`
	Last  Tick    `json:"last"`
	Runs  []Run   `json:"runs,omitempty"`
}

type timedTick struct {
	tick Tick
	at   time.Time
}

// BuildReport агрегирует весь набор замеров. Пары должны совпадать:
// смешивать USD/RUB и EUR/RUB в одном отчёте нельзя.
func BuildReport(ticks []Tick) (Report, error) {
	timed, err := sortedTicks(ticks)
	if err != nil {
		return Report{}, err
	}
	if len(timed) == 0 {
		return Report{}, fmt.Errorf("нет замеров для отчёта")
	}

	base := strings.ToUpper(timed[0].tick.Base)
	quote := strings.ToUpper(timed[0].tick.Quote)
	min, max := timed[0].tick.Rate, timed[0].tick.Rate
	sum := 0.0
	for _, item := range timed {
		tick := item.tick
		if strings.ToUpper(tick.Base) != base || strings.ToUpper(tick.Quote) != quote {
			return Report{}, fmt.Errorf("в отчёте смешаны пары %s/%s и %s/%s",
				base, quote, tick.Base, tick.Quote)
		}
		if tick.Rate < min {
			min = tick.Rate
		}
		if tick.Rate > max {
			max = tick.Rate
		}
		sum += tick.Rate
	}

	return Report{
		Count: len(timed),
		Base:  base,
		Quote: quote,
		Min:   min,
		Max:   max,
		Avg:   sum / float64(len(timed)),
		Delta: timed[len(timed)-1].tick.Rate - timed[0].tick.Rate,
		First: timed[0].tick,
		Last:  timed[len(timed)-1].tick,
		Runs:  buildRuns(timed, DefaultRunGap),
	}, nil
}

// BuildRuns разбивает замеры на серии по паузам больше gap.
func BuildRuns(ticks []Tick, gap time.Duration) ([]Run, error) {
	timed, err := sortedTicks(ticks)
	if err != nil {
		return nil, err
	}
	if len(timed) == 0 {
		return nil, nil
	}
	if gap <= 0 {
		gap = DefaultRunGap
	}
	return buildRuns(timed, gap), nil
}

func sortedTicks(ticks []Tick) ([]timedTick, error) {
	timed := make([]timedTick, 0, len(ticks))
	for _, tick := range ticks {
		at, err := parseTickTime(tick)
		if err != nil {
			return nil, err
		}
		timed = append(timed, timedTick{tick: tick, at: at})
	}
	sort.Slice(timed, func(i, j int) bool {
		return timed[i].at.Before(timed[j].at)
	})
	return timed, nil
}

func buildRuns(timed []timedTick, gap time.Duration) []Run {
	if len(timed) == 0 {
		return nil
	}
	runs := make([]Run, 0, 1)
	start := timed[0].at
	prev := timed[0].at
	count := 1
	for _, item := range timed[1:] {
		if item.at.Sub(prev) > gap {
			runs = append(runs, Run{
				From:  start.UTC().Format(time.RFC3339Nano),
				To:    prev.UTC().Format(time.RFC3339Nano),
				Count: count,
			})
			start = item.at
			count = 0
		}
		prev = item.at
		count++
	}
	runs = append(runs, Run{
		From:  start.UTC().Format(time.RFC3339Nano),
		To:    prev.UTC().Format(time.RFC3339Nano),
		Count: count,
	})
	return runs
}
