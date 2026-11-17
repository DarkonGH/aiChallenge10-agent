package compact

import (
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/memory"
)

// TestParseMemoryJSONLayers checks the normal answer: one object per layer,
// key order preserved, every record routed to its own layer.
func TestParseMemoryJSONLayers(t *testing.T) {
	out := "```json\n" +
		`{"working": {"срок переезда": "15 декабря", "бюджет": "480 000"},` +
		` "profile": {"роль": "офис-менеджер"},` +
		` "decisions": {"площадка": "коворкинг Пресня"},` +
		` "knowledge": {"адрес": "Пресня, 1"}}` +
		"\n```"
	u, err := ParseMemoryJSON(out)
	if err != nil {
		t.Fatalf("ParseMemoryJSON: %v", err)
	}
	if u.Empty() {
		t.Fatal("обновление пустое")
	}
	if got := u.Working; len(got) != 2 || got[0].Key != "срок переезда" || got[1].Key != "бюджет" {
		t.Errorf("working: %v", got)
	}
	if len(u.Profile) != 1 || u.Profile[0].Value != "офис-менеджер" {
		t.Errorf("profile: %v", u.Profile)
	}
	if len(u.Decisions) != 1 || u.Decisions[0].Key != "площадка" {
		t.Errorf("decisions: %v", u.Decisions)
	}
	if len(u.Knowledge) != 1 || u.Knowledge[0].Value != "Пресня, 1" {
		t.Errorf("knowledge: %v", u.Knowledge)
	}
}

// TestParseMemoryJSONFlatFallback checks that a flat key-value answer (the
// day-10 facts format) still lands somewhere: working memory.
func TestParseMemoryJSONFlatFallback(t *testing.T) {
	u, err := ParseMemoryJSON(`{"координатор арендодателя": "Марина Соколова"}`)
	if err != nil {
		t.Fatalf("ParseMemoryJSON: %v", err)
	}
	if len(u.Working) != 1 || u.Working[0].Value != "Марина Соколова" {
		t.Fatalf("плоский ответ не разложен в working: %+v", u)
	}
	if len(u.Profile)+len(u.Decisions)+len(u.Knowledge) != 0 {
		t.Errorf("плоский ответ попал не только в working: %+v", u)
	}
}

// TestParseMemoryJSONEmpty checks the "nothing worth storing" answer.
func TestParseMemoryJSONEmpty(t *testing.T) {
	u, err := ParseMemoryJSON("{}")
	if err != nil {
		t.Fatalf("ParseMemoryJSON: %v", err)
	}
	if !u.Empty() {
		t.Errorf("ожидалось пустое обновление, получено %+v", u)
	}
}

// TestParseMemoryJSONSkipsJunk checks that empty values and prose around the
// object are tolerated.
func TestParseMemoryJSONSkipsJunk(t *testing.T) {
	out := "Вот обновление:\n{\"working\": {\"пусто\": \"\", \"цель\": \"переезд\"}}\nГотово."
	u, err := ParseMemoryJSON(out)
	if err != nil {
		t.Fatalf("ParseMemoryJSON: %v", err)
	}
	if len(u.Working) != 1 || u.Working[0].Key != "цель" {
		t.Errorf("мусор не отфильтрован: %v", u.Working)
	}
}

// TestMemoryPromptListsLayers checks that the extractor sees the current store
// with all four layers named, so it can update instead of duplicating.
func TestMemoryPromptListsLayers(t *testing.T) {
	s, err := memory.Open(t.TempDir()+"/memory", t.TempDir()+"/shared")
	if err != nil {
		t.Fatal(err)
	}
	s.Apply(memory.Update{Working: []memory.Entry{{Key: "цель", Value: "переезд офиса"}}})
	p := memoryPrompt(s, "а сколько нужно рабочих мест?")
	for _, want := range []string{"Текущая память:", "working", "profile", "decisions", "knowledge", "цель: переезд офиса", "Новое сообщение пользователя:", "сколько нужно рабочих мест"} {
		if !strings.Contains(p, want) {
			t.Errorf("в промпте экстрактора нет %q:\n%s", want, p)
		}
	}
}
