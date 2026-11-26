package forex

import (
	"math"
	"testing"
	"time"
)

func TestBuildReportUsesAllTicks(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	ticks := []Tick{
		tickAt(t0.Add(2*time.Second), "RUB", 82.3),
		tickAt(t0, "RUB", 82.1),
		tickAt(t0.Add(1*time.Second), "RUB", 82.2),
	}
	report, err := BuildReport(ticks)
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}
	if report.Count != 3 || report.Base != "USD" || report.Quote != "RUB" {
		t.Fatalf("report = %+v", report)
	}
	if report.Min != 82.1 || report.Max != 82.3 {
		t.Fatalf("min/max = %v/%v", report.Min, report.Max)
	}
	if math.Abs(report.Avg-82.2) > 1e-9 {
		t.Fatalf("avg = %v", report.Avg)
	}
	if math.Abs(report.Delta-0.2) > 1e-9 {
		t.Fatalf("delta = %v", report.Delta)
	}
	if report.First.Rate != 82.1 || report.Last.Rate != 82.3 {
		t.Fatalf("first/last = %+v/%+v", report.First, report.Last)
	}
}

func TestBuildReportRejectsEmpty(t *testing.T) {
	if _, err := BuildReport(nil); err == nil {
		t.Fatal("ожидалась ошибка для пустого набора")
	}
}

func TestBuildReportRejectsMixedPairs(t *testing.T) {
	t0 := time.Now().UTC()
	ticks := []Tick{
		tickAt(t0, "RUB", 82.1),
		{Base: "USD", Quote: "EUR", Rate: 0.91, FetchedAt: t0.Add(time.Second).Format(time.RFC3339Nano)},
	}
	if _, err := BuildReport(ticks); err == nil {
		t.Fatal("ожидалась ошибка для смешанных пар")
	}
}
