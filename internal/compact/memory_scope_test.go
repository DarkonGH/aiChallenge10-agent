package compact

import (
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

func testStore(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(t.TempDir()+"/memory", t.TempDir()+"/shared")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestIsolateMemoryUpdateDropsForeignListingNumbers is the Avito regression:
// the new 2020 listing arrives as a URL without a price, but the extractor
// copies the old 2018 listing's price into the new "следующий шаг".
func TestIsolateMemoryUpdateDropsForeignListingNumbers(t *testing.T) {
	s := testStore(t)
	s.Apply(memory.Update{
		Working: []memory.Entry{{Key: "бюджет", Value: "2 497 925 ₽"}},
		Knowledge: []memory.Entry{{
			Key:   "Pajero Sport",
			Value: "2018, 27 380 км, цена 2 497 925 ₽",
		}},
	})

	userText := "посмотри https://www.avito.ru/arhangelsk/avtomobili/" +
		"mitsubishi_pajero_sport_2.4_at_2020_78_050_km_8471336379"

	upd := memory.Update{
		Working: []memory.Entry{
			{Key: "Pajero Sport 2020 объявление", Value: "2.4 AT, 2020, 78 050 км, Архангельск"},
			{Key: "следующий шаг", Value: "проверить на бюджет 2 497 925 ₽"},
		},
		Knowledge: []memory.Entry{
			{Key: "Pajero Sport 2020 Архангельск", Value: "2020, 78 050 км, объявление 8471336379"},
		},
	}

	got, notes := IsolateMemoryUpdate(s, userText, upd)
	if len(got.Working) != 1 || got.Working[0].Key != "Pajero Sport 2020 объявление" {
		t.Fatalf("рабочая память после изоляции: %+v", got.Working)
	}
	if len(got.Knowledge) != 1 || got.Knowledge[0].Key != "Pajero Sport 2020 Архангельск" {
		t.Fatalf("knowledge после изоляции: %+v", got.Knowledge)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "2497925") {
		t.Fatalf("ожидалось одно объяснение про чужую цену, получено %v", notes)
	}
}

// TestIsolateMemoryUpdateAllowsSameKey keeps legitimate updates: a price that
// already belongs to the same key may be repeated, even when the message has a
// URL.
func TestIsolateMemoryUpdateAllowsSameKey(t *testing.T) {
	s := testStore(t)
	s.Apply(memory.Update{Working: []memory.Entry{{Key: "бюджет", Value: "2 497 925 ₽"}}})

	upd := memory.Update{Working: []memory.Entry{{Key: "бюджет", Value: "2 497 925 ₽"}}}
	got, notes := IsolateMemoryUpdate(s, "https://example.com/listing/2020", upd)
	if len(got.Working) != 1 || got.Working[0].Key != "бюджет" {
		t.Fatalf("обновление того же ключа потеряно: %+v, notes=%v", got.Working, notes)
	}
	if len(notes) != 0 {
		t.Fatalf("лишнее предупреждение: %v", notes)
	}
}

// TestIsolateMemoryUpdateIgnoresTurnsWithoutURL keeps ordinary memory updates
// untouched: without a new external object there is nothing to isolate.
func TestIsolateMemoryUpdateIgnoresTurnsWithoutURL(t *testing.T) {
	s := testStore(t)
	upd := memory.Update{Working: []memory.Entry{{Key: "цена", Value: "2 497 925 ₽"}}}
	got, notes := IsolateMemoryUpdate(s, "у него цена 2 497 925 ₽", upd)
	if len(got.Working) != 1 {
		t.Fatalf("обычное обновление потеряно: %+v", got.Working)
	}
	if len(notes) != 0 {
		t.Fatalf("лишнее предупреждение: %v", notes)
	}
}

// TestSystemPromptSeparatesListings pins the instruction that backs the code
// guard: a new listing/person/file is a new object, not a continuation of the
// old one.
func TestSystemPromptSeparatesListings(t *testing.T) {
	for _, want := range []string{
		"новый объект",
		"номер объявления",
		"не подставляй их из старой памяти",
		"не смешивай в общем ключе",
	} {
		if !strings.Contains(memorySystemPrompt, want) {
			t.Errorf("в системном промпте нет %q", want)
		}
	}
}
