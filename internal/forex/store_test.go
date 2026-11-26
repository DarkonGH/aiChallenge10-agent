package forex

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tickAt(at time.Time, quote string, rate float64) Tick {
	return Tick{
		Base:      "USD",
		Quote:     quote,
		Rate:      rate,
		FetchedAt: at.Format(time.RFC3339Nano),
		Source:    "test",
	}
}

func TestStoreAppendReadPeriod(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forex.jsonl")
	store, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	t0 := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for i, rate := range []float64{82.1, 82.2, 82.3} {
		total, err := store.Append(tickAt(t0.Add(time.Duration(i)*time.Second), "RUB", rate))
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if total != i+1 {
			t.Fatalf("total = %d, want %d", total, i+1)
		}
	}
	if got := store.Count(); got != 3 {
		t.Fatalf("Count = %d", got)
	}

	all, err := store.ReadPeriod(time.Time{}, time.Time{}, "")
	if err != nil {
		t.Fatalf("ReadPeriod all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("len(all) = %d", len(all))
	}

	from := t0.Add(1 * time.Second)
	to := t0.Add(2 * time.Second)
	part, err := store.ReadPeriod(from, to, "rub")
	if err != nil {
		t.Fatalf("ReadPeriod part: %v", err)
	}
	if len(part) != 2 || part[0].Rate != 82.2 || part[1].Rate != 82.3 {
		t.Fatalf("part = %+v", part)
	}

	empty, err := store.ReadPeriod(time.Time{}, time.Time{}, "EUR")
	if err != nil {
		t.Fatalf("ReadPeriod empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("empty = %+v", empty)
	}
}

func TestStoreAppendNormalizesMissingTime(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "forex.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	total, err := store.Append(Tick{Base: "usd", Quote: "rub", Rate: 82.5})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if total != 1 {
		t.Fatalf("total = %d", total)
	}
	ticks, err := store.ReadPeriod(time.Time{}, time.Time{}, "RUB")
	if err != nil {
		t.Fatal(err)
	}
	if len(ticks) != 1 || ticks[0].Base != "USD" || ticks[0].FetchedAt == "" {
		t.Fatalf("ticks = %+v", ticks)
	}
}

func TestStoreRejectsMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forex.jsonl")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("ожидалась ошибка разбора журнала")
	}
}

func TestStoreRejectsBadTick(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "forex.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tick := range []Tick{
		{Base: "", Quote: "RUB", Rate: 1},
		{Base: "USD", Quote: "RUB", Rate: 0},
		{Base: "USD", Quote: "RUB", Rate: 1, FetchedAt: "вчера"},
	} {
		if _, err := store.Append(tick); err == nil {
			t.Fatalf("tick %+v прошёл без ошибки", tick)
		}
	}
}
