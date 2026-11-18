package compact

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DarkonGH/aiChallenge10-agent/internal/history"
	"github.com/DarkonGH/aiChallenge10-agent/internal/llm"
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

// TestImportFactsRoutesThroughModel checks the day-10 migration path: the whole
// facts store goes to the model with the same routing prompt as a live turn,
// and the answer is parsed back into layers. No keyword rules are involved.
func TestImportFactsRoutesThroughModel(t *testing.T) {
	var seenSystem, seenUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req llm.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		for _, m := range req.Messages {
			if m.Content == nil {
				continue
			}
			switch m.Role {
			case "system":
				seenSystem = *m.Content
			case "user":
				seenUser = *m.Content
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{
				"message": map[string]any{"role": "assistant", "content": `{"working": {"бюджет": "3 млн"}, "profile": {"город": "Санкт-Петербург"}, "decisions": {"двигатель": "дизель"}, "knowledge": {"рынок": "новых дизелей в РФ нет"}}`},
			}},
			"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 30, "total_tokens": 130},
		})
	}))
	defer srv.Close()

	facts := []history.Fact{
		{Key: "бюджет_общий", Value: "3 млн рублей"},
		{Key: "город", Value: "Санкт-Петербург"},
		{Key: "тип_двигателя", Value: "дизель"},
		{Key: "вывод_о_рынке", Value: "новых дизелей в РФ нет"},
	}
	u, err := ImportFacts(context.Background(), newTestClient(srv), "m", facts)
	if err != nil {
		t.Fatalf("ImportFacts: %v", err)
	}
	if len(u.Working) != 1 || len(u.Profile) != 1 || len(u.Decisions) != 1 || len(u.Knowledge) != 1 {
		t.Fatalf("слои разложены неверно: %+v", u)
	}
	if u.Profile[0].Value != "Санкт-Петербург" {
		t.Errorf("город попал не в profile: %+v", u.Profile)
	}
	// The model must see the routing rules and every fact, not a keyword list.
	for _, want := range []string{"раскладываешь", "working", "profile", "decisions", "knowledge"} {
		if !strings.Contains(seenSystem, want) {
			t.Errorf("в системном промпте нет %q", want)
		}
	}
	for _, f := range facts {
		if !strings.Contains(seenUser, f.Key) {
			t.Errorf("факт %q не попал в промпт импорта", f.Key)
		}
	}
}

// TestImportFactsRejectsEmptyStore keeps the offline error path honest.
func TestImportFactsRejectsEmptyStore(t *testing.T) {
	if _, err := ImportFacts(context.Background(), nil, "m", nil); err == nil {
		t.Error("пустой набор фактов принят без ошибки")
	}
}

// TestSystemPromptTellsToUpdateExistingKey pins the dedup rule: the store
// merges records by key, so the model must reuse the key when it supplements
// an existing fact — otherwise the same person becomes two records.
func TestSystemPromptTellsToUpdateExistingKey(t *testing.T) {
	for _, want := range []string{"под тем же ключом", "не создавай вторую запись"} {
		if !strings.Contains(memorySystemPrompt, want) {
			t.Errorf("в системном промпте нет %q", want)
		}
	}
}

// TestParseMemoryJSONRemoval checks the null contract: a null value deletes the
// record, so one diff can move a fact from working to decisions.
func TestParseMemoryJSONRemoval(t *testing.T) {
	u, err := ParseMemoryJSON(`{"working": {"открытый вопрос": null, "срок": "15 декабря"}, "decisions": {"выбор": "вариант B"}}`)
	if err != nil {
		t.Fatalf("ParseMemoryJSON: %v", err)
	}
	if got := u.Removed(memory.LayerWorking); len(got) != 1 || got[0] != "открытый вопрос" {
		t.Fatalf("удаление не разобрано: %v", got)
	}
	if len(u.Working) != 1 || u.Working[0].Key != "срок" {
		t.Errorf("working: %+v", u.Working)
	}
	if len(u.Decisions) != 1 || u.Decisions[0].Key != "выбор" {
		t.Errorf("decisions: %+v", u.Decisions)
	}
	if u.Empty() {
		t.Error("дифф с удалением и записью считается пустым")
	}
}
