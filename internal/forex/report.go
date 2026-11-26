package forex

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Report — сводка по всем замерам периода, а не по последнему.
type Report struct {
	Count int
	Base  string
	Quote string
	Min   float64
	Max   float64
	Avg   float64
	Delta float64
	First Tick
	Last  Tick
}

// BuildReport агрегирует весь набор замеров. Пары должны совпадать:
// смешивать USD/RUB и EUR/RUB в одном отчёте нельзя.
func BuildReport(ticks []Tick) (Report, error) {
	if len(ticks) == 0 {
		return Report{}, fmt.Errorf("нет замеров для отчёта")
	}

	type timedTick struct {
		tick Tick
		at   time.Time
	}
	timed := make([]timedTick, 0, len(ticks))
	for _, tick := range ticks {
		at, err := parseTickTime(tick)
		if err != nil {
			return Report{}, err
		}
		timed = append(timed, timedTick{tick: tick, at: at})
	}
	sort.Slice(timed, func(i, j int) bool {
		return timed[i].at.Before(timed[j].at)
	})

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
	}, nil
}
